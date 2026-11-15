package main

import (
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
