package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/seanbrill/local-llm/internal/agent"
	"github.com/seanbrill/local-llm/internal/ensemble"
	"github.com/seanbrill/local-llm/internal/voice"
)

// Agent routing (the default): the ensemble's chat member is the brain, and
// the other members are tools it calls (generate_image, generate_video, speak,
// look_at_image), run by the shared tool loop in internal/agent. Built-in
// tools (web_search, fetch_url, files, skills) join them when the ensemble's
// harness config turns them on. Media the tools make never pass through the
// model: a tool tells the model it is done, and the media go straight into the
// reply (delta.media while streaming, message.media otherwise, plus a markdown
// image for clients that only read the text).
//
// The keyword heuristic answers instead when there is no brain (no chat or
// vision member) or the brain can't call tools.

// memberToolset is the toolset name the member tools are registered under.
const memberToolset = "ensemble"

// Agent tool names (the member tools).
const (
	agentGenerateImage = "generate_image"
	agentGenerateVideo = "generate_video"
	agentSpeak         = "speak"
	agentLookAtImage   = "look_at_image"
)

// memberToolTimeout bounds one member tool call: a cold start plus a CPU video
// can take long. It is the agent's maximum.
const memberToolTimeout = 30 * time.Minute

const ensembleNote = "You are one assistant with several abilities, which you use through your tools: " +
	"drawing images, making short videos, speaking out loud, looking at images. Use one only when the user asks for " +
	"what it makes. Media your tools make are attached to your reply automatically: never write links, file names " +
	"or markdown images for them, and don't claim you can't make images, video or speech."

// brain is the agent router.
type brain struct {
	g    *Gate
	tool string // the member that thinks: chat, or see_image without a chat member
	sees bool   // the brain itself is a vision model
	ag   *agent.Agent

	mu  sync.Mutex
	off string // why agent routing is off for now ("" = on)
}

// canCallTools reports a chat runtime that can call tools: llama.cpp behind
// llmgate, which always runs llama-server with --jinja.
func canCallTools(runtime string) bool { return runtime == "" || runtime == "cpp" }

// newBrain sets up agent routing, or says why it can't run.
func newBrain(g *Gate, harness *agent.Config) (*brain, string) {
	b := &brain{g: g}
	if m, ok := g.mg.Member(ensemble.ToolChat); ok {
		b.tool, b.sees = ensemble.ToolChat, m.Modality == "vision"
	} else if _, ok := g.mg.Member(ensemble.ToolSeeImage); ok {
		b.tool, b.sees = ensemble.ToolSeeImage, true
	} else {
		return nil, "there is no chat member to be the brain"
	}
	if m, _ := g.mg.Member(b.tool); !canCallTools(m.Runtime) {
		return nil, fmt.Sprintf("the %s member (%s runtime) can't call tools", b.tool, m.Runtime)
	}
	cfg := agent.Config{Tools: []string{}}
	if harness != nil {
		cfg = *harness
	}
	cfg.Limits.ToolTimeout = agent.Duration(memberToolTimeout)
	cfg.Instructions = strings.TrimSpace(ensembleNote + "\n\n" + cfg.Instructions)
	cfg = cfg.WithDefaults()
	reg, unavailable, err := agent.Builtins(cfg)
	if err != nil {
		g.logf("agent: built-in tools are off: %v", err)
		cfg.Tools = []string{}
		reg, unavailable = agent.NewRegistry(), nil
	}
	for _, t := range b.memberTools() {
		if err := reg.Register(memberToolset, t); err != nil {
			return nil, err.Error()
		}
	}
	for _, u := range unavailable {
		g.logf("agent: %s is unavailable: %s", u.Tool, u.Reason)
	}
	ag, err := agent.New(cfg, agent.Options{Registry: reg, Unavailable: unavailable, Host: "ensemblegate",
		Logf: func(f string, a ...any) { g.logf("[agent] "+f, a...) }})
	if err != nil {
		return nil, "the agent config is invalid: " + err.Error()
	}
	b.ag = ag
	return b, ""
}

// on reports whether agent routing answers (it turns off for good when the
// brain refuses tools).
func (b *brain) on() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.off == ""
}

func (b *brain) turnOff(why string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.off == "" {
		b.off = why
		b.g.logf("agent routing is off: %s; routing with the %s from now on", why, b.g.router.Name())
	}
}

// tools lists the tools the brain can call.
func (b *brain) tools() []string {
	var out []string
	for _, t := range b.ag.Registry().Describe() {
		out = append(out, t.Name)
	}
	return out
}

// ---- per-request state --------------------------------------------------------

// turnState is one request's side channel between the tools and the reply.
type turnState struct {
	mu       sync.Mutex
	media    []ensemble.Media
	images   []string                    // image URLs in the conversation, in order (look_at_image)
	progress func(string)                // status while streaming; nil otherwise
	emit     func(m ensemble.Media) bool // streaming: send media now; false once the reply ended
	closed   bool
}

type stateKey struct{}

func stateOf(ctx context.Context) *turnState {
	if ts, ok := ctx.Value(stateKey{}).(*turnState); ok {
		return ts
	}
	return &turnState{}
}

func (ts *turnState) say(text string) {
	ts.mu.Lock()
	p, closed := ts.progress, ts.closed
	ts.mu.Unlock()
	if p != nil && !closed {
		p(text)
	}
}

// add keeps a generated clip for the reply, sending it at once when streaming.
func (ts *turnState) add(m ensemble.Media) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.closed {
		return
	}
	ts.media = append(ts.media, m)
	if ts.emit != nil {
		ts.emit(m)
	}
}

func (ts *turnState) close() {
	ts.mu.Lock()
	ts.closed = true
	ts.mu.Unlock()
}

// ---- member tools -------------------------------------------------------------

func schema(props string, required ...string) json.RawMessage {
	req, _ := json.Marshal(required)
	if len(required) == 0 {
		req = []byte("[]")
	}
	return json.RawMessage(`{"type":"object","properties":{` + props + `},"required":` + string(req) + `}`)
}

// toolArgs reads a tool's JSON arguments leniently (an empty string is {}).
func toolArgs(args json.RawMessage, v any) error {
	if len(bytes.TrimSpace(args)) == 0 {
		return nil
	}
	if err := json.Unmarshal(args, v); err != nil {
		return fmt.Errorf("bad arguments: %v", err)
	}
	return nil
}

// flexInt reads 2, 2.0 or "2".
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		return nil
	}
	var n float64
	if _, err := fmt.Sscan(s, &n); err != nil {
		return fmt.Errorf("%s is not a number", string(b))
	}
	*f = flexInt(n)
	return nil
}

func (b *brain) memberTools() []agent.Tool {
	g := b.g
	var out []agent.Tool
	if g.mg.Has(ensemble.ToolGenerateImage) {
		out = append(out, &agent.FuncTool{
			ToolName: agentGenerateImage,
			Desc: "Draw an image with the image model. Use it when the user asks for a picture, drawing, photo, " +
				"illustration, logo or other image. Write the prompt in English and in detail: subject, style, " +
				"composition, lighting, colors.",
			Params:  schema(`"prompt":{"type":"string","description":"what to draw, in detail"}`, "prompt"),
			Trusted: true,
			Fn: func(ctx context.Context, args json.RawMessage) (string, error) {
				return b.generate(ctx, args, "image", g.members.GenerateImage)
			},
		})
	}
	if g.mg.Has(ensemble.ToolGenerateVideo) {
		out = append(out, &agent.FuncTool{
			ToolName: agentGenerateVideo,
			Desc: "Make a short video clip (a few seconds) with the video model. Use it when the user asks for a " +
				"video, clip or animation. Describe the scene and the motion in English. It takes minutes.",
			Params:  schema(`"prompt":{"type":"string","description":"the scene and its motion, in detail"}`, "prompt"),
			Trusted: true,
			Fn: func(ctx context.Context, args json.RawMessage) (string, error) {
				return b.generate(ctx, args, "video", g.members.GenerateVideo)
			},
		})
	}
	if g.mg.Has(ensemble.ToolSpeak) {
		out = append(out, &agent.FuncTool{
			ToolName: agentSpeak,
			Desc: "Say something out loud in your own voice; the audio plays with your reply. Use it when the user " +
				"asks to hear you (\"say ...\", \"read this aloud\", \"what does it sound like\"). Give the exact words, " +
				"as plain text without markdown.",
			Params:  schema(`"text":{"type":"string","description":"the words to say"}`, "text"),
			Trusted: true,
			Fn: func(ctx context.Context, args json.RawMessage) (string, error) {
				var a struct {
					Text string `json:"text"`
				}
				if err := toolArgs(args, &a); err != nil {
					return "", err
				}
				text := speakable(a.Text)
				if text == "" {
					return "", errors.New("text is empty: give the words to say")
				}
				ts := stateOf(ctx)
				m, err := g.members.Speak(ctx, g.persona.resolve(voice.SpeechRequest{Input: text}), ts.say)
				if err != nil {
					return "", err
				}
				ts.add(m)
				return "Said it out loud: the audio plays with your reply. Don't repeat the words unless the user wants them in writing.", nil
			},
		})
	}
	if g.mg.Has(ensemble.ToolSeeImage) && b.tool != ensemble.ToolSeeImage && !b.sees {
		out = append(out, &agent.FuncTool{
			ToolName: agentLookAtImage,
			Desc: "Look at an image the user attached and answer a question about it with the vision model. Images " +
				"are numbered in the order they appear in the conversation; the default is the latest.",
			Params: schema(`"question":{"type":"string","description":"what to find out, e.g. \"Describe this image in detail.\""},`+
				`"image":{"type":"integer","description":"which image, 1 = the first; default the latest"}`, "question"),
			Fn: func(ctx context.Context, args json.RawMessage) (string, error) {
				var a struct {
					Question string  `json:"question"`
					Image    flexInt `json:"image"`
				}
				if err := toolArgs(args, &a); err != nil {
					return "", err
				}
				ts := stateOf(ctx)
				if len(ts.images) == 0 {
					return "", errors.New("there is no image in this conversation")
				}
				n := int(a.Image)
				if n <= 0 || n > len(ts.images) {
					n = len(ts.images)
				}
				q := strings.TrimSpace(a.Question)
				if q == "" {
					q = "Describe this image in detail."
				}
				body := map[string]any{"max_tokens": 1024, "temperature": 0.2, "messages": []any{map[string]any{
					"role": "user", "content": []any{
						map[string]any{"type": "text", "text": q},
						map[string]any{"type": "image_url", "image_url": map[string]any{"url": ts.images[n-1]}},
					}}}}
				_, text, err := g.members.ChatText(ctx, ensemble.ToolSeeImage, body, ts.say)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Image %d: %s", n, stripThink(text)), nil
			},
		})
	}
	return out
}

// generate runs an image or video member for a tool call.
func (b *brain) generate(ctx context.Context, args json.RawMessage, kind string,
	gen func(context.Context, string, func(string)) (ensemble.Media, error)) (string, error) {
	var a struct {
		Prompt string `json:"prompt"`
	}
	if err := toolArgs(args, &a); err != nil {
		return "", err
	}
	prompt := strings.TrimSpace(a.Prompt)
	if prompt == "" {
		return "", fmt.Errorf("prompt is empty: say what the %s should show", kind)
	}
	ts := stateOf(ctx)
	m, err := gen(ctx, prompt, ts.say)
	if err != nil {
		return "", err
	}
	ts.add(m)
	return fmt.Sprintf("The %s is ready and is shown to the user with your reply. Don't write a link or markdown for it; "+
		"say in a sentence what you made.", kind), nil
}

// ---- the brain upstream ---------------------------------------------------------

// toolsRefusal reads a brain error that means "this model can't take tools".
// retry: answer this request without tools; permanent: stop offering them.
func toolsRefusal(status int, body []byte) (retry, permanent bool) {
	if status != http.StatusBadRequest && status != http.StatusInternalServerError && status != http.StatusNotImplemented {
		return false, false
	}
	m := strings.ToLower(errorText(body))
	if strings.Contains(m, "jinja") || strings.Contains(m, "not support") || strings.Contains(m, "unsupported") {
		return strings.Contains(m, "tool") || strings.Contains(m, "jinja"), true
	}
	return strings.Contains(m, "tool") || strings.Contains(m, "template"), false
}

// withoutTools drops the tool fields from a chat body.
func withoutTools(body []byte) []byte {
	var f map[string]json.RawMessage
	if json.Unmarshal(body, &f) != nil {
		return body
	}
	delete(f, "tools")
	delete(f, "tool_choice")
	delete(f, "parallel_tool_calls")
	out, _ := json.Marshal(f)
	return out
}

// upstream calls the brain, starting it first when needed (status lines go to
// the stream while it warms up). The brain is leased per call, so between
// steps it can be evicted to make room for the member a tool needs.
func (b *brain) upstream(ts *turnState) agent.Upstream {
	return func(ctx context.Context, body []byte) (*http.Response, error) {
		resp, err := b.g.members.ChatRaw(ctx, b.tool, body, ts.say)
		if err != nil || resp.StatusCode == http.StatusOK || !bytes.Contains(body, []byte(`"tools"`)) {
			return resp, err
		}
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		retry, permanent := toolsRefusal(resp.StatusCode, msg)
		if permanent {
			b.turnOff("the brain refused tools: " + errorText(msg))
		}
		if !retry {
			resp.Body = io.NopCloser(bytes.NewReader(msg))
			return resp, nil
		}
		b.g.logf("agent: the brain refused tools (%s); answering this request without them", errorText(msg))
		return b.g.members.ChatRaw(ctx, b.tool, withoutTools(body), ts.say)
	}
}

// ---- the request ------------------------------------------------------------------

// imageNotes replaces images in the conversation with numbered notes for a
// brain that can't see, and returns the images' URLs in order.
func imageNotes(msgs []map[string]any, canLook bool) ([]map[string]any, []string) {
	var urls []string
	out := make([]map[string]any, len(msgs))
	for i, m := range msgs {
		parts, ok := m["content"].([]any)
		if !ok || !hasImage(parts) {
			out[i] = m
			continue
		}
		var text []string
		for _, p := range parts {
			pm, _ := p.(map[string]any)
			switch pm["type"] {
			case "text":
				if s, _ := pm["text"].(string); s != "" {
					text = append(text, s)
				}
			case "image_url":
				u := ""
				switch iu := pm["image_url"].(type) {
				case string:
					u = iu
				case map[string]any:
					u, _ = iu["url"].(string)
				}
				if u == "" {
					continue
				}
				urls = append(urls, u)
				if canLook {
					text = append(text, fmt.Sprintf("[image %d attached: call look_at_image to see it]", len(urls)))
				} else {
					text = append(text, "[an image was attached, but no model here can see images]")
				}
			}
		}
		c := make(map[string]any, len(m))
		for k, v := range m {
			c[k] = v
		}
		c["content"] = strings.Join(text, "\n")
		out[i] = c
	}
	return out, urls
}

// agentWriter stands between the agent and the client. Streaming, it passes
// whole events to the client's stream (so status lines and media chunks
// written meanwhile never split one) and swallows the headers, which were
// already sent; an error status turns into an error event. Otherwise it keeps
// the reply so media can be added to it.
type agentWriter struct {
	s       *sse
	hdr     http.Header
	status  int
	pending []byte
	buf     bytes.Buffer
}

func (w *agentWriter) Header() http.Header { return w.hdr }

func (w *agentWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}

func (w *agentWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.s == nil || w.status != http.StatusOK || !strings.HasPrefix(w.hdr.Get("Content-Type"), "text/event-stream") {
		return w.buf.Write(p)
	}
	w.pending = append(w.pending, p...)
	if i := bytes.LastIndex(w.pending, []byte("\n\n")); i >= 0 {
		w.s.raw(string(w.pending[:i+2]))
		w.pending = append(w.pending[:0], w.pending[i+2:]...)
	}
	return len(p), nil
}

func (w *agentWriter) Flush() {}

// mediaMarkdown is the text that shows media in clients that read only the
// content: an image inline. (Video and audio ride in the media field.)
func mediaMarkdown(media []ensemble.Media) string {
	var b strings.Builder
	for _, m := range media {
		if m.Type == "image" {
			b.WriteString("\n\n![generated image](" + m.URL + ")")
		}
	}
	return b.String()
}

// run answers req with the agent. With s set the reply streams to s;
// otherwise the finished agentWriter holds it. narrow, when set, limits the
// tools offered.
func (b *brain) run(ctx context.Context, req *ChatRequest, s *sse, narrow []string) (*agentWriter, *turnState) {
	ts := &turnState{}
	if s != nil {
		ts.progress = s.say
		ts.emit = func(m ensemble.Media) bool {
			delta := map[string]any{"media": []ensemble.Media{m}}
			if md := mediaMarkdown([]ensemble.Media{m}); md != "" {
				delta["content"] = md + "\n\n"
			}
			s.chunk(delta, nil)
			return true
		}
	}
	msgs := req.Messages
	if !b.sees {
		_, canLook := b.ag.Registry().Lookup(agentLookAtImage)
		msgs, ts.images = imageNotes(msgs, canLook)
	}
	body := make(map[string]any, len(req.Body)+1)
	for k, v := range req.Body {
		body[k] = v
	}
	body["messages"] = prepareMessages(msgs, b.g.persona.prompt(), true)
	if _, set := body["harness"]; !set && narrow != nil {
		body["harness"] = map[string]any{"tools": narrow}
	}
	raw, _ := json.Marshal(body)

	w := &agentWriter{s: s, hdr: http.Header{}}
	r := (&http.Request{Method: http.MethodPost, URL: &url.URL{Path: "/v1/chat/completions"}, Header: http.Header{}}).
		WithContext(context.WithValue(ctx, stateKey{}, ts))
	b.ag.ServeChat(w, r, raw, b.upstream(ts))
	ts.close()
	return w, ts
}

// errorOf reads an error reply the agent wrote.
func errorOf(status int, body []byte) error {
	msg := errorText(body)
	if msg == "" || msg == "no details" {
		msg = http.StatusText(status)
	}
	return &memberError{label: "the ensemble's brain", status: status, msg: msg}
}

// serveStream answers a streaming request on s.
func (b *brain) serveStream(ctx context.Context, s *sse, req *ChatRequest) {
	w, _ := b.run(ctx, req, s, nil)
	if len(w.pending) > 0 {
		s.raw(string(w.pending) + "\n\n")
	}
	switch {
	case w.status != http.StatusOK && w.status != 0:
		s.fail(errorOf(w.status, w.buf.Bytes()))
	case w.buf.Len() > 0: // the brain answered JSON to a streaming request (passthrough)
		var out map[string]any
		if json.Unmarshal(w.buf.Bytes(), &out) != nil {
			s.fail(errors.New("the brain sent a reply that isn't JSON"))
			return
		}
		s.chunk(map[string]any{"role": "assistant", "content": completionText(out)}, nil)
		s.chunk(map[string]any{}, "stop")
		s.done()
	}
}

// reply answers req whole: the completion the agent made, with the media.
func (b *brain) reply(ctx context.Context, req *ChatRequest, narrow []string) (map[string]any, Reply, error) {
	w, ts := b.run(ctx, req, nil, narrow)
	if w.status != http.StatusOK {
		return nil, Reply{}, errorOf(w.status, w.buf.Bytes())
	}
	var out map[string]any
	if err := json.Unmarshal(w.buf.Bytes(), &out); err != nil {
		return nil, Reply{}, errors.New("the brain sent a reply that isn't JSON")
	}
	return out, Reply{Content: completionText(out), Media: ts.media, Raw: out}, nil
}

// serveJSON answers a non-streaming request.
func (b *brain) serveJSON(ctx context.Context, w http.ResponseWriter, req *ChatRequest) {
	aw, ts := b.run(ctx, req, nil, nil)
	if aw.status != http.StatusOK {
		ct := aw.hdr.Get("Content-Type")
		if ct == "" {
			ct = "application/json"
		}
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(aw.status)
		_, _ = w.Write(aw.buf.Bytes())
		return
	}
	var out map[string]any
	if err := json.Unmarshal(aw.buf.Bytes(), &out); err != nil {
		writeErr(w, http.StatusBadGateway, "the brain sent a reply that isn't JSON")
		return
	}
	writeJSON(w, http.StatusOK, b.g.completion(Reply{Content: completionText(out) + mediaMarkdown(ts.media), Media: ts.media, Raw: out}))
}

// narrowed lists the brain's tools without the given ones.
func (b *brain) narrowed(without ...string) []string {
	skip := map[string]bool{}
	for _, t := range without {
		skip[t] = true
	}
	out := []string{}
	for _, t := range b.tools() {
		if !skip[t] {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}
