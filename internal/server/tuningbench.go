package server

// The tuning benchmark (docs/TUNING.md): run an image once per candidate
// tuning, one at a time, and measure it.
//
//	POST /api/tuning/benchmark              {ref, engine, compute, candidates:[Tuning] | presets:[id], workload, force} -> {id}
//	GET  /api/tuning/benchmark/{id}         the job: {state, step, total, current, results:[...], ...}
//	POST /api/tuning/benchmark/{id}/cancel  stop it (its container is removed)
//	POST /api/tuning/benchmark/{id}/apply   {index}: save that result's tuning for the image
//
// Each candidate gets its own container, localllm-tune-<id>, on an ephemeral
// loopback port. The factory waits for /health, sends a warm-up, then the
// workload through the gate with "harness": false (temperature 0, 256 new
// tokens), and reads llama-server's timings. The container is always removed:
// after each candidate, on error, on cancel and when the factory shuts down
// (and at startup, for one a crash left behind: builder.RecoverOnStartup).

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/seanbrill/local-llm/internal/builder"
	"github.com/seanbrill/local-llm/internal/tuning"
)

// benchEngine is what the benchmark does on a container engine (tests fake it).
type benchEngine interface {
	Run(ctx context.Context, o builder.RunOptions) error
	Port(ctx context.Context, engine, name string) (int, error)
	MemGB(ctx context.Context, engine, name string) (float64, error)
	Logs(ctx context.Context, engine, name string) string
	Remove(ctx context.Context, engine, name string) error
}

type builderBenchEngine struct{ b *builder.Builder }

func (e builderBenchEngine) Run(ctx context.Context, o builder.RunOptions) error {
	_, err := e.b.Run(ctx, o)
	return err
}

func (builderBenchEngine) Port(ctx context.Context, engine, name string) (int, error) {
	return builder.PublishedPort(ctx, engine, name)
}

func (builderBenchEngine) MemGB(ctx context.Context, engine, name string) (float64, error) {
	return builder.ContainerMemGB(ctx, engine, name)
}

func (builderBenchEngine) Logs(ctx context.Context, engine, name string) string {
	out, _ := builder.EngineOutput(ctx, engine, "logs", "--tail", "20", name)
	return strings.TrimSpace(string(out))
}

func (builderBenchEngine) Remove(ctx context.Context, engine, name string) error {
	_, err := builder.EngineOutput(ctx, engine, "rm", "-f", name)
	return err
}

// benchResult is one candidate's measurements.
type benchResult struct {
	Label    string          `json:"label"`
	Tuning   tuning.Tuning   `json:"tuning"`
	Estimate tuning.Estimate `json:"estimate"`
	OK       bool            `json:"ok"`
	Error    string          `json:"error,omitempty"`
	Skipped  bool            `json:"skipped,omitempty"` // not run (it doesn't fit, or isn't valid)
	LoadS    float64         `json:"load_s"`            // container start to healthy
	// PromptTPS and GenTPS are llama-server's prompt_per_second and
	// predicted_per_second for the workload; TTFTMs is its prompt time.
	PromptTPS float64 `json:"prompt_tps"`
	GenTPS    float64 `json:"gen_tps"`
	TTFTMs    float64 `json:"ttft_ms"`
	MemGB     float64 `json:"mem_gb"` // the container's memory after the workload (0 = unknown)
	// DraftAcceptRate is draft_n_accepted / draft_n (null without look-ahead).
	DraftAcceptRate *float64 `json:"draft_accept_rate"`
	PromptTokens    int      `json:"prompt_tokens"`
	GenTokens       int      `json:"gen_tokens"`
}

type benchJob struct {
	ID       string        `json:"id"`
	Ref      string        `json:"ref"`
	Engine   string        `json:"engine"`
	Compute  string        `json:"compute"`
	Workload string        `json:"workload"`
	State    string        `json:"state"` // queued | running | done | error | canceled
	Step     int           `json:"step"`  // candidates finished
	Total    int           `json:"total"`
	Current  string        `json:"current"` // the candidate running ("" when none)
	Phase    string        `json:"phase"`   // starting | loading | warmup | workload | ""
	Results  []benchResult `json:"results"`
	Error    string        `json:"error,omitempty"`
	Log      []string      `json:"log"`
	Started  time.Time     `json:"started_at"`
	Finished *time.Time    `json:"finished_at,omitempty"`

	cands  []benchCandidate
	it     *imageTuning
	cancel context.CancelFunc
	done   chan struct{}
}

type benchCandidate struct {
	label  string
	tuning tuning.Tuning
}

type benchManager struct {
	s   *Server
	eng benchEngine

	mu   sync.Mutex
	jobs map[string]*benchJob
	ids  []string // oldest first
	wg   sync.WaitGroup

	client      *http.Client
	loadTimeout time.Duration
	pollEvery   time.Duration
	genTokens   int
}

func newBenchManager(s *Server) *benchManager {
	return &benchManager{
		s: s, eng: builderBenchEngine{s.b}, jobs: map[string]*benchJob{},
		client:      &http.Client{Timeout: 10 * time.Minute},
		loadTimeout: 10 * time.Minute, pollEvery: time.Second, genTokens: 256,
	}
}

const maxBenchCandidates = 8

func (m *benchManager) snapshot(id string) (benchJob, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return benchJob{}, false
	}
	c := *j
	c.Results = append([]benchResult{}, j.Results...)
	c.Log = append([]string{}, j.Log...)
	return c, true
}

func (m *benchManager) update(j *benchJob, f func(j *benchJob)) {
	m.mu.Lock()
	f(j)
	m.mu.Unlock()
}

func (m *benchManager) logf(j *benchJob, format string, args ...any) {
	line := time.Now().Format("15:04:05") + " " + fmt.Sprintf(format, args...)
	m.update(j, func(j *benchJob) {
		j.Log = append(j.Log, line)
		if len(j.Log) > 500 {
			j.Log = j.Log[len(j.Log)-500:]
		}
	})
}

// running is the benchmark in progress, if any (one runs at a time: two
// would measure each other).
func (m *benchManager) running() *benchJob {
	for _, j := range m.jobs {
		if j.State == "queued" || j.State == "running" {
			return j
		}
	}
	return nil
}

func (m *benchManager) start(j *benchJob) error {
	m.mu.Lock()
	if r := m.running(); r != nil {
		m.mu.Unlock()
		return fmt.Errorf("a benchmark of %s is already running (%s)", r.Ref, r.ID)
	}
	ctx, cancel := context.WithCancel(context.Background())
	j.cancel, j.done, j.State, j.Started = cancel, make(chan struct{}), "running", time.Now().UTC()
	m.jobs[j.ID] = j
	m.ids = append(m.ids, j.ID)
	for len(m.ids) > 10 { // keep the last few finished ones
		old := m.ids[0]
		if o := m.jobs[old]; o != nil && (o.State == "running" || o.State == "queued") {
			break
		}
		delete(m.jobs, old)
		m.ids = m.ids[1:]
	}
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		defer close(j.done)
		defer cancel()
		m.run(ctx, j)
	}()
	return nil
}

func (m *benchManager) cancelJob(id string) bool {
	m.mu.Lock()
	j, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		return false
	}
	if j.cancel != nil {
		j.cancel()
	}
	return true
}

// closeAll cancels every benchmark and waits (up to timeout) for their
// containers to be removed.
func (m *benchManager) closeAll(timeout time.Duration) {
	m.mu.Lock()
	for _, j := range m.jobs {
		if j.cancel != nil {
			j.cancel()
		}
	}
	m.mu.Unlock()
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

func (m *benchManager) run(ctx context.Context, j *benchJob) {
	m.logf(j, "Benchmarking %s on %s (%s workload, %d candidates)", j.Ref, j.Engine, j.Workload, len(j.cands))
	for i, c := range j.cands {
		if ctx.Err() != nil {
			break
		}
		m.update(j, func(j *benchJob) { j.Current, j.Phase = c.label, "starting" })
		r := m.runOne(ctx, j, c)
		m.update(j, func(j *benchJob) { j.Results = append(j.Results, r); j.Step = i + 1 })
		if r.OK {
			m.logf(j, "%s: prompt %.1f t/s, generation %.1f t/s, loaded in %.1fs", c.label, r.PromptTPS, r.GenTPS, r.LoadS)
		} else {
			m.logf(j, "%s: %s", c.label, r.Error)
		}
	}
	now := time.Now().UTC()
	m.update(j, func(j *benchJob) {
		j.Current, j.Phase, j.Finished = "", "", &now
		switch {
		case ctx.Err() != nil:
			j.State = "canceled"
		case !anyOK(j.Results):
			j.State, j.Error = "error", "no candidate ran"
		default:
			j.State = "done"
		}
	})
	m.logf(j, "Benchmark %s", j.State)
}

func anyOK(rs []benchResult) bool {
	for _, r := range rs {
		if r.OK {
			return true
		}
	}
	return false
}

func benchName(id string) string { return "localllm-tune-" + id }

// runOne runs one candidate in a fresh container and always removes it.
func (m *benchManager) runOne(ctx context.Context, j *benchJob, c benchCandidate) (r benchResult) {
	it := j.it
	r = benchResult{Label: c.label, Tuning: c.tuning}
	eff, notes, err := effectiveTuning(it, nil, c.tuning)
	if err != nil {
		r.Error, r.Skipped = err.Error(), true
		return r
	}
	p := price(eff, notes, it.facts, it.target)
	r.Tuning, r.Estimate = eff, p.Estimate
	if !p.Fits && it.compute == "vulkan" {
		r.Error, r.Skipped = "skipped: it "+p.Warning, true
		return r
	}

	name := benchName(j.ID)
	memGB, _ := strconv.ParseFloat(it.labels["local-llm.memory"], 64)
	o := builder.RunOptions{
		Ref: j.Ref, HostPort: -1, Engine: j.Engine, Compute: it.compute, MemoryGB: memGB,
		Name: name, Tool: "tune",
		Env:    tuning.EnvChange(it.baked, eff, tuning.ImageDraftPath),
		Labels: []string{builder.LabelTuning + "=" + eff.Label()},
	}
	defer func() {
		// Removal runs even when ctx is canceled (cancel, shutdown).
		rctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := m.eng.Remove(rctx, j.Engine, name); err != nil {
			m.logf(j, "removing %s: %v", name, err)
		}
	}()
	start := time.Now()
	if err := m.eng.Run(ctx, o); err != nil {
		r.Error = "start: " + err.Error()
		return r
	}
	port, err := m.eng.Port(ctx, j.Engine, name)
	if err != nil {
		r.Error = "port: " + err.Error()
		return r
	}
	m.update(j, func(j *benchJob) { j.Phase = "loading" })
	if err := m.waitHealthy(ctx, port); err != nil {
		r.Error = err.Error()
		if ctx.Err() == nil {
			lctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if tail := m.eng.Logs(lctx, j.Engine, name); tail != "" {
				r.Error += "\n" + tail
			}
			cancel()
		}
		return r
	}
	r.LoadS = round2(time.Since(start).Seconds())

	m.update(j, func(j *benchJob) { j.Phase = "warmup" })
	if _, err := m.chat(ctx, port, warmupMessages(), 8, false); err != nil {
		r.Error = "warm-up: " + err.Error()
		return r
	}
	m.update(j, func(j *benchJob) { j.Phase = "workload" })
	tm, err := m.chat(ctx, port, workloadMessages(j.Workload), m.genTokens, true)
	if err != nil {
		r.Error = "workload: " + err.Error()
		return r
	}
	r.PromptTPS, r.GenTPS, r.TTFTMs = round2(tm.PromptPerSecond), round2(tm.PredictedPerSecond), round2(tm.PromptMs)
	r.PromptTokens, r.GenTokens = tm.PromptN, tm.PredictedN
	if tm.DraftN > 0 {
		rate := round2(float64(tm.DraftNAccepted) / float64(tm.DraftN))
		r.DraftAcceptRate = &rate
	}
	sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	if mem, err := m.eng.MemGB(sctx, j.Engine, name); err == nil {
		r.MemGB = round2(mem)
	}
	cancel()
	r.OK = true
	return r
}

func (m *benchManager) waitHealthy(ctx context.Context, port int) error {
	deadline := time.Now().Add(m.loadTimeout)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.s.modelURL(port, "/health"), nil)
		if err != nil {
			return err
		}
		resp, err := m.client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if ctx.Err() != nil {
			return errors.New("canceled")
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the model didn't load within %s", m.loadTimeout)
		}
		select {
		case <-ctx.Done():
			return errors.New("canceled")
		case <-time.After(m.pollEvery):
		}
	}
}

// llamaTimings is llama-server's "timings" on a completion.
type llamaTimings struct {
	PromptN            int     `json:"prompt_n"`
	PromptMs           float64 `json:"prompt_ms"`
	PromptPerSecond    float64 `json:"prompt_per_second"`
	PredictedN         int     `json:"predicted_n"`
	PredictedPerSecond float64 `json:"predicted_per_second"`
	DraftN             int     `json:"draft_n"`
	DraftNAccepted     int     `json:"draft_n_accepted"`
}

func (m *benchManager) chat(ctx context.Context, port int, msgs []map[string]string, maxTokens int, needTimings bool) (llamaTimings, error) {
	body, _ := json.Marshal(map[string]any{
		"messages": msgs, "max_tokens": maxTokens, "temperature": 0, "stream": false,
		"harness":      false, // measure the model, not the agent loop
		"cache_prompt": false,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.s.modelURL(port, "/v1/chat/completions"), bytes.NewReader(body))
	if err != nil {
		return llamaTimings{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		return llamaTimings{}, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return llamaTimings{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data[:min(len(data), 300)])))
	}
	var out struct {
		Timings *llamaTimings `json:"timings"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return llamaTimings{}, fmt.Errorf("reading the reply: %v", err)
	}
	if out.Timings == nil {
		if needTimings {
			return llamaTimings{}, errors.New("the reply has no timings (is this llama-server?)")
		}
		return llamaTimings{}, nil
	}
	return *out.Timings, nil
}

func warmupMessages() []map[string]string {
	return []map[string]string{{"role": "user", "content": "Reply with the single word: ready."}}
}

// workloadMessages is the measured request. "coding" is a ~1.5K-token Go
// file to revise and return, the shape of an agent's edit (most of the
// answer repeats the prompt, which look-ahead speeds up); "chat" is a short
// question with a long answer.
func workloadMessages(workload string) []map[string]string {
	if workload == "chat" {
		return []map[string]string{
			{"role": "system", "content": "You are a helpful assistant. Answer clearly and thoroughly."},
			{"role": "user", "content": "Explain how a hash map works, how collisions are handled, and when you would pick a balanced tree instead. Give a short example in Python."},
		}
	}
	return []map[string]string{
		{"role": "system", "content": "You are a careful senior Go engineer. When asked to change code, reply with the complete updated file in one Go code block and nothing else."},
		{"role": "user", "content": "Add a doc comment to every exported identifier in this file and make Get return a copy of the entry's tags. Return the whole file.\n\n```go\n" + benchGoFile + "```"},
	}
}

// benchGoFile is the coding workload's file (~1.3K tokens).
const benchGoFile = `package inventory

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

var ErrNotFound = errors.New("inventory: item not found")
var ErrExists = errors.New("inventory: item already exists")

type Item struct {
	SKU       string
	Name      string
	Quantity  int
	PriceCents int64
	Tags      []string
	UpdatedAt time.Time
}

type Store struct {
	mu    sync.RWMutex
	items map[string]*Item
	now   func() time.Time
}

func NewStore() *Store {
	return &Store{items: map[string]*Item{}, now: time.Now}
}

func (s *Store) Add(it Item) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.ToUpper(strings.TrimSpace(it.SKU))
	if key == "" {
		return errors.New("inventory: empty SKU")
	}
	if _, ok := s.items[key]; ok {
		return ErrExists
	}
	it.SKU = key
	it.UpdatedAt = s.now()
	s.items[key] = &it
	return nil
}

func (s *Store) Get(sku string) (Item, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	it, ok := s.items[strings.ToUpper(strings.TrimSpace(sku))]
	if !ok {
		return Item{}, ErrNotFound
	}
	return *it, nil
}

func (s *Store) Adjust(sku string, delta int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.items[strings.ToUpper(strings.TrimSpace(sku))]
	if !ok {
		return 0, ErrNotFound
	}
	if it.Quantity+delta < 0 {
		return it.Quantity, errors.New("inventory: quantity would go negative")
	}
	it.Quantity += delta
	it.UpdatedAt = s.now()
	return it.Quantity, nil
}

func (s *Store) Remove(sku string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.ToUpper(strings.TrimSpace(sku))
	if _, ok := s.items[key]; !ok {
		return ErrNotFound
	}
	delete(s.items, key)
	return nil
}

func (s *Store) Tagged(tag string) []Item {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Item
	for _, it := range s.items {
		for _, t := range it.Tags {
			if strings.EqualFold(t, tag) {
				out = append(out, *it)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SKU < out[j].SKU })
	return out
}

func (s *Store) LowStock(threshold int) []Item {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Item
	for _, it := range s.items {
		if it.Quantity <= threshold {
			out = append(out, *it)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Quantity != out[j].Quantity {
			return out[i].Quantity < out[j].Quantity
		}
		return out[i].SKU < out[j].SKU
	})
	return out
}

func (s *Store) Value() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var total int64
	for _, it := range s.items {
		total += int64(it.Quantity) * it.PriceCents
	}
	return total
}

type Report struct {
	Items      int
	Units      int
	ValueCents int64
	Low        []string
}

func (s *Store) Report(threshold int) Report {
	var r Report
	for _, it := range s.LowStock(threshold) {
		r.Low = append(r.Low, it.SKU)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	r.Items = len(s.items)
	for _, it := range s.items {
		r.Units += it.Quantity
		r.ValueCents += int64(it.Quantity) * it.PriceCents
	}
	return r
}
`

func newBenchID() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- handlers ------------------------------------------------------------------

// handleBenchStart starts a benchmark: POST /api/tuning/benchmark with
// {ref, engine, compute, candidates:[Tuning] or presets:[id], workload
// ("coding" default | "chat"), force}. Candidates merge on the image's baked
// tuning. It answers {id}, 409 while another benchmark runs (or, without
// force, when what's already running leaves too little memory).
func (s *Server) handleBenchStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Ref        string            `json:"ref"`
		Engine     string            `json:"engine"`
		Compute    string            `json:"compute"`
		Candidates []json.RawMessage `json:"candidates"`
		Presets    []string          `json:"presets"`
		Workload   string            `json:"workload"`
		Force      bool              `json:"force"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	switch req.Workload {
	case "":
		req.Workload = "coding"
	case "coding", "chat":
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `workload is "coding" or "chat"`})
		return
	}
	var cands []benchCandidate
	for _, id := range req.Presets {
		id = strings.ToLower(strings.TrimSpace(id))
		if !validPreset(id) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("unknown preset %q", id)})
			return
		}
		cands = append(cands, benchCandidate{label: tuning.PresetLabel(id), tuning: tuning.Tuning{Preset: id}})
	}
	for i, raw := range req.Candidates {
		t, err := tuning.Parse(raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("candidate %d: %v", i+1, err)})
			return
		}
		label := fmt.Sprintf("Candidate %d", len(cands)+1)
		if t.Preset != "" && t.Preset != tuning.PresetCustom {
			label = tuning.PresetLabel(t.Preset)
			if !t.Generated() {
				label += " (edited)"
			}
		}
		cands = append(cands, benchCandidate{label: label, tuning: t})
	}
	if len(cands) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "give candidates or presets to compare"})
		return
	}
	if len(cands) > maxBenchCandidates {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("at most %d candidates", maxBenchCandidates)})
		return
	}
	it, code, err := s.loadImageTuning(r.Context(), req.Ref, req.Engine)
	if err != nil {
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	if it.compute == "" {
		it.compute = normalizeCompute(req.Compute)
	}
	if !req.Force {
		if msg, code := s.runFits(r.Context(), it.model.ID, it.compute); msg != "" {
			writeJSON(w, http.StatusConflict, map[string]any{"error": msg, "code": code, "needs_force": true})
			return
		}
	}
	j := &benchJob{ID: newBenchID(), Ref: req.Ref, Engine: it.engine, Compute: it.compute, Workload: req.Workload,
		State: "queued", Total: len(cands), Results: []benchResult{}, Log: []string{}, cands: cands, it: it}
	if err := s.tuning().bench.start(j); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": j.ID, "total": j.Total})
}

func validPreset(id string) bool {
	for _, p := range tuning.PresetIDs {
		if p == id {
			return true
		}
	}
	return false
}

func (s *Server) handleBenchState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	j, ok := s.tuning().bench.snapshot(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such benchmark"})
		return
	}
	writeJSON(w, http.StatusOK, j)
}

func (s *Server) handleBenchCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if !s.tuning().bench.cancelJob(r.PathValue("id")) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such benchmark"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleBenchApply saves result {index}'s tuning for the benchmarked image
// (as PUT /api/tuning would). It answers {saved}.
func (s *Server) handleBenchApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Index int `json:"index"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	j, ok := s.tuning().bench.snapshot(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such benchmark"})
		return
	}
	if req.Index < 0 || req.Index >= len(j.Results) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no result at that index"})
		return
	}
	res := j.Results[req.Index]
	if !res.OK {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "that candidate didn't run: " + res.Error})
		return
	}
	t := res.Tuning
	if err := s.tuning().save(j.Ref, j.Engine, &t); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"saved": t, "ref": j.Ref, "engine": j.Engine})
}
