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
// Everything else is proxied verbatim (including SSE token streaming), so the
// image remains a drop-in OpenAI-compatible server. Stdlib only.
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
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("[llmgate] ")

	cfg := loadConfig()

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

	// 2. Reverse proxy with system-prompt injection.
	gate := newGate(cfg)
	httpSrv := &http.Server{Addr: ":" + strconv.Itoa(cfg.publicPort), Handler: gate}
	go func() {
		log.Printf("listening on :%d -> upstream :%d (inject=%q, prompt=%t)",
			cfg.publicPort, cfg.upstreamPort, cfg.injectMode, cfg.systemPrompt != "")
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("proxy server error: %v", err)
		}
	}()

	err := <-childDone
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
}

func newGate(c config) *gate {
	target, _ := url.Parse("http://127.0.0.1:" + strconv.Itoa(c.upstreamPort))
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.FlushInterval = -1 // flush immediately so SSE streaming isn't buffered
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		// Most common during the model-load window: upstream not up yet.
		http.Error(w, "upstream not ready: "+err.Error(), http.StatusBadGateway)
	}
	return &gate{proxy: rp, systemPrompt: c.systemPrompt, injectMode: c.injectMode}
}

func (g *gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if g.systemPrompt != "" && r.Method == http.MethodPost &&
		r.URL.Path == "/v1/chat/completions" {
		if body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20)); err == nil {
			r.Body.Close()
			newBody := injectSystemPrompt(body, g.systemPrompt, g.injectMode)
			r.Body = io.NopCloser(bytes.NewReader(newBody))
			r.ContentLength = int64(len(newBody))
			r.Header.Set("Content-Length", strconv.Itoa(len(newBody)))
		}
	}
	g.proxy.ServeHTTP(w, r)
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
