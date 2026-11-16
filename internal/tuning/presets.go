package tuning

import (
	"fmt"
	"math"
	"slices"
	"strings"
)

// Option is one preset, resolved for a model and the memory it runs in.
type Option struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Explain  string   `json:"explain"`
	Tuning   Tuning   `json:"tuning"`
	Estimate Estimate `json:"estimate"`
	Fits     bool     `json:"fits"`
	Warning  string   `json:"warning,omitempty"`
}

var presetLabels = map[string]string{
	PresetSpeed:       "Speed",
	PresetBalanced:    "Balanced",
	PresetLongContext: "Long context",
	PresetQuality:     "Quality",
}

// PresetLabel is a preset's display name.
func PresetLabel(id string) string {
	if l, ok := presetLabels[id]; ok {
		return l
	}
	if id == PresetCustom {
		return "Custom"
	}
	return id
}

// Presets resolves every preset for m on tg.
func Presets(m Model, tg Target) []Option {
	out := make([]Option, 0, len(PresetIDs))
	for _, id := range PresetIDs {
		t, fits, warn := generate(id, Tuning{Preset: id}, m, tg)
		e := EstimateMemory(t, m)
		out = append(out, Option{ID: id, Label: PresetLabel(id), Explain: explain(id, t, m), Tuning: t, Estimate: e, Fits: fits, Warning: warn})
	}
	return out
}

// Resolve fills the fields a generated preset decides (those t leaves unset)
// for m on tg, then normalizes. A custom (or no) preset is only normalized.
func Resolve(t Tuning, m Model, tg Target) (Tuning, []string, error) {
	t.Preset = strings.ToLower(strings.TrimSpace(t.Preset))
	if !t.Generated() {
		return Normalize(t, m)
	}
	// Normalize what was asked first, so its notes are kept (generate
	// normalizes again, and would hide them).
	asked, notes, err := Normalize(t, m)
	if err != nil {
		return asked, notes, err
	}
	g, _, _ := generate(asked.Preset, asked, m, tg)
	out, more, err := Normalize(g, m)
	for _, n := range more {
		if !slices.Contains(notes, n) {
			notes = append(notes, n)
		}
	}
	return out, notes, err
}

// base is a preset's fixed choices, before the context and caches are sized.
func base(id string, m Model) (Tuning, []int) {
	var t Tuning
	var ctxs []int
	spec := SpecNgram
	if m.Vision || m.Modality == "embedding" {
		spec = SpecNone
	}
	a := m.Arch
	switch id {
	case PresetSpeed:
		t = Tuning{FlashAttn: "on", KVCache: "q8_0", Spec: spec, Parallel: 1}
		if a != nil && a.ReasoningEffort {
			t.ReasoningEffort = "low"
		}
		ctxs = []int{16384, 8192, 4096}
	case PresetBalanced:
		t = Tuning{FlashAttn: "on", KVCache: "q8_0", Spec: spec, Parallel: 2, KVUnified: BoolPtr(true)}
		ctxs = []int{32768, 16384, 8192, 4096}
	case PresetLongContext:
		t = Tuning{FlashAttn: "on", KVCache: "q8_0", Spec: spec, Parallel: 1}
		top := m.TrainedCtx()
		if top <= 0 {
			top = 32768 // unknown: don't promise what the model may not do
		}
		for c := top; c >= 8192; c /= 2 {
			ctxs = append(ctxs, c)
		}
		ctxs = append(ctxs, 4096)
	case PresetQuality:
		t = Tuning{FlashAttn: "on", KVCache: "f16", Spec: SpecNone, Parallel: 1}
		if a != nil && a.Thinks {
			t.Reasoning = "on"
		}
		if a != nil && a.ReasoningEffort {
			t.ReasoningEffort = "high"
		}
		ctxs = []int{32768, 16384, 8192, 4096}
	}
	if m.Modality == "embedding" {
		t.Spec, t.Reasoning, t.ReasoningEffort = SpecNone, "", ""
	}
	t.Preset = id
	return t, ctxs
}

// generate builds preset id around the fields fixed already sets: it tries
// the preset's contexts from the largest down and keeps the first that fits
// (long_context also tries a q4_0 cache when q8_0 can't reach the model's
// trained context), then sizes the prompt cache and context checkpoints to
// the memory left.
func generate(id string, fixed Tuning, m Model, tg Target) (Tuning, bool, string) {
	b, ctxs := base(id, m)
	if fixed.CtxSize > 0 {
		ctxs = []int{fixed.CtxSize}
	}
	if tc := m.TrainedCtx(); tc > 0 {
		capped := ctxs[:0:0]
		for _, c := range ctxs {
			if c > tc {
				c = tc
			}
			if len(capped) == 0 || capped[len(capped)-1] != c {
				capped = append(capped, c)
			}
		}
		ctxs = capped
	}
	kvs := []string{""}
	if id == PresetLongContext && fixed.KVCache == "" {
		kvs = []string{"", "q4_0"}
	}

	var best Tuning
	var bestOK bool
	for _, kv := range kvs {
		for _, c := range ctxs {
			t := Overlay(b, fixed)
			t.Preset = id
			if kv != "" {
				t.KVCache = kv
			}
			t.CtxSize = c
			if t2, _, err := Normalize(t, m); err == nil {
				t = t2
			}
			t, ok := sizeCaches(t, fixed, m, tg)
			if ok {
				if !bestOK || t.CtxSize > best.CtxSize {
					best, bestOK = t, true
				}
				break
			}
			if !bestOK && (best.CtxSize == 0 || c < best.CtxSize) {
				best = t // the smallest that didn't fit, for the warning
			}
		}
		if bestOK && (m.TrainedCtx() <= 0 || best.CtxSize >= m.TrainedCtx()) {
			break // q8_0 already reaches the trained context
		}
	}
	e := EstimateMemory(best, m)
	fits, warn := Fit(e, tg)
	if !bestOK && warn == "" {
		fits, warn = false, "doesn't fit"
	}
	if fits && len(ctxs) > 1 && best.CtxSize < ctxs[0] && id != PresetLongContext {
		warn = fmt.Sprintf("%s context didn't fit, so %s", Tokens(ctxs[0]), Tokens(best.CtxSize))
	}
	return best, fits, warn
}

// sizeCaches sets the prompt cache (cache_ram_mb) and, for sliding-window
// and hybrid models, the context checkpoints to the memory the rest leaves,
// unless fixed sets them. It reports whether the result fits.
func sizeCaches(t Tuning, fixed Tuning, m Model, tg Target) (Tuning, bool) {
	usable := tg.UsableGB()
	if usable <= 0 {
		// Unknown memory: llama.cpp's own limits, modest ones.
		if fixed.CacheRAMMB == nil {
			t.CacheRAMMB = IntPtr(2048)
		}
		return t, true
	}
	a := m.Arch
	if a == nil {
		g := guessArch(m)
		a = &g
	}
	probe := t
	probe.CacheRAMMB = IntPtr(0)
	if fixed.CacheRAMMB != nil {
		probe.CacheRAMMB = fixed.CacheRAMMB
	}
	ckpt := a.SWAElems > 0 || a.RecurrentBytes > 0
	if ckpt {
		probe.CtxCheckpoints = IntPtr(0)
		if fixed.CtxCheckpoints != nil {
			probe.CtxCheckpoints = fixed.CtxCheckpoints
		}
	}
	spare := usable - EstimateMemory(probe, m).TotalGB
	if spare < 0.25 {
		// Doesn't fit even without the caches: report it at its smallest.
		if ckpt && fixed.CtxCheckpoints == nil {
			probe.CtxCheckpoints = IntPtr(1)
		}
		return probe, false
	}
	if ckpt && fixed.CtxCheckpoints == nil {
		bpe := kvTypes[t.KVCache]
		if bpe == 0 {
			bpe = 2
		}
		per := checkpointBytes(*a, max(t.CtxSize, 1), t.effUBatch(), bpe) * float64(max(t.Parallel, 1)) / gb
		n := 8
		if per > 0 {
			n = int(math.Floor(spare * 0.3 / per))
		}
		n = min(max(n, 1), 8)
		probe.CtxCheckpoints = IntPtr(n)
		t.CtxCheckpoints = IntPtr(n)
		spare = usable - EstimateMemory(probe, m).TotalGB
		if spare < 0.25 {
			return t, false
		}
	}
	if fixed.CacheRAMMB == nil {
		mib := int(spare*0.5*gb/(1024*1024)) / 256 * 256
		t.CacheRAMMB = IntPtr(min(mib, 4096))
	}
	return t, EstimateMemory(t, m).TotalGB <= usable
}

func explain(id string, t Tuning, m Model) string {
	ctx := Tokens(t.CtxSize)
	look := "n-gram look-ahead (re-uses text already in the chat: a free speed-up when editing code)"
	if t.Spec == SpecNone || t.Spec == "" {
		look = "no look-ahead"
	} else if t.Spec == SpecDraft {
		look = "a draft model guessing ahead"
	}
	switch id {
	case PresetSpeed:
		s := fmt.Sprintf("Fastest replies: one slot, %s context, %s cache, %s.", ctx, t.KVCache, look)
		if t.ReasoningEffort == "low" {
			s += " Reasoning effort low."
		}
		return s
	case PresetBalanced:
		return fmt.Sprintf("Chat and tools: %s context shared by %d slots, so a client's background requests (titles, tool calls) don't wait for the chat; %s cache, %s.", ctx, max(t.Parallel, 1), t.KVCache, look)
	case PresetLongContext:
		s := fmt.Sprintf("Longest memory: %s context", ctx)
		if tc := m.TrainedCtx(); tc > 0 && t.CtxSize >= tc {
			s += " (all the model was trained for)"
		}
		return s + fmt.Sprintf(" with a %s cache, one slot, %s. Long prompts take longer to read.", t.KVCache, look)
	case PresetQuality:
		s := fmt.Sprintf("Best answers: full-precision f16 cache, no look-ahead, %s context", ctx)
		if t.Reasoning == "on" {
			s += ", thinking on"
		}
		if t.ReasoningEffort == "high" {
			s += ", reasoning effort high"
		}
		return s + ". Slower."
	}
	return ""
}
