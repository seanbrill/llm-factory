// Command llmgate is the in-container entrypoint shim (PID 1) for built images.
//
// It does two jobs:
//  1. Supervises the real `llama-server` process (assembles its flags from env,
//     pipes its logs, and forwards shutdown signals).
//  2. Acts as a reverse proxy on the public port that injects a baked-in system
//     prompt into OpenAI /v1/chat/completions requests — this is how the GUI's
//     optional "initialization prompt" specializes a model's startup behavior
//     for every client, without that client needing to know the prompt.
//
// When the image has a baked harness (HARNESS_FILE, normally
// /etc/llmgate/harness.json, non-empty), chat completions also go through the
// agent loop of internal/agent: the model gets tools (web search, page
// reading, workspace files, skills, optionally a shell) that run here, inside
// the container. GET /v1/harness describes the harness (or says there is none).
//
// Everything else is proxied verbatim (including SSE token streaming), so the
// image remains a drop-in OpenAI-compatible server. Stdlib only, plus the
// stdlib-only internal/agent package, which the builder stages beside it.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/seanbrill/local-llm/internal/agent"
)

// maxChatBody caps a chat-completions body read for prompt injection or the
// harness (images in vision requests make bodies large).
const maxChatBody = agent.MaxRequestBytes

func main() {
	log.SetFlags(0)
	log.SetPrefix("[llmgate] ")

	cfg := loadConfig()

	// The harness is loaded first, so a bad HARNESS_* setting stops the
	// container with a clear message instead of serving without tools.
	h, err := loadHarness(cfg.harnessFile, os.Getenv)
	if err != nil {
		log.Fatalf("harness: %v", err)
	}

	// 1. Start llama-server as a child bound to the internal upstream port.
	child := exec.Command(cfg.llamaBin, cfg.llamaArgs()...)
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	log.Printf("starting: %s %s", cfg.llamaBin, strings.Join(cfg.llamaArgs(), " "))
	if err := child.Start(); err != nil {
		log.Fatalf("failed to start llama-server: %v", err)
	}

	// Forward termination signals to the child for a clean shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		s := <-sigCh
		log.Printf("received %s, forwarding to llama-server", s)
		_ = child.Process.Signal(syscall.SIGTERM)
	}()

	// If the child exits, so do we (with its status).
	childDone := make(chan error, 1)
	go func() { childDone <- child.Wait() }()

	// 2. Reverse proxy with system-prompt injection (and the harness loop).
	gate := newGate(cfg, h)
	httpSrv := &http.Server{Addr: ":" + strconv.Itoa(cfg.publicPort), Handler: gate}
	go func() {
		log.Printf("listening on :%d -> upstream :%d (inject=%q, prompt=%t, harness=%t)",
			cfg.publicPort, cfg.upstreamPort, cfg.injectMode, cfg.systemPrompt != "", h != nil)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("proxy server error: %v", err)
		}
	}()

	err = <-childDone
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		log.Fatalf("llama-server exited: %v", err)
	}
}

// ---------------------------------------------------------------------------
// config
// ---------------------------------------------------------------------------

type config struct {
	llamaBin        string
	modelPath       string
	publicPort      int
	upstreamPort    int
	ctxSize         int
	threads         int
	parallel        int
	ngl             int    // GPU layers; 0 = CPU only
	mmprojPath      string // vision projector path; "" or an empty file = text model
	mmprojNoOffload bool   // keep the projector on CPU (Vulkan VLM caveat)
	modality        string // text|code|reasoning|vision|embedding (drives runtime flags)
	extraArgs       string // optional space-separated passthrough
	systemPrompt    string
	injectMode      string // "missing" | "always"
	harnessFile     string // baked harness.json; "" or an empty file = no harness
}

func loadConfig() config {
	c := config{
		llamaBin:        env("LLAMA_BIN", "llama-server"),
		modelPath:       env("MODEL_PATH", "/models/model.gguf"),
		publicPort:      envInt("PORT", 8080),
		upstreamPort:    envInt("UPSTREAM_PORT", 8081),
		ctxSize:         envInt("CTX_SIZE", 4096),
		threads:         envInt("THREADS", defaultThreads()),
		parallel:        envInt("PARALLEL", 1),
		ngl:             envInt("NGL", 0),
		mmprojPath:      env("MMPROJ_PATH", ""),
		mmprojNoOffload: envInt("MMPROJ_NO_OFFLOAD", 0) != 0,
		modality:        env("MODALITY", "text"),
		extraArgs:       os.Getenv("EXTRA_ARGS"),
		injectMode:      env("INJECT_MODE", "missing"),
		harnessFile:     os.Getenv("HARNESS_FILE"),
	}
	// The baked initialization prompt: inline env wins, else a file.
	c.systemPrompt = strings.TrimSpace(os.Getenv("SYSTEM_PROMPT"))
	if c.systemPrompt == "" {
		if f := os.Getenv("SYSTEM_PROMPT_FILE"); f != "" {
			if data, err := os.ReadFile(f); err == nil {
				c.systemPrompt = strings.TrimSpace(string(data))
			}
		}
	}
	return c
}

func (c config) llamaArgs() []string {
	args := []string{
		"--model", c.modelPath,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(c.upstreamPort),
		"--ctx-size", strconv.Itoa(c.ctxSize),
		"--threads", strconv.Itoa(c.threads),
		"--threads-batch", strconv.Itoa(c.threads),
		"--parallel", strconv.Itoa(c.parallel),
		"--cont-batching",
		"--jinja",
		"--metrics",
	}
	if c.ngl > 0 {
		args = append(args, "--n-gpu-layers", strconv.Itoa(c.ngl))
	}
	// Embedding models serve /v1/embeddings instead of chat; --embedding switches
	// llama-server into that mode (pooling auto-detects from the model).
	if c.modality == "embedding" {
		args = append(args, "--embedding")
	}
	// Vision projector: only when a real (non-empty) mmproj was baked. Text models
	// bake a zero-byte placeholder, which we skip. On Vulkan the projector encode
	// is kept on CPU (--no-mmproj-offload) to dodge the known Venus VLM-encoder bug.
	if c.mmprojPath != "" {
		if fi, err := os.Stat(c.mmprojPath); err == nil && fi.Size() > 0 {
			args = append(args, "--mmproj", c.mmprojPath)
			if c.mmprojNoOffload {
				args = append(args, "--no-mmproj-offload")
			}
		}
	}
	if strings.TrimSpace(c.extraArgs) != "" {
		args = append(args, strings.Fields(c.extraArgs)...)
	}
	return args
}

// ---------------------------------------------------------------------------
// proxy + injection
// ---------------------------------------------------------------------------

type gate struct {
	proxy        *httputil.ReverseProxy
	systemPrompt string
	injectMode   string
	harness      *agent.Agent   // nil: no baked harness
	upstream     agent.Upstream // llama-server's chat completions, for the harness
}

func newGate(c config, h *agent.Agent) *gate {
	base := "http://127.0.0.1:" + strconv.Itoa(c.upstreamPort)
	target, _ := url.Parse(base)
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.FlushInterval = -1 // flush immediately so SSE streaming isn't buffered
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		// Most common during the model-load window: upstream not up yet.
		http.Error(w, "upstream not ready: "+err.Error(), http.StatusBadGateway)
	}
	// The harness talks to llama-server directly: no proxy settings and no
	// overall timeout (a long answer streams for minutes; the request's
	// context ends it).
	client := &http.Client{Transport: &http.Transport{Proxy: nil, MaxIdleConnsPerHost: 8, IdleConnTimeout: 90 * time.Second}}
	return &gate{
		proxy: rp, systemPrompt: c.systemPrompt, injectMode: c.injectMode,
		harness: h, upstream: agent.HTTPUpstream(client, base+"/v1/chat/completions"),
	}
}

// isChatPath reports whether a path is llama-server's chat-completions route
// (it serves it with and without the /v1 prefix).
func isChatPath(p string) bool { return p == "/v1/chat/completions" || p == "/chat/completions" }

func (g *gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/harness" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		g.serveHarnessInfo(w)
		return
	}
	if r.Method == http.MethodPost && isChatPath(r.URL.Path) && (g.systemPrompt != "" || g.harness != nil) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxChatBody))
		r.Body.Close()
		if err != nil {
			http.Error(w, "request body too large or unreadable: "+err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		if g.systemPrompt != "" {
			body = injectSystemPrompt(body, g.systemPrompt, g.injectMode)
		}
		if g.harness != nil {
			g.harness.ServeChat(w, r, body, g.upstream)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		r.Header.Set("Content-Length", strconv.Itoa(len(body)))
	}
	g.proxy.ServeHTTP(w, r)
}

// serveHarnessInfo answers GET /v1/harness: what the baked harness offers, so
// the factory (and any client) can tell this model runs its own tool loop.
func (g *gate) serveHarnessInfo(w http.ResponseWriter) {
	info := agent.DisabledInfo("llmgate")
	if g.harness != nil {
		info = g.harness.Info()
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(info)
}

// loadHarness reads the baked harness config (plus HARNESS_* overrides) and
// builds the agent. It returns nil when the image has no harness.
func loadHarness(path string, getenv func(string) string) (*agent.Agent, error) {
	cfg, ok, err := agent.Load(path, getenv)
	if err != nil || !ok {
		return nil, err
	}
	h, err := agent.New(cfg, agent.Options{
		Host: "llmgate",
		Logf: func(format string, args ...any) { log.Printf("[harness] "+format, args...) },
	})
	if err != nil {
		return nil, err
	}
	info := h.Info()
	for _, u := range info.Unavailable {
		log.Printf("harness: %s is unavailable: %s", u.Tool, u.Reason)
	}
	var names []string
	for _, t := range info.Tools {
		names = append(names, t.Name)
	}
	if len(names) == 0 {
		log.Printf("harness: none of the tools %v can run here; serving without the harness", cfg.Tools)
		return nil, nil
	}
	log.Printf("harness on: %d tools (%s), max %d steps, %s per tool", len(names), strings.Join(names, ", "),
		cfg.Limits.MaxSteps, cfg.Limits.ToolTimeout.D())
	return h, nil
}

// injectSystemPrompt prepends (or, in "always" mode, replaces) the system
// message in a chat-completions request body. On any parse error it returns the
// original body unchanged so a malformed request still reaches the server.
func injectSystemPrompt(body []byte, prompt, mode string) []byte {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	raw, ok := m["messages"].([]any)
	if !ok {
		return body
	}

	hasSystem := false
	for _, it := range raw {
		if msg, ok := it.(map[string]any); ok {
			if role, _ := msg["role"].(string); role == "system" {
				hasSystem = true
				break
			}
		}
	}

	sys := map[string]any{"role": "system", "content": prompt}
	switch mode {
	case "always":
		kept := make([]any, 0, len(raw))
		for _, it := range raw {
			if msg, ok := it.(map[string]any); ok {
				if role, _ := msg["role"].(string); role == "system" {
					continue // drop client-supplied system messages
				}
			}
			kept = append(kept, it)
		}
		m["messages"] = append([]any{sys}, kept...)
	default: // "missing": only inject when the client didn't send one
		if hasSystem {
			return body
		}
		m["messages"] = append([]any{sys}, raw...)
	}

	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// ---------------------------------------------------------------------------
// env helpers
// ---------------------------------------------------------------------------

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

// defaultThreads is the llama-server thread count when THREADS is unset: the
// number of CPUs the container actually sees. The Dockerfiles used to hardcode
// THREADS=4, which left most cores idle on multi-core hosts (e.g. an 8-vCPU
// macOS Podman machine ran a 14B at ~half speed). Set THREADS explicitly to
// override (e.g. to leave a core free for other work).
func defaultThreads() int {
	if n := runtime.NumCPU(); n > 0 {
		return n
	}
	return 4
}
