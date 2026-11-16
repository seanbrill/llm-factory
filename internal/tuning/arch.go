package tuning

import (
	"errors"
	"fmt"
	"strings"
)

// ArchInfo is what the memory estimate needs from a model's metadata. KV
// sizes are elements per token (K plus V, summed over the layers), so the
// cache is FullElems*cells + SWAElems*swaCells, times the cache type's bytes
// per element.
type ArchInfo struct {
	Arch       string `json:"arch"`
	Layers     int    `json:"layers"`
	TrainedCtx int    `json:"trained_ctx"`
	Embd       int    `json:"embd,omitempty"`
	Heads      int    `json:"heads,omitempty"`
	VocabSize  int    `json:"vocab_size,omitempty"`
	// FullElems: the full-attention layers' K+V elements per token.
	FullElems int64 `json:"full_elems"`
	// SWAElems: the sliding-window layers' K+V elements per token; their cache
	// holds about SlidingWindow tokens, not the whole context.
	SWAElems      int64 `json:"swa_elems,omitempty"`
	SlidingWindow int   `json:"sliding_window,omitempty"`
	// KVLayers counts layers with a KV cache (a hybrid model's recurrent layers
	// keep a fixed state instead).
	KVLayers int `json:"kv_layers"`
	// RecurrentBytes is the fixed recurrent state of one sequence (Mamba,
	// Gated DeltaNet, short convolutions), f32. 0 = no recurrent layers.
	RecurrentBytes int64 `json:"recurrent_bytes,omitempty"`
	// QuantKVOK says every head size divides a quantized cache block (32), so
	// q8_0/q4_0 caches load.
	QuantKVOK bool `json:"quant_kv_ok"`
	// ReasoningEffort says the chat template reads reasoning_effort (gpt-oss
	// style: low/medium/high); Thinks says it has a thinking toggle
	// (enable_thinking). False when the template wasn't read.
	ReasoningEffort bool `json:"reasoning_effort,omitempty"`
	Thinks          bool `json:"thinks,omitempty"`
}

// Recurrent says the model keeps a recurrent state (a hybrid or SSM model).
func (a ArchInfo) Recurrent() bool { return a.RecurrentBytes > 0 }

// swaPeriods are architectures whose sliding-window layout llama.cpp sets in
// code (llama-model.cpp, set_swa_pattern(n)): in each group of n layers the
// last is full attention, the others slide.
var swaPeriods = map[string]int{
	"gemma2":       2,
	"gemma3":       6,
	"gpt-oss":      2,
	"cohere2":      4,
	"llama4":       4,
	"exaone4":      4,
	"smallthinker": 4,
}

func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case uint64:
		return int(x), true
	case int64:
		return int(x), true
	}
	return 0, false
}

// perLayer reads key as a per-layer array, or a scalar for every layer.
func (m *GGUFMeta) perLayer(key string, n int) ([]int, bool) {
	out := make([]int, n)
	if arr, ok := m.KV[key].([]any); ok {
		if len(arr) == 0 {
			return nil, false
		}
		for i := range out {
			j := i
			if j >= len(arr) {
				j = len(arr) - 1
			}
			out[i], _ = toInt(arr[j])
		}
		return out, true
	}
	x, ok := toInt(m.KV[key])
	if !ok {
		return nil, false
	}
	for i := range out {
		out[i] = x
	}
	return out, true
}

// Info extracts ArchInfo from the metadata.
func (m *GGUFMeta) Info() (ArchInfo, error) {
	a := ArchInfo{Arch: m.Arch()}
	if a.Arch == "" {
		return a, errors.New("gguf: no general.architecture")
	}
	k := func(s string) string { return a.Arch + "." + s }
	u := func(s string) int {
		v, _ := m.Uint(k(s))
		return int(v)
	}
	a.Layers = u("block_count")
	a.TrainedCtx = u("context_length")
	a.Embd = u("embedding_length")
	a.Heads = u("attention.head_count")
	if n := m.ArrLen["tokenizer.ggml.tokens"]; n > 0 {
		a.VocabSize = int(n)
	}
	if a.Layers <= 0 {
		return a, fmt.Errorf("gguf: %s metadata has no block_count", a.Arch)
	}
	headsKV, ok := m.perLayer(k("attention.head_count_kv"), a.Layers)
	if !ok {
		headsKV, ok = m.perLayer(k("attention.head_count"), a.Layers)
	}
	if !ok {
		return a, fmt.Errorf("gguf: %s metadata has no attention heads", a.Arch)
	}
	headDim := 0
	if a.Heads > 0 && a.Embd > 0 {
		headDim = a.Embd / a.Heads
	}
	kLen, vLen := u("attention.key_length"), u("attention.value_length")
	if kLen == 0 {
		kLen = headDim
	}
	if vLen == 0 {
		vLen = headDim
	}
	kSWA, vSWA := u("attention.key_length_swa"), u("attention.value_length_swa")
	if kSWA == 0 {
		kSWA = kLen
	}
	if vSWA == 0 {
		vSWA = vLen
	}
	if kLen <= 0 || vLen <= 0 {
		return a, fmt.Errorf("gguf: %s metadata lacks the attention head size", a.Arch)
	}

	// Which layers slide.
	a.SlidingWindow = u("attention.sliding_window")
	swa := make([]bool, a.Layers)
	if a.SlidingWindow > 0 {
		if arr, ok := m.KV[k("attention.sliding_window_pattern")].([]any); ok {
			for i := range swa {
				if i < len(arr) {
					swa[i], _ = arr[i].(bool)
				}
			}
		} else {
			period, ok := toInt(m.KV[k("attention.sliding_window_pattern")])
			if !ok || period <= 1 {
				period = swaPeriods[a.Arch]
				if period == 0 && strings.HasPrefix(a.Arch, "gemma") {
					period = 6
				}
			}
			for i := range swa {
				swa[i] = period > 1 && i%period < period-1
			}
		}
	}
	// Which layers attend at all: a hybrid model marks its recurrent layers
	// with 0 KV heads, or attends every full_attention_interval-th layer.
	fai := u("full_attention_interval")
	shared := u("attention.shared_kv_layers")
	a.QuantKVOK = true
	rec := 0
	for i := 0; i < a.Layers; i++ {
		h := headsKV[i]
		if fai > 0 && (i+1)%fai != 0 {
			h = 0
		}
		if shared > 0 && i >= a.Layers-shared {
			continue // reuses an earlier layer's cache
		}
		if h <= 0 {
			rec++
			continue
		}
		a.KVLayers++
		kl, vl := kLen, vLen
		if swa[i] {
			kl, vl = kSWA, vSWA
		}
		if kl%32 != 0 || vl%32 != 0 {
			a.QuantKVOK = false
		}
		e := int64(h) * int64(kl+vl)
		if swa[i] {
			a.SWAElems += e
		} else {
			a.FullElems += e
		}
	}
	if a.SWAElems == 0 {
		a.SlidingWindow = 0
	}
	// Recurrent state per sequence (f32): the SSM state plus the convolution
	// window, for every layer without a KV cache.
	if rec > 0 {
		inner, state, conv, groups := u("ssm.inner_size"), u("ssm.state_size"), u("ssm.conv_kernel"), u("ssm.group_count")
		per := int64(inner)*int64(state) + int64(max(conv-1, 0))*int64(inner+2*groups*state)
		if lc := u("shortconv.l_cache"); lc > 0 && per == 0 {
			per = int64(lc) * int64(a.Embd)
		}
		a.RecurrentBytes = per * 4 * int64(rec)
	}
	tpl := m.String("tokenizer.chat_template")
	a.ReasoningEffort = a.Arch == "gpt-oss" || strings.Contains(tpl, "reasoning_effort")
	a.Thinks = strings.Contains(tpl, "enable_thinking")
	if a.KVLayers == 0 && a.RecurrentBytes == 0 {
		return a, fmt.Errorf("gguf: %s metadata has no attention layers", a.Arch)
	}
	return a, nil
}
