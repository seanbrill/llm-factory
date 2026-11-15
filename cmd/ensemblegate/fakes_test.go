package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seanbrill/local-llm/internal/ensemble"
	"github.com/seanbrill/local-llm/internal/voice"
)

// ---- a fake engine: images are httptest servers ------------------------------

type fakeCtr struct {
	spec  RunSpec
	port  int
	state string
}

type fakeEngine struct {
	mu      sync.Mutex
	images  map[string]*httptest.Server // image ref -> the "model" it serves
	ctrs    map[string]*fakeCtr
	runs    []RunSpec
	removed []string
	runErr  map[string]error // image -> error from Run
	logs    string
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{images: map[string]*httptest.Server{}, ctrs: map[string]*fakeCtr{}, runErr: map[string]error{}}
}

func portOf(srv *httptest.Server) int {
	_, p, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	n, _ := strconv.Atoi(p)
	return n
}

func (e *fakeEngine) Run(_ context.Context, spec RunSpec) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.runs = append(e.runs, spec)
	if err := e.runErr[spec.Image]; err != nil {
		return err
	}
	srv := e.images[spec.Image]
	if srv == nil {
		return fmt.Errorf("Unable to find image '%s' locally", spec.Image)
	}
	if _, ok := e.ctrs[spec.Name]; ok {
		return fmt.Errorf("Conflict. The container name %q is already in use", spec.Name)
	}
	e.ctrs[spec.Name] = &fakeCtr{spec: spec, port: portOf(srv), state: "running"}
	return nil
}

func (e *fakeEngine) Remove(_ context.Context, name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.ctrs[name]; ok {
		e.removed = append(e.removed, name)
	}
	delete(e.ctrs, name)
	return nil
}

func (e *fakeEngine) HostPort(_ context.Context, name string) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, ok := e.ctrs[name]
	if !ok {
		return 0, errors.New("No such container: " + name)
	}
	return c.port, nil
}

func (e *fakeEngine) State(_ context.Context, name string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c, ok := e.ctrs[name]; ok {
		return c.state, nil
	}
	return "", nil
}

func (e *fakeEngine) List(_ context.Context, label string) ([]Container, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	k, v, _ := strings.Cut(label, "=")
	var out []Container
	for name, c := range e.ctrs {
		if c.spec.Labels[k] == v {
			out = append(out, Container{Name: name, Image: c.spec.Image, State: c.state, Labels: c.spec.Labels})
		}
	}
	return out, nil
}

func (e *fakeEngine) Logs(context.Context, string, int) string { return e.logs }

func (e *fakeEngine) setState(name, state string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c, ok := e.ctrs[name]; ok {
		c.state = state
	}
}

func (e *fakeEngine) running(name string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.ctrs[name]
	return ok
}

func (e *fakeEngine) runCount(image string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, r := range e.runs {
		if r.Image == image {
			n++
		}
	}
	return n
}

// adopt pretends a container was left running from before a restart.
func (e *fakeEngine) adopt(ensID, tool, image string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	name := ensemble.MemberContainer(ensID, tool)
	e.ctrs[name] = &fakeCtr{spec: RunSpec{Name: name, Image: image,
		Labels: map[string]string{ensemble.LabelMember: ensID, ensemble.LabelTool: tool}}, port: portOf(e.images[image]), state: "running"}
}

// ---- fake members -------------------------------------------------------------

// recorder keeps the requests a fake member got.
type recorder struct {
	mu   sync.Mutex
	reqs []recorded
}

type recorded struct {
	Path string
	Body []byte
	Form map[string]string
	File []byte
}

func (r *recorder) add(x recorded) {
	r.mu.Lock()
	r.reqs = append(r.reqs, x)
	r.mu.Unlock()
}

func (r *recorder) all() []recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recorded(nil), r.reqs...)
}

func (r *recorder) last(t *testing.T) recorded {
	t.Helper()
	all := r.all()
	if len(all) == 0 {
		t.Fatal("the member got no request")
	}
	return all[len(all)-1]
}

func healthOK(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"status":"ok"}`)) }

// turn is one scripted brain answer: text, or tool calls.
type turn struct {
	text   string
	calls  [][2]string // name, arguments JSON
	status int         // an error status instead
	errMsg string
}

// fakeLlama is a llama-server: it answers chat with the scripted turns in
// order (the last one repeats), streaming when asked, and records each body.
type fakeLlama struct {
	rec   recorder
	mu    sync.Mutex
	turns []turn
	n     int
}

func (f *fakeLlama) next() turn {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.turns[len(f.turns)-1]
	if f.n < len(f.turns) {
		t = f.turns[f.n]
	}
	f.n++
	return t
}

func (f *fakeLlama) bodies(t *testing.T) []map[string]any {
	var out []map[string]any
	for _, r := range f.rec.all() {
		var m map[string]any
		if err := json.Unmarshal(r.Body, &m); err != nil {
			t.Fatalf("chat body isn't JSON: %v", err)
		}
		out = append(out, m)
	}
	return out
}

func (f *fakeLlama) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { healthOK(w) })
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.rec.add(recorded{Path: r.URL.Path, Body: b})
		var req struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal(b, &req)
		t := f.next()
		if t.status != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(t.status)
			fmt.Fprintf(w, `{"error":{"code":%d,"message":%q,"type":"invalid_request_error"}}`, t.status, t.errMsg)
			return
		}
		var calls []map[string]any
		for i, c := range t.calls {
			calls = append(calls, map[string]any{"index": i, "id": fmt.Sprintf("call_%d", i), "type": "function",
				"function": map[string]any{"name": c[0], "arguments": c[1]}})
		}
		finish := "stop"
		if len(calls) > 0 {
			finish = "tool_calls"
		}
		if !req.Stream {
			msg := map[string]any{"role": "assistant", "content": t.text}
			if len(calls) > 0 {
				msg["tool_calls"] = calls
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "x", "object": "chat.completion", "model": "fake",
				"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}},
				"usage":   map[string]int{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(delta map[string]any, fin any) {
			b, _ := json.Marshal(map[string]any{"id": "x", "object": "chat.completion.chunk", "model": "fake",
				"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": fin}}})
			fmt.Fprintf(w, "data: %s\n\n", b)
			w.(http.Flusher).Flush()
		}
		send(map[string]any{"role": "assistant", "content": ""}, nil)
		for _, word := range strings.SplitAfter(t.text, " ") {
			if word != "" {
				send(map[string]any{"content": word}, nil)
			}
		}
		if len(calls) > 0 {
			send(map[string]any{"tool_calls": calls}, nil)
		}
		send(map[string]any{}, finish)
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	return mux
}

// fakeSTT is whisper.cpp (/inference) or, with openai set, an audio.cpp
// speech-to-text image (/v1/audio/transcriptions only).
type fakeSTT struct {
	rec    recorder
	text   string
	openai bool
}

func (f *fakeSTT) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { healthOK(w) })
	answer := func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, "bad form", 400)
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "no file", 400)
			return
		}
		audio, _ := io.ReadAll(file)
		form := map[string]string{}
		for k, v := range r.MultipartForm.Value {
			form[k] = v[0]
		}
		f.rec.add(recorded{Path: r.URL.Path, Form: form, File: audio})
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"text":%q}`, " "+f.text+" ")
	}
	if f.openai {
		mux.HandleFunc("/v1/audio/transcriptions", answer)
	} else {
		mux.HandleFunc("/inference", answer)
	}
	return mux
}

// fakeTTS answers /v1/audio/speech with a WAV and records the body.
type fakeTTS struct{ rec recorder }

func (f *fakeTTS) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { healthOK(w) })
	mux.HandleFunc("/v1/audio/speech", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.rec.add(recorded{Path: r.URL.Path, Body: b})
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(testWAV(16000, 1, 0.5))
	})
	return mux
}

// fakeSD is stable-diffusion.cpp's sd-server: no /health, / answers.
type fakeSD struct{ rec recorder }

func (f *fakeSD) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/sdapi/v1/txt2img", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.rec.add(recorded{Path: r.URL.Path, Body: b})
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"images":[%q]}`, base64.StdEncoding.EncodeToString([]byte("PNGDATA")))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("sd-server"))
	})
	return mux
}

// fakeVideo is videogate.
type fakeVideo struct{ rec recorder }

func (f *fakeVideo) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { healthOK(w) })
	mux.HandleFunc("/generate", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.rec.add(recorded{Path: r.URL.Path, Body: b})
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"video":"data:video/webm;base64,%s"}`, base64.StdEncoding.EncodeToString([]byte("WEBM")))
	})
	return mux
}

// testWAV makes a 16-bit WAV of a tone.
func testWAV(rate, channels int, secs float64) []byte {
	n := int(float64(rate) * secs)
	samples := make([]float32, n)
	for i := range samples {
		samples[i] = float32(0.3 * math.Sin(2*math.Pi*220*float64(i)/float64(rate)))
	}
	mono := voice.EncodePCM16(samples, rate)
	if channels == 1 {
		return mono
	}
	// Interleave the same samples into each channel.
	data := mono[44:]
	out := make([]byte, 44, 44+len(data)*channels)
	copy(out, mono[:44])
	for i := 0; i+1 < len(data); i += 2 {
		for c := 0; c < channels; c++ {
			out = append(out, data[i], data[i+1])
		}
	}
	le := func(off int, v uint32) {
		out[off], out[off+1], out[off+2], out[off+3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
	}
	out[22] = byte(channels)
	le(28, uint32(rate*2*channels))
	out[32] = byte(2 * channels)
	le(40, uint32(len(out)-44))
	le(4, uint32(len(out)-8))
	return out
}

// ---- the test ensemble --------------------------------------------------------

type testEns struct {
	t      *testing.T
	eng    *fakeEngine
	g      *Gate
	srv    *httptest.Server
	brain  *fakeLlama
	vision *fakeLlama
	stt    *fakeSTT
	tts    *fakeTTS
	sd     *fakeSD
	video  *fakeVideo
}

type ensOpt func(*ensemble.Manifest, *testEns)

// newTestEns builds a Conductor over fake members (all tools by default; opts
// change the manifest) and serves it.
func newTestEns(t *testing.T, opts ...ensOpt) *testEns {
	t.Helper()
	te := &testEns{t: t, eng: newFakeEngine(),
		brain: &fakeLlama{turns: []turn{{text: "Hello there."}}}, vision: &fakeLlama{turns: []turn{{text: "A red ball."}}},
		stt: &fakeSTT{text: "what is the capital of france"}, tts: &fakeTTS{}, sd: &fakeSD{}, video: &fakeVideo{}}
	serve := func(image string, h http.Handler) {
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		te.eng.images[image] = srv
	}
	serve("local-llm/chat", te.brain.handler())
	serve("local-llm/vision", te.vision.handler())
	serve("local-llm/whisper", te.stt.handler())
	serve("local-llm/piper", te.tts.handler())
	serve("local-llm/sd", te.sd.handler())
	serve("local-llm/video", te.video.handler())
	mani := ensemble.Manifest{ModelHost: "127.0.0.1", Ensemble: ensemble.Ensemble{
		ID: "test", Name: "Test", Routing: ensemble.RoutingAgent, VRAMBudgetGB: 100,
		Members: []ensemble.Member{
			{Tool: ensemble.ToolChat, Modality: "text", Image: "local-llm/chat", Model: "qwen", Runtime: "cpp", VRAMGB: 6},
			{Tool: ensemble.ToolSeeImage, Modality: "vision", Image: "local-llm/vision", Model: "gemma-vision", Runtime: "cpp", VRAMGB: 4},
			{Tool: ensemble.ToolTranscribe, Modality: "audio-stt", Image: "local-llm/whisper", Model: "whisper-base-en", VRAMGB: 1},
			{Tool: ensemble.ToolSpeak, Modality: "tts", Image: "local-llm/piper", Model: "piper-en-us", Runtime: "cpp", VRAMGB: 1},
			{Tool: ensemble.ToolGenerateImage, Modality: "image", Image: "local-llm/sd", Model: "sdxl-turbo", VRAMGB: 4},
			{Tool: ensemble.ToolGenerateVideo, Modality: "video", Image: "local-llm/video", Model: "wan", VRAMGB: 8},
		}}}
	for _, o := range opts {
		o(&mani, te)
	}
	te.g = newGate(mani, te.eng, nil, t.Logf)
	te.g.mg.pollEvery = 10 * time.Millisecond
	te.g.mg.startTimeout = 5 * time.Second
	te.g.keepalive = time.Hour
	te.srv = httptest.NewServer(te.g.Handler())
	t.Cleanup(te.srv.Close)
	return te
}

// withMember replaces (or adds) the member for a tool.
func withMember(m ensemble.Member) ensOpt {
	return func(mani *ensemble.Manifest, _ *testEns) {
		for i := range mani.Members {
			if mani.Members[i].Tool == m.Tool {
				mani.Members[i] = m
				return
			}
		}
		mani.Members = append(mani.Members, m)
	}
}

// without drops members.
func without(tools ...string) ensOpt {
	return func(mani *ensemble.Manifest, _ *testEns) {
		drop := map[string]bool{}
		for _, t := range tools {
			drop[t] = true
		}
		kept := mani.Members[:0]
		for _, m := range mani.Members {
			if !drop[m.Tool] {
				kept = append(kept, m)
			}
		}
		mani.Members = kept
	}
}

func routing(r string) ensOpt {
	return func(mani *ensemble.Manifest, _ *testEns) { mani.Routing = r }
}

// image serves a fake member under an image ref.
func (te *testEns) image(ref string, h http.Handler) {
	srv := httptest.NewServer(h)
	te.t.Cleanup(srv.Close)
	te.eng.images[ref] = srv
}

func (te *testEns) post(path string, body any) (*http.Response, []byte) {
	te.t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(te.srv.URL+path, "application/json", bytes.NewReader(b))
	if err != nil {
		te.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

func (te *testEns) chat(body map[string]any) (int, map[string]any) {
	te.t.Helper()
	resp, b := te.post("/v1/chat/completions", body)
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		te.t.Fatalf("reply isn't JSON (%d): %s", resp.StatusCode, b)
	}
	return resp.StatusCode, out
}

// sseEvents reads a stream's data payloads and comment lines.
func sseEvents(t *testing.T, b []byte) (data []map[string]any, comments []string, done bool) {
	t.Helper()
	for _, block := range strings.Split(string(b), "\n\n") {
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, ":"):
				comments = append(comments, strings.TrimSpace(line[1:]))
			case strings.HasPrefix(line, "data: "):
				p := strings.TrimPrefix(line, "data: ")
				if p == "[DONE]" {
					done = true
					continue
				}
				var m map[string]any
				if err := json.Unmarshal([]byte(p), &m); err != nil {
					t.Fatalf("bad event %q: %v", p, err)
				}
				data = append(data, m)
			}
		}
	}
	return
}

func streamContent(events []map[string]any) (content string, media []any) {
	for _, e := range events {
		choices, _ := e["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		d, _ := choices[0].(map[string]any)["delta"].(map[string]any)
		if s, ok := d["content"].(string); ok {
			content += s
		}
		if m, ok := d["media"].([]any); ok {
			media = append(media, m...)
		}
	}
	return
}

func message(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	choices, _ := out["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("no choices in %v", out)
	}
	msg, _ := choices[0].(map[string]any)["message"].(map[string]any)
	return msg
}

// systemOf returns a chat body's system message text.
func systemOf(body map[string]any) string {
	msgs, _ := body["messages"].([]any)
	if len(msgs) == 0 {
		return ""
	}
	m, _ := msgs[0].(map[string]any)
	if m["role"] != "system" {
		return ""
	}
	return textOf(m["content"])
}

func toolNames(body map[string]any) []string {
	var out []string
	tools, _ := body["tools"].([]any)
	for _, t := range tools {
		fn, _ := t.(map[string]any)["function"].(map[string]any)
		name, _ := fn["name"].(string)
		out = append(out, name)
	}
	return out
}
