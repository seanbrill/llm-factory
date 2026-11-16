package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seanbrill/local-llm/internal/builder"
	"github.com/seanbrill/local-llm/internal/catalog"
	"github.com/seanbrill/local-llm/internal/tuning"
)

const (
	tunedRef = "local-llm/qwen3-8b:latest"
	plainRef = "local-llm/plain:latest"
	visRef   = "local-llm/vis:latest"
)

// newTuneServer is a Server with three images faked: a tuned qwen3-8b on a
// 12 GiB Podman machine (no draft baked), an untuned one, and a vision model.
func newTuneServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	b, err := builder.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	cat := &catalog.Catalog{Models: []catalog.Model{
		{ID: "qwen3-8b", Name: "Qwen3 8B", Runtime: "cpp", Modality: "text", Params: "8B", SizeGB: 5.0, File: "Qwen3-8B-Q4_K_M.gguf", Draft: "qwen3-0.6b"},
		{ID: "qwen3-0.6b", Name: "Qwen3 0.6B", Runtime: "cpp", Modality: "text", Params: "0.6B", SizeGB: 0.6, File: "Qwen3-0.6B-Q8_0.gguf"},
		{ID: "gemma-v", Name: "Gemma V", Runtime: "cpp", Modality: "vision", Params: "4B", SizeGB: 3, File: "g.gguf", MMProjFile: "mmproj.gguf"},
	}}
	// As a build stores it: resolved, every preset field set.
	baked := tuning.Tuning{Preset: "speed", CtxSize: 16384, FlashAttn: "on", KVCache: "q8_0", Spec: "ngram", Parallel: 1, CacheRAMMB: tuning.IntPtr(1024)}
	images := map[string]map[string]string{
		tunedRef: {"local-llm.model": "qwen3-8b", "local-llm.engine": "podman", "local-llm.compute": "vulkan",
			"local-llm.ctx": "16384", builder.LabelTuning: baked.Label()},
		plainRef: {"local-llm.model": "qwen3-8b", "local-llm.engine": "podman", "local-llm.compute": "vulkan"},
		visRef:   {"local-llm.model": "gemma-v", "local-llm.engine": "podman", "local-llm.compute": "vulkan"},
	}
	s := &Server{b: b, cat: cat, mux: newRouteMux(), modelHost: "127.0.0.1"}
	s.imageLabelsFn = func(_ context.Context, _, ref string) (map[string]string, error) {
		if l, ok := images[ref]; ok {
			return l, nil
		}
		return nil, errors.New("no such image")
	}
	tu := s.tuning()
	tu.memFn = func(context.Context, string) (float64, error) { return 12.88, nil }
	tu.hostMemFn = func(context.Context) float64 { return 25.77 }
	tu.fetchFn = func(context.Context, string) (tuning.ArchInfo, error) {
		return tuning.ArchInfo{}, errors.New("offline")
	}
	s.registerTuning()
	return s
}

// tcall sends a request through the mux (so {id} patterns resolve).
func tcall(t *testing.T, s *Server, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(method, path, rd))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func q(ref string) string { return "ref=" + url.QueryEscape(ref) + "&engine=podman" }

func TestTuningGetPutDelete(t *testing.T) {
	s := newTuneServer(t)
	code, out := tcall(t, s, "GET", "/api/tuning?"+q(tunedRef), nil)
	if code != 200 {
		t.Fatalf("GET: %d %v", code, out)
	}
	if out["source"] != "baked" || out["saved"] != nil {
		t.Errorf("source %v saved %v", out["source"], out["saved"])
	}
	eff := out["effective"].(map[string]any)
	if eff["ctx_size"].(float64) != 16384 || eff["spec"] != "ngram" || eff["flash_attn"] != "on" {
		t.Errorf("effective: %v", eff)
	}
	m := out["model"].(map[string]any)
	if m["draft"] != "qwen3-0.6b" || m["name"] != "Qwen3 8B" || m["id"] != "qwen3-8b" || m["vision"] != false {
		t.Errorf("model: %v", m)
	}
	if e := out["engine"].(map[string]any); e["name"] != "podman" || e["mem_gb"].(float64) != 12.88 {
		t.Errorf("engine: %v", e)
	}
	presets := out["presets"].([]any)
	if len(presets) != 4 {
		t.Fatalf("presets: %v", presets)
	}
	for _, p := range presets {
		p := p.(map[string]any)
		est := p["estimate"].(map[string]any)
		for _, k := range []string{"id", "label", "explain", "tuning", "fits"} {
			if _, ok := p[k]; !ok {
				t.Errorf("preset missing %s: %v", k, p)
			}
		}
		if est["total_gb"].(float64) <= est["weights_gb"].(float64) {
			t.Errorf("estimate: %v", est)
		}
	}

	// Save a tuning: it becomes the effective one.
	code, out = tcall(t, s, "PUT", "/api/tuning", map[string]any{"ref": tunedRef, "engine": "podman",
		"tuning": map[string]any{"ctx_size": 32768, "kv_cache": "q8_0", "flash_attn": "auto"}})
	if code != 200 {
		t.Fatalf("PUT: %d %v", code, out)
	}
	if !contains(out["notes"], "a quantized cache turns flash attention on") {
		t.Errorf("notes: %v", out["notes"])
	}
	code, out = tcall(t, s, "GET", "/api/tuning?"+q(tunedRef), nil)
	if code != 200 || out["source"] != "saved" || out["effective"].(map[string]any)["ctx_size"].(float64) != 32768 {
		t.Errorf("after PUT: %d %v", code, out)
	}
	data, err := os.ReadFile(filepath.Join(s.b.ConfigDir, "tuning.json"))
	if err != nil || !strings.Contains(string(data), `"podman|`+tunedRef+`"`) {
		t.Errorf("store: %s %v", data, err)
	}

	code, _ = tcall(t, s, "DELETE", "/api/tuning?"+q(tunedRef), nil)
	if code != 200 {
		t.Fatalf("DELETE: %d", code)
	}
	if _, out = tcall(t, s, "GET", "/api/tuning?"+q(tunedRef), nil); out["source"] != "baked" || out["saved"] != nil {
		t.Errorf("after DELETE: %v", out)
	}

	// A bad tuning is refused, with the field named.
	code, out = tcall(t, s, "PUT", "/api/tuning", map[string]any{"ref": tunedRef, "engine": "podman", "tuning": map[string]any{"parallel": 99}})
	if code != 400 || !strings.Contains(out["error"].(string), "parallel") {
		t.Errorf("bad PUT: %d %v", code, out)
	}
	code, _ = tcall(t, s, "PUT", "/api/tuning", map[string]any{"ref": tunedRef, "tuning": map[string]any{"ctx": 4096}})
	if code != 400 {
		t.Errorf("unknown field accepted: %d", code)
	}
	if code, _ = tcall(t, s, "GET", "/api/tuning?ref=nope&engine=podman", nil); code != 404 {
		t.Errorf("missing image: %d", code)
	}
}

func contains(v any, want string) bool {
	l, _ := v.([]any)
	for _, x := range l {
		if x == want {
			return true
		}
	}
	return false
}

func TestTuningPresetsAndEstimate(t *testing.T) {
	s := newTuneServer(t)
	code, out := tcall(t, s, "GET", "/api/tuning/presets?model=qwen3-8b&compute=vulkan&engine=podman", nil)
	if code != 200 || len(out["presets"].([]any)) != 4 {
		t.Fatalf("presets: %d %v", code, out)
	}
	code, out = tcall(t, s, "GET", "/api/tuning/presets?model=qwen3-8b&engine=native", nil)
	if code != 200 || out["engine"].(map[string]any)["compute"] != "metal" {
		t.Fatalf("native presets: %d %v", code, out)
	}

	// By model: a vision model's look-ahead is turned off, with the note.
	code, out = tcall(t, s, "POST", "/api/tuning/estimate", map[string]any{"model": "gemma-v", "engine": "podman", "compute": "vulkan",
		"tuning": map[string]any{"spec": "ngram", "ctx_size": 8192}})
	if code != 200 {
		t.Fatalf("estimate: %d %v", code, out)
	}
	if out["tuning"].(map[string]any)["spec"] != "none" || !contains(out["notes"], "look-ahead is off for vision models") {
		t.Errorf("vision: %v", out)
	}
	for _, k := range []string{"weights_gb", "kv_gb", "total_gb"} {
		if _, ok := out["estimate"].(map[string]any)[k]; !ok {
			t.Errorf("estimate missing %s", k)
		}
	}
	if _, ok := out["fits"].(bool); !ok {
		t.Errorf("fits: %v", out["fits"])
	}

	// By image: merged on the baked tuning; a bigger context costs more.
	_, small := tcall(t, s, "POST", "/api/tuning/estimate", map[string]any{"ref": tunedRef, "engine": "podman", "tuning": map[string]any{}})
	_, big := tcall(t, s, "POST", "/api/tuning/estimate", map[string]any{"ref": tunedRef, "engine": "podman", "tuning": map[string]any{"ctx_size": 65536}})
	if small["tuning"].(map[string]any)["ctx_size"].(float64) != 16384 {
		t.Errorf("baked ctx: %v", small["tuning"])
	}
	if gb := func(o map[string]any) float64 { return o["estimate"].(map[string]any)["kv_gb"].(float64) }; gb(big) <= gb(small) {
		t.Errorf("kv: %v vs %v", big["estimate"], small["estimate"])
	}

	// A draft the image didn't bake.
	code, out = tcall(t, s, "POST", "/api/tuning/estimate", map[string]any{"ref": tunedRef, "engine": "podman", "tuning": map[string]any{"spec": "draft"}})
	if code != 400 || !strings.Contains(out["error"].(string), "no draft model baked") {
		t.Errorf("draft: %d %v", code, out)
	}
	// Not a chat model.
	if code, _ = tcall(t, s, "POST", "/api/tuning/estimate", map[string]any{"tuning": map[string]any{}}); code != 400 {
		t.Errorf("no ref or model: %d", code)
	}
}

func TestBuildTuning(t *testing.T) {
	s := newTuneServer(t)
	m, _ := s.cat.Get("qwen3-8b")
	ctx := context.Background()

	got, draft, err := s.buildTuning(ctx, json.RawMessage(`{"spec":"draft","ctx_size":8192}`), m, "podman", "vulkan", 0)
	if err != nil || draft == nil || draft.ID != "qwen3-0.6b" || got.DraftModel != "qwen3-0.6b" {
		t.Fatalf("draft build: %+v %v %v", got, draft, err)
	}
	// ctx_size fills a tuning that sets none; a preset sizes its own.
	got, _, _ = s.buildTuning(ctx, json.RawMessage(`{"kv_cache":"q8_0"}`), m, "podman", "vulkan", 32768)
	if got.CtxSize != 32768 || got.FlashAttn != "on" {
		t.Errorf("ctx_size shortcut: %+v", got)
	}
	got, _, err = s.buildTuning(ctx, json.RawMessage(`{"preset":"speed"}`), m, "podman", "vulkan", 4096)
	if err != nil || got.CtxSize == 4096 || got.CtxSize == 0 || got.KVCache != "q8_0" {
		t.Errorf("preset: %+v %v", got, err)
	}
	// No tuning: nothing changes.
	if got, _, err = s.buildTuning(ctx, nil, m, "podman", "vulkan", 8192); got != nil || err != nil {
		t.Errorf("no tuning: %+v %v", got, err)
	}
	// A model with no paired draft.
	v, _ := s.cat.Get("qwen3-0.6b")
	if _, _, err = s.buildTuning(ctx, json.RawMessage(`{"spec":"draft"}`), v, "podman", "vulkan", 0); err == nil || !strings.Contains(err.Error(), "ngram") {
		t.Errorf("unpaired draft: %v", err)
	}
}

func TestRunTuningLayers(t *testing.T) {
	s := newTuneServer(t)
	ctx := context.Background()

	// An untuned image with no request: the run is unchanged.
	if rt, err := s.runTuningFor(ctx, plainRef, "podman", nil, ""); rt != nil || err != nil {
		t.Fatalf("plain: %+v %v", rt, err)
	}
	// The baked tuning alone: no -e overrides.
	rt, err := s.runTuningFor(ctx, tunedRef, "podman", nil, "")
	if err != nil || rt == nil || len(rt.env) != 0 || rt.effective.CtxSize != 16384 {
		t.Fatalf("baked: %+v %v", rt, err)
	}
	// Saved over baked, request over saved.
	saved := tuning.Tuning{CtxSize: 32768, Parallel: 2, KVUnified: tuning.BoolPtr(true)}
	if err := s.tuning().save(tunedRef, "podman", &saved); err != nil {
		t.Fatal(err)
	}
	rt, err = s.runTuningFor(ctx, tunedRef, "podman", json.RawMessage(`{"ctx_size":24576}`), "")
	if err != nil {
		t.Fatal(err)
	}
	env := strings.Join(rt.env, " ")
	for _, want := range []string{"CTX_SIZE=24576", "PARALLEL=2", "KV_UNIFIED=1"} {
		if !strings.Contains(env, want) {
			t.Errorf("env %q missing %s", env, want)
		}
	}
	if strings.Contains(env, "SPEC_TYPE") || strings.Contains(env, "KV_CACHE_TYPE") {
		t.Errorf("unchanged settings passed again: %s", env)
	}
	if !strings.HasPrefix(rt.label, builder.LabelTuning+"=") || rt.priced.Estimate.TotalGB <= 0 {
		t.Errorf("label %q estimate %+v", rt.label, rt.priced.Estimate)
	}
	// tuning_base "baked" skips the saved settings.
	rt, err = s.runTuningFor(ctx, tunedRef, "podman", json.RawMessage(`{"spec":"none"}`), "baked")
	if err != nil || rt.effective.Parallel == 2 || rt.effective.CtxSize != 16384 || strings.Join(rt.env, " ") != "SPEC_TYPE=none" {
		t.Errorf("baked base: %+v %v", rt, err)
	}
	if _, err = s.runTuningFor(ctx, tunedRef, "podman", nil, "mine"); err == nil {
		t.Error("a bad tuning_base was accepted")
	}
	// A context the 12 GiB machine can't hold is blocked on Vulkan.
	rt, err = s.runTuningFor(ctx, tunedRef, "podman", json.RawMessage(`{"ctx_size":131072,"kv_cache":"f16","cache_ram_mb":-1}`), "baked")
	if err != nil {
		t.Fatal(err)
	}
	if block, msg := tunedFit(rt, "vulkan"); !block || !strings.Contains(msg, "Podman machine") {
		t.Errorf("guardrail: %v %q (estimate %+v)", block, msg, rt.priced.Estimate)
	}
}

// ---- benchmark ------------------------------------------------------------------

type fakeBenchEngine struct {
	mu      sync.Mutex
	port    int
	runErr  error
	runs    []builder.RunOptions
	removed []string
}

func (f *fakeBenchEngine) Run(_ context.Context, o builder.RunOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, o)
	return f.runErr
}

func (f *fakeBenchEngine) Port(context.Context, string, string) (int, error) { return f.port, nil }
func (f *fakeBenchEngine) MemGB(context.Context, string, string) (float64, error) {
	return 7.5, nil
}
func (f *fakeBenchEngine) Logs(context.Context, string, string) string {
	return "llama-server: out of memory"
}
func (f *fakeBenchEngine) Remove(_ context.Context, _, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, name)
	return nil
}

func (f *fakeBenchEngine) counts() (runs, removed int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.runs), len(f.removed)
}

// fakeLlama is llama-server behind the gate: /health (503 until ready) and
// chat completions with timings.
func fakeLlama(t *testing.T, ready func() bool, seen chan<- map[string]any) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			if ready != nil && !ready() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/v1/chat/completions":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if seen != nil {
				seen <- body
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"timings":{"prompt_n":1500,"prompt_ms":1250.5,
				"prompt_per_second":1199.5,"predicted_n":256,"predicted_per_second":41.25,"draft_n":120,"draft_n_accepted":90}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func benchSetup(t *testing.T, ready func() bool, seen chan<- map[string]any) (*Server, *fakeBenchEngine) {
	s := newTuneServer(t)
	srv := fakeLlama(t, ready, seen)
	port, _ := strconv.Atoi(srv.URL[strings.LastIndex(srv.URL, ":")+1:])
	eng := &fakeBenchEngine{port: port}
	bm := s.tuning().bench
	bm.eng, bm.pollEvery, bm.loadTimeout = eng, 10*time.Millisecond, 5*time.Second
	return s, eng
}

func waitBench(t *testing.T, s *Server, id string, states ...string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, out := tcall(t, s, "GET", "/api/tuning/benchmark/"+id, nil)
		for _, st := range states {
			if out["state"] == st {
				return out
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("benchmark stuck: %v", out)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBenchmarkRunsAndApplies(t *testing.T) {
	seen := make(chan map[string]any, 16)
	s, eng := benchSetup(t, nil, seen)
	code, out := tcall(t, s, "POST", "/api/tuning/benchmark", map[string]any{"ref": tunedRef, "engine": "podman", "force": true,
		"presets": []string{"speed"}, "candidates": []any{map[string]any{"ctx_size": 8192, "spec": "none"}}})
	if code != 200 {
		t.Fatalf("start: %d %v", code, out)
	}
	id := out["id"].(string)
	out = waitBench(t, s, id, "done", "error")
	if out["state"] != "done" || out["step"].(float64) != 2 || out["total"].(float64) != 2 {
		t.Fatalf("state: %v", out)
	}
	res := out["results"].([]any)
	r0 := res[0].(map[string]any)
	if r0["label"] != "Speed" || r0["ok"] != true || r0["gen_tps"].(float64) != 41.25 || r0["prompt_tps"].(float64) != 1199.5 ||
		r0["ttft_ms"].(float64) != 1250.5 || r0["mem_gb"].(float64) != 7.5 || r0["draft_accept_rate"].(float64) != 0.75 {
		t.Errorf("result: %v", r0)
	}
	if r1 := res[1].(map[string]any); r1["label"] != "Candidate 2" || r1["tuning"].(map[string]any)["ctx_size"].(float64) != 8192 {
		t.Errorf("candidate: %v", r1)
	}
	// One container per candidate, each removed; named for the job, on an
	// ephemeral port, labelled as a benchmark.
	runs, removed := eng.counts()
	if runs != 2 || removed != 2 {
		t.Errorf("runs %d removed %d", runs, removed)
	}
	o := eng.runs[1]
	if o.Name != "localllm-tune-"+id || o.HostPort != -1 || o.Tool != "tune" || o.Autostart || o.Route != "" {
		t.Errorf("run options: %+v", o)
	}
	if !strings.Contains(strings.Join(o.Env, " "), "CTX_SIZE=8192") || !strings.Contains(strings.Join(o.Env, " "), "SPEC_TYPE=none") {
		t.Errorf("env: %v", o.Env)
	}
	// The workload bypasses the harness, at temperature 0.
	var workload map[string]any
	for len(seen) > 0 {
		if b := <-seen; b["max_tokens"].(float64) == 256 {
			workload = b
		}
	}
	if workload == nil || workload["harness"] != false || workload["temperature"].(float64) != 0 {
		t.Errorf("workload request: %v", workload)
	}

	// Apply the second result: it's saved for the image.
	code, out = tcall(t, s, "POST", "/api/tuning/benchmark/"+id+"/apply", map[string]any{"index": 1})
	if code != 200 {
		t.Fatalf("apply: %d %v", code, out)
	}
	if sv := s.tuning().saved(tunedRef, "podman"); sv == nil || sv.CtxSize != 8192 {
		t.Errorf("saved: %+v", sv)
	}
	if code, _ = tcall(t, s, "POST", "/api/tuning/benchmark/"+id+"/apply", map[string]any{"index": 5}); code != 400 {
		t.Errorf("bad index: %d", code)
	}
}

func TestBenchmarkCancelRemovesContainer(t *testing.T) {
	s, eng := benchSetup(t, func() bool { return false }, nil) // never loads
	code, out := tcall(t, s, "POST", "/api/tuning/benchmark", map[string]any{"ref": tunedRef, "engine": "podman", "force": true, "presets": []string{"speed", "balanced"}})
	if code != 200 {
		t.Fatalf("start: %d %v", code, out)
	}
	id := out["id"].(string)
	// One at a time.
	if code, _ = tcall(t, s, "POST", "/api/tuning/benchmark", map[string]any{"ref": tunedRef, "engine": "podman", "force": true, "presets": []string{"speed"}}); code != 409 {
		t.Errorf("second benchmark: %d", code)
	}
	for {
		if runs, _ := eng.counts(); runs > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if code, _ = tcall(t, s, "POST", "/api/tuning/benchmark/"+id+"/cancel", nil); code != 200 {
		t.Fatalf("cancel: %d", code)
	}
	out = waitBench(t, s, id, "canceled")
	if runs, removed := eng.counts(); runs != 1 || removed != 1 {
		t.Errorf("runs %d removed %d", runs, removed)
	}
	if out["results"].([]any)[0].(map[string]any)["ok"] != false {
		t.Errorf("results: %v", out["results"])
	}
}

func TestBenchmarkErrorsAndShutdown(t *testing.T) {
	// A container that won't start is still removed; the job ends in error.
	s, eng := benchSetup(t, nil, nil)
	eng.runErr = errors.New("no /dev/dri")
	_, out := tcall(t, s, "POST", "/api/tuning/benchmark", map[string]any{"ref": tunedRef, "engine": "podman", "force": true, "presets": []string{"speed"}})
	out = waitBench(t, s, out["id"].(string), "error")
	if r := out["results"].([]any)[0].(map[string]any); !strings.Contains(r["error"].(string), "no /dev/dri") {
		t.Errorf("result: %v", r)
	}
	if _, removed := eng.counts(); removed != 1 {
		t.Errorf("removed %d", removed)
	}

	// Shutdown cancels a running benchmark and waits for the removal.
	s, eng = benchSetup(t, func() bool { return false }, nil)
	_, out = tcall(t, s, "POST", "/api/tuning/benchmark", map[string]any{"ref": tunedRef, "engine": "podman", "force": true, "presets": []string{"speed"}})
	for {
		if runs, _ := eng.counts(); runs > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.Close()
	if _, removed := eng.counts(); removed != 1 {
		t.Errorf("shutdown left the container: removed %d", removed)
	}
	if st, _ := s.tuning().bench.snapshot(out["id"].(string)); st.State != "canceled" {
		t.Errorf("state after shutdown: %s", st.State)
	}

	// A candidate that can't fit the engine is skipped, not run.
	s, eng = benchSetup(t, nil, nil)
	_, out = tcall(t, s, "POST", "/api/tuning/benchmark", map[string]any{"ref": tunedRef, "engine": "podman", "force": true,
		"candidates": []any{map[string]any{"ctx_size": 131072, "kv_cache": "f16", "cache_ram_mb": -1}}})
	out = waitBench(t, s, out["id"].(string), "error")
	if r := out["results"].([]any)[0].(map[string]any); r["skipped"] != true {
		t.Errorf("result: %v", r)
	}
	if runs, _ := eng.counts(); runs != 0 {
		t.Errorf("ran a misfit")
	}

	// Bad requests.
	for _, body := range []map[string]any{
		{"ref": tunedRef, "engine": "podman"},
		{"ref": tunedRef, "engine": "podman", "presets": []string{"fastest"}},
		{"ref": tunedRef, "engine": "podman", "presets": []string{"speed"}, "workload": "poetry"},
		{"ref": tunedRef, "engine": "podman", "candidates": []any{map[string]any{"parallel": "2"}}},
	} {
		if code, _ := tcall(t, s, "POST", "/api/tuning/benchmark", body); code != 400 {
			t.Errorf("%v: %d", body, code)
		}
	}
	if code, _ := tcall(t, s, "GET", "/api/tuning/benchmark/nope", nil); code != 404 {
		t.Errorf("unknown id: %d", code)
	}
}

func TestParamsB(t *testing.T) {
	for in, want := range map[string]float64{"12B": 12, "21B-A3.6B": 21, "E4B": 4, "568M": 0.568, "": 0, "30B-A3B": 30} {
		if got := paramsB(in); got != want {
			t.Errorf("%q: %g want %g", in, got, want)
		}
	}
}
