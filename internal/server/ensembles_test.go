package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/seanbrill/local-llm/internal/builder"
	"github.com/seanbrill/local-llm/internal/catalog"
	"github.com/seanbrill/local-llm/internal/ensemble"
	"github.com/seanbrill/local-llm/internal/voice"
)

// newEnsServer is a Server over a temp folder, with the engine faked: images
// carry the labels in images, and containers lists rows.
func newEnsServer(t *testing.T, images map[string]map[string]string, rows func() []map[string]any) *Server {
	t.Helper()
	dir := t.TempDir()
	b, err := builder.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	cpu := false
	cat := &catalog.Catalog{Models: []catalog.Model{
		{ID: "qwen3-tts-0.6b-base", Runtime: "audiocpp", Modality: "tts", MinRAMGB: 3, MinVRAMGB: 2, VoiceClone: true, CloneNeedsTranscript: true,
			AudioCpp: &catalog.AudioCppConfig{Family: "qwen3_tts"}},
		{ID: "pocket-tts-small-en", Runtime: "audiocpp", Modality: "tts", MinRAMGB: 1, VoiceClone: true,
			AudioCpp: &catalog.AudioCppConfig{Family: "pocket_tts", DefaultVoice: "alba"}},
		{ID: "piper-en", Runtime: "python", Modality: "tts", MinRAMGB: 1, CommercialUse: &cpu},
	}}
	s := &Server{
		b: b, cat: cat, mux: newRouteMux(), modelHost: "127.0.0.1",
		ens: ensemble.NewStore(dir), voices: voice.NewStore(b.ConfigDir), mediaDir: filepath.Join(dir, "media"),
	}
	s.imageLabelsFn = func(_ context.Context, _, ref string) (map[string]string, error) {
		if l, ok := images[ref]; ok {
			return l, nil
		}
		return nil, errors.New("no such image")
	}
	s.containersFn = func(context.Context) ([]map[string]any, error) {
		if rows == nil {
			return nil, nil
		}
		return rows(), nil
	}
	return s
}

func do(t *testing.T, h http.HandlerFunc, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(method, path, rd))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// speechWAV is a 4 s mono clip (a tone; the store only checks its shape).
func speechWAV(rate int) []byte {
	s := make([]float32, rate*4)
	for i := range s {
		s[i] = float32(0.3 * math.Sin(float64(i)*2*math.Pi*220/float64(rate)))
	}
	return voice.EncodePCM16(s, rate)
}

func uploadClip(t *testing.T, s *Server, consent bool) (int, map[string]any) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "voice.wav")
	_, _ = fw.Write(speechWAV(24000))
	_ = mw.WriteField("name", "Sean")
	_ = mw.WriteField("transcript", "hello there")
	if consent {
		_ = mw.WriteField("consent", "true")
	}
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/voices", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	s.handleVoices(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestVoicesAPI(t *testing.T) {
	s := newEnsServer(t, nil, nil)
	if code, out := uploadClip(t, s, false); code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "permission") {
		t.Fatalf("no consent: %d %v", code, out)
	}
	code, clip := uploadClip(t, s, true)
	if code != http.StatusOK {
		t.Fatalf("upload: %d %v", code, clip)
	}
	id := clip["id"].(string)
	if clip["transcript"] != "hello there" || clip["consent_at"].(float64) < 1e12 {
		t.Errorf("clip = %v (times are unix ms)", clip)
	}
	fi, err := os.Stat(filepath.Join(s.voices.Dir(), id+".wav"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("clip file mode = %v, %v; want 0600", fi.Mode(), err)
	}
	if code, out := do(t, s.handleVoices, http.MethodGet, "/api/voices", nil); code != 200 || len(out["voices"].([]any)) != 1 {
		t.Errorf("list: %d %v", code, out)
	}
	if code, out := do(t, s.handleVoiceUpdate, http.MethodPost, "/api/voices/update", map[string]string{"id": id, "transcript": " hi again "}); code != 200 || out["transcript"] != "hi again" {
		t.Errorf("update: %d %v", code, out)
	}
	rec := httptest.NewRecorder()
	s.handleVoiceAudio(rec, httptest.NewRequest(http.MethodGet, "/api/voices/audio?id="+id, nil))
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || !bytes.HasPrefix(rec.Body.Bytes(), []byte("RIFF")) {
		t.Errorf("audio: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	rec = httptest.NewRecorder()
	s.handleVoiceAudio(rec, httptest.NewRequest(http.MethodGet, "/api/voices/audio?id=../../etc/passwd", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("a path as id: %d", rec.Code)
	}

	// A persona using the clip; deleting the clip clears its voice.
	if code, out := do(t, s.handlePersonas, http.MethodPost, "/api/personas", map[string]any{"name": "Ava", "prompt": "You are Ava.", "voice": map[string]string{"kind": "clone", "clip": id}}); code != 200 {
		t.Fatalf("persona: %d %v", code, out)
	}
	if code, out := do(t, s.handleVoiceDelete, http.MethodPost, "/api/voices/delete", map[string]string{"id": id}); code != 200 || len(out["personas_cleared"].([]any)) != 1 {
		t.Errorf("delete: %d %v", code, out)
	}
	if p, _ := s.persona("ava"); p.Voice != nil {
		t.Errorf("persona still names the deleted clip: %+v", p.Voice)
	}
}

func TestPersonasRefuseNameClashAndCheckVoice(t *testing.T) {
	s := newEnsServer(t, nil, nil)
	if code, _ := do(t, s.handlePersonas, http.MethodPost, "/api/personas", map[string]any{"name": "Ava", "prompt": "one"}); code != 200 {
		t.Fatal(code)
	}
	code, out := do(t, s.handlePersonas, http.MethodPost, "/api/personas", map[string]any{"name": "ava!", "prompt": "two"})
	if code != http.StatusConflict {
		t.Fatalf("a new persona with a taken name: %d %v (it overwrote the other)", code, out)
	}
	if p, _ := s.persona("ava"); p.Prompt != "one" {
		t.Errorf("prompt = %q", p.Prompt)
	}
	// Editing by id updates in place, renames included.
	if code, _ := do(t, s.handlePersonas, http.MethodPost, "/api/personas", map[string]any{"id": "ava", "name": "Ava Two", "prompt": "edited", "voice": map[string]string{"kind": "builtin", "name": "alba"}}); code != 200 {
		t.Fatal(code)
	}
	if p, _ := s.persona("ava"); p.Prompt != "edited" || p.Voice == nil || p.Voice.Name != "alba" {
		t.Errorf("persona = %+v", p)
	}
	// A clone needs a saved clip; the default voice is stored as none.
	if code, _ := do(t, s.handlePersonas, http.MethodPost, "/api/personas", map[string]any{"id": "ava", "name": "Ava", "prompt": "x", "voice": map[string]string{"kind": "clone", "clip": "v-0123456789ab"}}); code != http.StatusBadRequest {
		t.Errorf("missing clip: %d", code)
	}
	if code, out := do(t, s.handlePersonas, http.MethodPost, "/api/personas", map[string]any{"id": "ava", "name": "Ava", "prompt": "x", "voice": map[string]string{"kind": ""}}); code != 200 || out["voice"] != nil {
		t.Errorf("default voice: %d %v", code, out)
	}
	// A persona an ensemble uses can't be deleted.
	if _, err := s.ens.Save(ensemble.Ensemble{Name: "Helper", Persona: "ava", Members: []ensemble.Member{{Tool: "chat", Image: "local-llm/q"}}}); err != nil {
		t.Fatal(err)
	}
	if code, _ := do(t, s.handlePersonaDelete, http.MethodPost, "/api/personas/delete", map[string]string{"id": "ava"}); code != http.StatusConflict {
		t.Errorf("deleting a persona in use: %d", code)
	}
}

func TestEnsemblesSaveEnrichAndClash(t *testing.T) {
	images := map[string]map[string]string{
		"local-llm/qtts": {"local-llm.model": "qwen3-tts-0.6b-base", "local-llm.modality": "tts", "local-llm.runtime": "audiocpp", "local-llm.compute": "cpu"},
		"local-llm/chat": {"local-llm.model": "qwen3-8b", "local-llm.modality": "text"},
	}
	s := newEnsServer(t, images, nil)
	body := map[string]any{"name": "My Helper", "members": []map[string]any{
		{"tool": "chat", "image": "local-llm/chat"},
		{"tool": "speak", "image": "local-llm/qtts"},
	}}
	code, out := do(t, s.handleEnsembles, http.MethodPost, "/api/ensembles", body)
	if code != 200 {
		t.Fatalf("save: %d %v", code, out)
	}
	e, _, _ := s.ens.Get("my-helper")
	sp, _ := e.Member("speak")
	if !sp.VoiceClone || !sp.CloneNeedsTranscript || !sp.CloneOnly || sp.Family != "qwen3_tts" || sp.VRAMGB != 3 || sp.Runtime != "audiocpp" {
		t.Errorf("speak member not filled from the catalog: %+v", sp)
	}
	if e.Routing != ensemble.RoutingAgent {
		t.Errorf("routing = %q, want agent by default", e.Routing)
	}
	if code, _ := do(t, s.handleEnsembles, http.MethodPost, "/api/ensembles", body); code != http.StatusConflict {
		t.Errorf("same name again: %d (it overwrote the other)", code)
	}
	body["id"] = "my-helper"
	if code, _ := do(t, s.handleEnsembles, http.MethodPost, "/api/ensembles", body); code != 200 {
		t.Errorf("edit by id: %d", code)
	}
}

// fakeGate is a Conductor's /ensemble/status and /ensemble/config.
type fakeGate struct {
	mu      sync.Mutex
	id      string
	applied *ensemble.Config
	posts   int
}

func (g *fakeGate) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ensemble/status", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		st := ensemble.Status{ID: g.id, Name: g.id, Router: "agent", Members: []ensemble.MemberStatus{{Tool: "chat", State: "stopped"}}}
		if g.applied != nil {
			st.Persona = &ensemble.PersonaStatus{Hash: g.applied.Hash}
		}
		_ = json.NewEncoder(w).Encode(st)
	})
	mux.HandleFunc("/ensemble/config", func(w http.ResponseWriter, r *http.Request) {
		var c ensemble.Config
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil || c.Hash != c.ComputeHash() {
			http.Error(w, `{"error":"bad config"}`, 400)
			return
		}
		g.mu.Lock()
		g.applied, g.posts = &c, g.posts+1
		g.mu.Unlock()
		_ = json.NewEncoder(w).Encode(ensemble.PersonaStatus{Hash: c.Hash})
	})
	return mux
}

func portOf(t *testing.T, u string) int {
	t.Helper()
	p, err := strconv.Atoi(u[strings.LastIndexByte(u, ':')+1:])
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEnsembleStatusSyncsPersonaAndVoice(t *testing.T) {
	g := &fakeGate{id: "helper"}
	srv := httptest.NewServer(g.handler())
	defer srv.Close()
	port := portOf(t, srv.URL)
	rows := func() []map[string]any {
		return []map[string]any{{"Names": "localllm-" + strconv.Itoa(port), "State": "running", "ID": "c1", "Engine": "docker",
			"Labels": "local-llm.model=,local-llm.ensemble=helper,local-llm.kind=ensemble"}}
	}
	images := map[string]map[string]string{ensembleImage("helper"): {"local-llm.kind": "ensemble"}}
	s := newEnsServer(t, images, rows)
	_, clip := uploadClip(t, s, true)
	if code, out := do(t, s.handlePersonas, http.MethodPost, "/api/personas", map[string]any{"name": "Ava", "prompt": "You are Ava.", "voice": map[string]string{"kind": "clone", "clip": clip["id"].(string)}}); code != 200 {
		t.Fatal(code, out)
	}
	e := ensemble.Ensemble{Name: "Helper", Persona: "ava", Members: []ensemble.Member{
		{Tool: "chat", Image: "local-llm/q"},
		{Tool: "speak", Image: "local-llm/qtts", Model: "qwen3-tts-0.6b-base", Runtime: "audiocpp", VoiceClone: true, CloneNeedsTranscript: true},
	}}
	if _, err := s.ens.Save(e); err != nil {
		t.Fatal(err)
	}

	status := func() map[string]any {
		code, out := do(t, s.handleEnsemblesStatus, http.MethodGet, "/api/ensembles/status", nil)
		if code != 200 {
			t.Fatalf("status: %d %v", code, out)
		}
		return out["ensembles"].([]any)[0].(map[string]any)
	}
	live := status()
	if live["built"] != true || live["running"] == nil || live["gate"] == nil || live["voice_error"] != nil {
		t.Fatalf("status = %v", live)
	}
	g.mu.Lock()
	applied := g.applied
	g.mu.Unlock()
	if applied == nil || applied.Persona.Prompt != "You are Ava." || applied.Voice.Kind != "clone" || applied.Voice.RefText != "hello there" {
		t.Fatalf("pushed config = %+v", applied)
	}
	if wav, _ := base64.StdEncoding.DecodeString(applied.Voice.RefAudio); !bytes.HasPrefix(wav, []byte("RIFF")) {
		t.Error("the clip wasn't sent as base64 WAV")
	}
	if p := live["gate"].(map[string]any)["persona"].(map[string]any); p["synced"] != true {
		t.Errorf("persona not marked synced: %v", p)
	}
	// Unchanged: no second push.
	status()
	if g.posts != 1 {
		t.Errorf("config pushed %d times for one persona", g.posts)
	}
	// The clip's transcript removed: Qwen3 Base needs it, so the page says so.
	empty := ""
	if _, err := s.voices.Update(clip["id"].(string), nil, &empty); err != nil {
		t.Fatal(err)
	}
	if ve, _ := status()["voice_error"].(string); !strings.Contains(ve, "transcript") {
		t.Errorf("voice_error = %q, want the missing transcript named", ve)
	}
	// A speech member that can't clone.
	e.ID = "helper"
	e.Members[1] = ensemble.Member{Tool: "speak", Image: "local-llm/piper", Model: "piper-en", Runtime: "python"}
	if _, err := s.ens.Save(e); err != nil {
		t.Fatal(err)
	}
	if ve, _ := status()["voice_error"].(string); !strings.Contains(ve, "clone") {
		t.Errorf("voice_error = %q, want a can't-clone error", ve)
	}
}

func TestEnsembleRunRefusesUnbuilt(t *testing.T) {
	s := newEnsServer(t, nil, nil)
	if _, err := s.ens.Save(ensemble.Ensemble{Name: "x", Members: []ensemble.Member{{Tool: "chat", Image: "local-llm/q"}}}); err != nil {
		t.Fatal(err)
	}
	code, out := do(t, s.handleEnsembleRun, http.MethodPost, "/api/ensembles/run", map[string]string{"id": "x"})
	if code != http.StatusConflict || out["code"] != "not_built" {
		t.Errorf("run unbuilt: %d %v", code, out)
	}
}

func TestChatStreamSavesEnsembleMedia(t *testing.T) {
	png := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nfake"))
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		media, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{
			"content": "\n\n![generated image](" + png + ")\n\n",
			"media":   []any{map[string]string{"type": "image", "url": png, "mime": "image/png"}},
		}}}})
		_, _ = io.WriteString(w, ": ensemble: drawing\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"Here\"}}]}\n\ndata: "+string(media)+"\n\ndata: [DONE]\n\n")
	}))
	defer up.Close()
	s := newEnsServer(t, nil, nil)
	b, _ := json.Marshal(map[string]any{"port": portOf(t, up.URL), "messages": []any{map[string]string{"role": "user", "content": "draw"}}})
	rec := httptest.NewRecorder()
	s.handleChatStream(rec, httptest.NewRequest(http.MethodPost, "/api/chat/stream", bytes.NewReader(b)))
	out := rec.Body.String()
	if strings.Contains(out, "data:image/png") {
		t.Fatalf("a data URL reached the browser:\n%s", out)
	}
	if !strings.Contains(out, `"url":"/api/media/`) || !strings.Contains(out, ": ensemble: drawing") || !strings.Contains(out, "Here") || !strings.Contains(out, "[DONE]") {
		t.Fatalf("stream = %s", out)
	}
	ents, _ := os.ReadDir(s.mediaDir)
	if len(ents) != 1 {
		t.Errorf("media saved = %d files", len(ents))
	}
}

func TestTranscribeAtFallsBack(t *testing.T) {
	var paths []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/inference" {
			http.NotFound(w, r)
			return
		}
		_ = r.ParseMultipartForm(1 << 20)
		if r.FormValue("model") != "stt" {
			http.Error(w, "model", 400)
			return
		}
		_, _ = io.WriteString(w, `{"text":" hi "}`)
	}))
	defer up.Close()
	s := newEnsServer(t, nil, nil)
	text, err := s.transcribeAt(context.Background(), portOf(t, up.URL), "a.wav", speechWAV(16000), nil)
	if err != nil || text != "hi" || strings.Join(paths, ",") != "/inference,/v1/audio/transcriptions" {
		t.Errorf("text %q, err %v, paths %v", text, err, paths)
	}
}

func TestGatewayServesEnsembles(t *testing.T) {
	th := newTestHarness(t)
	key := th.h.store.Get().GatewayKey
	var gotType, gotPath string
	var gotBody []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ensemble/status" {
			http.NotFound(w, r) // not synced in this test
			return
		}
		gotType, gotPath = r.Header.Get("Content-Type"), r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write([]byte("RIFF"))
	}))
	defer up.Close()
	port := upstreamPort(t, up.URL)
	th.setRows(
		map[string]any{"Names": "localllm-" + strconv.Itoa(port), "State": "running", "Image": "local-llm/ens-helper:latest", "Engine": "docker",
			"Labels": "local-llm.model=,local-llm.modality=ensemble,local-llm.ensemble=helper,local-llm.ref=local-llm/ens-helper:latest"},
		map[string]any{"Names": "ens-helper-chat", "State": "running", "Image": "local-llm/q", "Engine": "docker", "Ports": "127.0.0.1:55001->8080/tcp",
			"Labels": "local-llm.model=q,local-llm.modality=text,local-llm.ensemble-member=helper"},
		modelRow(port+1, "local-llm/demo:latest", "text"),
	)
	resp := th.gw(t, "GET", "/v1/models", key, nil)
	var list struct {
		Data []oaiModel `json:"data"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	ids := []string{}
	for _, m := range list.Data {
		ids = append(ids, m.ID+"/"+m.Info["modality"].(string))
	}
	if strings.Join(ids, ",") != "demo/text,ens-helper/ensemble" {
		t.Fatalf("models = %v (the ensemble listed, its members not)", ids)
	}

	resp = th.gw(t, "POST", "/v1/audio/speech", key, []byte(`{"model":"ens-helper","input":"hi"}`))
	resp.Body.Close()
	if resp.StatusCode != 200 || gotPath != "/v1/audio/speech" || gotType != "application/json" {
		t.Errorf("speech: %d %s %s", resp.StatusCode, gotPath, gotType)
	}

	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	_ = mw.WriteField("model", "ens-helper")
	fw, _ := mw.CreateFormFile("file", "q.wav")
	_, _ = fw.Write([]byte("RIFFdata"))
	_ = mw.Close()
	req, _ := http.NewRequest("POST", th.srv.URL+"/v1/audio/transcriptions", bytes.NewReader(form.Bytes()))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || gotPath != "/v1/audio/transcriptions" || gotType != mw.FormDataContentType() || !bytes.Equal(gotBody, form.Bytes()) {
		t.Errorf("transcriptions: %d %s %s (the upload must pass through unchanged)", resp.StatusCode, gotPath, gotType)
	}

	resp = th.gw(t, "POST", "/v1/audio/speech", key, []byte(`{"model":"demo","input":"hi"}`))
	if msg, _ := decodeErr(t, resp); resp.StatusCode != 400 || !strings.Contains(msg, "ensemble") {
		t.Errorf("speech on a text model: %d %q", resp.StatusCode, msg)
	}
	resp = th.gw(t, "POST", "/v1/completions", key, []byte(`{"model":"ens-helper","prompt":"hi"}`))
	if msg, _ := decodeErr(t, resp); resp.StatusCode != 400 || !strings.Contains(msg, "chat/completions") {
		t.Errorf("completions on an ensemble: %d %q", resp.StatusCode, msg)
	}
}
