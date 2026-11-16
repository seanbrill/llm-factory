package gate

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
)

// Llama is how llama-server is started: by llmgate inside a model image and
// by llmpack from a native package.
type Llama struct {
	Model    string // GGUF path
	Host     string // listen address; "" = 127.0.0.1 (only the gate in front reaches it)
	Port     int
	CtxSize  int
	Threads  int // 0 = llama.cpp's own choice (its performance cores)
	Parallel int // 0 = 1
	// GPULayers is --n-gpu-layers: "" leaves llama-server's default, else a
	// number, "auto" (as many as fit in the GPU's memory) or "all".
	GPULayers       string
	MMProj          string // vision projector; "" or an empty file = text model
	MMProjNoOffload bool   // keep the projector on the CPU (Vulkan VLM caveat)
	Modality        string // text|code|reasoning|vision|embedding (drives flags)
	NoWebUI         bool   // turn llama-server's own web page off (prebuilt releases carry one)
	Extra           []string

	// Runtime tuning (docs/TUNING.md). Unset fields add no flag, so
	// llama-server's own defaults apply; ApplyEnv reads them from the
	// environment.
	FlashAttn       string // --flash-attn auto|on|off
	KVCacheType     string // --cache-type-k and --cache-type-v (f16, q8_0, q4_0)
	KVUnified       bool   // --kv-unified (with Parallel > 1)
	Batch           int    // --batch-size
	UBatch          int    // --ubatch-size
	SpecType        string // none|ngram|draft
	DraftModel      string // draft GGUF for SpecType draft; "" or an empty file = no draft
	DraftMax        int    // tokens drafted per step
	DraftMin        int
	Reasoning       string // --reasoning auto|on|off
	ReasoningEffort string // --reasoning-effort low|medium|high
	ReasoningBudget *int   // --reasoning-budget N (-1 unlimited)
	CacheRAMMB      *int   // --cache-ram N MiB (-1 unlimited, 0 off)
	CtxCheckpoints  *int   // --ctx-checkpoints N
	Temperature     *float64
	TopP            *float64
	TopK            *int
	MinP            *float64
	RepeatPenalty   *float64
}

// Args is llama-server's command line (without the program).
func (l Llama) Args() []string {
	host := l.Host
	if host == "" {
		host = "127.0.0.1"
	}
	parallel := l.Parallel
	if parallel <= 0 {
		parallel = 1
	}
	args := []string{
		"--model", l.Model,
		"--host", host,
		"--port", strconv.Itoa(l.Port),
		"--ctx-size", strconv.Itoa(l.CtxSize),
	}
	if l.Threads > 0 {
		args = append(args, "--threads", strconv.Itoa(l.Threads), "--threads-batch", strconv.Itoa(l.Threads))
	}
	args = append(args,
		"--parallel", strconv.Itoa(parallel),
		"--cont-batching",
		"--jinja",
		"--metrics",
	)
	if l.NoWebUI {
		args = append(args, "--no-webui")
	}
	if g := strings.TrimSpace(l.GPULayers); g != "" && g != "0" {
		args = append(args, "--n-gpu-layers", g)
	}
	// Embedding models serve /v1/embeddings instead of chat; --embedding
	// switches llama-server into that mode (pooling auto-detects from the model).
	if l.Modality == "embedding" {
		args = append(args, "--embedding")
	}
	// Vision projector: only when a real (non-empty) mmproj exists. Text model
	// images bake a zero-byte placeholder, which is skipped. On Vulkan the
	// projector encode can be kept on the CPU (--no-mmproj-offload) to dodge
	// the known Venus VLM-encoder bug.
	if l.MMProj != "" {
		if fi, err := os.Stat(l.MMProj); err == nil && fi.Size() > 0 {
			args = append(args, "--mmproj", l.MMProj)
			if l.MMProjNoOffload {
				args = append(args, "--no-mmproj-offload")
			}
		}
	}
	args = append(args, l.tuningArgs()...)
	return append(args, l.Extra...)
}

// DraftReady reports whether SpecType draft has a draft model to load (a
// real, non-empty file: images without one bake an empty placeholder).
func (l Llama) DraftReady() bool {
	if l.DraftModel == "" {
		return false
	}
	fi, err := os.Stat(l.DraftModel)
	return err == nil && fi.Size() > 0
}

// tuningArgs are the flags of the tuning fields that are set. The flag names
// are llama.cpp v0.9.0's (common/arg.cpp): --draft-max/--draft-min were
// removed there in favour of --spec-draft-n-max and --spec-ngram-mod-n-max.
func (l Llama) tuningArgs() []string {
	var a []string
	itoa := strconv.Itoa
	ftoa := func(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }
	if l.FlashAttn != "" {
		a = append(a, "--flash-attn", l.FlashAttn)
	}
	if l.KVCacheType != "" {
		a = append(a, "--cache-type-k", l.KVCacheType, "--cache-type-v", l.KVCacheType)
	}
	if l.KVUnified && l.Parallel > 1 {
		a = append(a, "--kv-unified")
	}
	if l.Batch > 0 {
		a = append(a, "--batch-size", itoa(l.Batch))
	}
	if l.UBatch > 0 {
		a = append(a, "--ubatch-size", itoa(l.UBatch))
	}
	switch l.SpecType {
	case "ngram":
		a = append(a, "--spec-type", "ngram-mod")
		if l.DraftMax > 0 {
			a = append(a, "--spec-ngram-mod-n-max", itoa(l.DraftMax))
		}
		if l.DraftMin > 0 {
			a = append(a, "--spec-ngram-mod-n-min", itoa(l.DraftMin))
		}
	case "draft":
		if l.DraftReady() {
			a = append(a, "--spec-type", "draft-simple", "--spec-draft-model", l.DraftModel)
			if l.DraftMax > 0 {
				a = append(a, "--spec-draft-n-max", itoa(l.DraftMax))
			}
			if l.DraftMin > 0 {
				a = append(a, "--spec-draft-n-min", itoa(l.DraftMin))
			}
			// The draft goes where the main model goes.
			if g := strings.TrimSpace(l.GPULayers); g != "" && g != "0" {
				a = append(a, "--spec-draft-ngl", "all")
			}
		}
	}
	if l.Reasoning != "" {
		a = append(a, "--reasoning", l.Reasoning)
	}
	if l.ReasoningEffort != "" {
		a = append(a, "--reasoning-effort", l.ReasoningEffort)
	}
	if l.ReasoningBudget != nil {
		a = append(a, "--reasoning-budget", itoa(*l.ReasoningBudget))
	}
	if l.CacheRAMMB != nil {
		a = append(a, "--cache-ram", itoa(*l.CacheRAMMB))
	}
	if l.CtxCheckpoints != nil {
		a = append(a, "--ctx-checkpoints", itoa(*l.CtxCheckpoints))
	}
	if l.Temperature != nil {
		a = append(a, "--temp", ftoa(*l.Temperature))
	}
	if l.TopP != nil {
		a = append(a, "--top-p", ftoa(*l.TopP))
	}
	if l.TopK != nil {
		a = append(a, "--top-k", itoa(*l.TopK))
	}
	if l.MinP != nil {
		a = append(a, "--min-p", ftoa(*l.MinP))
	}
	if l.RepeatPenalty != nil {
		a = append(a, "--repeat-penalty", ftoa(*l.RepeatPenalty))
	}
	return a
}

// ApplyEnv sets the tuning fields from environment variables (the names
// internal/tuning writes): CTX_SIZE, PARALLEL and THREADS when set, plus
// FLASH_ATTN, KV_CACHE_TYPE, KV_UNIFIED, BATCH_SIZE, UBATCH_SIZE, SPEC_TYPE,
// DRAFT_MODEL_PATH, DRAFT_MAX, DRAFT_MIN, REASONING, REASONING_EFFORT,
// REASONING_BUDGET, CACHE_RAM_MB, CTX_CHECKPOINTS, TEMPERATURE, TOP_P,
// TOP_K, MIN_P and REPEAT_PENALTY. Empty variables are unset. A bad value is
// an error naming the variable, so a container stops with the reason
// instead of passing llama-server a flag it refuses.
func (l *Llama) ApplyEnv(getenv func(string) string) error {
	var errs []string
	get := func(k string) string { return strings.TrimSpace(getenv(k)) }
	num := func(k string, lo, hi int) (int, bool) {
		v := get(k)
		if v == "" {
			return 0, false
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < lo || n > hi {
			errs = append(errs, fmt.Sprintf("%s=%q isn't a whole number from %d to %d", k, v, lo, hi))
			return 0, false
		}
		return n, true
	}
	flt := func(k string, lo, hi float64) *float64 {
		v := get(k)
		if v == "" {
			return nil
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(f) || f < lo || f > hi {
			errs = append(errs, fmt.Sprintf("%s=%q isn't a number from %g to %g", k, v, lo, hi))
			return nil
		}
		return &f
	}
	enum := func(k string, allowed ...string) string {
		v := strings.ToLower(get(k))
		if v == "" {
			return ""
		}
		for _, a := range allowed {
			if v == a {
				return v
			}
		}
		errs = append(errs, fmt.Sprintf("%s=%q isn't one of %s", k, v, strings.Join(allowed, ", ")))
		return ""
	}
	if n, ok := num("CTX_SIZE", 0, 1<<24); ok {
		l.CtxSize = n
	}
	if n, ok := num("PARALLEL", 1, 256); ok {
		l.Parallel = n
	}
	if n, ok := num("THREADS", 1, 4096); ok {
		l.Threads = n
	}
	l.FlashAttn = enum("FLASH_ATTN", "auto", "on", "off")
	l.KVCacheType = enum("KV_CACHE_TYPE", "f16", "bf16", "f32", "q8_0", "q5_1", "q5_0", "q4_1", "q4_0", "iq4_nl")
	switch enum("KV_UNIFIED", "1", "0", "true", "false", "on", "off") {
	case "1", "true", "on":
		l.KVUnified = true
	case "0", "false", "off":
		l.KVUnified = false
	}
	if n, ok := num("BATCH_SIZE", 1, 1<<20); ok {
		l.Batch = n
	}
	if n, ok := num("UBATCH_SIZE", 1, 1<<20); ok {
		l.UBatch = n
	}
	l.SpecType = enum("SPEC_TYPE", "none", "ngram", "draft")
	if v := get("DRAFT_MODEL_PATH"); v != "" {
		l.DraftModel = v
	}
	if n, ok := num("DRAFT_MAX", 0, 1024); ok {
		l.DraftMax = n
	}
	if n, ok := num("DRAFT_MIN", 0, 1024); ok {
		l.DraftMin = n
	}
	l.Reasoning = enum("REASONING", "auto", "on", "off")
	l.ReasoningEffort = enum("REASONING_EFFORT", "low", "medium", "high")
	if n, ok := num("REASONING_BUDGET", -1, 1<<24); ok {
		l.ReasoningBudget = &n
	}
	if n, ok := num("CACHE_RAM_MB", -1, 1<<24); ok {
		l.CacheRAMMB = &n
	}
	if n, ok := num("CTX_CHECKPOINTS", 0, 4096); ok {
		l.CtxCheckpoints = &n
	}
	l.Temperature = flt("TEMPERATURE", 0, 100)
	l.TopP = flt("TOP_P", 0, 1)
	if n, ok := num("TOP_K", 0, 1<<20); ok {
		l.TopK = &n
	}
	l.MinP = flt("MIN_P", 0, 1)
	l.RepeatPenalty = flt("REPEAT_PENALTY", 0, 10)
	if len(errs) > 0 {
		return fmt.Errorf("tuning: %s", strings.Join(errs, "; "))
	}
	return nil
}
