package server

// Native packages (docs/NATIVE.md): /api/native/*. A package is made in the
// background like an image build, with the same log and cancel, but on a
// queue of its own that makes one package at a time, so packaging never waits
// behind (or holds up) an engine's image builds and stays off the Builds page.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/seanbrill/local-llm/internal/agent"
	"github.com/seanbrill/local-llm/internal/builder"
	"github.com/seanbrill/local-llm/internal/native"
)

const nativeLane = "native"

// nativeAPI serves /api/native/*.
type nativeAPI struct {
	s *Server
	q *buildQueue
	// labels reads an image's labels and build makes a package; tests replace them.
	labels func(ctx context.Context, engine, ref string) (map[string]string, error)
	build  func(ctx context.Context, o builder.NativeOptions, log builder.LogFunc) (*builder.NativePackage, error)

	mu   sync.Mutex
	jobs map[string]nativeJobInfo // job id -> what it makes
}

type nativeJobInfo struct {
	Package     string `json:"package"` // the package's name (packages/<name>.zip)
	Target      string `json:"target"`
	TargetLabel string `json:"target_label"`
	FromImage   string `json:"from_image,omitempty"`
}

// nativeJob is a packaging job as /api/native/jobs and /state report it.
type nativeJob struct {
	jobSummary
	nativeJobInfo
}

func (s *Server) registerNative() {
	n := &nativeAPI{s: s, q: newBuildQueue(), labels: s.b.ImageLabels, build: s.b.BuildNativePackage, jobs: map[string]nativeJobInfo{}}
	s.mux.HandleFunc("/api/native/targets", n.handleTargets)
	s.mux.HandleFunc("/api/native/source", n.handleSource)
	s.mux.HandleFunc("/api/native/package", n.handlePackage)
	s.mux.HandleFunc("/api/native/jobs", n.handleJobs)
	s.mux.HandleFunc("/api/native/state", n.handleState)
	s.mux.HandleFunc("/api/native/cancel", n.handleCancel)
	s.mux.HandleFunc("/api/native/packages", n.handlePackages)
	s.mux.HandleFunc("/api/native/download", n.handleDownload)
	s.mux.HandleFunc("/api/native/delete", n.handleDelete)
}

// nativeTarget is a target as the UI lists it.
type nativeTarget struct {
	native.Target
	// Ready is false when this factory can't make its launcher (no prebuilt
	// one and no Go toolchain); Why says so.
	Ready bool   `json:"ready"`
	Why   string `json:"why,omitempty"`
}

// handleTargets lists the systems packages can be made for:
// GET /api/native/targets -> {"targets": [...], "llama_cpp": "v0.9.0 (b12110)"}.
func (n *nativeAPI) handleTargets(w http.ResponseWriter, r *http.Request) {
	_, goErr := exec.LookPath("go")
	_, srcErr := os.Stat(filepath.Join(n.s.b.BaseDir, "cmd", "llmpack"))
	out := []nativeTarget{}
	for _, t := range native.Targets() {
		nt := nativeTarget{Target: t, Ready: true}
		if _, err := os.Stat(filepath.Join(n.s.b.LaunchersDir(), builder.LauncherFileName(t))); err != nil && (goErr != nil || srcErr != nil) {
			nt.Ready, nt.Why = false, "this factory image has no launcher for it: rebuild the factory with its start script"
		}
		out = append(out, nt)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"targets":   out,
		"llama_cpp": native.LlamaCppRelease + " (" + native.LlamaCppBuild + ")",
	})
}

// nativeSource is what a package of a model or image would carry by default.
type nativeSource struct {
	ModelID      string        `json:"model_id"`
	ModelName    string        `json:"model_name"`
	SizeGB       float64       `json:"size_gb"`
	Modality     string        `json:"modality"`
	Capable      bool          `json:"capable"`
	Why          string        `json:"why,omitempty"` // why it can't be packaged
	FromImage    string        `json:"from_image,omitempty"`
	SystemPrompt string        `json:"system_prompt"`
	InjectMode   string        `json:"inject_mode"`
	CtxSize      int           `json:"ctx_size"`
	Harness      *agent.Config `json:"harness"` // nil = none; the shell toolset is already removed
	ShellDropped bool          `json:"shell_dropped"`
	Names        []string      `json:"default_names"` // per target, in Targets() order
}

// source works out a package's model and settings from an image (its labels)
// or from a catalog entry (the defaults).
func (n *nativeAPI) source(ctx context.Context, modelID, ref, engine string) (nativeSource, int, error) {
	var src nativeSource
	var harnessRaw string
	if ref != "" {
		labels, err := n.labels(ctx, normalizeEngine(engine), ref)
		if err != nil {
			return src, http.StatusNotFound, err
		}
		modelID = labels["local-llm.model"]
		if modelID == "" {
			return src, http.StatusBadRequest, errors.New(ref + " wasn't built by this factory (it has no local-llm.model label)")
		}
		src.FromImage = ref
		src.SystemPrompt = labels["local-llm.system_prompt"]
		src.InjectMode = labels["local-llm.inject_mode"]
		src.CtxSize, _ = strconv.Atoi(labels["local-llm.ctx"])
		harnessRaw = labels[builder.LabelHarnessConfig]
	}
	m, ok := n.s.cat.Get(modelID)
	if !ok {
		return src, http.StatusBadRequest, errors.New("unknown model id: " + modelID)
	}
	src.ModelID, src.ModelName, src.SizeGB, src.Modality = m.ID, m.Name, m.SizeGB, m.Mod()
	src.Why = builder.NativeCapable(m)
	src.Capable = src.Why == ""
	if src.InjectMode == "" {
		src.InjectMode = "missing"
	}
	if src.CtxSize <= 0 {
		src.CtxSize = builder.DefaultNativeCtx
	}
	if strings.TrimSpace(harnessRaw) != "" {
		if c, err := parseHarnessOption(json.RawMessage(harnessRaw)); err == nil {
			src.Harness, src.ShellDropped, _ = builder.NormalizeNativeHarness(c)
		}
	}
	for _, t := range native.Targets() {
		src.Names = append(src.Names, builder.DefaultPackageName(m.ID, t.ID))
	}
	return src, http.StatusOK, nil
}

// handleSource prefills the package dialog:
// GET /api/native/source?ref=&engine= (an image) or ?model_id= (a catalog entry).
func (n *nativeAPI) handleSource(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("ref") == "" && q.Get("model_id") == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ref or model_id is required"})
		return
	}
	src, code, err := n.source(r.Context(), q.Get("model_id"), q.Get("ref"), q.Get("engine"))
	if err != nil {
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, src)
}

// handlePackage queues a package: POST /api/native/package
//
//	{"ref": "...", "engine": "docker"} or {"model_id": "..."},
//	"target": "macos-arm64", "name": "" (default <model>-<target>),
//	"system_prompt", "inject_mode", "ctx_size", "harness" (a harness.json
//	object, true, or false/null)
//
// Settings left out come from the image (or the catalog's defaults).
// It answers {"id", "status", "position", "package"}.
func (n *nativeAPI) handlePackage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ModelID      string          `json:"model_id"`
		Ref          string          `json:"ref"`
		Engine       string          `json:"engine"`
		Target       string          `json:"target"`
		Name         string          `json:"name"`
		SystemPrompt *string         `json:"system_prompt"`
		InjectMode   string          `json:"inject_mode"`
		CtxSize      int             `json:"ctx_size"`
		Harness      json.RawMessage `json:"harness"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request: " + err.Error()})
		return
	}
	t, ok := native.TargetByID(req.Target)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown target: " + req.Target})
		return
	}
	if req.ModelID == "" && req.Ref == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ref or model_id is required"})
		return
	}
	src, code, err := n.source(r.Context(), req.ModelID, req.Ref, req.Engine)
	if err != nil {
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	if !src.Capable {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": src.Why})
		return
	}
	m, _ := n.s.cat.Get(src.ModelID)
	o := builder.NativeOptions{
		Model: m, Target: t.ID, Name: strings.TrimSpace(req.Name),
		SystemPrompt: src.SystemPrompt, InjectMode: src.InjectMode, CtxSize: src.CtxSize,
		Harness: src.Harness, FromImage: src.FromImage,
	}
	if req.SystemPrompt != nil {
		o.SystemPrompt = *req.SystemPrompt
	}
	if req.InjectMode != "" {
		o.InjectMode = req.InjectMode
	}
	if req.CtxSize > 0 {
		o.CtxSize = req.CtxSize
	}
	if len(req.Harness) > 0 {
		if o.Harness, err = parseHarnessOption(req.Harness); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	if _, _, err := builder.NormalizeNativeHarness(o.Harness); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "harness: " + err.Error()})
		return
	}
	if o.Name == "" {
		o.Name = builder.DefaultPackageName(m.ID, t.ID)
	}
	if !builder.ValidPackageName(o.Name) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": o.Name + " can't be a package name: use letters, digits, dots, dashes and underscores"})
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, j := range n.q.list() {
		if j.Active && n.jobs[j.ID].Package == o.Name {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "the package " + o.Name + " is already being made: wait for it, or pick another name"})
			return
		}
	}
	job := &buildJob{
		kind: "native", lane: nativeLane, ref: o.Name, name: m.Name,
		cfg: buildConfig{ModelID: m.ID, ImageName: o.Name, SystemPrompt: o.SystemPrompt, InjectMode: o.InjectMode, CtxSize: o.CtxSize, Harness: o.Harness},
		run: func(ctx context.Context, log builder.LogFunc) error {
			if _, err := n.build(ctx, o, log); err != nil {
				// Said as it is: the image-build hints (engine down, …) don't apply here.
				return &builder.HintError{Hint: err.Error(), Err: err}
			}
			return nil
		},
	}
	pos, err := n.q.enqueue(job)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	n.jobs[job.id] = nativeJobInfo{Package: o.Name, Target: t.ID, TargetLabel: t.Label, FromImage: o.FromImage}
	status := "queued"
	if pos == 0 {
		status = "started"
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": job.id, "status": status, "position": pos, "package": o.Name})
}

func (n *nativeAPI) info(id string) nativeJobInfo {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.jobs[id]
}

// handleJobs lists packaging jobs: GET /api/native/jobs -> {"jobs": [...]}.
func (n *nativeAPI) handleJobs(w http.ResponseWriter, r *http.Request) {
	list := n.q.list()
	out := make([]nativeJob, 0, len(list))
	for _, j := range list {
		out = append(out, nativeJob{jobSummary: j, nativeJobInfo: n.info(j.ID)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": out})
}

// handleState streams a job's log: GET /api/native/state?id=&offset=.
func (n *nativeAPI) handleState(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	snap, ok := n.q.snapshot(id, offset)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such packaging job"})
		return
	}
	writeJSON(w, http.StatusOK, struct {
		buildSnapshot
		nativeJobInfo
	}{snap, n.info(id)})
}

// handleCancel stops a job: POST /api/native/cancel {"id": "..."}.
func (n *nativeAPI) handleCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	status, err := n.q.cancelJob(req.ID)
	switch {
	case errors.Is(err, errJobNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such packaging job"})
	case err != nil:
		writeJSON(w, http.StatusConflict, map[string]string{"error": "that package is already finished", "status": status})
	default:
		writeJSON(w, http.StatusOK, map[string]string{"id": req.ID, "status": status})
	}
}

// handlePackages lists the finished packages: GET /api/native/packages.
func (n *nativeAPI) handlePackages(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"packages": n.s.b.NativePackages()})
}

// handleDownload sends a package: GET /api/native/download?name=<name>.
func (n *nativeAPI) handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	p, err := n.s.b.NativePackagePath(r.URL.Query().Get("name"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	f, err := os.Open(p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(p)+`"`)
	if sum, err := os.ReadFile(p + ".sha256"); err == nil {
		if hexSum, _, ok := strings.Cut(strings.TrimSpace(string(sum)), " "); ok && len(hexSum) == 64 {
			w.Header().Set("X-Checksum-Sha256", hexSum)
		}
	}
	http.ServeContent(w, r, filepath.Base(p), fi.ModTime(), f) // ranges, so a browser can resume
}

// handleDelete removes a package: POST /api/native/delete {"name": "..."}.
func (n *nativeAPI) handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	n.mu.Lock()
	for _, j := range n.q.list() {
		if j.Active && n.jobs[j.ID].Package == req.Name {
			n.mu.Unlock()
			writeJSON(w, http.StatusConflict, map[string]string{"error": "that package is being made: cancel it first"})
			return
		}
	}
	n.mu.Unlock()
	if err := n.s.b.RemoveNativePackage(req.Name); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deleted": req.Name})
}
