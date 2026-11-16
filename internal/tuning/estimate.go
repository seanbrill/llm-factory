package tuning

import (
	"fmt"
	"math"
)

// Target is the memory a model runs in.
type Target struct {
	Engine  string  `json:"name"`    // docker | podman | native
	Compute string  `json:"compute"` // cpu | cuda | vulkan | metal
	MemGB   float64 `json:"mem_gb"`  // the engine machine's memory (a native package: the computer's)
}

// engineReserveGB is what an engine's machine keeps for itself (kernel,
// container runtime, the gate) besides the model.
const engineReserveGB = 1.0

// UsableGB is the memory the model may take: the engine VM's memory less its
// own share; on a Mac with Metal about two thirds of the computer's memory
// (macOS's GPU working-set limit). 0 = unknown.
func (t Target) UsableGB() float64 {
	if t.MemGB <= 0 {
		return 0
	}
	if t.Engine == "native" {
		if t.Compute == "metal" {
			return t.MemGB * 2 / 3
		}
		return t.MemGB - 2*engineReserveGB
	}
	return t.MemGB - engineReserveGB
}

// Estimate is a tuning's memory, in GB (1e9 bytes, like the engines report).
// weights_gb, kv_gb and total_gb are the headline; the rest itemize total.
type Estimate struct {
	WeightsGB float64 `json:"weights_gb"` // model (+ projector, + draft) files
	KVGB      float64 `json:"kv_gb"`      // the KV cache (+ the draft's)
	TotalGB   float64 `json:"total_gb"`
	ComputeGB float64 `json:"compute_gb"` // compute buffers
	StateGB   float64 `json:"state_gb,omitempty"`
	// CacheGB is what can grow while it runs: the prompt cache in RAM
	// (cache_ram_mb) and context checkpoints, at their limits.
	CacheGB    float64 `json:"cache_gb"`
	OverheadGB float64 `json:"overhead_gb"`
	// Approx says the model's GGUF header wasn't read: the KV size is a
	// guess from its parameter count.
	Approx bool `json:"approx,omitempty"`
}

const (
	gb             = 1e9
	serverOverhead = 0.35 * gb // llama-server, its runtime and the gate
	computeBase    = 0.25 * gb
)

func pad256(n int) int { return (n + 255) / 256 * 256 }

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// kvBytes is a KV cache's size for ctx tokens over par slots.
func kvBytes(a ArchInfo, ctx, par, ubatch int, unified bool, bpe float64) float64 {
	cells := pad256(ctx)
	b := float64(a.FullElems) * float64(cells) * bpe
	if a.SWAElems > 0 {
		// llama-kv-cache-iswa.cpp: min(size, n_swa*(unified ? n_seq : 1) + n_ubatch), per stream.
		swa := pad256(a.SlidingWindow*par + ubatch)
		if !unified {
			swa = par * pad256(a.SlidingWindow+ubatch)
		}
		b += float64(a.SWAElems) * float64(min(cells, swa)) * bpe
	}
	return b
}

// checkpointBytes is one context checkpoint of one slot: the parts of the
// cache that can't be rolled back (the sliding window, the recurrent state).
func checkpointBytes(a ArchInfo, ctx, ubatch int, bpe float64) float64 {
	b := float64(a.RecurrentBytes)
	if a.SWAElems > 0 {
		b += float64(a.SWAElems) * float64(min(pad256(ctx), pad256(a.SlidingWindow+ubatch))) * bpe
	}
	return b
}

// guessArch stands in for an unread GGUF header: a Llama-like dense model's
// KV size per parameter (8B: 32 layers x 8 KV heads x 128 x 2).
func guessArch(m Model) ArchInfo {
	params := m.ParamsB
	if params <= 0 {
		params = float64(m.WeightsBytes) / gb / 0.6 // ~Q4_K_M bytes per parameter
	}
	if params <= 0 {
		params = 8
	}
	return ArchInfo{FullElems: int64(params * 8192), Embd: 4096, Heads: 32, VocabSize: 152064, QuantKVOK: true}
}

// EstimateMemory is t's memory for m. t should be normalized; an unset
// context counts as 8192 tokens.
func EstimateMemory(t Tuning, m Model) Estimate {
	var e Estimate
	a := m.Arch
	if a == nil {
		g := guessArch(m)
		a = &g
		e.Approx = true
	}
	bpe := kvTypes[t.KVCache]
	if bpe == 0 {
		bpe = kvTypes["f16"]
	}
	ctx := t.CtxSize
	if ctx <= 0 {
		ctx = 8192
	}
	par := max(t.Parallel, 1)
	unified := t.KVUnified != nil && *t.KVUnified && par > 1
	ub := t.effUBatch()

	weights := float64(m.WeightsBytes + m.MMProjBytes)
	kv := kvBytes(*a, ctx, par, ub, unified, bpe)
	if t.Spec == SpecDraft {
		weights += float64(m.DraftBytes)
		if m.DraftArch != nil {
			kv += kvBytes(*m.DraftArch, ctx, par, ub, unified, kvTypes["f16"])
		} else if m.DraftBytes > 0 {
			kv += float64(m.DraftBytes) / 4
		}
	}
	state := float64(a.RecurrentBytes) * float64(par)

	embd, heads, vocab := a.Embd, a.Heads, a.VocabSize
	if embd <= 0 {
		embd = 4096
	}
	if heads <= 0 {
		heads = 32
	}
	if vocab <= 0 {
		vocab = 152064
	}
	compute := computeBase + float64(ub)*float64(embd)*4*48 + float64(par)*float64(vocab)*4
	if t.FlashAttn == "off" {
		// Without flash attention the scores of a whole ubatch over the
		// cache are materialized.
		compute += float64(ub) * float64(pad256(ctx)) * float64(heads) * 4
	}
	if m.Vision {
		compute += 0.3 * gb // the projector's encoder
	}

	// What grows while it runs.
	var cache float64
	switch {
	case t.CacheRAMMB == nil:
		cache = DefaultCacheRAMMB * 1024 * 1024
	case *t.CacheRAMMB < 0:
		cache = 2 * kv // no limit: a couple of caches' worth, at least
	default:
		cache = float64(*t.CacheRAMMB) * 1024 * 1024
	}
	if a.SWAElems > 0 || a.RecurrentBytes > 0 {
		n := DefaultCkpts
		if t.CtxCheckpoints != nil {
			n = *t.CtxCheckpoints
		}
		cache += float64(n*par) * checkpointBytes(*a, ctx, ub, bpe)
	}

	e.WeightsGB = round2(weights / gb)
	e.KVGB = round2(kv / gb)
	e.ComputeGB = round2(compute / gb)
	e.StateGB = round2(state / gb)
	e.CacheGB = round2(cache / gb)
	e.OverheadGB = round2(serverOverhead / gb)
	e.TotalGB = round2((weights + kv + compute + state + cache + serverOverhead) / gb)
	return e
}

// Fit says whether e fits the target, with a warning when it doesn't (or
// can't be told).
func Fit(e Estimate, tg Target) (fits bool, warning string) {
	u := tg.UsableGB()
	if u <= 0 {
		return true, ""
	}
	if e.TotalGB <= u {
		return true, ""
	}
	where := "the engine's machine"
	switch tg.Engine {
	case "podman":
		where = "the Podman machine"
	case "docker":
		where = "Docker"
	case "native":
		where = "this computer's GPU memory"
	}
	return false, fmt.Sprintf("needs ~%.1f GB but %s has ~%.1f GB for a model (%.1f GB in all)", e.TotalGB, where, u, tg.MemGB)
}
