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
// image remains a drop-in OpenAI-compatible server. The proxy, the prompt
// injection and llama-server's flags live in internal/gate, which the native
// package launcher (cmd/llmpack) shares. Stdlib only, plus the stdlib-only
// internal/gate and internal/agent packages, which the builder stages beside it.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/seanbrill/local-llm/internal/agent"
	"github.com/seanbrill/local-llm/internal/gate"
)

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
	front := newGate(cfg, h)
	httpSrv := &http.Server{Addr: ":" + strconv.Itoa(cfg.publicPort), Handler: agent.NewHostGuard(front, nil)}
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
	l := gate.Llama{
		Model:           c.modelPath,
		Port:            c.upstreamPort,
		CtxSize:         c.ctxSize,
		Threads:         c.threads,
		Parallel:        c.parallel,
		MMProj:          c.mmprojPath,
		MMProjNoOffload: c.mmprojNoOffload,
		Modality:        c.modality,
		Extra:           strings.Fields(c.extraArgs),
	}
	if c.ngl > 0 {
		l.GPULayers = strconv.Itoa(c.ngl)
	}
	return l.Args()
}

// ---------------------------------------------------------------------------
// proxy + injection (internal/gate)
// ---------------------------------------------------------------------------

// newGate is the reverse proxy on the public port: system-prompt injection
// and, with a harness, the tool loop, in front of llama-server.
func newGate(c config, h *agent.Agent) http.Handler {
	g, err := gate.New(gate.Options{
		Upstream:     "http://127.0.0.1:" + strconv.Itoa(c.upstreamPort),
		SystemPrompt: c.systemPrompt,
		InjectMode:   c.injectMode,
		Harness:      h,
		Host:         "llmgate",
	})
	if err != nil {
		log.Fatalf("gate: %v", err)
	}
	return g
}

// loadHarness reads the baked harness config (plus HARNESS_* overrides) and
// builds the agent. It returns nil when the image has no harness.
func loadHarness(path string, getenv func(string) string) (*agent.Agent, error) {
	return gate.LoadHarness(path, getenv, "llmgate", log.Printf)
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
