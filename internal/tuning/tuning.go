// Package tuning is llama-server's runtime tuning for the factory's chat
// images and native packages: the settings (context window, flash attention,
// KV cache type, parallel slots, speculative decoding, reasoning, prompt
// cache, sampling defaults), their validation against llama.cpp v0.9.0's
// rules, presets resolved for a model and the memory it runs in, and a memory
// estimate read from the model's GGUF header.
//
// A Tuning travels as JSON (the API, the image label local-llm.tuning, a
// native package's manifest) and reaches llama-server as environment
// variables (Env), which llmgate and llmpack turn into flags through
// gate.Llama.ApplyEnv. Standard library only.
package tuning

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Presets.
const (
	PresetSpeed       = "speed"
	PresetBalanced    = "balanced"
	PresetLongContext = "long_context"
	PresetQuality     = "quality"
	PresetCustom      = "custom"
)

// PresetIDs are the generated presets, in the order the UI lists them.
var PresetIDs = []string{PresetSpeed, PresetBalanced, PresetLongContext, PresetQuality}

// Speculative decoding modes (Tuning.Spec).
const (
	SpecNone  = "none"
	SpecNgram = "ngram"
	SpecDraft = "draft"
)

// KV cache types the factory offers.
var kvTypes = map[string]float64{ // bytes per element
	"f16":  2,
	"q8_0": 34.0 / 32,
	"q4_0": 18.0 / 32,
}

// Bounds.
const (
	MinCtx       = 512
	MaxCtx       = 1 << 21
	MaxParallel  = 16
	MaxBatch     = 16384
	MaxDraft     = 64   // draft model tokens per step
	MaxNgram     = 1024 // ngram-mod tokens per step (llama.cpp's own bound)
	MaxCacheRAM  = 1 << 20
	MaxThreads   = 512
	MaxCkpts     = 256
	DefaultCkpts = 32 // llama.cpp v0.9.0 --ctx-checkpoints
	// DefaultCacheRAMMB is llama-server's --cache-ram default (MiB).
	DefaultCacheRAMMB = 8192
)

// Tuning is llama-server's runtime tuning. Every field is optional: unset
// leaves the image's (or llama.cpp's) default. Pointer fields are the ones
// where zero means something (temperature 0 is greedy, cache_ram_mb 0 turns
// the prompt cache off, reasoning_budget 0 skips thinking).
type Tuning struct {
	// Preset names where the values came from: speed, balanced,
	// long_context, quality (resolved for the model and the memory it runs
	// in, see Resolve) or custom. Set fields win over the preset's.
	Preset string `json:"preset,omitempty"`

	CtxSize   int    `json:"ctx_size,omitempty"`   // tokens; capped at the model's trained context
	FlashAttn string `json:"flash_attn,omitempty"` // auto|on|off
	KVCache   string `json:"kv_cache,omitempty"`   // f16|q8_0|q4_0 (K and V)
	Parallel  int    `json:"parallel,omitempty"`   // server slots
	// KVUnified shares one KV pool between the slots (each can use the whole
	// context); only meaningful with parallel > 1.
	KVUnified *bool `json:"kv_unified,omitempty"`
	Batch     int   `json:"batch,omitempty"`  // logical batch (-b)
	UBatch    int   `json:"ubatch,omitempty"` // physical batch (-ub)

	Spec       string `json:"spec,omitempty"`        // none|ngram|draft
	DraftModel string `json:"draft_model,omitempty"` // catalog id of the draft (build time)
	DraftMax   int    `json:"draft_max,omitempty"`   // tokens drafted per step
	DraftMin   int    `json:"draft_min,omitempty"`   // fewest worth verifying

	Reasoning       string `json:"reasoning,omitempty"`        // auto|on|off
	ReasoningEffort string `json:"reasoning_effort,omitempty"` // low|medium|high (templates that read it, e.g. gpt-oss)
	ReasoningBudget *int   `json:"reasoning_budget,omitempty"` // thinking tokens: -1 unlimited, 0 none

	CacheRAMMB     *int `json:"cache_ram_mb,omitempty"`    // prompt cache in RAM, MiB: -1 unlimited, 0 off
	CtxCheckpoints *int `json:"ctx_checkpoints,omitempty"` // per slot (sliding-window and hybrid models)
	Threads        int  `json:"threads,omitempty"`

	Temperature   *float64 `json:"temperature,omitempty"`
	TopP          *float64 `json:"top_p,omitempty"`
	TopK          *int     `json:"top_k,omitempty"`
	MinP          *float64 `json:"min_p,omitempty"`
	RepeatPenalty *float64 `json:"repeat_penalty,omitempty"`

	// The agent harness's limits (images with a baked harness), overriding
	// the ones baked in: tool steps per answer, tool calls per step, seconds
	// per tool call, and KB of text per tool result.
	MaxSteps     int     `json:"max_steps,omitempty"`
	MaxCalls     int     `json:"max_calls_per_step,omitempty"`
	ToolTimeoutS int     `json:"tool_timeout_s,omitempty"`
	MaxOutputKB  float64 `json:"max_output_kb,omitempty"`
}

// Harness limit ranges (they match internal/agent's).
const (
	MaxHarnessSteps    = 50
	MaxHarnessCalls    = 32
	MaxHarnessTimeoutS = 1800
	MinHarnessOutputKB = 0.5
	MaxHarnessOutputKB = 1024
)

// IntPtr, FloatPtr and BoolPtr make optional values.
func IntPtr(v int) *int           { return &v }
func FloatPtr(v float64) *float64 { return &v }
func BoolPtr(v bool) *bool        { return &v }

// IsZero reports whether nothing is set.
func (t Tuning) IsZero() bool {
	b, _ := json.Marshal(t)
	return string(b) == "{}"
}

// Generated reports whether t's preset is one Resolve generates.
func (t Tuning) Generated() bool {
	for _, p := range PresetIDs {
		if t.Preset == p {
			return true
		}
	}
	return false
}

// Parse reads a Tuning from JSON; unknown fields are an error, so a typo is
// caught. Empty input, "null" and "{}" are the zero Tuning.
func Parse(raw []byte) (Tuning, error) {
	var t Tuning
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return t, nil
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		return t, fmt.Errorf("tuning: %w", err)
	}
	return t, nil
}

// Label is the JSON baked as the image label local-llm.tuning ("" for none).
func (t Tuning) Label() string {
	if t.IsZero() {
		return ""
	}
	b, _ := json.Marshal(t)
	return string(b)
}

// FromLabel reads the local-llm.tuning label; a missing or unreadable label
// is the zero Tuning.
func FromLabel(s string) Tuning {
	t, err := Parse([]byte(s))
	if err != nil {
		return Tuning{}
	}
	return t
}

// Overlay puts over's set fields on top of base. Choosing a different preset
// starts over from that preset (base's values belonged to the old one); the
// same preset, or none, keeps base's values under over's.
func Overlay(base, over Tuning) Tuning {
	if over.Preset != "" && over.Preset != base.Preset {
		base = Tuning{Preset: over.Preset}
	}
	out := base
	if over.Preset != "" {
		out.Preset = over.Preset
	}
	setI := func(dst *int, v int) {
		if v != 0 {
			*dst = v
		}
	}
	setS := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	setI(&out.CtxSize, over.CtxSize)
	setS(&out.FlashAttn, over.FlashAttn)
	setS(&out.KVCache, over.KVCache)
	setI(&out.Parallel, over.Parallel)
	if over.KVUnified != nil {
		out.KVUnified = BoolPtr(*over.KVUnified)
	}
	setI(&out.Batch, over.Batch)
	setI(&out.UBatch, over.UBatch)
	setS(&out.Spec, over.Spec)
	setS(&out.DraftModel, over.DraftModel)
	setI(&out.DraftMax, over.DraftMax)
	setI(&out.DraftMin, over.DraftMin)
	setS(&out.Reasoning, over.Reasoning)
	setS(&out.ReasoningEffort, over.ReasoningEffort)
	if over.ReasoningBudget != nil {
		out.ReasoningBudget = IntPtr(*over.ReasoningBudget)
	}
	if over.CacheRAMMB != nil {
		out.CacheRAMMB = IntPtr(*over.CacheRAMMB)
	}
	if over.CtxCheckpoints != nil {
		out.CtxCheckpoints = IntPtr(*over.CtxCheckpoints)
	}
	setI(&out.Threads, over.Threads)
	if over.Temperature != nil {
		out.Temperature = FloatPtr(*over.Temperature)
	}
	if over.TopP != nil {
		out.TopP = FloatPtr(*over.TopP)
	}
	if over.TopK != nil {
		out.TopK = IntPtr(*over.TopK)
	}
	if over.MinP != nil {
		out.MinP = FloatPtr(*over.MinP)
	}
	if over.RepeatPenalty != nil {
		out.RepeatPenalty = FloatPtr(*over.RepeatPenalty)
	}
	setI(&out.MaxSteps, over.MaxSteps)
	setI(&out.MaxCalls, over.MaxCalls)
	setI(&out.ToolTimeoutS, over.ToolTimeoutS)
	if over.MaxOutputKB != 0 {
		out.MaxOutputKB = over.MaxOutputKB
	}
	return out
}

// Model is what validation and the presets know about the model.
type Model struct {
	ID       string
	Name     string
	Modality string // text|code|reasoning|vision|embedding
	Vision   bool   // has a vision projector (mmproj)
	// WeightsBytes is the model file (plus the projector); DraftBytes the
	// draft model's file, when there is one.
	WeightsBytes int64
	MMProjBytes  int64
	// Arch is the GGUF metadata (nil = unknown: the estimate falls back to a
	// guess from ParamsB).
	Arch    *ArchInfo
	ParamsB float64 // total parameters in billions, for the fallback
	// Draft is the catalog's draft model for this one ("" = none);
	// DraftAvailable says a draft can be used where the tuning runs: at
	// build time the catalog pairs one, at run time the image baked one.
	Draft          string
	DraftAvailable bool
	DraftBytes     int64
	DraftArch      *ArchInfo
}

// TrainedCtx is the model's trained context (0 = unknown).
func (m Model) TrainedCtx() int {
	if m.Arch != nil {
		return m.Arch.TrainedCtx
	}
	return 0
}

// Normalize validates t for m and returns it in canonical form, with the
// rules that changed something as notes ("a quantized cache turns flash
// attention on"). Values out of range are errors.
func Normalize(t Tuning, m Model) (Tuning, []string, error) {
	var notes []string
	var errs []string
	bad := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }
	note := func(format string, a ...any) { notes = append(notes, fmt.Sprintf(format, a...)) }
	lower := func(s *string) { *s = strings.ToLower(strings.TrimSpace(*s)) }
	lower(&t.Preset)
	lower(&t.FlashAttn)
	lower(&t.KVCache)
	lower(&t.Spec)
	lower(&t.Reasoning)
	lower(&t.ReasoningEffort)
	t.DraftModel = strings.TrimSpace(t.DraftModel)

	switch t.Preset {
	case "", PresetSpeed, PresetBalanced, PresetLongContext, PresetQuality, PresetCustom:
	default:
		bad("preset %q isn't speed, balanced, long_context, quality or custom", t.Preset)
	}

	// Context.
	if t.CtxSize != 0 && (t.CtxSize < MinCtx || t.CtxSize > MaxCtx) {
		bad("ctx_size %d is out of range (%d to %d tokens)", t.CtxSize, MinCtx, MaxCtx)
	} else if tc := m.TrainedCtx(); tc > 0 && t.CtxSize > tc {
		note("the context is capped at %s, what %s was trained for", Tokens(tc), nameOr(m))
		t.CtxSize = tc
	}

	// KV cache and flash attention.
	switch t.FlashAttn {
	case "", "auto", "on", "off":
	case "true", "1", "enabled":
		t.FlashAttn = "on"
	case "false", "0", "disabled":
		t.FlashAttn = "off"
	default:
		bad("flash_attn %q isn't auto, on or off", t.FlashAttn)
	}
	if t.KVCache != "" {
		if _, ok := kvTypes[t.KVCache]; !ok {
			bad("kv_cache %q isn't f16, q8_0 or q4_0", t.KVCache)
		} else if t.KVCache != "f16" {
			if m.Arch != nil && !m.Arch.QuantKVOK {
				note("%s's attention heads don't divide into %s blocks, so the cache stays f16", nameOr(m), t.KVCache)
				t.KVCache = "f16"
			} else if t.FlashAttn != "on" {
				if t.FlashAttn == "off" {
					note("a quantized cache needs flash attention, so it's on")
				} else {
					note("a quantized cache turns flash attention on")
				}
				t.FlashAttn = "on"
			}
		}
	}

	// Slots.
	if t.Parallel < 0 || t.Parallel > MaxParallel {
		bad("parallel %d is out of range (1 to %d)", t.Parallel, MaxParallel)
	}
	if t.KVUnified != nil && *t.KVUnified && t.Parallel <= 1 {
		t.KVUnified = nil // one slot has the whole cache anyway
	}

	// Batches.
	if t.Batch != 0 && (t.Batch < 32 || t.Batch > MaxBatch) {
		bad("batch %d is out of range (32 to %d)", t.Batch, MaxBatch)
	}
	if t.UBatch != 0 && (t.UBatch < 32 || t.UBatch > MaxBatch) {
		bad("ubatch %d is out of range (32 to %d)", t.UBatch, MaxBatch)
	}
	if t.UBatch > 0 && t.UBatch > t.effBatch() {
		if t.Batch == 0 {
			t.Batch = t.UBatch
		} else {
			note("the physical batch can't exceed the logical one, so ubatch is %d", t.Batch)
			t.UBatch = t.Batch
		}
	}

	// Speculative decoding.
	switch t.Spec {
	case "", SpecNone, SpecNgram, SpecDraft:
	case "off", "false":
		t.Spec = SpecNone
	case "ngram-mod", "lookup":
		t.Spec = SpecNgram
	default:
		bad("spec %q isn't none, ngram or draft", t.Spec)
	}
	if t.Spec != "" && t.Spec != SpecNone {
		switch {
		case m.Modality == "embedding":
			note("look-ahead is off for embedding models (they don't generate)")
			t.Spec = SpecNone
		case m.Vision:
			// llama.cpp v0.9.0 doesn't refuse it, but a draft can't see the
			// image (it gets zero rows: "drafts after multimodal input will be
			// poor") and the ngram path has an unguarded multimodal batch case.
			note("look-ahead is off for vision models")
			t.Spec = SpecNone
		case t.Spec == SpecDraft && !m.DraftAvailable:
			if m.Draft == "" && t.DraftModel == "" {
				bad("%s has no draft model paired in the catalog (the draft must share its tokenizer); use spec \"ngram\" instead, which needs no second model", nameOr(m))
			} else {
				bad("this image has no draft model baked in: rebuild it with spec \"draft\", or use spec \"ngram\"")
			}
		}
	}
	if t.Spec == SpecDraft && t.DraftModel == "" {
		t.DraftModel = m.Draft
	}
	if t.Spec != SpecDraft {
		t.DraftModel = ""
	}
	maxDraft := MaxDraft
	if t.Spec == SpecNgram {
		maxDraft = MaxNgram
	}
	if t.DraftMax < 0 || t.DraftMax > maxDraft {
		bad("draft_max %d is out of range (1 to %d)", t.DraftMax, maxDraft)
	}
	if t.DraftMin < 0 || t.DraftMin > maxDraft {
		bad("draft_min %d is out of range (0 to %d)", t.DraftMin, maxDraft)
	}
	if t.Spec == "" || t.Spec == SpecNone {
		if t.DraftMax != 0 || t.DraftMin != 0 {
			t.DraftMax, t.DraftMin = 0, 0
		}
	} else if t.DraftMax > 0 && t.DraftMin > t.DraftMax {
		note("draft_min can't exceed draft_max, so it's %d", t.DraftMax)
		t.DraftMin = t.DraftMax
	}

	// Reasoning.
	switch t.Reasoning {
	case "", "auto", "on", "off":
	case "true", "1":
		t.Reasoning = "on"
	case "false", "0":
		t.Reasoning = "off"
	default:
		bad("reasoning %q isn't auto, on or off", t.Reasoning)
	}
	switch t.ReasoningEffort {
	case "", "low", "medium", "high":
	default:
		bad("reasoning_effort %q isn't low, medium or high", t.ReasoningEffort)
	}
	if t.ReasoningBudget != nil && *t.ReasoningBudget < -1 {
		bad("reasoning_budget %d: use -1 for unlimited, 0 for none or a token count", *t.ReasoningBudget)
	}
	if m.Modality == "embedding" && (t.Reasoning != "" || t.ReasoningEffort != "" || t.ReasoningBudget != nil) {
		t.Reasoning, t.ReasoningEffort, t.ReasoningBudget = "", "", nil
	}

	// Caches and threads.
	if t.CacheRAMMB != nil && (*t.CacheRAMMB < -1 || *t.CacheRAMMB > MaxCacheRAM) {
		bad("cache_ram_mb %d is out of range (-1 for no limit, 0 for off, up to %d MiB)", *t.CacheRAMMB, MaxCacheRAM)
	}
	if t.CtxCheckpoints != nil && (*t.CtxCheckpoints < 0 || *t.CtxCheckpoints > MaxCkpts) {
		bad("ctx_checkpoints %d is out of range (0 to %d)", *t.CtxCheckpoints, MaxCkpts)
	}
	if t.Threads < 0 || t.Threads > MaxThreads {
		bad("threads %d is out of range (1 to %d)", t.Threads, MaxThreads)
	}

	// Harness limits.
	if t.MaxSteps < 0 || t.MaxSteps > MaxHarnessSteps {
		bad("max_steps %d is out of range (1 to %d)", t.MaxSteps, MaxHarnessSteps)
	}
	if t.MaxCalls < 0 || t.MaxCalls > MaxHarnessCalls {
		bad("max_calls_per_step %d is out of range (1 to %d)", t.MaxCalls, MaxHarnessCalls)
	}
	if t.ToolTimeoutS < 0 || t.ToolTimeoutS > MaxHarnessTimeoutS {
		bad("tool_timeout_s %d is out of range (1 to %d seconds)", t.ToolTimeoutS, MaxHarnessTimeoutS)
	}
	if t.MaxOutputKB != 0 && (math.IsNaN(t.MaxOutputKB) || t.MaxOutputKB < MinHarnessOutputKB || t.MaxOutputKB > MaxHarnessOutputKB) {
		bad("max_output_kb %g is out of range (%g to %g)", t.MaxOutputKB, MinHarnessOutputKB, float64(MaxHarnessOutputKB))
	}

	// Sampling defaults (a request's own values still win).
	checkF := func(name string, v *float64, lo, hi float64) {
		if v != nil && (math.IsNaN(*v) || *v < lo || *v > hi) {
			bad("%s %g is out of range (%g to %g)", name, *v, lo, hi)
		}
	}
	checkF("temperature", t.Temperature, 0, 5)
	checkF("top_p", t.TopP, 0, 1)
	checkF("min_p", t.MinP, 0, 1)
	checkF("repeat_penalty", t.RepeatPenalty, 0.5, 2)
	if t.TopK != nil && (*t.TopK < 0 || *t.TopK > 10000) {
		bad("top_k %d is out of range (0 to 10000; 0 turns it off)", *t.TopK)
	}

	if len(errs) > 0 {
		return t, notes, fmt.Errorf("tuning: %s", strings.Join(errs, "; "))
	}
	return t, notes, nil
}

func (t Tuning) effBatch() int {
	if t.Batch > 0 {
		return t.Batch
	}
	return 2048 // llama.cpp's default
}

func (t Tuning) effUBatch() int {
	if t.UBatch > 0 {
		return t.UBatch
	}
	return min(512, t.effBatch()) // llama.cpp's default
}

func nameOr(m Model) string {
	if m.Name != "" {
		return m.Name
	}
	if m.ID != "" {
		return m.ID
	}
	return "the model"
}

// Tokens formats a token count: 32768 -> "32K", 1000 -> "1000".
func Tokens(n int) string {
	if n >= 1024 && n%1024 == 0 {
		return strconv.Itoa(n/1024) + "K"
	}
	return strconv.Itoa(n)
}

// Environment variables llmgate and llmpack read (gate.Llama.ApplyEnv).
const (
	EnvCtxSize         = "CTX_SIZE"
	EnvParallel        = "PARALLEL"
	EnvThreads         = "THREADS"
	EnvFlashAttn       = "FLASH_ATTN"
	EnvKVCacheType     = "KV_CACHE_TYPE"
	EnvKVUnified       = "KV_UNIFIED"
	EnvBatchSize       = "BATCH_SIZE"
	EnvUBatchSize      = "UBATCH_SIZE"
	EnvSpecType        = "SPEC_TYPE"
	EnvDraftModelPath  = "DRAFT_MODEL_PATH"
	EnvDraftMax        = "DRAFT_MAX"
	EnvDraftMin        = "DRAFT_MIN"
	EnvReasoning       = "REASONING"
	EnvReasoningEffort = "REASONING_EFFORT"
	EnvReasoningBudget = "REASONING_BUDGET"
	EnvCacheRAMMB      = "CACHE_RAM_MB"
	EnvCtxCheckpoints  = "CTX_CHECKPOINTS"
	EnvTemperature     = "TEMPERATURE"
	EnvTopP            = "TOP_P"
	EnvTopK            = "TOP_K"
	EnvMinP            = "MIN_P"
	EnvRepeatPenalty   = "REPEAT_PENALTY"

	// The harness's (internal/agent Config.WithEnv).
	EnvHarnessMaxSteps   = "HARNESS_MAX_STEPS"
	EnvHarnessMaxCalls   = "HARNESS_MAX_CALLS_PER_STEP"
	EnvHarnessTimeout    = "HARNESS_TOOL_TIMEOUT"
	EnvHarnessMaxOutputB = "HARNESS_MAX_OUTPUT_BYTES"
)

// EnvNames lists every tuning variable (sorted).
func EnvNames() []string {
	n := []string{EnvCtxSize, EnvParallel, EnvThreads, EnvFlashAttn, EnvKVCacheType, EnvKVUnified,
		EnvBatchSize, EnvUBatchSize, EnvSpecType, EnvDraftModelPath, EnvDraftMax, EnvDraftMin,
		EnvReasoning, EnvReasoningEffort, EnvReasoningBudget, EnvCacheRAMMB, EnvCtxCheckpoints,
		EnvTemperature, EnvTopP, EnvTopK, EnvMinP, EnvRepeatPenalty,
		EnvHarnessMaxSteps, EnvHarnessMaxCalls, EnvHarnessTimeout, EnvHarnessMaxOutputB}
	sort.Strings(n)
	return n
}

// ImageDraftPath is where an image keeps its draft model.
const ImageDraftPath = "/models/draft.gguf"

// EnvMap is t as environment variables: only the set fields. draftPath is
// where the draft model is (the image's /models/draft.gguf, a package's file).
func (t Tuning) EnvMap(draftPath string) map[string]string {
	e := map[string]string{}
	i := func(k string, v int) {
		if v != 0 {
			e[k] = strconv.Itoa(v)
		}
	}
	s := func(k, v string) {
		if v != "" {
			e[k] = v
		}
	}
	f := func(k string, v *float64) {
		if v != nil {
			e[k] = strconv.FormatFloat(*v, 'g', -1, 64)
		}
	}
	ip := func(k string, v *int) {
		if v != nil {
			e[k] = strconv.Itoa(*v)
		}
	}
	i(EnvCtxSize, t.CtxSize)
	i(EnvParallel, t.Parallel)
	i(EnvThreads, t.Threads)
	s(EnvFlashAttn, t.FlashAttn)
	s(EnvKVCacheType, t.KVCache)
	if t.KVUnified != nil {
		e[EnvKVUnified] = map[bool]string{true: "1", false: "0"}[*t.KVUnified]
	}
	i(EnvBatchSize, t.Batch)
	i(EnvUBatchSize, t.UBatch)
	s(EnvSpecType, t.Spec)
	if t.Spec == SpecDraft {
		s(EnvDraftModelPath, draftPath)
	}
	if t.Spec != "" && t.Spec != SpecNone {
		i(EnvDraftMax, t.DraftMax)
		i(EnvDraftMin, t.DraftMin)
	}
	s(EnvReasoning, t.Reasoning)
	s(EnvReasoningEffort, t.ReasoningEffort)
	ip(EnvReasoningBudget, t.ReasoningBudget)
	ip(EnvCacheRAMMB, t.CacheRAMMB)
	ip(EnvCtxCheckpoints, t.CtxCheckpoints)
	f(EnvTemperature, t.Temperature)
	f(EnvTopP, t.TopP)
	ip(EnvTopK, t.TopK)
	f(EnvMinP, t.MinP)
	f(EnvRepeatPenalty, t.RepeatPenalty)
	i(EnvHarnessMaxSteps, t.MaxSteps)
	i(EnvHarnessMaxCalls, t.MaxCalls)
	i(EnvHarnessTimeout, t.ToolTimeoutS)
	if t.MaxOutputKB != 0 {
		e[EnvHarnessMaxOutputB] = strconv.Itoa(int(math.Round(t.MaxOutputKB * 1024)))
	}
	return e
}

// Env is EnvMap as sorted KEY=VALUE pairs.
func (t Tuning) Env(draftPath string) []string {
	m := t.EnvMap(draftPath)
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// EnvChange is the run-time environment that turns an image baked with
// baked into one running effective: the variables that differ, with
// "KEY=" (empty: the program's default) for one effective leaves unset.
// The context size is never cleared (the image's own CTX_SIZE stays).
func EnvChange(baked, effective Tuning, draftPath string) []string {
	b, e := baked.EnvMap(draftPath), effective.EnvMap(draftPath)
	var out []string
	for _, k := range EnvNames() {
		ev, inE := e[k]
		bv, inB := b[k]
		switch {
		case inE && (!inB || ev != bv):
			out = append(out, k+"="+ev)
		case !inE && inB && k != EnvCtxSize:
			out = append(out, k+"=")
		}
	}
	return out
}
