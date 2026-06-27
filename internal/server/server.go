// Package server exposes the local web GUI: a small JSON API plus an embedded
// vanilla-JS frontend for configuring, naming, building, running, and testing
// model images.
package server

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yourorg/local-llm/internal/builder"
	"github.com/yourorg/local-llm/internal/catalog"
	"github.com/yourorg/local-llm/internal/llm"
)

//go:embed web
var webFS embed.FS

// Server wires the API handlers to the builder and catalog.
type Server struct {
	b         *builder.Builder
	cat       *catalog.Catalog
	mux       *http.ServeMux
	modelHost string // host where launched model containers are reachable

	bs buildState // current/last build, buffered so the UI can reconnect after a refresh

	statsMu   sync.Mutex
	buildSecs map[string]float64 // compute -> last successful build duration (s)
	statsPath string             // where buildSecs is persisted
}

// buildState buffers an in-flight (or just-finished) build so a refreshed UI can
// reconnect: it holds the streamed log lines plus status/timing. Only one build
// runs at a time (tryStart enforces it).
// buildConfig is the form config of a build, kept so a refreshed UI can restore
// the build form (and show what's building).
type buildConfig struct {
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
}

type buildState struct {
	mu         sync.Mutex
	active     bool
	status     string // "" (idle) | running | done | error
	ref        string
	cfg        buildConfig
	etaSecs    int
	startTime  time.Time
	finishTime time.Time
	lines      []string
}

func (b *buildState) tryStart(ref string, eta int, cfg buildConfig) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active {
		return false
	}
	b.active = true
	b.status = "running"
	b.ref = ref
	b.cfg = cfg
	b.etaSecs = eta
	b.startTime = time.Now()
	b.finishTime = time.Time{}
	b.lines = nil
	return true
}

func (b *buildState) append(line string) {
	b.mu.Lock()
	b.lines = append(b.lines, line)
	b.mu.Unlock()
}

func (b *buildState) finish(status string) {
	b.mu.Lock()
	b.active = false
	b.status = status
	b.finishTime = time.Now()
	b.mu.Unlock()
}

type buildSnapshot struct {
	Active     bool        `json:"active"`
	Status     string      `json:"status"`
	ElapsedMs  int64       `json:"elapsed_ms"`
	EtaSeconds int         `json:"eta_seconds"`
	Ref        string      `json:"ref"`
	Compute    string      `json:"compute"`
	Config     buildConfig `json:"config"`
	Lines      []string    `json:"lines"`
	NextOffset int         `json:"next_offset"`
}

// snapshot returns log lines from offset onward plus current meta.
func (b *buildState) snapshot(offset int) buildSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	total := len(b.lines)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	lines := append([]string{}, b.lines[offset:]...)
	var elapsed int64
	if !b.startTime.IsZero() {
		end := time.Now()
		if !b.active && !b.finishTime.IsZero() {
			end = b.finishTime
		}
		elapsed = end.Sub(b.startTime).Milliseconds()
	}
	return buildSnapshot{
		Active: b.active, Status: b.status, ElapsedMs: elapsed,
		EtaSeconds: b.etaSecs, Ref: b.ref, Compute: b.cfg.Compute, Config: b.cfg,
		Lines: lines, NextOffset: total,
	}
}

// New constructs the HTTP handler tree. modelHost is where model containers'
// published ports can be reached from this process — "127.0.0.1" when the
// factory runs natively, "host.docker.internal" when it runs in a container.
//
// webDir, if non-empty, serves the UI from that directory on disk instead of
// the embedded copy — enabling UI hot-reload in dev (edit + refresh, no
// rebuild). When empty, the embedded assets are used (production).
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
	s := &Server{
		b: b, cat: cat, mux: http.NewServeMux(), modelHost: modelHost,
		statsPath: filepath.Join(b.BaseDir, "config", "build-stats.json"),
	}
	s.loadStats()
	s.mux.Handle("/", web)
	s.mux.HandleFunc("/api/catalog", s.handleCatalog)
	s.mux.HandleFunc("/api/models", s.handleModels)
	s.mux.HandleFunc("/api/model/delete", s.handleModelDelete)
	s.mux.HandleFunc("/api/build", s.handleBuild)
	s.mux.HandleFunc("/api/build/state", s.handleBuildState)
	s.mux.HandleFunc("/api/images", s.handleImages)
	s.mux.HandleFunc("/api/image/delete", s.handleImageDelete)
	s.mux.HandleFunc("/api/image/download", s.handleImageDownload)
	s.mux.HandleFunc("/api/image/config", s.handleImageConfig)
	s.mux.HandleFunc("/api/run", s.handleRun)
	s.mux.HandleFunc("/api/containers", s.handleContainers)
	s.mux.HandleFunc("/api/container/logs", s.handleContainerLogs)
	s.mux.HandleFunc("/api/stop", s.handleStop)
	s.mux.HandleFunc("/api/chat", s.handleChat)
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.HandleFunc("/readyz", s.handleReadyz)
	s.mux.HandleFunc("/api/sysinfo", s.handleSysInfo)
	return s, nil
}

// Handler returns the root http.Handler.
func (s *Server) Handler() http.Handler { return s.mux }

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
	writeJSON(w, http.StatusOK, s.cat.Models)
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
	writeJSON(w, http.StatusOK, imgs)
}

func (s *Server) handleContainers(w http.ResponseWriter, r *http.Request) {
	cs, err := s.b.Containers(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, engineErrPayload(err))
		return
	}
	writeJSON(w, http.StatusOK, cs)
}

// handleSysInfo returns the active engine machine's RAM/CPU/GPU so the build form
// can show whether a model fits the user's actual system.
func (s *Server) handleSysInfo(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.b.SysInfo(ctx))
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
	writeJSON(w, code, map[string]any{"ready": ready, "engines": versions})
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

// handleBuild starts a build (one at a time) and returns immediately. The build
// runs detached in a goroutine; its log lines + status are buffered in
// s.bs so the UI can poll /api/build/state and reconnect after a refresh.
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
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	model, ok := s.cat.Get(req.ModelID)
	if !ok {
		http.Error(w, "unknown model id: "+req.ModelID, http.StatusBadRequest)
		return
	}

	engine := normalizeEngine(req.Engine)
	compute := normalizeCompute(req.Compute)
	route := sanitizeRoute(req.Route)

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
	}

	cfg := buildConfig{
		ModelID: req.ModelID, ImageName: req.ImageName, Tag: req.Tag,
		Engine: engine, Compute: compute, SystemPrompt: req.SystemPrompt, InjectMode: req.InjectMode,
		CtxSize: req.CtxSize, MemoryGB: req.MemoryGB, Route: route, Autostart: req.Autostart,
	}
	// One build at a time. tryStart resets the buffer and marks running; a second
	// attempt (e.g. after a refresh + re-click) is rejected so the UI reconnects.
	if !s.bs.tryStart(opts.Ref(), int(s.etaFor(compute)), cfg) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a build is already in progress"})
		return
	}

	go s.runBuild(opts, compute)
	writeJSON(w, http.StatusOK, map[string]string{"status": "started"})
}

// runBuild executes a build detached from any request, appending progress to the
// buffered build state.
func (s *Server) runBuild(opts builder.BuildOptions, compute string) {
	log := func(line string) { s.bs.append(line) }

	// Detached context so a client refresh/disconnect can't cancel the build.
	// (Capped so a wedged build can't hold the slot forever.)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	start := time.Now()
	if err := s.b.Build(ctx, opts, log); err != nil {
		// Lead with the recognized cause (engine down / disk / memory / port);
		// the full engine output was already streamed into the log above.
		if hint := builder.ClassifyEngineError(err); hint != "" {
			s.bs.append("ERROR: " + hint)
			s.bs.append("detail: " + err.Error())
		} else {
			s.bs.append("ERROR: " + err.Error())
		}
		s.bs.finish("error")
		return
	}
	s.recordBuild(compute, time.Since(start).Seconds())
	s.bs.append("DONE")
	s.bs.finish("done")
}

// handleBuildState returns buffered build progress from ?offset onward so the UI
// can stream it (and reconnect after a refresh).
func (s *Server) handleBuildState(w http.ResponseWriter, r *http.Request) {
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	writeJSON(w, http.StatusOK, s.bs.snapshot(offset))
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

	id, err := s.b.Run(r.Context(), builder.RunOptions{
		Ref: req.Ref, HostPort: req.Port, Engine: engine, Compute: compute,
		MemoryGB: memGB, Route: route, Autostart: autostart,
		SystemPrompt: req.SystemPrompt, InjectMode: req.InjectMode,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, engineErrPayload(err))
		return
	}
	if err := s.b.RegenProxy(r.Context()); err != nil {
		log.Printf("regen proxy after run: %v", err) // non-fatal
	}
	resp := map[string]any{"id": id, "port": req.Port, "engine": engine, "compute": compute}
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
	if err := s.b.Stop(r.Context(), normalizeEngine(req.Engine), req.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, engineErrPayload(err))
		return
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
