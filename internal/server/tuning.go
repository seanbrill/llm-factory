package server

// Runtime tuning (docs/TUNING.md): presets for a model on an engine, the
// settings saved per image (config/tuning.json), memory estimates, and the
// benchmark (tuningbench.go).
//
//	GET    /api/tuning?ref=&engine=                    an image's baked, saved and effective tuning, and its presets
//	GET    /api/tuning/presets?model=&compute=&engine= presets before an image exists (the Build plan)
//	POST   /api/tuning/estimate                        price a tuning: {ref, engine, tuning} or {model, compute, engine, tuning}
//	PUT    /api/tuning                                 save {ref, engine, tuning} for the image
//	DELETE /api/tuning?ref=&engine=                    forget the saved tuning
//
// POST /api/build and POST /api/run take a "tuning" too (server.go).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/seanbrill/local-llm/internal/builder"
	"github.com/seanbrill/local-llm/internal/catalog"
	"github.com/seanbrill/local-llm/internal/tuning"
)

// tuneState is the server's tuning machinery, made on first use.
type tuneState struct {
	s         *Server
	storePath string // config/tuning.json
	archPath  string // config/gguf-info.json

	mu    sync.Mutex // guards the store file
	archM sync.Mutex
	arch  map[string]tuning.ArchInfo // model file name -> header info
	miss  map[string]time.Time       // remote reads that failed, not retried for a while

	// Hooks (tests fake them).
	memFn     func(ctx context.Context, engine string) (float64, error) // engine machine memory, GB
	hostMemFn func(ctx context.Context) float64                         // this computer's memory, GB
	fetchFn   func(ctx context.Context, url string) (tuning.ArchInfo, error)

	bench *benchManager
}

func (s *Server) tuning() *tuneState {
	s.tuneOnce.Do(func() {
		cfg := s.b.ConfigDir
		t := &tuneState{
			s: s, storePath: filepath.Join(cfg, "tuning.json"), archPath: filepath.Join(cfg, "gguf-info.json"),
			arch: map[string]tuning.ArchInfo{}, miss: map[string]time.Time{},
			memFn: func(ctx context.Context, engine string) (float64, error) {
				gb, _, err := builder.EngineMemory(ctx, engine)
				return gb, err
			},
			hostMemFn: func(ctx context.Context) float64 { return builder.HostMemGB(ctx) * (1 << 30) / 1e9 },
			fetchFn: func(ctx context.Context, url string) (tuning.ArchInfo, error) {
				return tuning.FetchArch(ctx, &http.Client{Timeout: 10 * time.Second}, url)
			},
		}
		if data, err := os.ReadFile(t.archPath); err == nil {
			_ = json.Unmarshal(data, &t.arch)
		}
		t.bench = newBenchManager(s)
		s.tune = t
	})
	return s.tune
}

func (s *Server) registerTuning() {
	s.mux.HandleFunc("/api/tuning", s.handleTuning)
	s.mux.HandleFunc("/api/tuning/presets", s.handleTuningPresets)
	s.mux.HandleFunc("/api/tuning/estimate", s.handleTuningEstimate)
	s.mux.HandleFunc("/api/tuning/benchmark", s.handleBenchStart)
	s.mux.HandleFunc("/api/tuning/benchmark/{id}", s.handleBenchState)
	s.mux.HandleFunc("/api/tuning/benchmark/{id}/cancel", s.handleBenchCancel)
	s.mux.HandleFunc("/api/tuning/benchmark/{id}/apply", s.handleBenchApply)
}

// Close stops what the server runs in the background (benchmarks, whose
// containers it removes), for the factory's shutdown.
func (s *Server) Close() {
	s.tuning().bench.closeAll(20 * time.Second)
}

// ---- the store ---------------------------------------------------------------

type savedTuning struct {
	Ref     string        `json:"ref"`
	Engine  string        `json:"engine"`
	Tuning  tuning.Tuning `json:"tuning"`
	SavedAt string        `json:"saved_at"`
}

func storeKey(ref, engine string) string { return normalizeEngine(engine) + "|" + ref }

func (t *tuneState) load() map[string]savedTuning {
	out := map[string]savedTuning{}
	if data, err := os.ReadFile(t.storePath); err == nil {
		_ = json.Unmarshal(data, &out)
	}
	return out
}

// writeAtomic writes data to path through a temp file and a rename.
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func (t *tuneState) saved(ref, engine string) *tuning.Tuning {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e, ok := t.load()[storeKey(ref, engine)]; ok {
		tu := e.Tuning
		return &tu
	}
	return nil
}

func (t *tuneState) save(ref, engine string, tu *tuning.Tuning) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	all := t.load()
	k := storeKey(ref, engine)
	if tu == nil {
		delete(all, k)
	} else {
		all[k] = savedTuning{Ref: ref, Engine: normalizeEngine(engine), Tuning: *tu, SavedAt: time.Now().UTC().Format(time.RFC3339)}
	}
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(t.storePath, append(data, '\n'))
}

// ---- what the estimate knows about a model --------------------------------------

// archInfo is a model's GGUF header info: from the downloaded file, else
// read remotely (a range request on its download URL), cached in
// config/gguf-info.json. nil when neither works.
func (t *tuneState) archInfo(ctx context.Context, m catalog.Model) *tuning.ArchInfo {
	if m.File == "" || !strings.HasSuffix(strings.ToLower(m.File), ".gguf") {
		return nil
	}
	t.archM.Lock()
	if a, ok := t.arch[m.File]; ok {
		t.archM.Unlock()
		return &a
	}
	missed := time.Since(t.miss[m.File]) < 10*time.Minute
	t.archM.Unlock()

	var a tuning.ArchInfo
	var err error
	if p := t.s.b.ModelPath(m); fileExists(p) {
		var meta *tuning.GGUFMeta
		if meta, err = tuning.ReadGGUFMetaFile(p); err == nil {
			a, err = meta.Info()
		}
	} else if m.URL != "" && !missed && t.fetchFn != nil {
		fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		a, err = t.fetchFn(fctx, m.URL)
		cancel()
	} else {
		return nil
	}
	t.archM.Lock()
	defer t.archM.Unlock()
	if err != nil {
		t.miss[m.File] = time.Now()
		return nil
	}
	t.arch[m.File] = a
	if data, err := json.MarshalIndent(t.arch, "", "  "); err == nil {
		_ = writeAtomic(t.archPath, append(data, '\n'))
	}
	return &a
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Size() > 0
}

// paramsB reads a catalog params label: "12B" -> 12, "21B-A3.6B" -> 21,
// "E4B" -> 4, "568M" -> 0.568.
func paramsB(s string) float64 {
	s = strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(s)), "E")
	end := 0
	for end < len(s) && (s[end] == '.' || (s[end] >= '0' && s[end] <= '9')) {
		end++
	}
	v, err := strconv.ParseFloat(s[:end], 64)
	if err != nil {
		return 0
	}
	if end < len(s) && s[end] == 'M' {
		return v / 1000
	}
	return v
}

// modelFacts is what validation, the presets and the estimate know about m.
// labels are the image's (nil before an image exists): a run can use a
// draft only if the image baked one; a build can if the catalog pairs one.
func (s *Server) modelFacts(ctx context.Context, m catalog.Model, labels map[string]string) tuning.Model {
	t := s.tuning()
	f := tuning.Model{ID: m.ID, Name: m.Name, Modality: m.Mod(), Vision: m.MMProjFile != "", ParamsB: paramsB(m.Params), Draft: m.Draft}
	if ok, n := s.b.ModelOnDisk(m); ok && n > 0 {
		f.WeightsBytes = n
	} else {
		f.WeightsBytes = int64(m.SizeGB * 1e9)
	}
	if m.MMProjFile != "" {
		f.MMProjBytes = 900e6 // a typical F16 projector, until it's downloaded
		if fi, err := os.Stat(s.b.MMProjPath(m)); err == nil && fi.Size() > 0 {
			f.MMProjBytes = fi.Size()
		}
	}
	f.Arch = t.archInfo(ctx, m)
	draftID := m.Draft
	if labels != nil {
		draftID = labels[builder.LabelDraft]
		f.DraftAvailable = draftID != ""
	}
	if d, ok := s.cat.Get(draftID); ok && draftID != "" {
		f.DraftAvailable = true
		f.DraftBytes = int64(d.SizeGB * 1e9)
		if ok, n := s.b.ModelOnDisk(d); ok && n > 0 {
			f.DraftBytes = n
		}
		f.DraftArch = t.archInfo(ctx, d)
	}
	return f
}

// target is the memory a model runs in on engine ("docker", "podman", or
// "native" for a package on this computer).
func (s *Server) tuneTarget(ctx context.Context, engine, compute string) tuning.Target {
	t := s.tuning()
	if engine == "native" {
		if compute == "" {
			compute = "metal"
		}
		return tuning.Target{Engine: "native", Compute: compute, MemGB: t.hostMemFn(ctx)}
	}
	engine = normalizeEngine(engine)
	tg := tuning.Target{Engine: engine, Compute: normalizeCompute(compute)}
	if t.memFn != nil {
		mctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		if gb, err := t.memFn(mctx, engine); err == nil {
			tg.MemGB = gb
		}
		cancel()
	}
	return tg
}

// bakedView is the tuning an image was built with, with its context filled in
// from its labels (the CTX_SIZE it bakes even without a tuning).
func bakedView(labels map[string]string) tuning.Tuning {
	t := builder.BakedTuning(labels)
	if t.CtxSize == 0 {
		if n, _ := strconv.Atoi(labels["local-llm.ctx"]); n > 0 {
			t.CtxSize = n
		} else if labels["local-llm.compute"] == "cpu" {
			t.CtxSize = 4096 // Dockerfile.cpu's CTX_SIZE
		} else {
			t.CtxSize = 8192 // Dockerfile.vulkan's and .cuda's
		}
	}
	return t
}

// ---- JSON shapes ---------------------------------------------------------------

type tuneModelInfo struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	TrainedCtx int              `json:"trained_ctx"` // 0 = unknown (header not read)
	Vision     bool             `json:"vision"`
	Draft      string           `json:"draft"` // catalog draft id ("" = none)
	DraftBaked bool             `json:"draft_baked,omitempty"`
	Modality   string           `json:"modality"`
	Arch       *tuning.ArchInfo `json:"arch,omitempty"`
}

type tuneEngineInfo struct {
	Name     string  `json:"name"`
	Compute  string  `json:"compute"`
	MemGB    float64 `json:"mem_gb"`    // 0 = unknown
	UsableGB float64 `json:"usable_gb"` // what a model may take
}

func modelInfo(m catalog.Model, f tuning.Model, labels map[string]string) tuneModelInfo {
	return tuneModelInfo{ID: m.ID, Name: m.Name, TrainedCtx: f.TrainedCtx(), Vision: f.Vision, Draft: m.Draft,
		DraftBaked: labels != nil && labels[builder.LabelDraft] != "", Modality: m.Mod(), Arch: f.Arch}
}

func engineInfo(tg tuning.Target) tuneEngineInfo {
	return tuneEngineInfo{Name: tg.Engine, Compute: tg.Compute, MemGB: round2(tg.MemGB), UsableGB: round2(tg.UsableGB())}
}

func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }

// priced is a tuning with its estimate, for the API.
type priced struct {
	Tuning   tuning.Tuning   `json:"tuning"`
	Estimate tuning.Estimate `json:"estimate"`
	Fits     bool            `json:"fits"`
	Warning  string          `json:"warning,omitempty"`
	Notes    []string        `json:"notes"`
}

func price(t tuning.Tuning, notes []string, f tuning.Model, tg tuning.Target) priced {
	e := tuning.EstimateMemory(t, f)
	fits, warn := tuning.Fit(e, tg)
	if notes == nil {
		notes = []string{}
	}
	return priced{Tuning: t, Estimate: e, Fits: fits, Warning: warn, Notes: notes}
}

// imageTuning is an image's model, facts, target and baked tuning.
type imageTuning struct {
	labels  map[string]string
	model   catalog.Model
	facts   tuning.Model
	target  tuning.Target
	engine  string
	compute string
	baked   tuning.Tuning
}

var errNotChatImage = errors.New("runtime tuning is for llama.cpp chat images")

func (s *Server) loadImageTuning(ctx context.Context, ref, engine string) (*imageTuning, int, error) {
	if strings.TrimSpace(ref) == "" {
		return nil, http.StatusBadRequest, errors.New("ref is required")
	}
	engine = normalizeEngine(engine)
	labels, err := s.imageLabels(ctx, engine, ref)
	if err != nil {
		return nil, http.StatusNotFound, err
	}
	if e := labels["local-llm.engine"]; e != "" {
		engine = normalizeEngine(e)
	}
	m, ok := s.cat.Get(labels["local-llm.model"])
	if !ok {
		return nil, http.StatusBadRequest, fmt.Errorf("%s wasn't built from a model in this factory's catalog", ref)
	}
	if m.Rt() != "cpp" || !llamaChat(m) {
		return nil, http.StatusBadRequest, fmt.Errorf("%w; %s is a %s model", errNotChatImage, m.Name, m.Mod())
	}
	it := &imageTuning{labels: labels, model: m, engine: engine, compute: labels["local-llm.compute"]}
	it.facts = s.modelFacts(ctx, m, labels)
	it.target = s.tuneTarget(ctx, engine, it.compute)
	it.baked = bakedView(labels)
	return it, http.StatusOK, nil
}

// llamaChat reports whether m runs in a llama.cpp (llmgate) image.
func llamaChat(m catalog.Model) bool {
	switch m.Mod() {
	case "text", "code", "reasoning", "vision", "embedding":
		return m.Rt() == "cpp"
	}
	return false
}

// effective merges an image's tuning layers: baked < saved < request.
func effectiveTuning(it *imageTuning, saved *tuning.Tuning, req tuning.Tuning) (tuning.Tuning, []string, error) {
	t := it.baked
	if saved != nil {
		t = tuning.Overlay(t, *saved)
	}
	t = tuning.Overlay(t, req)
	return tuning.Resolve(t, it.facts, it.target)
}

// ---- handlers ------------------------------------------------------------------

// handleTuning: GET (an image's tuning), PUT (save), DELETE (forget).
func (s *Server) handleTuning(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleTuningGet(w, r)
	case http.MethodPut:
		s.handleTuningPut(w, r)
	case http.MethodDelete:
		ref, engine := r.URL.Query().Get("ref"), r.URL.Query().Get("engine")
		it, code, err := s.loadImageTuning(r.Context(), ref, engine)
		if err != nil {
			writeJSON(w, code, map[string]string{"error": err.Error()})
			return
		}
		if err := s.tuning().save(ref, it.engine, nil); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		http.Error(w, "GET, PUT or DELETE", http.StatusMethodNotAllowed)
	}
}

// handleTuningGet answers GET /api/tuning?ref=&engine= with
//
//	{baked, saved (null = none), effective, source ("baked"|"saved"),
//	 estimate, fits, warning, notes, presets:[Option], model:{...}, engine:{...}}
func (s *Server) handleTuningGet(w http.ResponseWriter, r *http.Request) {
	ref, engine := r.URL.Query().Get("ref"), r.URL.Query().Get("engine")
	it, code, err := s.loadImageTuning(r.Context(), ref, engine)
	if err != nil {
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	saved := s.tuning().saved(ref, it.engine)
	source := "baked"
	if saved != nil {
		source = "saved"
	}
	eff, notes, err := effectiveTuning(it, saved, tuning.Tuning{})
	resp := map[string]any{
		"ref": ref, "baked": it.baked, "saved": saved, "source": source,
		"presets": tuning.Presets(it.facts, it.target),
		"model":   modelInfo(it.model, it.facts, it.labels),
		"engine":  engineInfo(it.target),
	}
	if err != nil {
		// A saved tuning the image can no longer take: show it, with why.
		resp["effective"], resp["error"] = it.baked, err.Error()
		eff, notes = it.baked, nil
	} else {
		resp["effective"] = eff
	}
	p := price(eff, notes, it.facts, it.target)
	resp["estimate"], resp["fits"], resp["warning"], resp["notes"] = p.Estimate, p.Fits, p.Warning, p.Notes
	writeJSON(w, http.StatusOK, resp)
}

// handleTuningPut saves PUT /api/tuning {ref, engine, tuning} for the image:
// the tuning is checked against the image (merged on its baked tuning) and
// stored resolved. It answers {saved, effective, estimate, fits, warning, notes}.
func (s *Server) handleTuningPut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ref    string          `json:"ref"`
		Engine string          `json:"engine"`
		Tuning json.RawMessage `json:"tuning"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	tu, err := tuning.Parse(req.Tuning)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	it, code, err := s.loadImageTuning(r.Context(), req.Ref, req.Engine)
	if err != nil {
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	eff, notes, err := effectiveTuning(it, nil, tu)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "notes": nonNil(notes)})
		return
	}
	saved, _, err := tuning.Resolve(tu, it.facts, it.target)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if err := s.tuning().save(req.Ref, it.engine, &saved); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	p := price(eff, notes, it.facts, it.target)
	writeJSON(w, http.StatusOK, map[string]any{"saved": saved, "effective": eff, "estimate": p.Estimate,
		"fits": p.Fits, "warning": p.Warning, "notes": p.Notes})
}

func nonNil(n []string) []string {
	if n == nil {
		return []string{}
	}
	return n
}

// catalogTuning is a model's facts and target before an image exists.
func (s *Server) catalogTuning(ctx context.Context, modelID, engine, compute string) (catalog.Model, tuning.Model, tuning.Target, error) {
	m, ok := s.cat.Get(modelID)
	if !ok {
		return m, tuning.Model{}, tuning.Target{}, errors.New("unknown model id: " + modelID)
	}
	if !llamaChat(m) {
		return m, tuning.Model{}, tuning.Target{}, fmt.Errorf("%w; %s is a %s model", errNotChatImage, m.Name, m.Mod())
	}
	if engine != "native" {
		engine = normalizeEngine(engine)
	}
	return m, s.modelFacts(ctx, m, nil), s.tuneTarget(ctx, engine, compute), nil
}

// handleTuningPresets answers GET /api/tuning/presets?model=&compute=&engine=
// (engine docker|podman|native) with {presets:[Option], model:{...}, engine:{...}}.
func (s *Server) handleTuningPresets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	m, f, tg, err := s.catalogTuning(r.Context(), q.Get("model"), q.Get("engine"), q.Get("compute"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"presets": tuning.Presets(f, tg), "model": modelInfo(m, f, nil), "engine": engineInfo(tg),
	})
}

// handleTuningEstimate prices a tuning: POST /api/tuning/estimate with
// {ref, engine, tuning} (merged on the image's baked tuning, as a run would
// be without the saved one) or {model, compute, engine, tuning}. It answers
// {tuning (normalized), estimate, fits, warning, notes}, or 400 {error, notes}.
func (s *Server) handleTuningEstimate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Ref     string          `json:"ref"`
		Model   string          `json:"model"`
		Engine  string          `json:"engine"`
		Compute string          `json:"compute"`
		Tuning  json.RawMessage `json:"tuning"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	tu, err := tuning.Parse(req.Tuning)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var f tuning.Model
	var tg tuning.Target
	var eff tuning.Tuning
	var notes []string
	if req.Ref != "" {
		it, code, err := s.loadImageTuning(r.Context(), req.Ref, req.Engine)
		if err != nil {
			writeJSON(w, code, map[string]string{"error": err.Error()})
			return
		}
		f, tg = it.facts, it.target
		eff, notes, err = effectiveTuning(it, nil, tu)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "notes": nonNil(notes)})
			return
		}
	} else {
		if req.Model == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ref or model is required"})
			return
		}
		_, f, tg, err = s.catalogTuning(r.Context(), req.Model, req.Engine, req.Compute)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		eff, notes, err = tuning.Resolve(tu, f, tg)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "notes": nonNil(notes)})
			return
		}
	}
	writeJSON(w, http.StatusOK, price(eff, notes, f, tg))
}

// ---- build and run ---------------------------------------------------------------

// buildTuning resolves a build request's "tuning" for model m on engine and
// compute. ctxSize is the request's ctx_size (the plan's context tier): it
// applies when the tuning sets no context and picks no preset. It returns
// nil for no tuning, and the draft model to bake for spec "draft".
func (s *Server) buildTuning(ctx context.Context, raw json.RawMessage, m catalog.Model, engine, compute string, ctxSize int) (*tuning.Tuning, *catalog.Model, error) {
	tu, err := tuning.Parse(raw)
	if err != nil || tu.IsZero() {
		return nil, nil, err
	}
	if !llamaChat(m) {
		return nil, nil, fmt.Errorf("%w; %s is a %s model", errNotChatImage, m.Name, m.Mod())
	}
	if tu.CtxSize == 0 && !tu.Generated() && ctxSize > 0 {
		tu.CtxSize = ctxSize
	}
	f := s.modelFacts(ctx, m, nil)
	resolved, _, err := tuning.Resolve(tu, f, s.tuneTarget(ctx, engine, compute))
	if err != nil {
		return nil, nil, err
	}
	var draft *catalog.Model
	if resolved.Spec == tuning.SpecDraft {
		d, ok := s.cat.Get(resolved.DraftModel)
		if !ok {
			return nil, nil, fmt.Errorf("draft model %q isn't in the catalog", resolved.DraftModel)
		}
		if resolved.DraftModel != m.Draft {
			return nil, nil, fmt.Errorf("%s's draft model is %q (the catalog pairs models that share a tokenizer); %q isn't paired with it", m.Name, m.Draft, resolved.DraftModel)
		}
		draft = &d
	}
	return &resolved, draft, nil
}

// runTuning is a run's tuning: the image's layers merged (baked < saved <
// request, or baked < request with base "baked"), the -e overrides of the
// image's ENV, and the estimate. nil when nothing anywhere sets a tuning,
// so an untuned image runs exactly as before.
type runTuning struct {
	effective tuning.Tuning
	env       []string
	label     string
	priced    priced
}

func (s *Server) runTuningFor(ctx context.Context, ref, engine string, raw json.RawMessage, base string) (*runTuning, error) {
	req, err := tuning.Parse(raw)
	if err != nil {
		return nil, err
	}
	switch base {
	case "", "saved", "baked":
	default:
		return nil, fmt.Errorf(`tuning_base %q isn't "saved" or "baked"`, base)
	}
	var saved *tuning.Tuning
	if base != "baked" {
		saved = s.tuning().saved(ref, engine)
	}
	labels, _ := s.imageLabels(ctx, engine, ref)
	if req.IsZero() && saved == nil && builder.BakedTuning(labels).IsZero() {
		return nil, nil
	}
	it, _, err := s.loadImageTuning(ctx, ref, engine)
	if err != nil {
		if req.IsZero() {
			return nil, nil // not a tunable image; the run goes on as before
		}
		return nil, err
	}
	eff, notes, err := effectiveTuning(it, saved, req)
	if err != nil {
		return nil, err
	}
	return &runTuning{
		effective: eff,
		env:       tuning.EnvChange(it.baked, eff, tuning.ImageDraftPath),
		label:     builder.LabelTuning + "=" + eff.Label(),
		priced:    price(eff, notes, it.facts, it.target),
	}, nil
}

// packageTuning is a native package's tuning: the request's (resolved for
// this computer, as the package's likeliest home), else the image's baked
// one when packaging an image. ctxSize is the package's context, which wins.
func (n *nativeAPI) packageTuning(ctx context.Context, raw json.RawMessage, m catalog.Model, ref, engine, accel string, ctxSize int) (*tuning.Tuning, *catalog.Model, error) {
	if t, err := tuning.Parse(raw); err != nil || !t.IsZero() {
		return n.s.buildTuning(ctx, raw, m, "native", accel, ctxSize)
	}
	if ref == "" {
		return nil, nil, nil
	}
	labels, err := n.s.imageLabels(ctx, normalizeEngine(engine), ref)
	if err != nil {
		return nil, nil, nil
	}
	t := builder.BakedTuning(labels)
	if t.IsZero() {
		return nil, nil, nil
	}
	if ctxSize > 0 {
		t.CtxSize = ctxSize
	}
	var draft *catalog.Model
	if t.Spec == tuning.SpecDraft {
		d, ok := n.s.cat.Get(labels[builder.LabelDraft])
		if !ok {
			t.Spec, t.DraftModel = tuning.SpecNgram, "" // the image's draft isn't in the catalog any more
		} else {
			draft = &d
		}
	}
	return &t, draft, nil
}

// tunedFit is the run guardrail for a tuned run: its estimate against the
// engine's memory. A GPU (Vulkan) run that can't fit is blocked (it needs
// "Run anyway"); a CPU one is only warned, since it can page.
func tunedFit(rt *runTuning, compute string) (block bool, msg string) {
	if rt.priced.Fits {
		return false, ""
	}
	msg = "With these settings the model " + rt.priced.Warning +
		". Pick a smaller context, a quantized KV cache or a lighter preset, or give the engine more memory."
	return compute == "vulkan", msg
}
