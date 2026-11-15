package harness

import (
	"strings"
	"testing"
)

func row(name, state, image string, labels ...string) map[string]any {
	return map[string]any{
		"Names":  name,
		"State":  state,
		"Image":  image,
		"Labels": strings.Join(labels, ","),
		"Engine": "docker",
		"Ports":  "",
	}
}

func TestModelsFromContainers(t *testing.T) {
	rows := []map[string]any{
		row("localllm-8080", "running", "local-llm/qwen3.5-9b:latest",
			"local-llm.model=qwen3.5-9b-instruct", "local-llm.modality=text", "local-llm.ref=local-llm/qwen3.5-9b:latest"),
		// The same image twice: ids must be disambiguated.
		row("localllm-8081", "running", "local-llm/qwen3.5-9b:latest",
			"local-llm.model=qwen3.5-9b-instruct", "local-llm.modality=text", "local-llm.ref=local-llm/qwen3.5-9b:latest", "local-llm.persona=Pirate"),
		// A tagged image, old (no modality label: falls back to the catalog).
		row("localllm-8082", "running", "myorg/coder:q4", "local-llm.model=qwen-coder", "local-llm.ref=myorg/coder:q4"),
		// Embeddings are exposed (for /v1/embeddings).
		row("localllm-8083", "running", "local-llm/nomic-embed:latest", "local-llm.model=nomic", "local-llm.modality=embedding"),
		// Not exposed: stopped, image generation, python runtime, ensemble specialist.
		row("localllm-8090", "exited", "local-llm/stopped:latest", "local-llm.model=x", "local-llm.modality=text"),
		row("localllm-8091", "running", "local-llm/sdxl:latest", "local-llm.model=sdxl", "local-llm.modality=image"),
		row("localllm-8092", "running", "local-llm/kokoro:latest", "local-llm.model=k", "local-llm.modality=text", "local-llm.runtime=python"),
		row("ens-vision", "running", "local-llm/qwen-vl:latest", "local-llm.model=qwen-vl", "local-llm.modality=vision"),
		// Podman (already normalized), port from the mapping, untagged image id.
		{
			"Names": "custom-name", "State": "running", "Image": "3f2a9c1b7d4e",
			"Labels": "local-llm.model=gemma-4b,local-llm.modality=vision",
			"Engine": "podman", "Ports": "127.0.0.1:8095->8080/tcp", "Created": float64(1700000000),
		},
		// No reachable port: skipped.
		row("weird", "running", "local-llm/noport:latest", "local-llm.model=np", "local-llm.modality=text"),
	}
	modalityOf := func(id string) string {
		if id == "qwen-coder" {
			return "code"
		}
		return ""
	}
	got := ModelsFromContainers(rows, modalityOf)
	type want struct {
		id, name, modality string
		port               int
	}
	exp := []want{
		{"coder:q4", "coder:q4", "code", 8082},
		{"gemma-4b", "gemma-4b", "vision", 8095},
		{"nomic-embed", "nomic-embed (embeddings)", "embedding", 8083},
		{"qwen3.5-9b@8080", "qwen3.5-9b (port 8080)", "text", 8080},
		{"qwen3.5-9b@8081", "qwen3.5-9b (Pirate, port 8081)", "text", 8081},
	}
	if len(got) != len(exp) {
		t.Fatalf("got %d models: %+v", len(got), got)
	}
	for i, e := range exp {
		g := got[i]
		if g.ID != e.id || g.Name != e.name || g.Modality != e.modality || g.Port != e.port {
			t.Errorf("model %d = %+v, want %+v", i, g, e)
		}
	}
	if got[1].Engine != "podman" || got[1].Created != 1700000000 {
		t.Errorf("podman row: %+v", got[1])
	}
}

func TestResolveModel(t *testing.T) {
	models := ModelsFromContainers([]map[string]any{
		row("localllm-8080", "running", "local-llm/qwen3.5-9b:latest", "local-llm.model=q", "local-llm.modality=text", "local-llm.ref=local-llm/qwen3.5-9b:latest"),
		row("localllm-8081", "running", "local-llm/qwen3.5-9b:latest", "local-llm.model=q", "local-llm.modality=text", "local-llm.ref=local-llm/qwen3.5-9b:latest"),
		row("localllm-8082", "running", "local-llm/phi-4:latest", "local-llm.model=p", "local-llm.modality=text", "local-llm.ref=local-llm/phi-4:latest"),
	}, nil)
	cases := map[string]int{
		"qwen3.5-9b@8081":             8081,
		"qwen3.5-9b@8080":             8080,
		"qwen3.5-9b":                  8080, // ambiguous base id -> lowest port
		"QWEN3.5-9B":                  8080,
		"phi-4":                       8082,
		"localllm-8082":               8082, // container name alias
		"local-llm/phi-4:latest":      8082, // image ref alias
		" phi-4 ":                     8082,
		"local-llm/qwen3.5-9b:latest": 8080,
	}
	for name, port := range cases {
		m, ok := ResolveModel(models, name)
		if !ok || m.Port != port {
			t.Errorf("ResolveModel(%q) = %v %v, want port %d", name, m.Port, ok, port)
		}
	}
	for _, name := range []string{"", "nope", "qwen3.5-9b@9999"} {
		if _, ok := ResolveModel(models, name); ok {
			t.Errorf("ResolveModel(%q) matched", name)
		}
	}
	one := models[:1] // sorted by id: phi-4 first
	if m, ok := ResolveModel(one, ""); !ok || m.ID != "phi-4" {
		t.Error("an empty model name should route to the only model")
	}
}

func TestBaseID(t *testing.T) {
	cases := []struct{ ref, model, container, want string }{
		{"local-llm/qwen3.5-9b:latest", "", "", "qwen3.5-9b"},
		{"registry.local:5000/team/Llama-3.1-8B:Q4_K_M", "", "", "llama-3.1-8b:q4_k_m"},
		{"phi-4", "", "", "phi-4"},
		{"local-llm/x@sha256:abc", "", "", "x"},
		{"sha256:0123456789abcdef0123", "My Model", "", "my-model"},
		{"0123456789ab", "", "/localllm-8080", "localllm-8080"},
	}
	for _, c := range cases {
		if got := baseID(c.ref, c.model, c.container); got != c.want {
			t.Errorf("baseID(%q, %q, %q) = %q, want %q", c.ref, c.model, c.container, got, c.want)
		}
	}
}
