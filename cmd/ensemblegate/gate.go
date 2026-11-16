package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/seanbrill/local-llm/internal/agent"
	"github.com/seanbrill/local-llm/internal/ensemble"
	"github.com/seanbrill/local-llm/internal/voice"
)

// Gate is the Conductor's HTTP surface: one OpenAI-compatible model in front
// of the ensemble's members.
//
//	GET  /health                    the Conductor is up
//	GET  /v1/models                 the ensemble, as one model
//	GET  /v1/harness                the brain's tools (agent routing), as llmgate reports them
//	POST /v1/chat/completions       the brain with its member tools (agent routing), or one member picked by the router
//	POST /v1/audio/transcriptions   speech to text (the STT member); /inference is an alias
//	POST /v1/audio/speech           text to speech in the persona's voice (the voice contract)
//	POST /ensemble/voice-chat       a spoken question in, a spoken answer out (STT -> chat -> TTS)
//	GET  /ensemble/status           members, their states, memory, persona
//	GET/POST /ensemble/config       the persona and voice the factory pushes
//	POST /ensemble/members          {tool, action: start|stop}
type Gate struct {
	mani    ensemble.Manifest
	mg      *Manager
	members *Members
	router  Router
	// brain is agent routing (nil when it can't run: brainOff says why).
	brain    *brain
	brainOff string
	persona  *personaState
	logf     func(string, ...any)
	// keepalive is how often a waiting stream gets a comment line, so clients
	// and proxies don't time out while a member warms up or media generates.
	keepalive time.Duration
}

// newGate builds the Conductor. harness is the baked agent config (built-in
// tools for the brain); nil offers the member tools only.
func newGate(mani ensemble.Manifest, eng Engine, harness *agent.Config, logf func(string, ...any)) *Gate {
	mg := newManager(mani, eng, logf)
	g := &Gate{mani: mani, mg: mg, members: newMembers(mg), persona: &personaState{}, logf: logf, keepalive: 8 * time.Second}
	h := Heuristic{has: mg.Has}
	g.router = h
	if mani.Routing == "tool-calling" && mg.Has(ensemble.ToolConductor) {
		var tools []string
		for _, t := range ensemble.Tools {
			if mg.Has(t) {
				tools = append(tools, t)
			}
		}
		g.router = Conductor{fallback: h, tools: tools, ask: g.askConductor}
	}
	if mani.Routing == "" || mani.Routing == ensemble.RoutingAgent {
		g.brain, g.brainOff = newBrain(g, harness)
		if g.brain == nil {
			logf("agent routing is off: %s; routing with the %s", g.brainOff, g.router.Name())
		}
	}
	return g
}

// agentOn reports whether the brain answers chat.
func (g *Gate) agentOn() bool { return g.brain != nil && g.brain.on() }

// routerStatus names the routing in effect and why.
func (g *Gate) routerStatus() (name, note string, tools []string) {
	switch {
	case g.agentOn():
		m, _ := g.mg.Member(g.brain.tool)
		return ensemble.RoutingAgent, memberLabel(m) + " is the brain; the other members are its tools", g.brain.tools()
	case g.brain != nil:
		g.brain.mu.Lock()
		why := g.brain.off
		g.brain.mu.Unlock()
		return g.router.Name(), "agent routing is off: " + why, nil
	case g.brainOff != "":
		return g.router.Name(), "agent routing is off: " + g.brainOff, nil
	}
	return g.router.Name(), "", nil
}

// askConductor asks the conductor model one short question.
func (g *Gate) askConductor(ctx context.Context, prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	_, text, err := g.members.ChatText(ctx, ensemble.ToolConductor, map[string]any{
		"model": "router", "max_tokens": 16, "temperature": 0,
		"messages": []map[string]any{{"role": "user", "content": prompt + " /no_think"}},
	}, nil)
	return text, err
}

// Handler is the HTTP mux.
func (g *Gate) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("/v1/models", g.handleModels)
	mux.HandleFunc("/v1/harness", g.handleHarness)
	mux.HandleFunc(agent.AdminPath, g.handleHarnessAdmin)
	mux.HandleFunc(agent.AdminPath+"/", g.handleHarnessAdmin)
	mux.HandleFunc("/v1/chat/completions", post(g.handleChat))
	mux.HandleFunc("/v1/audio/transcriptions", post(g.handleTranscriptions))
	mux.HandleFunc("/inference", post(g.handleTranscriptions)) // whisper.cpp's path, for clients that know it
	mux.HandleFunc("/v1/audio/speech", post(g.handleSpeech))
	mux.HandleFunc("/ensemble/voice-chat", post(g.handleVoiceChat))
	mux.HandleFunc("/ensemble/status", g.handleStatus)
	mux.HandleFunc("/ensemble/config", g.handleConfig)
	mux.HandleFunc("/ensemble/members", post(g.handleMembers))
	return mux
}

func post(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			writeErr(w, http.StatusMethodNotAllowed, "use POST")
			return
		}
		h(w, r)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr answers with an OpenAI-shaped error.
func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": map[string]any{"message": msg, "type": errType(code)}})
}

func errType(code int) string {
	if code >= 500 {
		return "server_error"
	}
	return "invalid_request_error"
}

// statusFor maps an error to an HTTP status.
func statusFor(err error) int {
	var me *missingError
	var ce *voice.CloneError
	var mem *memberError
	var br *badRequest
	switch {
	case errors.As(err, &me), errors.As(err, &ce), errors.As(err, &br):
		return http.StatusBadRequest
	case errors.As(err, &mem):
		return http.StatusBadGateway
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout
	}
	return http.StatusBadGateway
}

// readBody reads at most limit bytes.
func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, bool) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeErr(w, http.StatusRequestEntityTooLarge, "the request body is too large")
		} else {
			writeErr(w, http.StatusBadRequest, "can't read the request body")
		}
		return nil, false
	}
	return b, true
}

func (g *Gate) handleModels(w http.ResponseWriter, r *http.Request) {
	var tools []string
	for _, t := range ensemble.Tools {
		if g.mg.Has(t) {
			tools = append(tools, t)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": []map[string]any{{
		"id": g.mani.ID, "object": "model", "created": 0, "owned_by": "llm-factory",
		"local_llm": map[string]any{"kind": "ensemble", "name": g.mani.Name, "tools": tools},
	}}})
}

// handleHarness answers GET /v1/harness like llmgate: the tools the brain
// can call, or "enabled": false.
func (g *Gate) handleHarness(w http.ResponseWriter, r *http.Request) {
	info := agent.DisabledInfo("ensemblegate")
	if g.agentOn() {
		info = g.brain.ag.Info()
	}
	writeJSON(w, http.StatusOK, info)
}

// handleHarnessAdmin serves the brain's MCP admin API (agent.AdminHandler).
func (g *Gate) handleHarnessAdmin(w http.ResponseWriter, r *http.Request) {
	if g.brain == nil {
		writeErr(w, http.StatusNotFound, "this ensemble has no brain to give MCP tools to")
		return
	}
	g.brain.ag.AdminHandler().ServeHTTP(w, r)
}

func (g *Gate) handleStatus(w http.ResponseWriter, r *http.Request) {
	members, used := g.mg.Status()
	router, note, tools := g.routerStatus()
	writeJSON(w, http.StatusOK, ensemble.Status{
		ID: g.mani.ID, Name: g.mani.Name, Router: router, RouterNote: note, AgentTools: tools,
		BudgetGB: g.mani.VRAMBudgetGB, UsedGB: used, Persona: g.persona.status(), Members: members,
	})
}

func (g *Gate) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, g.persona.status())
	case http.MethodPost, http.MethodPut:
		b, ok := readBody(w, r, 16<<20)
		if !ok {
			return
		}
		var c ensemble.Config
		if err := json.Unmarshal(b, &c); err != nil {
			writeErr(w, http.StatusBadRequest, "the config isn't valid JSON: "+err.Error())
			return
		}
		if err := validateConfig(&c); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		g.persona.set(c)
		st := g.persona.status()
		g.logf("persona set: %q, voice %q", st.Name, st.Voice)
		writeJSON(w, http.StatusOK, st)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "use GET or POST")
	}
}

func (g *Gate) handleMembers(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody(w, r, 1<<20)
	if !ok {
		return
	}
	var req struct {
		Tool   string `json:"tool"`
		Action string `json:"action"`
	}
	if err := json.Unmarshal(b, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "send {tool, action}")
		return
	}
	var err error
	switch req.Action {
	case "start":
		err = g.mg.Start(r.Context(), req.Tool)
	case "stop":
		err = g.mg.Stop(r.Context(), req.Tool)
	default:
		writeErr(w, http.StatusBadRequest, "action is start or stop")
		return
	}
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- chat -----------------------------------------------------------------

// Reply is a finished answer: text plus generated media.
type Reply struct {
	Content string
	Media   []ensemble.Media
	Raw     map[string]any // the member's own completion, when chat answered
}

func toMsgs(v any) []map[string]any {
	arr, _ := v.([]any)
	out := make([]map[string]any, 0, len(arr))
	for _, m := range arr {
		if mm, ok := m.(map[string]any); ok {
			out = append(out, mm)
		}
	}
	return out
}

func (g *Gate) handleChat(w http.ResponseWriter, r *http.Request) {
	raw, ok := readBody(w, r, 64<<20)
	if !ok {
		return
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "the request body must be a JSON object")
		return
	}
	req := &ChatRequest{Body: body, Messages: toMsgs(body["messages"])}
	req.Stream, _ = body["stream"].(bool)
	if len(req.Messages) == 0 {
		writeErr(w, http.StatusBadRequest, "messages is empty")
		return
	}
	if err := g.hearAudio(r.Context(), req); err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	if g.agentOn() {
		g.logf("chat -> agent (brain: %s)", g.brain.tool)
		w.Header().Set("X-Ensemble-Route", ensemble.RoutingAgent)
		if req.Stream {
			s := startSSE(w, g.mani.ID)
			ctx, stop := context.WithCancel(r.Context())
			defer stop()
			go s.keepAlive(ctx, g.keepalive)
			g.brain.serveStream(ctx, s, req)
			return
		}
		g.brain.serveJSON(r.Context(), w, req)
		return
	}
	d := g.router.Route(r.Context(), req)
	g.logf("chat -> %s (%s)", d.Tool, d.Why)
	w.Header().Set("X-Ensemble-Route", d.Tool)
	if req.Stream {
		g.streamAnswer(w, r, req, d)
		return
	}
	reply, err := g.answer(r.Context(), req, d, nil)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, g.completion(reply))
}

// chatBody is the client's body for the chat or vision member, with the
// persona and cleaned history.
func (g *Gate) chatBody(req *ChatRequest, tool string) map[string]any {
	body := make(map[string]any, len(req.Body))
	for k, v := range req.Body {
		body[k] = v
	}
	vision := tool == ensemble.ToolSeeImage
	if m, ok := g.mg.Member(tool); ok && m.Modality == "vision" {
		vision = true
	}
	body["messages"] = prepareMessages(req.Messages, g.persona.prompt(), vision)
	return body
}

// caption is the text that goes with generated media.
func caption(m ensemble.Media) string {
	switch m.Type {
	case "image":
		return "Here's the image.\n\n![generated image](" + m.URL + ")"
	case "video":
		return "Here's the video."
	}
	return ""
}

// answer produces a whole reply (no streaming).
func (g *Gate) answer(ctx context.Context, req *ChatRequest, d Decision, progress func(string)) (Reply, error) {
	switch d.Tool {
	case ensemble.ToolGenerateImage, ensemble.ToolGenerateVideo:
		gen := g.members.GenerateImage
		if d.Tool == ensemble.ToolGenerateVideo {
			gen = g.members.GenerateVideo
		}
		m, err := gen(ctx, d.Prompt, progress)
		if err != nil {
			return Reply{}, err
		}
		return Reply{Content: caption(m), Media: []ensemble.Media{m}}, nil
	case ensemble.ToolSpeak:
		if d.Prompt != "" { // "say 'hello there'": say exactly that
			m, err := g.members.Speak(ctx, g.persona.resolve(voice.SpeechRequest{Input: d.Prompt}), progress)
			if err != nil {
				return Reply{}, err
			}
			return Reply{Content: d.Prompt, Media: []ensemble.Media{m}}, nil
		}
		reply, err := g.chatReply(ctx, req, ensemble.ToolChat, progress)
		if err != nil {
			return Reply{}, err
		}
		if text := speakable(reply.Content); text != "" {
			if m, err := g.members.Speak(ctx, g.persona.resolve(voice.SpeechRequest{Input: text}), progress); err == nil {
				reply.Media = append(reply.Media, m)
			} else {
				reply.Content += "\n\n(I couldn't say it out loud: " + err.Error() + ")"
			}
		}
		return reply, nil
	default:
		return g.chatReply(ctx, req, d.Tool, progress)
	}
}

func (g *Gate) chatReply(ctx context.Context, req *ChatRequest, tool string, progress func(string)) (Reply, error) {
	raw, text, err := g.members.ChatText(ctx, tool, g.chatBody(req, tool), progress)
	if err != nil {
		return Reply{}, err
	}
	return Reply{Content: text, Raw: raw}, nil
}

// completion is a chat.completion for reply: the member's own when chat
// answered (usage and all), else one made here. Media ride in
// choices[0].message.media.
func (g *Gate) completion(reply Reply) map[string]any {
	out := reply.Raw
	if out == nil {
		out = map[string]any{
			"id": newID(), "object": "chat.completion", "created": time.Now().Unix(), "model": g.mani.ID,
			"choices": []any{map[string]any{"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": reply.Content}}},
		}
	}
	if choices, _ := out["choices"].([]any); len(choices) > 0 {
		if c, ok := choices[0].(map[string]any); ok {
			if msg, ok := c["message"].(map[string]any); ok {
				msg["content"] = reply.Content
				if len(reply.Media) > 0 {
					msg["media"] = reply.Media
				}
			}
		}
	}
	return out
}

func newID() string { return "ens-" + strconv.FormatInt(time.Now().UnixNano(), 36) }

// ---- streaming --------------------------------------------------------------

// sse writes an OpenAI-style event stream, plus ": ensemble: …" comment lines
// with what the ensemble is doing (every SSE client ignores comments; the
// factory's Playground shows them).
type sse struct {
	mu      sync.Mutex
	w       http.ResponseWriter
	fl      http.Flusher
	id      string
	model   string
	status  string
	written time.Time
}

func startSSE(w http.ResponseWriter, model string) *sse {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	s := &sse{w: w, fl: fl, id: newID(), model: model, written: time.Now()}
	s.raw(": ensemble stream\n\n")
	return s
}

func (s *sse) raw(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = io.WriteString(s.w, text)
	if s.fl != nil {
		s.fl.Flush()
	}
	s.written = time.Now()
}

// say reports what the ensemble is doing.
func (s *sse) say(text string) {
	s.mu.Lock()
	s.status = text
	s.mu.Unlock()
	s.raw(": ensemble: " + strings.ReplaceAll(text, "\n", " ") + "\n\n")
}

// keepAlive repeats the last status while nothing else is written.
func (s *sse) keepAlive(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.mu.Lock()
		idle, status := time.Since(s.written) >= every, s.status
		s.mu.Unlock()
		if idle {
			if status == "" {
				status = "Working…"
			}
			s.raw(": ensemble: " + status + "\n\n")
		}
	}
}

func (s *sse) chunk(delta map[string]any, finish any) {
	b, _ := json.Marshal(map[string]any{
		"id": s.id, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": s.model,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
	})
	s.raw("data: " + string(b) + "\n\n")
}

// fail sends an error event (OpenAI clients raise it) and ends the stream.
func (s *sse) fail(err error) {
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": err.Error(), "type": errType(statusFor(err))}})
	s.raw("data: " + string(b) + "\n\n")
	s.done()
}

func (s *sse) done() { s.raw("data: [DONE]\n\n") }

// streamAnswer answers as a stream: chat tokens as the member produces them;
// media and speech as one chunk when ready. Status comments and keep-alives
// flow while members warm up.
func (g *Gate) streamAnswer(w http.ResponseWriter, r *http.Request, req *ChatRequest, d Decision) {
	s := startSSE(w, g.mani.ID)
	ctx, stop := context.WithCancel(r.Context())
	defer stop()
	go s.keepAlive(ctx, g.keepalive)

	chatTool, speakAfter := d.Tool, false
	switch d.Tool {
	case ensemble.ToolChat, ensemble.ToolSeeImage:
	case ensemble.ToolSpeak:
		if d.Prompt != "" {
			g.streamWhole(ctx, s, req, d)
			return
		}
		chatTool, speakAfter = ensemble.ToolChat, true
	default:
		g.streamWhole(ctx, s, req, d)
		return
	}
	body := g.chatBody(req, chatTool)
	body["stream"] = true
	resp, l, err := g.members.Chat(ctx, chatTool, body, s.say)
	if err != nil {
		s.fail(err)
		return
	}
	defer l.Release()
	defer resp.Body.Close()
	var content string
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		content = relaySSE(resp.Body, s)
	} else { // a member that ignored "stream": send its whole reply as one chunk
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			s.fail(fmt.Errorf("%s sent a reply that isn't JSON", memberLabel(l.Member)))
			return
		}
		content = completionText(out)
		s.chunk(map[string]any{"role": "assistant", "content": content}, nil)
	}
	if speakAfter {
		if text := speakable(content); text != "" {
			if m, err := g.members.Speak(ctx, g.persona.resolve(voice.SpeechRequest{Input: text}), s.say); err == nil {
				s.chunk(map[string]any{"media": []ensemble.Media{m}}, nil)
			} else {
				s.chunk(map[string]any{"content": "\n\n(I couldn't say it out loud: " + err.Error() + ")"}, nil)
			}
		}
	}
	s.chunk(map[string]any{}, "stop")
	s.done()
}

// streamWhole streams a reply that arrives whole (media, verbatim speech).
func (g *Gate) streamWhole(ctx context.Context, s *sse, req *ChatRequest, d Decision) {
	reply, err := g.answer(ctx, req, d, s.say)
	if err != nil {
		s.fail(err)
		return
	}
	delta := map[string]any{"role": "assistant", "content": reply.Content}
	if len(reply.Media) > 0 {
		delta["media"] = reply.Media
	}
	s.chunk(delta, nil)
	s.chunk(map[string]any{}, "stop")
	s.done()
}

// relaySSE copies a member's event stream to s up to (not including) its
// [DONE], and returns the reply text it carried.
func relaySSE(body io.Reader, s *sse) string {
	br := bufio.NewReaderSize(body, 64<<10)
	var content strings.Builder
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "data:") {
				payload := strings.TrimSpace(t[5:])
				if payload == "[DONE]" {
					return content.String()
				}
				var c struct {
					Choices []struct {
						Delta struct {
							Content string `json:"content"`
						} `json:"delta"`
					} `json:"choices"`
				}
				if json.Unmarshal([]byte(payload), &c) == nil && len(c.Choices) > 0 {
					content.WriteString(c.Choices[0].Delta.Content)
				}
			}
			s.raw(line)
		}
		if err != nil {
			return content.String()
		}
	}
}

// ---- speech -----------------------------------------------------------------

// badRequest is a request the client must fix (400).
type badRequest struct{ msg string }

func (e *badRequest) Error() string { return e.msg }

// hearAudio turns the spoken parts of the last user message (OpenAI's
// input_audio content parts: {"data": base64, "format": "wav"}) into text with
// the speech-to-text member, so every route reads them. Audio in earlier
// messages becomes a short note: it was heard when it was sent.
func (g *Gate) hearAudio(ctx context.Context, req *ChatRequest) error {
	last := -1
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i]["role"] == "user" {
			last = i
			break
		}
	}
	changed := false
	for i, m := range req.Messages {
		parts, ok := m["content"].([]any)
		if !ok {
			continue
		}
		spoken := false
		for _, p := range parts {
			if pm, _ := p.(map[string]any); pm["type"] == "input_audio" {
				spoken = true
			}
		}
		if !spoken {
			continue
		}
		out := make([]any, 0, len(parts))
		for _, p := range parts {
			pm, _ := p.(map[string]any)
			if pm["type"] != "input_audio" {
				out = append(out, p)
				continue
			}
			if i != last {
				out = append(out, map[string]any{"type": "text", "text": "[a voice message]"})
				continue
			}
			ia, _ := pm["input_audio"].(map[string]any)
			data, _ := ia["data"].(string)
			format, _ := ia["format"].(string)
			audio, err := base64.StdEncoding.DecodeString(data)
			if err != nil || len(audio) == 0 {
				return &badRequest{"input_audio.data must be base64-encoded audio"}
			}
			if !g.mg.Has(ensemble.ToolTranscribe) {
				return &missingError{ensemble.ToolTranscribe}
			}
			if format == "" {
				format = "wav"
			}
			text, err := g.members.Transcribe(ctx, audio, "speech."+format, "", nil)
			if err != nil {
				return fmt.Errorf("speech to text failed: %w", err)
			}
			out = append(out, map[string]any{"type": "text", "text": text})
		}
		c := make(map[string]any, len(m))
		for k, v := range m {
			c[k] = v
		}
		c["content"] = out
		req.Messages[i] = c
		changed = true
	}
	if changed {
		req.Body["messages"] = anySlice(req.Messages)
	}
	return nil
}

// handleTranscriptions is OpenAI's /v1/audio/transcriptions (multipart: file,
// language, response_format), answered by the speech-to-text member.
func (g *Gate) handleTranscriptions(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "send the audio as multipart form data in the field \"file\"")
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "missing the audio \"file\"")
		return
	}
	audio, err := io.ReadAll(f)
	f.Close()
	if err != nil || len(audio) == 0 {
		writeErr(w, http.StatusBadRequest, "the audio file is empty")
		return
	}
	text, err := g.members.Transcribe(r.Context(), audio, hdr.Filename, r.FormValue("language"), nil)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	switch r.FormValue("response_format") {
	case "text":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, text)
	case "verbose_json":
		writeJSON(w, http.StatusOK, map[string]any{"task": "transcribe", "text": text})
	case "", "json":
		writeJSON(w, http.StatusOK, map[string]string{"text": text})
	default:
		writeErr(w, http.StatusBadRequest, "response_format: the ensemble answers json, verbose_json or text")
	}
}

// handleSpeech is OpenAI's /v1/audio/speech under the voice contract: the
// persona's voice unless the request names one; the audio streams through as
// the speech member produces it (WAV, whatever response_format asked).
func (g *Gate) handleSpeech(w http.ResponseWriter, r *http.Request) {
	b, ok := readBody(w, r, 16<<20)
	if !ok {
		return
	}
	var req voice.SpeechRequest
	if err := json.Unmarshal(b, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "the request body must be JSON: {model, input, voice, response_format, speed}")
		return
	}
	req = g.persona.resolve(req)
	body, mime, done, err := g.members.SpeakStream(r.Context(), req, nil)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	defer done()
	defer body.Close()
	w.Header().Set("Content-Type", mime)
	if req.IsClone() {
		w.Header().Set("X-Ensemble-Voice", "clone")
	} else if req.Voice != "" {
		w.Header().Set("X-Ensemble-Voice", "builtin:"+req.Voice)
	}
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
		}
		if rerr != nil {
			return
		}
	}
}

// VoiceReply is the answer to POST /ensemble/voice-chat.
type VoiceReply struct {
	Transcript  string           `json:"transcript"`
	Route       string           `json:"route"`
	Reply       voiceReplyBody   `json:"reply"`
	Audio       string           `json:"audio,omitempty"` // data URL of the spoken answer
	SpeechError string           `json:"speech_error,omitempty"`
	Timings     map[string]int64 `json:"timings"`
}

type voiceReplyBody struct {
	Content string           `json:"content"`
	Media   []ensemble.Media `json:"media"`
}

// handleVoiceChat runs the voice chain: the question's audio (multipart
// "file") to text with the speech-to-text member, the text (after "messages",
// the history as JSON) through the usual routing, and the reply to speech in
// the persona's voice. A failed last step still returns the text answer.
func (g *Gate) handleVoiceChat(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "send multipart form data: file (the spoken question) and messages (JSON history)")
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "missing the recorded question (\"file\")")
		return
	}
	audio, _ := io.ReadAll(f)
	f.Close()
	var history []map[string]any
	if s := strings.TrimSpace(r.FormValue("messages")); s != "" {
		var arr []any
		if err := json.Unmarshal([]byte(s), &arr); err != nil {
			writeErr(w, http.StatusBadRequest, "messages must be a JSON array of chat messages")
			return
		}
		history = toMsgs(arr)
	}
	ctx := r.Context()
	out := VoiceReply{Timings: map[string]int64{}}
	t0 := time.Now()
	text, err := g.members.Transcribe(ctx, audio, hdr.Filename, r.FormValue("language"), nil)
	out.Timings["stt_ms"] = time.Since(t0).Milliseconds()
	if err != nil {
		writeErr(w, statusFor(err), "speech to text failed: "+err.Error())
		return
	}
	out.Transcript = text
	if strings.Trim(text, " .[]()") == "" || strings.EqualFold(strings.Trim(text, " []()"), "BLANK_AUDIO") {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": map[string]any{"message": "no speech was heard in that recording: try again, a little closer to the mic"}, "transcript": text})
		return
	}
	msgs := append(history, map[string]any{"role": "user", "content": text})
	req := &ChatRequest{Body: map[string]any{"messages": anySlice(msgs)}, Messages: msgs}
	t1 := time.Now()
	var reply Reply
	if g.agentOn() {
		// The whole answer is spoken anyway, so the speak tool isn't offered.
		out.Route = ensemble.RoutingAgent
		g.logf("voice-chat -> agent (brain: %s)", g.brain.tool)
		_, reply, err = g.brain.reply(ctx, req, g.brain.narrowed(agentSpeak))
	} else {
		d := g.router.Route(ctx, req)
		out.Route = d.Tool
		g.logf("voice-chat -> %s (%s)", d.Tool, d.Why)
		reply, err = g.answer(ctx, req, d, nil)
	}
	out.Timings["chat_ms"] = time.Since(t1).Milliseconds()
	if err != nil {
		writeJSON(w, statusFor(err), map[string]any{"error": map[string]any{"message": err.Error()}, "transcript": text})
		return
	}
	out.Reply = voiceReplyBody{Content: reply.Content, Media: []ensemble.Media{}}
	for _, m := range reply.Media {
		if m.Type == "audio" && out.Audio == "" {
			out.Audio = m.URL // already spoken (a "say …" request)
			continue
		}
		out.Reply.Media = append(out.Reply.Media, m)
	}
	if out.Audio == "" && r.FormValue("speak") != "false" {
		t2 := time.Now()
		if say := speakable(reply.Content); say != "" {
			if m, err := g.members.Speak(ctx, g.persona.resolve(voice.SpeechRequest{Input: say}), nil); err == nil {
				out.Audio = m.URL
			} else {
				out.SpeechError = err.Error()
			}
		}
		out.Timings["tts_ms"] = time.Since(t2).Milliseconds()
	}
	writeJSON(w, http.StatusOK, out)
}

func anySlice(msgs []map[string]any) []any {
	out := make([]any, len(msgs))
	for i, m := range msgs {
		out[i] = m
	}
	return out
}
