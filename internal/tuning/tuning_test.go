package tuning

import (
	"bytes"
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seanbrill/local-llm/internal/gate"
)

// ---- a GGUF writer, for headers to read back ------------------------------------

type ggufKV struct {
	key string
	val any // uint32, string, bool, []uint32, []bool, []string
}

func writeGGUF(kvs []ggufKV) []byte {
	var b bytes.Buffer
	w := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	str := func(s string) { w(uint64(len(s))); b.WriteString(s) }
	b.WriteString("GGUF")
	w(uint32(3))
	w(uint64(0))
	w(uint64(len(kvs)))
	for _, kv := range kvs {
		str(kv.key)
		switch v := kv.val.(type) {
		case uint32:
			w(uint32(ggufUint32))
			w(v)
		case string:
			w(uint32(ggufString))
			str(v)
		case bool:
			w(uint32(ggufBool))
			w(v)
		case []uint32:
			w(uint32(ggufArray))
			w(uint32(ggufUint32))
			w(uint64(len(v)))
			for _, x := range v {
				w(x)
			}
		case []bool:
			w(uint32(ggufArray))
			w(uint32(ggufBool))
			w(uint64(len(v)))
			for _, x := range v {
				w(x)
			}
		case []string:
			w(uint32(ggufArray))
			w(uint32(ggufString))
			w(uint64(len(v)))
			for _, x := range v {
				str(x)
			}
		default:
			panic("writeGGUF: unsupported value")
		}
	}
	b.Write(make([]byte, 64)) // where the tensor infos would start
	return b.Bytes()
}

// gemmaLike is a small gemma4-shaped header: 12 layers, every 6th global
// (1 KV head of 512) and the rest sliding (8 KV heads of 256, window 1024).
func gemmaLike(vocab int) []byte {
	heads := make([]uint32, 12)
	pattern := make([]bool, 12)
	for i := range heads {
		heads[i], pattern[i] = 8, true
		if i%6 == 5 {
			heads[i], pattern[i] = 1, false
		}
	}
	tokens := make([]string, vocab)
	for i := range tokens {
		tokens[i] = "t"
	}
	return writeGGUF([]ggufKV{
		{"general.architecture", "gemma4"},
		{"gemma4.block_count", uint32(12)},
		{"gemma4.context_length", uint32(131072)},
		{"gemma4.embedding_length", uint32(2048)},
		{"gemma4.attention.head_count", uint32(8)},
		{"gemma4.attention.head_count_kv", heads},
		{"gemma4.attention.key_length", uint32(512)},
		{"gemma4.attention.value_length", uint32(512)},
		{"gemma4.attention.key_length_swa", uint32(256)},
		{"gemma4.attention.value_length_swa", uint32(256)},
		{"gemma4.attention.sliding_window", uint32(1024)},
		{"gemma4.attention.sliding_window_pattern", pattern},
		{"tokenizer.ggml.tokens", tokens},
		{"tokenizer.chat_template", "{% if enable_thinking %}...{% endif %}"},
	})
}

func TestGGUFInfo(t *testing.T) {
	meta, err := ReadGGUFMeta(bytes.NewReader(gemmaLike(300)))
	if err != nil {
		t.Fatal(err)
	}
	a, err := meta.Info()
	if err != nil {
		t.Fatal(err)
	}
	want := ArchInfo{Arch: "gemma4", Layers: 12, TrainedCtx: 131072, Embd: 2048, Heads: 8, VocabSize: 300,
		FullElems: 2 * 1 * 1024, SWAElems: 10 * 8 * 512, SlidingWindow: 1024, KVLayers: 12, QuantKVOK: true, Thinks: true}
	if a != want {
		t.Errorf("got  %+v\nwant %+v", a, want)
	}
	// A long array is counted, not kept.
	meta, _ = ReadGGUFMeta(bytes.NewReader(gemmaLike(5000)))
	if meta.ArrLen["tokenizer.ggml.tokens"] != 5000 || meta.KV["tokenizer.ggml.tokens"] != nil {
		t.Errorf("long array: %d %v", meta.ArrLen["tokenizer.ggml.tokens"], meta.KV["tokenizer.ggml.tokens"] != nil)
	}
	// From a file, and not-a-GGUF.
	p := filepath.Join(t.TempDir(), "m.gguf")
	_ = os.WriteFile(p, gemmaLike(10), 0o644)
	if m, err := ReadGGUFMetaFile(p); err != nil || m.Arch() != "gemma4" {
		t.Errorf("file: %v", err)
	}
	if _, err := ReadGGUFMeta(strings.NewReader("PK\x03\x04 not a gguf")); err == nil {
		t.Error("read a zip as GGUF")
	}
	// Truncated input is an error, not a panic.
	full := gemmaLike(10)
	for _, n := range []int{3, 20, 60, len(full) / 2} {
		if _, err := ReadGGUFMeta(bytes.NewReader(full[:n])); err == nil {
			t.Errorf("truncated at %d read", n)
		}
	}
}

func TestFetchArch(t *testing.T) {
	data := gemmaLike(300)
	var ranged atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			ranged.Store(true)
		}
		http.ServeContent(w, r, "m.gguf", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	a, err := FetchArch(context.Background(), srv.Client(), srv.URL+"/m.gguf")
	if err != nil || a.Layers != 12 || a.SWAElems != 10*8*512 {
		t.Fatalf("%+v %v", a, err)
	}
	if !ranged.Load() {
		t.Error("no Range header sent")
	}
	if _, err := FetchArch(context.Background(), srv.Client(), srv.URL+"/x"); err != nil {
		// ServeContent answers any path; a 404 must be an error.
		t.Log(err)
	}
	notFound := httptest.NewServer(http.NotFoundHandler())
	defer notFound.Close()
	if _, err := FetchArch(context.Background(), notFound.Client(), notFound.URL); err == nil {
		t.Error("a 404 read")
	}
}

// ---- normalization -----------------------------------------------------------------

func gemmaModel(t *testing.T, weights int64) Model {
	t.Helper()
	meta, err := ReadGGUFMeta(bytes.NewReader(gemmaLike(262144 / 64)))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := meta.Info()
	return Model{ID: "g", Name: "Gemma", Modality: "text", WeightsBytes: weights, Arch: &a, Draft: "g-small"}
}

func TestNormalize(t *testing.T) {
	m := gemmaModel(t, 7e9)
	cases := []struct {
		name string
		in   Tuning
		want func(Tuning) bool
		note string
		err  string
	}{
		{"quantized cache turns FA on", Tuning{KVCache: "q8_0"}, func(o Tuning) bool { return o.FlashAttn == "on" }, "a quantized cache turns flash attention on", ""},
		{"quantized cache overrides FA off", Tuning{KVCache: "q4_0", FlashAttn: "off"}, func(o Tuning) bool { return o.FlashAttn == "on" }, "a quantized cache needs flash attention, so it's on", ""},
		{"f16 leaves FA alone", Tuning{KVCache: "f16", FlashAttn: "off"}, func(o Tuning) bool { return o.FlashAttn == "off" }, "", ""},
		{"context capped at trained", Tuning{CtxSize: 262144}, func(o Tuning) bool { return o.CtxSize == 131072 }, "the context is capped at 128K, what Gemma was trained for", ""},
		{"one slot drops kv_unified", Tuning{Parallel: 1, KVUnified: BoolPtr(true)}, func(o Tuning) bool { return o.KVUnified == nil }, "", ""},
		{"two slots keep kv_unified", Tuning{Parallel: 2, KVUnified: BoolPtr(true)}, func(o Tuning) bool { return o.KVUnified != nil && *o.KVUnified }, "", ""},
		{"ubatch over batch", Tuning{Batch: 512, UBatch: 1024}, func(o Tuning) bool { return o.UBatch == 512 }, "the physical batch can't exceed the logical one, so ubatch is 512", ""},
		{"spec aliases", Tuning{Spec: "ngram-mod"}, func(o Tuning) bool { return o.Spec == SpecNgram }, "", ""},
		{"spec none clears draft sizes", Tuning{Spec: "none", DraftMax: 8}, func(o Tuning) bool { return o.DraftMax == 0 }, "", ""},
		{"draft_min over draft_max", Tuning{Spec: "ngram", DraftMax: 16, DraftMin: 32}, func(o Tuning) bool { return o.DraftMin == 16 }, "draft_min can't exceed draft_max, so it's 16", ""},
		{"draft without a baked draft", Tuning{Spec: "draft"}, nil, "", "no draft model baked"},
		{"bad enum", Tuning{FlashAttn: "maybe"}, nil, "", "flash_attn"},
		{"bad kv", Tuning{KVCache: "q5_1"}, nil, "", "kv_cache"},
		{"ctx too small", Tuning{CtxSize: 100}, nil, "", "ctx_size"},
		{"parallel too big", Tuning{Parallel: 64}, nil, "", "parallel"},
		{"bad effort", Tuning{ReasoningEffort: "max"}, nil, "", "reasoning_effort"},
		{"bad budget", Tuning{ReasoningBudget: IntPtr(-5)}, nil, "", "reasoning_budget"},
		{"bad cache", Tuning{CacheRAMMB: IntPtr(-2)}, nil, "", "cache_ram_mb"},
		{"ngram draft_max range", Tuning{Spec: "ngram", DraftMax: 2000}, nil, "", "draft_max"},
		{"bad preset", Tuning{Preset: "turbo"}, nil, "", "preset"},
	}
	for _, c := range cases {
		out, notes, err := Normalize(c.in, m)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: err %v, want %q", c.name, err, c.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !c.want(out) {
			t.Errorf("%s: %+v", c.name, out)
		}
		if c.note != "" && !slices.Contains(notes, c.note) {
			t.Errorf("%s: notes %q", c.name, notes)
		}
	}

	// A draft the model has: filled in from the catalog.
	md := m
	md.DraftAvailable = true
	if out, _, err := Normalize(Tuning{Spec: "draft"}, md); err != nil || out.DraftModel != "g-small" {
		t.Errorf("draft: %+v %v", out, err)
	}
	// No draft paired at all: suggests ngram.
	mn := m
	mn.Draft = ""
	if _, _, err := Normalize(Tuning{Spec: "draft"}, mn); err == nil || !strings.Contains(err.Error(), "ngram") {
		t.Errorf("unpaired: %v", err)
	}
	// Vision and embedding models: no look-ahead.
	mv := m
	mv.Vision = true
	if out, notes, _ := Normalize(Tuning{Spec: "ngram"}, mv); out.Spec != SpecNone || !slices.Contains(notes, "look-ahead is off for vision models") {
		t.Errorf("vision: %+v %q", out, notes)
	}
	me := m
	me.Modality = "embedding"
	if out, _, _ := Normalize(Tuning{Spec: "ngram", Reasoning: "on"}, me); out.Spec != SpecNone || out.Reasoning != "" {
		t.Errorf("embedding: %+v", out)
	}
	// Heads that don't divide into quant blocks keep f16.
	odd := *m.Arch
	odd.QuantKVOK = false
	mo := m
	mo.Arch = &odd
	if out, _, _ := Normalize(Tuning{KVCache: "q8_0"}, mo); out.KVCache != "f16" {
		t.Errorf("odd heads: %+v", out)
	}
}

func TestOverlayAndLabel(t *testing.T) {
	base := Tuning{Preset: "speed", CtxSize: 16384, KVCache: "q8_0", Spec: "ngram", Temperature: FloatPtr(0.2)}
	got := Overlay(base, Tuning{CtxSize: 32768})
	if got.CtxSize != 32768 || got.KVCache != "q8_0" || got.Preset != "speed" || *got.Temperature != 0.2 {
		t.Errorf("same preset: %+v", got)
	}
	got = Overlay(base, Tuning{Preset: "quality", KVCache: "f16"})
	if got.CtxSize != 0 || got.Spec != "" || got.KVCache != "f16" || got.Temperature != nil {
		t.Errorf("new preset starts over: %+v", got)
	}
	if !(Tuning{}).IsZero() || base.IsZero() {
		t.Error("IsZero")
	}
	if back := FromLabel(base.Label()); back.CtxSize != 16384 || *back.Temperature != 0.2 {
		t.Errorf("label round trip: %+v", back)
	}
	if !FromLabel("not json").IsZero() || (Tuning{}).Label() != "" {
		t.Error("bad label")
	}
	if _, err := Parse([]byte(`{"ctx":1}`)); err == nil {
		t.Error("unknown field parsed")
	}
	if tu, err := Parse([]byte(`null`)); err != nil || !tu.IsZero() {
		t.Error("null")
	}
}

func TestEnvChangeAndGate(t *testing.T) {
	baked := Tuning{CtxSize: 16384, KVCache: "q8_0", FlashAttn: "on", Spec: "ngram", CacheRAMMB: IntPtr(1024)}
	eff := Tuning{CtxSize: 32768, KVCache: "q8_0", FlashAttn: "on", Parallel: 2, KVUnified: BoolPtr(true), Temperature: FloatPtr(0.7)}
	got := strings.Join(EnvChange(baked, eff, ImageDraftPath), " ")
	if got != "CACHE_RAM_MB= CTX_SIZE=32768 KV_UNIFIED=1 PARALLEL=2 SPEC_TYPE= TEMPERATURE=0.7" {
		t.Errorf("EnvChange: %s", got)
	}
	if n := EnvChange(baked, baked, ImageDraftPath); len(n) != 0 {
		t.Errorf("no change: %v", n)
	}

	// Every variable the tuning sets is one llmgate reads, to the same value.
	full := Tuning{CtxSize: 32768, FlashAttn: "on", KVCache: "q8_0", Parallel: 2, KVUnified: BoolPtr(true),
		Batch: 1024, UBatch: 256, Spec: "draft", DraftMax: 8, DraftMin: 2, Reasoning: "off", ReasoningEffort: "low",
		ReasoningBudget: IntPtr(512), CacheRAMMB: IntPtr(2048), CtxCheckpoints: IntPtr(4), Threads: 6,
		Temperature: FloatPtr(0.6), TopP: FloatPtr(0.95), TopK: IntPtr(20), MinP: FloatPtr(0.05), RepeatPenalty: FloatPtr(1.1)}
	env := map[string]string{}
	for _, kv := range full.Env("/models/draft.gguf") {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
		if !slices.Contains(EnvNames(), k) {
			t.Errorf("%s isn't in EnvNames", k)
		}
	}
	var l gate.Llama
	if err := l.ApplyEnv(func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	if l.CtxSize != 32768 || l.FlashAttn != "on" || l.KVCacheType != "q8_0" || l.Parallel != 2 || !l.KVUnified ||
		l.Batch != 1024 || l.UBatch != 256 || l.SpecType != "draft" || l.DraftModel != "/models/draft.gguf" ||
		l.DraftMax != 8 || l.DraftMin != 2 || l.Reasoning != "off" || l.ReasoningEffort != "low" ||
		*l.ReasoningBudget != 512 || *l.CacheRAMMB != 2048 || *l.CtxCheckpoints != 4 || l.Threads != 6 ||
		*l.Temperature != 0.6 || *l.TopP != 0.95 || *l.TopK != 20 || *l.MinP != 0.05 || *l.RepeatPenalty != 1.1 {
		t.Errorf("gate read %+v", l)
	}
	// A cleared variable ("KEY=") reads as unset.
	var c gate.Llama
	if err := c.ApplyEnv(func(k string) string { return map[string]string{"SPEC_TYPE": "", "CACHE_RAM_MB": ""}[k] }); err != nil || c.SpecType != "" || c.CacheRAMMB != nil {
		t.Errorf("cleared: %+v %v", c, err)
	}
}

// ---- presets and the estimate ---------------------------------------------------

func TestPresets(t *testing.T) {
	m := gemmaModel(t, 7e9)
	podman := Target{Engine: "podman", Compute: "vulkan", MemGB: 12.88}
	opts := Presets(m, podman)
	if len(opts) != 4 {
		t.Fatalf("%d presets", len(opts))
	}
	by := map[string]Option{}
	for _, o := range opts {
		by[o.ID] = o
		if !o.Fits || o.Explain == "" || o.Label == "" || o.Estimate.TotalGB > podman.UsableGB() {
			t.Errorf("%s: %+v", o.ID, o)
		}
		if _, _, err := Normalize(o.Tuning, m); err != nil {
			t.Errorf("%s doesn't normalize: %v", o.ID, err)
		}
	}
	sp, bal, lc, q := by[PresetSpeed].Tuning, by[PresetBalanced].Tuning, by[PresetLongContext].Tuning, by[PresetQuality].Tuning
	if sp.CtxSize != 16384 || sp.KVCache != "q8_0" || sp.Spec != SpecNgram || sp.Parallel != 1 || sp.FlashAttn != "on" {
		t.Errorf("speed: %+v", sp)
	}
	if bal.Parallel != 2 || bal.KVUnified == nil || bal.CtxSize != 32768 {
		t.Errorf("balanced: %+v", bal)
	}
	if lc.CtxSize != 131072 {
		t.Errorf("long context: %+v", lc)
	}
	if q.KVCache != "f16" || q.Spec != SpecNone || q.Reasoning != "on" {
		t.Errorf("quality: %+v", q)
	}
	// Caches are sized: an SWA model gets checkpoints, and a bounded prompt cache.
	if sp.CtxCheckpoints == nil || *sp.CtxCheckpoints < 1 || sp.CacheRAMMB == nil || *sp.CacheRAMMB > 4096 {
		t.Errorf("speed caches: %+v", sp)
	}
	// More context costs more memory.
	if by[PresetLongContext].Estimate.KVGB <= by[PresetSpeed].Estimate.KVGB {
		t.Error("long context's cache isn't bigger")
	}

	// Too big for the machine: every preset says so.
	big := gemmaModel(t, 12.1e9)
	for _, o := range Presets(big, podman) {
		if o.Fits || o.Warning == "" {
			t.Errorf("%s fits a 12 GB model in 12 GiB: %+v", o.ID, o.Estimate)
		}
	}
	// Native Metal has more room: bigger caches.
	nat := Presets(m, Target{Engine: "native", Compute: "metal", MemGB: 25.77})
	if *nat[0].Tuning.CacheRAMMB < *sp.CacheRAMMB {
		t.Errorf("native cache %d < podman %d", *nat[0].Tuning.CacheRAMMB, *sp.CacheRAMMB)
	}
	// A vision model: no look-ahead in any preset.
	mv := m
	mv.Vision, mv.MMProjBytes = true, 900e6
	for _, o := range Presets(mv, podman) {
		if o.Tuning.Spec != SpecNone {
			t.Errorf("vision %s: spec %s", o.ID, o.Tuning.Spec)
		}
	}
	// Resolve: a preset with an edit keeps the edit; a custom tuning is only normalized.
	r, _, err := Resolve(Tuning{Preset: "speed", CtxSize: 8192}, m, podman)
	if err != nil || r.CtxSize != 8192 || r.Spec != SpecNgram {
		t.Errorf("resolve: %+v %v", r, err)
	}
	r, notes, _ := Resolve(Tuning{Preset: "speed", KVCache: "q4_0", FlashAttn: "auto"}, m, podman)
	if r.FlashAttn != "on" || !slices.Contains(notes, "a quantized cache turns flash attention on") {
		t.Errorf("resolve notes: %+v %q", r, notes)
	}
	r, _, _ = Resolve(Tuning{CtxSize: 8192}, m, podman)
	if r.Spec != "" || r.CacheRAMMB != nil {
		t.Errorf("custom: %+v", r)
	}
}

func TestEstimate(t *testing.T) {
	m := gemmaModel(t, 7e9)
	f16 := EstimateMemory(Tuning{CtxSize: 32768, KVCache: "f16", CacheRAMMB: IntPtr(0)}, m)
	q8 := EstimateMemory(Tuning{CtxSize: 32768, KVCache: "q8_0", CacheRAMMB: IntPtr(0)}, m)
	if q8.KVGB >= f16.KVGB || q8.KVGB < f16.KVGB*0.5 {
		t.Errorf("q8_0 %g vs f16 %g", q8.KVGB, f16.KVGB)
	}
	if f16.WeightsGB != 7 || f16.Approx {
		t.Errorf("%+v", f16)
	}
	// Sliding layers cap at the window: doubling the context doesn't double the cache.
	f16x2 := EstimateMemory(Tuning{CtxSize: 65536, KVCache: "f16", CacheRAMMB: IntPtr(0)}, m)
	if f16x2.KVGB >= 2*f16.KVGB {
		t.Errorf("SWA not capped: %g vs %g", f16x2.KVGB, f16.KVGB)
	}
	// Without the header: a rough guess, marked approx.
	g := EstimateMemory(Tuning{CtxSize: 8192}, Model{WeightsBytes: 5e9, ParamsB: 8})
	if !g.Approx || g.KVGB <= 0 {
		t.Errorf("guess: %+v", g)
	}
	if fits, _ := Fit(Estimate{TotalGB: 20}, Target{}); !fits {
		t.Error("unknown memory should pass")
	}
	if fits, w := Fit(Estimate{TotalGB: 20}, Target{Engine: "podman", MemGB: 12.88}); fits || !strings.Contains(w, "Podman machine") {
		t.Errorf("fit: %v %q", fits, w)
	}
}

func TestHarnessLimits(t *testing.T) {
	m := gemmaModel(t, 4<<30)
	got, _, err := Normalize(Tuning{MaxSteps: 30, MaxCalls: 8, ToolTimeoutS: 120, MaxOutputKB: 24}, m)
	if err != nil {
		t.Fatal(err)
	}
	env := got.EnvMap("")
	if env[EnvHarnessMaxSteps] != "30" || env[EnvHarnessMaxCalls] != "8" || env[EnvHarnessTimeout] != "120" || env[EnvHarnessMaxOutputB] != "24576" {
		t.Errorf("env = %v", env)
	}
	if m := Overlay(Tuning{MaxSteps: 20}, Tuning{MaxOutputKB: 24}); m.MaxSteps != 20 || m.MaxOutputKB != 24 {
		t.Errorf("merge = %+v", m)
	}
	for _, bad := range []Tuning{{MaxSteps: 51}, {MaxCalls: 33}, {ToolTimeoutS: 1801}, {MaxOutputKB: 0.1}, {MaxOutputKB: 2048}} {
		if _, _, err := Normalize(bad, m); err == nil {
			t.Errorf("%+v should be refused", bad)
		}
	}
	// Clearing a saved limit puts the image's back.
	if ch := EnvChange(Tuning{}, Tuning{MaxSteps: 30}, ""); len(ch) != 1 || ch[0] != "HARNESS_MAX_STEPS=30" {
		t.Errorf("EnvChange = %v", ch)
	}
}
