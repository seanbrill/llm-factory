// Package server exposes the local web GUI: a small JSON API plus an embedded
// vanilla-JS frontend for configuring, naming, building, running, and testing
// model images.
package server

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/seanbrill/local-llm/internal/agent"
	"github.com/seanbrill/local-llm/internal/builder"
	"github.com/seanbrill/local-llm/internal/catalog"
	"github.com/seanbrill/local-llm/internal/ensemble"
	"github.com/seanbrill/local-llm/internal/filefreak"
	"github.com/seanbrill/local-llm/internal/llm"
	"github.com/seanbrill/local-llm/internal/version"
	"github.com/seanbrill/local-llm/internal/voice"
)

//go:embed web
var webFS embed.FS

// Server wires the API handlers to the builder and catalog.
type Server struct {
	b         *builder.Builder
	cat       *catalog.Catalog
	mux       *routeMux
	auth      *Auth       // the access token every protected route needs (auth.go)
	modelHost string      // host where launched model containers are reachable
	hapi      *harnessAPI // the harness API (harness.go); its store holds the MCP registry

	q *buildQueue // queued/running/recent builds, buffered so the UI can reconnect after a refresh

	statsMu   sync.Mutex
	buildSecs map[string]float64 // compute -> last successful build duration (s)
	statsPath string             // where buildSecs is persisted

	personasMu sync.Mutex // guards reads/writes of config/personas.json

	ens *ensemble.Store // saved Ensemble (multimodal super-model) definitions

	voices *voice.Store // persona voice clips (config/voices, 0600; never leave this machine)

	// Ports known not to be an ensemble's Conductor (ensembleAt), so chat with
	// a plain model doesn't probe it every time.
	gatePortsMu sync.Mutex
	notGate     map[int]time.Time

	// Test hooks for the ensemble endpoints (nil: the builder's).
	imageLabelsFn func(ctx context.Context, engine, ref string) (map[string]string, error)
	containersFn  func(ctx context.Context) ([]map[string]any, error)

	mediaDir string // where generated clips/images are saved + served from

	ff    *filefreak.Archive // the FileFreak model archive (filefreak.go); nil if its config can't be read
	ffErr error

	// Cached global GPU VRAM usage (nvidia-smi via a throwaway container is slow,
	// so we cache for a few seconds across /api/resources polls).
	gpuMu    sync.Mutex
	gpuUsed  float64
	gpuTotal float64
	gpuOK    bool
	gpuAt    time.Time

	// Runtime tuning (tuning.go), made on first use.
	tuneOnce sync.Once
	tune     *tuneState

	// Saved prompts and their results (prompts.go), made on first use.
	promptOnce sync.Once
	promptSt   *promptStore
}

// New constructs the HTTP handler tree. modelHost is where model containers'
// published ports can be reached from this process — "127.0.0.1" when the
// factory runs natively, "host.docker.internal" when it runs in a container.
//
// webDir, if non-empty, serves the UI from that directory on disk instead of
// the embedded copy — enabling UI hot-reload in dev (edit + refresh, no
// rebuild). When empty, the embedded assets are used (production).
//
// New also sets up the access token (LoadAuth), creating config/factory-token
// on first start; a bad FACTORY_TOKEN or FACTORY_AUTH is an error.
func New(b *builder.Builder, cat *catalog.Catalog, modelHost, webDir string) (*Server, error) {
	var web http.Handler
	if webDir != "" {
		web = http.FileServer(http.Dir(webDir))
	} else {
		sub, err := fs.Sub(webFS, "web")
		if err != nil {
			return nil, err
		}
		web = http.FileServer(http.FS(sub))
	}
	if modelHost == "" {
		modelHost = "127.0.0.1"
	}
	auth, err := LoadAuth(b.ConfigDir)
	if err != nil {
		return nil, err
	}
	s := &Server{
		b: b, cat: cat, mux: newRouteMux(), auth: auth, modelHost: modelHost,
		q:         newBuildQueue(),
		statsPath: filepath.Join(b.BaseDir, "config", "build-stats.json"),
		ens:       ensemble.NewStore(b.BaseDir),
		voices:    voice.NewStore(b.ConfigDir),
		mediaDir:  filepath.Join(b.BaseDir, "media"),
	}
	s.loadStats()
	_ = os.MkdirAll(s.mediaDir, 0o755)
	// The static UI, plus the access link (/?token=…) that signs a browser in,
	// and the lock screen's calls. Handler() puts every route behind the token
	// check; publicRoutes (auth.go) lists the few that answer without it.
	s.mux.Handle("/", auth.serveUI(web))
	s.mux.Handle("/auth", auth.serveUI(web))
	s.mux.HandleFunc(authStatusRoute, auth.handleStatus)
	s.mux.HandleFunc(authLoginRoute, auth.handleLogin)
	s.mux.HandleFunc(authLogoutRoute, auth.handleLogout)
	// Generated clips/images are saved here and served by stable URL so they
	// persist across reloads (bind-mount ./media to keep them across restarts).
	s.mux.Handle("/api/media/", http.StripPrefix("/api/media/", http.FileServer(http.Dir(s.mediaDir))))
	s.mux.HandleFunc("/api/catalog", s.handleCatalog)
	s.mux.HandleFunc("/api/models", s.handleModels)
	s.mux.HandleFunc("/api/model/delete", s.handleModelDelete)
	s.mux.HandleFunc("/api/build", s.handleBuild)
	s.mux.HandleFunc("/api/build/state", s.handleBuildState)
	s.mux.HandleFunc("/api/builds", s.handleBuilds)
	s.mux.HandleFunc("/api/builds/state", s.handleBuildsState)
	s.mux.HandleFunc("/api/builds/cancel", s.handleBuildsCancel)
	s.mux.HandleFunc("/api/gpu/probe", s.handleGPUProbe)
	s.mux.HandleFunc("/api/builds/move", s.handleBuildsMove)
	s.mux.HandleFunc("/api/builds/retry", s.handleBuildsRetry)
	s.mux.HandleFunc("/api/build/preflight", s.handleBuildPreflight)
	s.mux.HandleFunc("/api/storage", s.handleStorage)
	s.mux.HandleFunc("/api/storage/cleanup", s.handleStorageCleanup)
	s.mux.HandleFunc("/api/images", s.handleImages)
	s.mux.HandleFunc("/api/image/delete", s.handleImageDelete)
	s.mux.HandleFunc("/api/image/download", s.handleImageDownload)
	s.mux.HandleFunc("/api/image/config", s.handleImageConfig)
	s.mux.HandleFunc("/api/run", s.handleRun)
	s.mux.HandleFunc("/api/personas", s.handlePersonas)
	s.mux.HandleFunc("/api/personas/delete", s.handlePersonaDelete)
	s.mux.HandleFunc("/api/ensembles", s.handleEnsembles)
	s.mux.HandleFunc("/api/ensembles/delete", s.handleEnsembleDelete)
	s.mux.HandleFunc("/api/ensemble/build", s.handleEnsembleBuild)
	s.mux.HandleFunc("/api/ensembles/status", s.handleEnsemblesStatus)
	s.mux.HandleFunc("/api/ensembles/run", s.handleEnsembleRun)
	s.mux.HandleFunc("/api/ensembles/stop", s.handleEnsembleStop)
	s.mux.HandleFunc("/api/ensembles/member", s.handleEnsembleMember)
	s.mux.HandleFunc("/api/ensembles/voice-chat", s.handleEnsembleVoiceChat)
	s.mux.HandleFunc("/api/voices", s.handleVoices)
	s.mux.HandleFunc("/api/voices/update", s.handleVoiceUpdate)
	s.mux.HandleFunc("/api/voices/delete", s.handleVoiceDelete)
	s.mux.HandleFunc("/api/voices/audio", s.handleVoiceAudio)
	s.mux.HandleFunc("/api/voices/transcribe", s.handleVoiceTranscribe)
	s.mux.HandleFunc("/api/containers", s.handleContainers)
	s.mux.HandleFunc("/api/container/logs", s.handleContainerLogs)
	s.mux.HandleFunc("/api/stop", s.handleStop)
	s.mux.HandleFunc("/api/chat", s.handleChat)
	s.mux.HandleFunc("/api/chat/stream", s.handleChatStream)
	s.mux.HandleFunc("/api/image/generate", s.handleImageGenerate)
	s.mux.HandleFunc("/api/video/generate", s.handleVideoGenerate)
	s.mux.HandleFunc("/api/transcribe", s.handleTranscribe)
	s.mux.HandleFunc("/api/tts", s.handleTTS)
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.HandleFunc("/readyz", s.handleReadyz)
	s.mux.HandleFunc("/api/sysinfo", s.handleSysInfo)
	s.mux.HandleFunc("/api/version", s.handleVersion)
	s.mux.HandleFunc("/api/resources", s.handleResources)
	s.mux.HandleFunc("/api/system/reclaim", s.handleReclaimMemory)
	s.registerHarness() // model gateway (/v1) + /api/harness (harness.go)
	s.openArchive()     // the FileFreak archive, asked before every weight download (filefreak.go)
	s.registerFileFreak()
	s.registerNative()  // native packages (native.go)
	s.registerTuning()  // runtime tuning, presets and the benchmark (tuning.go)
	s.registerPrompts() // saved prompts and their results (prompts.go)
	return s, nil
}

// Handler returns the root http.Handler: every route behind the access-token
// check (auth.go).
func (s *Server) Handler() http.Handler { return s.auth.Wrap(s.mux) }

// Auth is the access-token setup, for the startup log and the browser link.
func (s *Server) Auth() *Auth { return s.auth }

// routeMux is the server's http.ServeMux, remembering each pattern registered
// on it so the access-token tests can walk every route.
type routeMux struct {
	*http.ServeMux
	patterns []string
}

func newRouteMux() *routeMux { return &routeMux{ServeMux: http.NewServeMux()} }

func (m *routeMux) Handle(pattern string, h http.Handler) {
	m.patterns = append(m.patterns, pattern)
	m.ServeMux.Handle(pattern, h)
}

func (m *routeMux) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	m.Handle(pattern, http.HandlerFunc(h))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// engineErrPayload formats an engine error for the UI: an actionable lead message
// when we recognize the failure (e.g. engine-down, port-in-use), with the raw
// error kept as `detail`. Stops bare "exit status 125" from reaching the user.
func engineErrPayload(err error) map[string]string {
	raw := err.Error()
	if hint := builder.ClassifyEngineError(err); hint != "" {
		return map[string]string{"error": hint, "detail": raw}
	}
	return map[string]string{"error": raw}
}

func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	// Annotate each model with the compute targets its runtime can be built for,
	// so the UI only offers those (Piper TTS is CPU-only, image gen Vulkan-only).
	// Compiles says the build compiles a runtime from source, which needs
	// sysinfo's compile_min_gb on the engine that builds it.
	// CanHarness says the model can take a baked agent harness (tools).
	type item struct {
		catalog.Model
		Computes   []string `json:"computes"`
		Compiles   bool     `json:"compiles"`
		CanHarness bool     `json:"can_harness"`
	}
	out := make([]item, 0, len(s.cat.Models))
	compiles := map[string]bool{} // runtime|modality (which picks the family) -> Compiles
	for _, m := range s.cat.Models {
		key := m.Rt() + "|" + m.Mod()
		c, seen := compiles[key]
		if !seen {
			c = s.b.Compiles(m)
			compiles[key] = c
		}
		out = append(out, item{Model: m, Computes: s.b.Computes(m), Compiles: c, CanHarness: builder.CanHarness(m)})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleModels returns the catalog annotated with on-disk download status/size.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	type item struct {
		catalog.Model
		Downloaded bool    `json:"downloaded"`
		OnDiskGB   float64 `json:"on_disk_gb"`
	}
	out := make([]item, 0, len(s.cat.Models))
	for _, m := range s.cat.Models {
		dl, size := s.b.ModelOnDisk(m)
		out = append(out, item{Model: m, Downloaded: dl, OnDiskGB: float64(size) / 1e9})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleModelDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ModelID string `json:"model_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	m, ok := s.cat.Get(req.ModelID)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown model id: " + req.ModelID})
		return
	}
	if err := s.b.RemoveModel(m); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleImageDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Ref    string `json:"ref"`
		Engine string `json:"engine"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if req.Ref == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ref is required"})
		return
	}
	if err := s.b.RemoveImage(r.Context(), normalizeEngine(req.Engine), req.Ref, true); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleImages(w http.ResponseWriter, r *http.Request) {
	imgs, err := s.b.Images(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, engineErrPayload(err))
		return
	}
	// Tell the UI which configured engines are unreachable so a partial/empty
	// list can be explained instead of read as "nothing here".
	if down := s.b.EnginesDown(); len(down) > 0 {
		w.Header().Set("X-Engines-Down", strings.Join(down, ","))
	}
	writeJSON(w, http.StatusOK, imgs)
}

func (s *Server) handleContainers(w http.ResponseWriter, r *http.Request) {
	cs, err := s.b.Containers(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, engineErrPayload(err))
		return
	}
	if down := s.b.EnginesDown(); len(down) > 0 {
		w.Header().Set("X-Engines-Down", strings.Join(down, ","))
	}
	writeJSON(w, http.StatusOK, cs)
}

// handleSysInfo returns each engine machine's RAM/CPU/GPU (plus a legacy summary
// and the build form's defaults) so the UI can show whether a model fits where
// it will build and run.
func (s *Server) handleSysInfo(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.b.SysInfo(ctx))
}

// handleVersion reports which build is running (shown in the sidebar) so two
// machines can confirm they run the same code.
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, version.Get())
}

// handleHealthz is a liveness probe: the factory process is up. Always 200.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReadyz is a readiness probe: at least one container engine is reachable.
// Returns the per-engine server version (empty = unreachable) so the UI can show
// a precise degraded banner instead of cryptic failures on every call.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	versions := s.b.EngineVersions(ctx)
	ready := false
	for _, v := range versions {
		if v != "" {
			ready = true
			break
		}
	}
	code := http.StatusOK
	if !ready {
		code = http.StatusServiceUnavailable
	}
	body := map[string]any{"ready": ready}
	// /readyz answers without the token (it's a probe). Engine versions are
	// for signed-in callers only: they tell anyone else what to attack.
	if s.auth.Authorized(r) {
		body["engines"] = versions
	}
	writeJSON(w, code, body)
}

// handleContainerLogs returns the tail of a container's logs so a model that
// crashed right after Run can be diagnosed instead of silently vanishing.
func (s *Server) handleContainerLogs(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "id query param is required", http.StatusBadRequest)
		return
	}
	tail, _ := strconv.Atoi(r.URL.Query().Get("tail"))
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	logs, err := s.b.ContainerLogs(ctx, normalizeEngine(r.URL.Query().Get("engine")), id, tail)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, engineErrPayload(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"logs": logs})
}

// handleBuild queues a build and returns immediately with {id, position}. It runs
// detached on its engine's worker (after the jobs ahead of it there); its log
// lines + status are buffered in the queue so the UI can poll /api/builds/state
// and reconnect after a refresh.
func (s *Server) handleBuild(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ModelID      string  `json:"model_id"`
		ImageName    string  `json:"image_name"`
		Tag          string  `json:"tag"`
		Engine       string  `json:"engine"`
		Compute      string  `json:"compute"`
		SystemPrompt string  `json:"system_prompt"`
		InjectMode   string  `json:"inject_mode"`
		CtxSize      int     `json:"ctx_size"`
		MemoryGB     float64 `json:"memory_gb"`
		Route        string  `json:"route"`
		Autostart    bool    `json:"autostart"`
		// Harness bakes the agent harness: a harness.json object, true for
		// the defaults, or false/null/absent for none.
		Harness json.RawMessage `json:"harness"`
		// Tuning bakes runtime tuning (docs/TUNING.md): a Tuning object, or
		// {"preset": "speed"} to size one for the engine. ctx_size still
		// works on its own, and fills in a tuning that sets no context.
		Tuning json.RawMessage `json:"tuning"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	harnessCfg, err := parseHarnessOption(req.Harness)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	model, ok := s.cat.Get(req.ModelID)
	if !ok {
		http.Error(w, "unknown model id: "+req.ModelID, http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.ImageName) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "image name is required"})
		return
	}
	if harnessCfg != nil {
		// Refuse now rather than fail the build once it starts.
		if err := builder.HarnessModelError(model); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}

	engine := normalizeEngine(req.Engine)
	compute := normalizeCompute(req.Compute)
	route := sanitizeRoute(req.Route)
	tune, draft, err := s.buildTuning(r.Context(), req.Tuning, model, engine, compute, req.CtxSize)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	opts := builder.BuildOptions{
		Model:        model,
		ImageName:    req.ImageName,
		Tag:          req.Tag,
		Engine:       engine,
		Compute:      compute,
		ExportTar:    true, // per project config: always export a .tar to ./images
		SystemPrompt: req.SystemPrompt,
		InjectMode:   req.InjectMode,
		CtxSize:      req.CtxSize,
		MemoryGB:     req.MemoryGB,
		Route:        route,
		Autostart:    req.Autostart,
		Harness:      harnessCfg,
		Tuning:       tune,
		Draft:        draft,
	}

	cfg := buildConfig{
		ModelID: req.ModelID, ImageName: req.ImageName, Tag: req.Tag,
		Engine: engine, Compute: compute, SystemPrompt: req.SystemPrompt, InjectMode: req.InjectMode,
		CtxSize: req.CtxSize, MemoryGB: req.MemoryGB, Route: route, Autostart: req.Autostart,
		Harness: harnessCfg, Tuning: tune,
	}
	// The job runs on a context of its own (not the request's), so a client
	// refresh/disconnect can't cancel it; POST /api/builds/cancel can.
	job := &buildJob{
		kind: "model", lane: engine, ref: opts.Ref(), name: model.Name, cfg: cfg,
		eta: int(s.etaFor(compute)),
		run: func(ctx context.Context, log builder.LogFunc) error {
			start := time.Now()
			o := opts
			o.OnPlan = reportBuildEngine(ctx)
			if err := s.b.Build(ctx, o, log); err != nil {
				return err
			}
			s.recordBuild(compute, time.Since(start).Seconds())
			return nil
		},
	}
	pos, err := s.q.enqueue(job)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	enqueueResponse(w, job, pos)
}

// ---- build-duration stats (for the UI's ETA) ------------------------------

func (s *Server) loadStats() {
	s.buildSecs = map[string]float64{}
	if data, err := os.ReadFile(s.statsPath); err == nil {
		_ = json.Unmarshal(data, &s.buildSecs)
	}
}

// etaFor returns the last measured whole-build duration (seconds) for a compute
// type, or 0 if none is recorded yet. The UI supplies a default estimate when
// this is 0, so default-tuning stays a UI-only concern (no backend restart).
func (s *Server) etaFor(compute string) float64 {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	return s.buildSecs[compute]
}

func (s *Server) recordBuild(compute string, secs float64) {
	s.statsMu.Lock()
	s.buildSecs[compute] = secs
	data, _ := json.Marshal(s.buildSecs)
	s.statsMu.Unlock()
	_ = os.MkdirAll(filepath.Dir(s.statsPath), 0o755)
	_ = os.WriteFile(s.statsPath, data, 0o644)
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Ref     string `json:"ref"`
		Port    int    `json:"port"`
		Engine  string `json:"engine"`
		Compute string `json:"compute"`
		// Optional run-time init-prompt override (no rebuild). When non-empty,
		// llmgate uses these instead of the baked prompt for this instance.
		SystemPrompt string `json:"system_prompt"`
		InjectMode   string `json:"inject_mode"`
		// Force bypasses the resource guardrail ("Run anyway").
		Force bool `json:"force"`
		// Workspace is the host folder an image with a baked harness gets at
		// /workspace ("" = the factory's workspace/ folder). See runWorkspace.
		Workspace string `json:"workspace"`
		// Tuning overrides the image's runtime tuning for this run (no
		// rebuild), merged on tuning_base: "saved" (default: baked < saved
		// < tuning) or "baked" (baked < tuning). See docs/TUNING.md.
		Tuning     json.RawMessage `json:"tuning"`
		TuningBase string          `json:"tuning_base"`
		// MCP names registered MCP servers (Harness page) the model's
		// harness gets (HARNESS_MCP). See mcp.go.
		MCP []string `json:"mcp"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// Inspect on the engine the image row reported (falls back to docker for "").
	engine := normalizeEngine(req.Engine)
	// Read the image's baked deploy settings (engine, compute, memory, route,
	// autostart) so a CUDA image always runs with --gpus all, a podman-built image
	// runs on podman, and the URL/limits/restart are applied as built.
	labels, _ := s.b.ImageLabels(r.Context(), engine, req.Ref)
	if e := labels["local-llm.engine"]; e != "" {
		engine = normalizeEngine(e)
	}
	compute := labels["local-llm.compute"]
	if compute == "" {
		compute = req.Compute
	}
	memGB, _ := strconv.ParseFloat(labels["local-llm.memory"], 64)
	route := labels["local-llm.route"]
	autostart := labels["local-llm.autostart"] == "true"
	orchestrator := labels["local-llm.kind"] == "ensemble" // mounts the docker socket
	workspace, err := s.runWorkspace(labels, req.Workspace)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// Runtime tuning (tuning.go): nil when neither the image, the saved
	// settings nor the request set one, so an untuned run is unchanged.
	tuned, err := s.runTuningFor(r.Context(), req.Ref, engine, req.Tuning, req.TuningBase)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	runHarness, mcpNotes, err := s.runHarness(labels, req.MCP, engine)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	// Resource guardrail: refuse to start a model that would over-subscribe VRAM
	// or RAM given what's already running (a crash risk), unless the caller forces
	// it ("Run anyway"). 409 + a structured payload so the UI can offer the choice.
	// First: can this engine's machine hold the model at all? (A Podman machine
	// can be far smaller than the Mac.) Blocks only an obvious misfit; a tight
	// or mmap-able one comes back as a warning on the started run.
	var warning string
	if !req.Force {
		block, msg := s.engineFits(r.Context(), labels["local-llm.model"], engine, compute)
		if tuned != nil {
			// The tuning's estimate (context, cache, draft…) replaces the
			// weights-plus-2-GB rule of thumb.
			block, msg = tunedFit(tuned, compute)
		}
		if block {
			writeJSON(w, http.StatusConflict, map[string]any{"error": msg, "code": "engine_memory", "needs_force": true})
			return
		}
		warning = msg
		if msg, code := s.runFits(r.Context(), labels["local-llm.model"], compute); msg != "" {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": msg, "code": code, "needs_force": true,
				"budget": s.resourceBudget(r.Context()),
			})
			return
		}
	}

	runOpts := builder.RunOptions{
		Ref: req.Ref, HostPort: req.Port, Engine: engine, Compute: compute,
		MemoryGB: memGB, Route: route, Autostart: autostart, Orchestrator: orchestrator,
		SystemPrompt: req.SystemPrompt, InjectMode: req.InjectMode, Workspace: workspace,
		Harness: runHarness,
	}
	if tuned != nil {
		runOpts.Env = append(runOpts.Env, tuned.env...)
		runOpts.Labels = append(runOpts.Labels, tuned.label)
	}
	id, err := s.b.Run(r.Context(), runOpts)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, engineErrPayload(err))
		return
	}
	s.rememberRun(req.Port, runHarness)
	if err := s.b.RegenProxy(r.Context()); err != nil {
		log.Printf("regen proxy after run: %v", err) // non-fatal
	}
	s.forgetGatePort(req.Port) // it may be an ensemble now
	resp := map[string]any{"id": id, "port": req.Port, "engine": engine, "compute": compute}
	if warning != "" {
		resp["warning"] = warning
	}
	if workspace != "" {
		resp["workspace"] = workspace
	}
	if tuned != nil {
		resp["tuning"], resp["estimate"] = tuned.effective, tuned.priced.Estimate
	}
	if names := mcpNames(runHarness); len(names) > 0 || len(mcpNotes) > 0 {
		resp["mcp"], resp["mcp_notes"] = names, mcpNotes
	}
	if route != "" {
		resp["url"] = "http://" + route + ".localhost" + proxyURLSuffix(s.b.ProxyPort())
	}
	writeJSON(w, http.StatusOK, resp)
}

// proxyURLSuffix returns ":<port>" unless the proxy is on 80 (clean URL).
func proxyURLSuffix(port string) string {
	if port == "80" || port == "" {
		return ""
	}
	return ":" + port
}

// normalizeEngine constrains the engine to the two we support, defaulting to
// docker so an empty/unknown value preserves the original behaviour.
func normalizeEngine(engine string) string {
	if engine == "podman" {
		return "podman"
	}
	return "docker"
}

// normalizeCompute constrains the compute target; anything unrecognized falls
// back to cpu (the safe, runs-anywhere default).
func normalizeCompute(compute string) string {
	switch compute {
	case "cuda", "vulkan":
		return compute
	default:
		return "cpu"
	}
}

// parseHarnessOption reads a build request's "harness" field: a harness.json
// object (unknown fields are an error, so a typo or a key in the wrong place
// is caught), true for the default harness, or false/null/absent for none.
// It returns the config as it will be baked.
func parseHarnessOption(raw json.RawMessage) (*agent.Config, error) {
	switch strings.TrimSpace(string(raw)) {
	case "", "null", "false":
		return nil, nil
	case "true":
		raw = json.RawMessage("{}")
	}
	c, err := agent.ParseConfig(raw)
	if err != nil {
		return nil, err
	}
	return builder.NormalizeHarness(&c)
}

// sanitizeRoute normalizes a user-typed URL alias into a hostname label:
// lowercase, leading slash stripped, only [a-z0-9-] kept (e.g. "/AI" -> "ai").
func sanitizeRoute(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "/")
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID     string `json:"id"`
		Engine string `json:"engine"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	engine := normalizeEngine(req.Engine)
	// An ensemble's Conductor started its members; stopping it removes them
	// too, or they would hold their memory with nothing to manage them.
	ensID := s.b.EnsembleOfContainer(r.Context(), engine, req.ID)
	if err := s.b.Stop(r.Context(), engine, req.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, engineErrPayload(err))
		return
	}
	if ensID != "" {
		if err := s.b.RemoveEnsembleMembers(r.Context(), engine, ensID); err != nil {
			log.Printf("remove ensemble %s members: %v", ensID, err)
		}
	}
	if err := s.b.RegenProxy(r.Context()); err != nil {
		log.Printf("regen proxy after stop: %v", err) // non-fatal
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

// handleImageConfig returns the build config baked into an image so the UI can
// pre-populate the build form and clone it as a template.
func (s *Server) handleImageConfig(w http.ResponseWriter, r *http.Request) {
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		http.Error(w, "ref query param is required", http.StatusBadRequest)
		return
	}
	engine := normalizeEngine(r.URL.Query().Get("engine"))
	labels, err := s.b.ImageLabels(r.Context(), engine, ref)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	repo, tag := ref, "latest"
	if i := strings.LastIndex(ref, ":"); i >= 0 {
		repo, tag = ref[:i], ref[i+1:]
	}
	injectMode := labels["local-llm.inject_mode"]
	if injectMode == "" {
		injectMode = "missing"
	}
	buildEngine := labels["local-llm.engine"]
	if buildEngine == "" {
		buildEngine = engine
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"repository":    repo,
		"tag":           tag,
		"model_id":      labels["local-llm.model"],
		"engine":        normalizeEngine(buildEngine),
		"compute":       labels["local-llm.compute"],
		"inject_mode":   injectMode,
		"system_prompt": labels["local-llm.system_prompt"],
		"ctx_size":      labels["local-llm.ctx"],
		"memory_gb":     labels["local-llm.memory"],
		"route":         labels["local-llm.route"],
		"autostart":     labels["local-llm.autostart"] == "true",
		// The baked harness config (null = none), so a clone keeps the tools.
		"harness": builder.BakedHarness(labels),
	})
}

// handleImageDownload streams a built image as a .tar download (browser picks
// where to save it). Streams `docker save` so it works regardless of whether an
// exported tarball still exists on disk.
func (s *Server) handleImageDownload(w http.ResponseWriter, r *http.Request) {
	ref := r.URL.Query().Get("ref")
	if ref == "" {
		http.Error(w, "ref query param is required", http.StatusBadRequest)
		return
	}
	engine := normalizeEngine(r.URL.Query().Get("engine"))
	if !s.b.ImageExists(r.Context(), engine, ref) {
		http.Error(w, "no such image: "+ref, http.StatusNotFound)
		return
	}
	filename := strings.NewReplacer("/", "_", ":", "_").Replace(ref) + ".tar"
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	if err := s.b.SaveImage(r.Context(), engine, ref, w); err != nil {
		// Headers/body may already be partially sent; log server-side.
		log.Printf("image download %s failed: %v", ref, err)
	}
}

// handleChat proxies a prompt to a running container's OpenAI-compatible API.
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Port      int           `json:"port"`
		System    string        `json:"system"`
		Prompt    string        `json:"prompt"`
		Messages  []llm.Message `json:"messages"` // multi-turn history (preferred)
		MaxTokens int           `json:"max_tokens"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if req.Port == 0 {
		req.Port = 8080
	}

	client := llm.NewClient(fmt.Sprintf("http://%s:%d", s.modelHost, req.Port), "", 120*time.Second)
	msgs := []llm.Message{}
	if req.System != "" {
		msgs = append(msgs, llm.Message{Role: "system", Content: req.System})
	}
	if len(req.Messages) > 0 {
		msgs = append(msgs, req.Messages...) // full conversation for context
	} else {
		msgs = append(msgs, llm.Message{Role: "user", Content: req.Prompt})
	}
	maxTok := req.MaxTokens
	if maxTok <= 0 {
		maxTok = 1024
	}

	resp, err := client.Chat(r.Context(), msgs, llm.ChatOptions{Temperature: 0.4, MaxTokens: maxTok})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"response": resp})
}

// modelURL builds the URL to an endpoint on a running model container. Models
// publish their server on modelHost:<port> (loopback natively, the gateway host
// in-container); the factory proxies to keep the browser same-origin.
func (s *Server) modelURL(port int, path string) string {
	if port == 0 {
		port = 8080
	}
	return fmt.Sprintf("http://%s:%d%s", s.modelHost, port, path)
}

// handleChatStream proxies an OpenAI chat request to a running llama-server and
// streams the text/event-stream response straight through to the browser. The
// request body is forwarded verbatim (so an OpenAI content-array carrying an
// image rides this path for vision too); we just stamp model+stream. The shim in
// front of the model already proxies SSE with no buffering.
func (s *Server) handleChatStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	port := 8080
	if p, ok := req["port"].(float64); ok {
		port = int(p)
	}
	delete(req, "port")
	req["model"] = "local-model" // required by schema, ignored with one model loaded
	req["stream"] = true
	body, _ := json.Marshal(req)
	// An ensemble applies its persona itself: make sure it has the current one.
	s.syncPersonaAt(r.Context(), port)

	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, s.modelURL(port, "/v1/chat/completions"), bytes.NewReader(body))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	upReq.Header.Set("Content-Type", "application/json")
	// No client timeout: streaming can run a while; r.Context cancels on disconnect.
	resp, err := (&http.Client{}).Do(upReq)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, engineErrPayload(err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		writeJSON(w, resp.StatusCode, map[string]string{"error": upstreamError(b)})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	// Line by line, so an ensemble's generated media (data URLs in delta.media)
	// are saved to /api/media and the browser gets short URLs.
	s.relaySSE(w, resp.Body)
}

// handleImageGenerate proxies a text-to-image request to a running sd-server and
// returns each image as a ready-to-use data URL. Synchronous and slow (SDXL can
// take ~2 min), so it uses a long upstream timeout.
func (s *Server) handleImageGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	port := 8080
	if p, ok := req["port"].(float64); ok {
		port = int(p)
	}
	delete(req, "port")
	body, _ := json.Marshal(req)

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	upReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.modelURL(port, "/sdapi/v1/txt2img"), bytes.NewReader(body))
	upReq.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{}).Do(upReq)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, engineErrPayload(err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		writeJSON(w, resp.StatusCode, map[string]string{"error": strings.TrimSpace(string(b))})
		return
	}
	var out struct {
		Images []string `json:"images"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "bad image response: " + err.Error()})
		return
	}
	for i, b64 := range out.Images {
		if !strings.HasPrefix(b64, "data:") {
			b64 = "data:image/png;base64," + b64
		}
		// Persist to disk + return a stable URL (survives reload); fall back to
		// the inline data URL if the save fails.
		if url, err := s.saveDataURL(b64); err == nil {
			out.Images[i] = url
		} else {
			out.Images[i] = b64
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"images": out.Images})
}

// handleVideoGenerate proxies a text-to-video request to a running videogate
// (POST /generate) and returns the clip as a data URL. Video gen is very slow, so
// it uses a generous upstream timeout.
func (s *Server) handleVideoGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	port := 8080
	if p, ok := req["port"].(float64); ok {
		port = int(p)
	}
	delete(req, "port")
	body, _ := json.Marshal(req)

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Minute)
	defer cancel()
	upReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.modelURL(port, "/generate"), bytes.NewReader(body))
	upReq.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{}).Do(upReq)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, engineErrPayload(err))
		return
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		writeJSON(w, resp.StatusCode, map[string]string{"error": strings.TrimSpace(string(out))})
		return
	}
	// Persist the clip to disk and hand back a stable /api/media URL instead of
	// the multi-MB data URL, so it survives a browser reload. Falls back to the
	// raw videogate response if anything about the save fails.
	var vr map[string]any
	if json.Unmarshal(out, &vr) == nil {
		if v, ok := vr["video"].(string); ok && v != "" {
			if url, err := s.saveDataURL(v); err == nil {
				vr["video"] = url
			}
		}
		writeJSON(w, http.StatusOK, vr)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

// handleTranscribe proxies an uploaded audio file to a running whisper-server
// (or an audio.cpp speech-to-text image, which takes WAV only) as a fresh
// multipart body and returns the transcript text.
func (s *Server) handleTranscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing audio 'file'"})
		return
	}
	defer file.Close()
	port, _ := strconv.Atoi(r.FormValue("port"))
	audio, err := io.ReadAll(file)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read uploaded audio: " + err.Error()})
		return
	}
	text, err := s.transcribeAt(r.Context(), port, hdr.Filename, audio,
		map[string]string{"temperature": r.FormValue("temperature"), "language": r.FormValue("language")})
	if err != nil {
		var se *sttError
		if errors.As(err, &se) && se.status != http.StatusBadGateway {
			writeJSON(w, se.status, map[string]string{"error": se.msg})
			return
		}
		writeJSON(w, http.StatusBadGateway, engineErrPayload(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": text})
}

// handleTTS proxies text to a running Piper server and streams the WAV back.
func (s *Server) handleTTS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Port         int     `json:"port"`
		Text         string  `json:"text"`
		Voice        string  `json:"voice"`
		Speed        float64 `json:"speed"`
		Instructions string  `json:"instructions"` // style/emotion, for models that take one (Qwen3-TTS)
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "empty text"})
		return
	}
	// Pass voice/speed/instructions through to the TTS server (honored by
	// pygate/Kokoro and audio.cpp; harmless to a server that ignores them).
	// "model" is the id audio.cpp images serve their one model under; Piper and
	// Kokoro ignore it.
	up := map[string]any{"input": req.Text, "model": "tts"}
	if req.Voice != "" {
		up["voice"] = req.Voice
	}
	if req.Speed > 0 {
		up["speed"] = req.Speed
	}
	if strings.TrimSpace(req.Instructions) != "" {
		up["instructions"] = req.Instructions
	}
	body, _ := json.Marshal(up)
	// An ensemble speaks in its persona's voice: make sure it has the current one.
	s.syncPersonaAt(r.Context(), req.Port)
	// An ensemble may have to start its speech member first.
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	upReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.modelURL(req.Port, "/v1/audio/speech"), bytes.NewReader(body))
	upReq.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{}).Do(upReq)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, engineErrPayload(err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		writeJSON(w, resp.StatusCode, map[string]string{"error": upstreamError(b)})
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	io.Copy(w, resp.Body)
}
