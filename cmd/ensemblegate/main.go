// Command ensemblegate is the Conductor — the entrypoint of an Ensemble image.
//
// It presents ONE OpenAI-compatible endpoint, and for each request it:
//  1. classifies the intent (image part → vision; "draw…" → image; "make a video…"
//     → video; "say…" → speech; otherwise chat), optionally via a tiny conductor
//     model doing tool-calling;
//  2. ensures the chosen specialist is running — starting it (and evicting the
//     least-recently-used others to stay under a VRAM budget) via the host engine;
//  3. routes the request to that specialist and returns the result.
//
// Specialists run as sibling containers (orchestrated mode) published on the host;
// the gate reaches them at <model_host>:<port>. Stdlib only + the `docker`/`podman`
// CLI. See docs/ENSEMBLE.md.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---- manifest (baked into the image) --------------------------------------

type Member struct {
	Tool     string  `json:"tool"`
	Modality string  `json:"modality"`
	Image    string  `json:"image"`
	Port     int     `json:"port"`
	VRAMGB   float64 `json:"vram_gb"`
}

type Manifest struct {
	Name         string   `json:"name"`
	PackageMode  string   `json:"package_mode"`
	Routing      string   `json:"routing"`
	Conductor    string   `json:"conductor"`
	Engine       string   `json:"engine"`
	Compute      string   `json:"compute"`
	VRAMBudgetGB float64  `json:"vram_budget_gb"`
	ModelHost    string   `json:"model_host"`
	Members      []Member `json:"members"`
}

var mani Manifest
var mgr *Manager

func main() {
	log.SetFlags(0)
	log.SetPrefix("[ensemblegate] ")
	loadManifest()
	mgr = NewManager(mani)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", handleChat)
	mux.HandleFunc("/v1/models", handleModels)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	port := env("PORT", "8080")
	log.Printf("Ensemble %q up on :%s — %d members, %s routing, %.0f GB budget, engine=%s host=%s",
		mani.Name, port, len(mani.Members), mani.Routing, mani.VRAMBudgetGB, mgr.engine, mani.ModelHost)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}

func loadManifest() {
	data, err := os.ReadFile(env("MANIFEST", "/ensemble/manifest.json"))
	if err != nil {
		log.Fatalf("read manifest: %v", err)
	}
	if err := json.Unmarshal(data, &mani); err != nil {
		log.Fatalf("parse manifest: %v", err)
	}
	if mani.ModelHost == "" {
		mani.ModelHost = "host.docker.internal"
	}
	if mani.Engine == "" {
		mani.Engine = "docker"
	}
	if mani.VRAMBudgetGB == 0 {
		mani.VRAMBudgetGB = 12
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// ---- Manager: specialist lifecycle within a VRAM budget -------------------

type Manager struct {
	mu      sync.Mutex
	mani    Manifest
	engine  string
	lastUse map[string]time.Time // tool -> last used (only tracked while running)
}

func NewManager(m Manifest) *Manager {
	return &Manager{mani: m, engine: m.Engine, lastUse: map[string]time.Time{}}
}

func (mg *Manager) find(tool string) *Member {
	for i := range mg.mani.Members {
		if mg.mani.Members[i].Tool == tool {
			return &mg.mani.Members[i]
		}
	}
	return nil
}

// ensureUp starts the specialist for tool if it isn't already running, evicting
// LRU members first so the VRAM budget holds. Embedded mode is a no-op (the
// specialist is a process already inside the image — a future path).
func (mg *Manager) ensureUp(ctx context.Context, tool string) (*Member, error) {
	m := mg.find(tool)
	if m == nil {
		return nil, fmt.Errorf("ensemble has no %q specialist", tool)
	}
	if mg.mani.PackageMode == "embedded" {
		return m, nil
	}
	mg.mu.Lock()
	defer mg.mu.Unlock()
	if _, ok := mg.lastUse[tool]; ok {
		mg.lastUse[tool] = time.Now()
		return m, nil
	}
	mg.evictForLocked(m.VRAMGB, tool)
	name := "ens-" + tool
	_ = mg.engineCmd(ctx, "rm", "-f", name).Run()
	args := []string{"run", "-d", "--name", name, "-p", fmt.Sprintf("127.0.0.1:%d:8080", m.Port)}
	if mg.mani.Compute == "cuda" {
		args = append(args, "--gpus", "all")
	} else if mg.mani.Compute == "vulkan" {
		args = append(args, "--device", "/dev/dri")
	}
	args = append(args, m.Image)
	if out, err := mg.engineCmd(ctx, args...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("start %s: %v: %s", tool, err, strings.TrimSpace(string(out)))
	}
	log.Printf("started %s (%s) on :%d", tool, m.Image, m.Port)
	if err := mg.waitHealthy(ctx, m.Port); err != nil {
		return nil, fmt.Errorf("%s never became ready: %w", tool, err)
	}
	mg.lastUse[tool] = time.Now()
	return m, nil
}

// evictForLocked stops least-recently-used running members until needGB fits under
// the budget (excluding the tool we're about to start). Caller holds mg.mu.
func (mg *Manager) evictForLocked(needGB float64, except string) {
	used := func() float64 {
		var s float64
		for t := range mg.lastUse {
			if mm := mg.find(t); mm != nil {
				s += mm.VRAMGB
			}
		}
		return s
	}
	for used()+needGB > mg.mani.VRAMBudgetGB {
		// pick the LRU running member
		var oldest string
		var oldestT time.Time
		for t, ts := range mg.lastUse {
			if t == except {
				continue
			}
			if oldest == "" || ts.Before(oldestT) {
				oldest, oldestT = t, ts
			}
		}
		if oldest == "" {
			return // nothing left to evict
		}
		_ = mg.engineCmd(context.Background(), "rm", "-f", "ens-"+oldest).Run()
		delete(mg.lastUse, oldest)
		log.Printf("evicted %s to free VRAM", oldest)
	}
}

func (mg *Manager) engineCmd(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, mg.engine, args...)
}

func (mg *Manager) waitHealthy(ctx context.Context, port int) error {
	url := fmt.Sprintf("http://%s:%d/health", mg.mani.ModelHost, port)
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(1500 * time.Millisecond)
	}
	return fmt.Errorf("health timeout")
}

func (mg *Manager) memberURL(m *Member, path string) string {
	return fmt.Sprintf("http://%s:%d%s", mg.mani.ModelHost, m.Port, path)
}

// ---- routing --------------------------------------------------------------

var (
	reVideo = regexp.MustCompile(`(?i)\b(generate|make|create|render|animate)\b.*\b(video|clip|animation|movie|gif)\b`)
	reImage = regexp.MustCompile(`(?i)\b(generate|draw|make|create|paint|render|design)\b.*\b(image|picture|photo|art|drawing|logo|illustration)\b`)
	reSpeak = regexp.MustCompile(`(?i)\b(say|speak|read aloud|voice this|tts|narrate)\b`)
)

// classify picks a tool from the request: an image part means vision, else the
// last user line's intent (video > image > speak), else chat.
func classify(messages []map[string]any) (tool, lastText string) {
	hasImage := false
	for _, m := range messages {
		if parts, ok := m["content"].([]any); ok {
			for _, p := range parts {
				if pm, ok := p.(map[string]any); ok {
					if t, _ := pm["type"].(string); t == "image_url" {
						hasImage = true
					}
				}
			}
		}
		if m["role"] == "user" {
			lastText = textOf(m["content"])
		}
	}
	switch {
	case hasImage:
		return "see_image", lastText
	case reVideo.MatchString(lastText):
		return "generate_video", lastText
	case reImage.MatchString(lastText):
		return "generate_image", lastText
	case reSpeak.MatchString(lastText):
		return "speak", lastText
	default:
		return "chat", lastText
	}
}

func textOf(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var b strings.Builder
		for _, p := range c {
			if pm, ok := p.(map[string]any); ok {
				if t, _ := pm["type"].(string); t == "text" {
					b.WriteString(fmt.Sprint(pm["text"]))
				}
			}
		}
		return b.String()
	}
	return ""
}

// ---- HTTP handlers --------------------------------------------------------

func handleModels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"object": "list", "data": []map[string]any{
		{"id": "ensemble", "object": "model", "owned_by": mani.Name},
	}})
}

func handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	var req map[string]any
	if err := json.Unmarshal(raw, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	msgs := toMsgs(req["messages"])
	tool, text := classify(msgs)
	tool = mgr.maybeToolCall(r.Context(), tool, text, msgs) // optional LLM tool-calling
	log.Printf("route: tool=%s", tool)

	m, err := mgr.ensureUp(r.Context(), tool)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}

	switch m.Modality {
	case "text", "vision":
		// True OpenAI passthrough (handles streaming).
		mgr.proxyChat(w, r, m, raw)
	case "image":
		mgr.genMedia(w, r, m, "/sdapi/v1/txt2img",
			map[string]any{"prompt": text, "steps": 8, "cfg_scale": 1.5}, "images", "image")
	case "video":
		mgr.genMedia(w, r, m, "/generate", map[string]any{"prompt": text}, "video", "video")
	case "tts":
		mgr.genMedia(w, r, m, "/v1/audio/speech",
			map[string]any{"input": text, "model": "tts", "voice": "default"}, "", "audio")
	default:
		writeErr(w, 500, "unhandled modality "+m.Modality)
	}
}

// proxyChat forwards the original OpenAI body to a chat/vision specialist and
// streams the response straight back.
func (mg *Manager) proxyChat(w http.ResponseWriter, r *http.Request, m *Member, body []byte) {
	upReq, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, mg.memberURL(m, "/v1/chat/completions"), bytes.NewReader(body))
	upReq.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{}).Do(upReq)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			_, _ = w.Write(buf[:n])
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			return
		}
	}
}

// genMedia drives a generative specialist (image/video/tts), wraps the produced
// media as a data URL, and returns it inside a normal chat-completion so the
// Ensemble stays a single OpenAI endpoint. arrayKey/scalarKey name the field the
// specialist returns; kind labels the media for the data URL.
func (mg *Manager) genMedia(w http.ResponseWriter, r *http.Request, m *Member, path string, body map[string]any, arrayKey, kind string) {
	jb, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Minute)
	defer cancel()
	upReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, mg.memberURL(m, path), bytes.NewReader(jb))
	upReq.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{}).Do(upReq)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		writeErr(w, resp.StatusCode, strings.TrimSpace(string(raw)))
		return
	}

	var dataURL string
	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		if kind == "video" {
			dataURL, _ = out["video"].(string)
		} else if arr, ok := out[arrayKey].([]any); ok && len(arr) > 0 {
			s, _ := arr[0].(string)
			if !strings.HasPrefix(s, "data:") {
				s = "data:image/png;base64," + s
			}
			dataURL = s
		}
	} else {
		// Raw bytes (e.g. TTS audio) — wrap them ourselves.
		mime := ct
		if mime == "" {
			mime = "audio/wav"
		}
		dataURL = "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw)
	}
	if dataURL == "" {
		writeErr(w, http.StatusBadGateway, "specialist returned no "+kind)
		return
	}
	writeChatCompletion(w, fmt.Sprintf("[%s]\n\n%s", kind, dataURL))
}

// maybeToolCall lets a conductor model override the heuristic when routing is
// tool-calling and a conductor image is configured. Best-effort: any failure
// falls back to the heuristic choice.
func (mg *Manager) maybeToolCall(ctx context.Context, heuristic, text string, _ []map[string]any) string {
	if mg.mani.Routing != "tool-calling" || mg.mani.Conductor == "" || strings.TrimSpace(text) == "" {
		return heuristic
	}
	// The conductor is itself a specialist (tool "_conductor"); ask it to pick.
	c, err := mg.ensureUp(ctx, "_conductor")
	if err != nil {
		return heuristic
	}
	tools := make([]string, 0, len(mg.mani.Members))
	for _, mm := range mg.mani.Members {
		if mm.Tool != "_conductor" {
			tools = append(tools, mm.Tool)
		}
	}
	prompt := "You are a router. Available tools: " + strings.Join(tools, ", ") +
		".\nReply with ONLY the single best tool name for this request:\n" + text
	body, _ := json.Marshal(map[string]any{
		"model": "router", "stream": false, "max_tokens": 12, "temperature": 0,
		"messages": []map[string]any{{"role": "user", "content": prompt}},
	})
	upReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, mg.memberURL(c, "/v1/chat/completions"), bytes.NewReader(body))
	upReq.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{}).Do(upReq)
	if err != nil {
		return heuristic
	}
	defer resp.Body.Close()
	var out struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil || len(out.Choices) == 0 {
		return heuristic
	}
	pick := strings.ToLower(strings.TrimSpace(out.Choices[0].Message.Content))
	for _, t := range tools {
		if strings.Contains(pick, t) {
			return t
		}
	}
	return heuristic
}

// ---- helpers --------------------------------------------------------------

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

func writeChatCompletion(w http.ResponseWriter, content string) {
	writeJSON(w, 200, map[string]any{
		"id": "ens-" + strconv.FormatInt(time.Now().UnixNano(), 36), "object": "chat.completion",
		"created": time.Now().Unix(), "model": "ensemble",
		"choices": []map[string]any{{"index": 0, "finish_reason": "stop",
			"message": map[string]any{"role": "assistant", "content": content}}},
	})
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": map[string]any{"message": msg}})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
