package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The container's llama-server command line, from the image's environment:
// the same flags llmgate passed before the proxy moved to internal/gate.
func TestLlamaArgsFromEnvConfig(t *testing.T) {
	c := config{modelPath: "/models/model.gguf", upstreamPort: 8081, ctxSize: 4096, threads: 6, parallel: 1,
		modality: "text", extraArgs: "  --flash-attn on "}
	want := "--model /models/model.gguf --host 127.0.0.1 --port 8081 --ctx-size 4096 --threads 6 --threads-batch 6 --parallel 1 --cont-batching --jinja --metrics --flash-attn on"
	if got := strings.Join(c.llamaArgs(), " "); got != want {
		t.Errorf("CPU image:\n got %s\nwant %s", got, want)
	}
	c.ngl, c.modality, c.extraArgs = 999, "embedding", ""
	want = "--model /models/model.gguf --host 127.0.0.1 --port 8081 --ctx-size 4096 --threads 6 --threads-batch 6 --parallel 1 --cont-batching --jinja --metrics --n-gpu-layers 999 --embedding"
	if got := strings.Join(c.llamaArgs(), " "); got != want {
		t.Errorf("GPU embedding image:\n got %s\nwant %s", got, want)
	}
}

// The image's tuning variables reach llama-server's command line.
func TestLlamaArgsWithTuningEnv(t *testing.T) {
	c := config{modelPath: "/models/model.gguf", upstreamPort: 8081, ctxSize: 32768, threads: 4, parallel: 2, modality: "text"}
	env := map[string]string{"FLASH_ATTN": "on", "KV_CACHE_TYPE": "q8_0", "KV_UNIFIED": "1", "SPEC_TYPE": "ngram", "CACHE_RAM_MB": "1024"}
	if err := c.tune.ApplyEnv(func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	want := "--model /models/model.gguf --host 127.0.0.1 --port 8081 --ctx-size 32768 --threads 4 --threads-batch 4 --parallel 2 --cont-batching --jinja --metrics --flash-attn on --cache-type-k q8_0 --cache-type-v q8_0 --kv-unified --spec-type ngram-mod --cache-ram 1024"
	if got := strings.Join(c.llamaArgs(), " "); got != want {
		t.Errorf("\n got %s\nwant %s", got, want)
	}
}

func TestHarnessContextWithSharedCache(t *testing.T) {
	for _, c := range []struct {
		ctx, parallel int
		unified       bool
		want          int
	}{{32768, 1, false, 32768}, {65536, 2, false, 32768}, {65536, 2, true, 65536}, {8192, 0, false, 8192}} {
		if got := harnessContext(c.ctx, c.parallel, c.unified); got != c.want {
			t.Errorf("harnessContext(%d, %d, %v) = %d, want %d", c.ctx, c.parallel, c.unified, got, c.want)
		}
	}
}

func TestCheckModel(t *testing.T) {
	dir := t.TempDir()
	empty, model := filepath.Join(dir, "empty.gguf"), filepath.Join(dir, "m.gguf")
	os.WriteFile(empty, nil, 0o644)
	os.WriteFile(model, []byte("GGUF"), 0o644)
	if err := checkModel(model); err != nil {
		t.Errorf("a model file: %v", err)
	}
	for _, p := range []string{filepath.Join(dir, "missing.gguf"), dir, empty} {
		if checkModel(p) == nil {
			t.Errorf("%s should fail", p)
		}
	}
}
