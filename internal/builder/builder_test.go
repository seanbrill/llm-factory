package builder

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/seanbrill/local-llm/internal/catalog"
)

func TestResolveCompute(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "docker")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{
		"Dockerfile.cpu", "Dockerfile.cuda", "Dockerfile.vulkan",
		"Dockerfile.tts.cpu", "Dockerfile.python.cpu", "Dockerfile.sd.vulkan",
	} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b := &Builder{BaseDir: base}
	text := catalog.Model{Name: "Qwen"}
	tts := catalog.Model{Name: "Piper", Modality: "tts"}
	kokoro := catalog.Model{Name: "Kokoro", Modality: "tts", Runtime: "python"}
	image := catalog.Model{Name: "SDXL", Modality: "image"}
	stt := catalog.Model{Name: "Whisper", Modality: "audio-stt"} // no Dockerfile at all here

	if got, want := b.Computes(text), []string{"cpu", "cuda", "vulkan"}; !slices.Equal(got, want) {
		t.Errorf("Computes(text) = %v, want %v", got, want)
	}
	if got, want := b.Computes(tts), []string{"cpu"}; !slices.Equal(got, want) {
		t.Errorf("Computes(tts) = %v, want %v", got, want)
	}

	cases := []struct {
		m       catalog.Model
		compute string
		want    string // "" => error
	}{
		{text, "vulkan", "vulkan"},
		{tts, "cpu", "cpu"},
		{tts, "vulkan", "cpu"}, // CPU-only runtime: GPU request falls back
		{kokoro, "cuda", "cpu"},
		{image, "vulkan", "vulkan"},
		{image, "cpu", ""},    // no CPU variant to fall back to
		{stt, "cuda", "cuda"}, // nothing on disk: passed through for the read to report
	}
	for _, c := range cases {
		got, err := b.resolveCompute(c.m, c.compute)
		if (err != nil) != (c.want == "") || got != c.want {
			t.Errorf("resolveCompute(%s, %q) = %q, %v; want %q", c.m.Name, c.compute, got, err, c.want)
		}
	}
}

func TestLabelValue(t *testing.T) {
	labels := "local-llm.tool=runtime,local-llm.route=ai,local-llm.ref=local-llm/qwen:latest"
	cases := map[string]string{
		"local-llm.tool":  "runtime",
		"local-llm.route": "ai",
		"local-llm.ref":   "local-llm/qwen:latest",
		"missing":         "",
	}
	for key, want := range cases {
		if got := labelValue(labels, key); got != want {
			t.Errorf("labelValue(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestResolveEngine(t *testing.T) {
	cases := map[string]string{
		"":        "docker", // default preserves original behaviour
		"docker":  "docker",
		"podman":  "podman",
		"Podman":  "docker", // only exact "podman" switches; anything else => docker
		"garbage": "docker",
	}
	for in, want := range cases {
		if got := resolveEngine(in); got != want {
			t.Errorf("resolveEngine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPortFromName(t *testing.T) {
	cases := map[string]string{
		"localllm-8080": "8080",
		"localllm-5001": "5001",
		"noport":        "",
	}
	for name, want := range cases {
		if got := portFromName(name); got != want {
			t.Errorf("portFromName(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestSdServerArgs(t *testing.T) {
	got, err := sdServerArgs(nil)
	if err != nil || !slices.Equal(got, []string{"-m", "/models/model.gguf"}) {
		t.Errorf("single-file checkpoint: got %v, %v", got, err)
	}
	got, err = sdServerArgs([]StagedWeight{{File: "flux1-ae.safetensors", Role: "vae"}, {File: "Qwen3-4B-Q4_K_M.gguf", Role: "llm"}})
	want := []string{"--diffusion-model", "/models/model.gguf", "--vae", "/models/extra/flux1-ae.safetensors", "--llm", "/models/extra/Qwen3-4B-Q4_K_M.gguf"}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("split model: got %v, %v; want %v", got, err, want)
	}
	if _, err := sdServerArgs([]StagedWeight{{File: "x.gguf", Role: "high_noise"}}); err == nil {
		t.Error("a video-only role should be rejected for an image model")
	}
}

func TestAudioCppServerConfig(t *testing.T) {
	m := catalog.Model{ID: "pocket", Modality: "tts", Runtime: "audiocpp", AudioCpp: &catalog.AudioCppConfig{
		Family: "pocket_tts", LoadOptions: map[string]string{"language": "english"}, DefaultVoice: "alba",
	}}
	if got := modelFamily(m); got != "audiocpp" {
		t.Fatalf("modelFamily = %q, want audiocpp", got)
	}
	raw, err := audioCppServerConfig(m)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Port    int    `json:"port"`
		Backend string `json:"backend"`
		Models  []struct {
			ID, Family, Path, Task string
			LoadOptions            map[string]string            `json:"load_options"`
			Default                map[string]string            `json:"default_voice_preset"`
			Presets                map[string]map[string]string `json:"voice_presets"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 8080 || cfg.Backend != "cpu" || len(cfg.Models) != 1 {
		t.Fatalf("unexpected server config: %s", raw)
	}
	got := cfg.Models[0]
	if got.ID != "tts" || got.Family != "pocket_tts" || got.Path != "/models/model.gguf" || got.Task != "tts" ||
		got.LoadOptions["language"] != "english" || got.Default["voice_id"] != "alba" || got.Presets["default"]["voice_id"] != "alba" {
		t.Errorf("unexpected model entry: %s", raw)
	}
	if _, err := audioCppServerConfig(catalog.Model{ID: "x", Modality: "tts", Runtime: "audiocpp"}); err == nil {
		t.Error("a model without audiocpp.family should be rejected")
	}
}
