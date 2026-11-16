package gate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// Without tuning the command line is what it always was.
func TestLlamaArgsNoTuning(t *testing.T) {
	l := Llama{Model: "/m.gguf", Port: 8081, CtxSize: 4096, Parallel: 1}
	if err := l.ApplyEnv(envOf(nil)); err != nil {
		t.Fatal(err)
	}
	want := "--model /m.gguf --host 127.0.0.1 --port 8081 --ctx-size 4096 --parallel 1 --cont-batching --jinja --metrics"
	if got := strings.Join(l.Args(), " "); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// Every tuning variable maps to llama.cpp v0.9.0's flag.
func TestLlamaArgsTuningFromEnv(t *testing.T) {
	draft := filepath.Join(t.TempDir(), "draft.gguf")
	if err := os.WriteFile(draft, []byte("GGUF"), 0o644); err != nil {
		t.Fatal(err)
	}
	l := Llama{Model: "/m.gguf", Port: 8081, GPULayers: "99"}
	err := l.ApplyEnv(envOf(map[string]string{
		"CTX_SIZE": "32768", "PARALLEL": "2", "THREADS": "6",
		"FLASH_ATTN": "on", "KV_CACHE_TYPE": "q8_0", "KV_UNIFIED": "1",
		"BATCH_SIZE": "2048", "UBATCH_SIZE": "512",
		"SPEC_TYPE": "draft", "DRAFT_MODEL_PATH": draft, "DRAFT_MAX": "8", "DRAFT_MIN": "2",
		"REASONING": "off", "REASONING_EFFORT": "low", "REASONING_BUDGET": "0",
		"CACHE_RAM_MB": "1024", "CTX_CHECKPOINTS": "4",
		"TEMPERATURE": "0.7", "TOP_P": "0.95", "TOP_K": "40", "MIN_P": "0.05", "REPEAT_PENALTY": "1.1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(l.Args(), " ")
	for _, want := range []string{
		"--ctx-size 32768", "--threads 6 --threads-batch 6", "--parallel 2",
		"--n-gpu-layers 99",
		"--flash-attn on", "--cache-type-k q8_0 --cache-type-v q8_0", "--kv-unified",
		"--batch-size 2048", "--ubatch-size 512",
		"--spec-type draft-simple --spec-draft-model " + draft + " --spec-draft-n-max 8 --spec-draft-n-min 2 --spec-draft-ngl all",
		"--reasoning off", "--reasoning-effort low", "--reasoning-budget 0",
		"--cache-ram 1024", "--ctx-checkpoints 4",
		"--temp 0.7", "--top-p 0.95", "--top-k 40", "--min-p 0.05", "--repeat-penalty 1.1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	// The removed v0.8 flags must never come back: llama-server refuses them.
	for _, gone := range []string{"--draft-max", "--draft-min", " -md "} {
		if strings.Contains(got, gone) {
			t.Errorf("%q is removed in llama.cpp v0.9.0: %s", gone, got)
		}
	}
}

func TestLlamaArgsNgramAndMissingDraft(t *testing.T) {
	l := Llama{Model: "/m.gguf", Port: 1, SpecType: "ngram", DraftMax: 32}
	if got := strings.Join(l.Args(), " "); !strings.Contains(got, "--spec-type ngram-mod --spec-ngram-mod-n-max 32") {
		t.Errorf("ngram: %s", got)
	}
	// An empty placeholder (or no file) means no draft: no spec flags.
	empty := filepath.Join(t.TempDir(), "draft.gguf")
	_ = os.WriteFile(empty, nil, 0o644)
	l = Llama{Model: "/m.gguf", Port: 1, SpecType: "draft", DraftModel: empty}
	if got := strings.Join(l.Args(), " "); strings.Contains(got, "--spec") {
		t.Errorf("empty draft still speculates: %s", got)
	}
	// One slot never gets --kv-unified.
	l = Llama{Model: "/m.gguf", Port: 1, Parallel: 1, KVUnified: true}
	if got := strings.Join(l.Args(), " "); strings.Contains(got, "--kv-unified") {
		t.Errorf("kv-unified with one slot: %s", got)
	}
}

func TestApplyEnvRejectsBadValues(t *testing.T) {
	for k, v := range map[string]string{
		"FLASH_ATTN": "maybe", "KV_CACHE_TYPE": "q3_k", "SPEC_TYPE": "eagle", "TOP_P": "2",
		"CACHE_RAM_MB": "-5", "DRAFT_MAX": "x", "REASONING": "--rm", "PARALLEL": "0",
	} {
		var l Llama
		err := l.ApplyEnv(envOf(map[string]string{k: v}))
		if err == nil || !strings.Contains(err.Error(), k) {
			t.Errorf("%s=%s: %v", k, v, err)
		}
	}
}
