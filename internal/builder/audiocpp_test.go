package builder

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/seanbrill/local-llm/internal/catalog"
)

// audioCppConfig is the part of audio.cpp's server config the factory writes.
type audioCppConfig struct {
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Backend string `json:"backend"`
	UI      bool   `json:"ui"`
	Models  []struct {
		ID       string                       `json:"id"`
		Family   string                       `json:"family"`
		Path     string                       `json:"path"`
		Task     string                       `json:"task"`
		Mode     string                       `json:"mode"`
		Load     map[string]string            `json:"load_options"`
		Defaults map[string]any               `json:"default_request_options"`
		Default  map[string]string            `json:"default_voice_preset"`
		Presets  map[string]map[string]string `json:"voice_presets"`
	} `json:"models"`
}

func parseAudioCppConfig(t *testing.T, m catalog.Model) audioCppConfig {
	t.Helper()
	raw, err := audioCppServerConfig(m)
	if err != nil {
		t.Fatalf("%s: %v", m.ID, err)
	}
	var cfg audioCppConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("%s: %v\n%s", m.ID, err, raw)
	}
	if len(cfg.Models) != 1 {
		t.Fatalf("%s: want one model entry: %s", m.ID, raw)
	}
	return cfg
}

func TestAudioCppServerConfigModes(t *testing.T) {
	pocket := catalog.Model{ID: "pocket", Modality: "tts", Runtime: "audiocpp", AudioCpp: &catalog.AudioCppConfig{
		Family: "pocket_tts", DefaultVoice: "alba", Streaming: true,
	}}
	cfg := parseAudioCppConfig(t, pocket)
	if got := cfg.Models[0]; got.Mode != "streaming" || got.ID != AudioCppTTSModelID || got.Task != "tts" {
		t.Errorf("streaming tts entry: %+v", got)
	}
	if cfg.Host != "0.0.0.0" || cfg.Port != 8080 || cfg.Backend != "cpu" || cfg.UI {
		t.Errorf("server settings: %+v", cfg)
	}

	xtts := catalog.Model{ID: "xtts", Modality: "tts", Runtime: "audiocpp", AudioCpp: &catalog.AudioCppConfig{Family: "xtts_v2"}}
	got := parseAudioCppConfig(t, xtts).Models[0]
	if got.Mode != "offline" {
		t.Errorf("a model that doesn't stream stays offline, got %q", got.Mode)
	}
	if got.Default != nil || got.Presets != nil || got.Defaults != nil {
		t.Errorf("no default voice or options were asked for: %+v", got)
	}

	// VoxCPM2 streams only with retry_badcase off; the option reaches the
	// config as a JSON bool.
	vox := catalog.Model{ID: "vox", Modality: "tts", Runtime: "audiocpp", AudioCpp: &catalog.AudioCppConfig{
		Family: "voxcpm2", Streaming: true, RequestOptions: map[string]any{"retry_badcase": false},
	}}
	got = parseAudioCppConfig(t, vox).Models[0]
	if got.Mode != "streaming" || got.Defaults["retry_badcase"] != false {
		t.Errorf("voxcpm2 entry: %+v", got)
	}

	stt := catalog.Model{ID: "moon", Modality: "audio-stt", Runtime: "audiocpp", AudioCpp: &catalog.AudioCppConfig{
		Family: "moonshine_asr", Streaming: true,
	}}
	got = parseAudioCppConfig(t, stt).Models[0]
	if got.ID != AudioCppSTTModelID || got.Task != "asr" || got.Mode != "streaming" || got.Family != "moonshine_asr" {
		t.Errorf("speech-to-text entry: %+v", got)
	}

	bad := []catalog.Model{
		{ID: "stt-voice", Modality: "audio-stt", Runtime: "audiocpp", AudioCpp: &catalog.AudioCppConfig{Family: "moonshine_asr", DefaultVoice: "alba"}},
		{ID: "image", Modality: "image", Runtime: "audiocpp", AudioCpp: &catalog.AudioCppConfig{Family: "stable_audio"}},
		{ID: "nofamily", Modality: "tts", Runtime: "audiocpp", AudioCpp: &catalog.AudioCppConfig{}},
	}
	for _, m := range bad {
		if _, err := audioCppServerConfig(m); err == nil {
			t.Errorf("%s: expected an error", m.ID)
		}
	}
}

// Every audio.cpp model in the shipped catalog gets a config the server will
// accept: its family, the task for its modality, and streaming mode for the
// families that stream (and only those).
func TestAudioCppCatalogConfigs(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "catalog", "models.default.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cat catalog.Catalog
	if err := json.Unmarshal(data, &cat); err != nil {
		t.Fatal(err)
	}
	// audio.cpp's model specs (model_specs/<family>.json at the pinned image's
	// commit) list these modes; XTTS v2 is offline only.
	streams := map[string]bool{
		"pocket_tts": true, "pocket_tts_small": true, "qwen3_tts": true, "voxcpm2": true,
		"ming3_tts": true, "moonshine_asr": true, "xtts_v2": false,
	}
	families := map[string]bool{}
	for _, m := range cat.Models {
		if m.Rt() != "audiocpp" {
			continue
		}
		if got := modelFamily(m); got != "audiocpp" {
			t.Errorf("%s: modelFamily = %q", m.ID, got)
		}
		cfg := parseAudioCppConfig(t, m)
		e := cfg.Models[0]
		families[e.Family] = true
		wantTask := map[string]string{"tts": "tts", "audio-stt": "asr"}[m.Mod()]
		if e.Task != wantTask || e.Family != m.AudioCpp.Family || e.Path != "/models/model.gguf" {
			t.Errorf("%s: entry %+v", m.ID, e)
		}
		canStream, known := streams[e.Family]
		if !known {
			t.Errorf("%s: family %q: add its streaming support to this test from audio.cpp's model spec", m.ID, e.Family)
		} else if (e.Mode == "streaming") != canStream {
			t.Errorf("%s: mode %q, but %s streaming support is %v", m.ID, e.Mode, e.Family, canStream)
		}
		if m.AudioCpp.DefaultVoice != "" && (e.Default["voice_id"] != m.AudioCpp.DefaultVoice || e.Presets["default"]["voice_id"] != m.AudioCpp.DefaultVoice) {
			t.Errorf("%s: default voice not wired: %+v", m.ID, e)
		}
		for _, w := range m.ExtraFiles {
			if _, err := audioCppExtraPath(StagedWeight{File: w.File, Role: w.Role, As: w.As}); err != nil {
				t.Errorf("%s: %v", m.ID, err)
			}
		}
	}
	for _, f := range []string{"pocket_tts", "pocket_tts_small", "qwen3_tts", "voxcpm2", "ming3_tts", "xtts_v2", "moonshine_asr"} {
		if !families[f] {
			t.Errorf("no catalog model uses audio.cpp family %s", f)
		}
	}
}

func TestAudioCppExtraPath(t *testing.T) {
	cases := []struct {
		w    StagedWeight
		want string // "" => error
	}{
		{StagedWeight{File: "pocket-tts-en-alba.safetensors", Role: "voice", As: "alba.safetensors"}, "embeddings/alba.safetensors"},
		{StagedWeight{File: "xtts-v2-q8_0-vocoder-f16.gguf", Role: "vocoder", As: "xtts-v2-vocoder-f16.gguf"}, "xtts-v2-vocoder-f16.gguf"},
		{StagedWeight{File: "mel_norms.bin", Role: "mel_norms"}, "mel_norms.bin"},
		{StagedWeight{File: "x.gguf", Role: "vocoder", As: "model.gguf"}, ""},
		{StagedWeight{File: "x.json", Role: "config", As: "audiocpp.json"}, ""},
		{StagedWeight{File: "x", Role: "voice", As: "../evil"}, ""},
		{StagedWeight{File: "x", Role: "voice", As: `a\b`}, ""},
		{StagedWeight{File: "x", Role: "vocoder", As: "embeddings"}, ""},
	}
	for _, c := range cases {
		got, err := audioCppExtraPath(c.w)
		if (err != nil) != (c.want == "") || got != c.want {
			t.Errorf("audioCppExtraPath(%+v) = %q, %v; want %q", c.w, got, err, c.want)
		}
	}
}

// Staging lays the build context out the way the Dockerfile copies it: voices
// under extra/embeddings, sidecars at extra/, both beside model.gguf in /models.
func TestStageAudioCppExtras(t *testing.T) {
	models := t.TempDir()
	work := t.TempDir()
	for name, body := range map[string]string{
		"pocket-tts-en-alba.safetensors": "voice",
		"xtts-v2-q8_0-vocoder-f16.gguf":  "vocoder",
	} {
		if err := os.WriteFile(filepath.Join(models, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b := &Builder{ModelsDir: models}
	m := catalog.Model{ID: "m", ExtraFiles: []catalog.WeightFile{
		{File: "pocket-tts-en-alba.safetensors", URL: "https://example.invalid/a", Role: "voice", As: "alba.safetensors"},
		{File: "xtts-v2-q8_0-vocoder-f16.gguf", URL: "https://example.invalid/v", Role: "vocoder", As: "xtts-v2-vocoder-f16.gguf"},
	}}
	if err := b.stageAudioCppExtras(context.Background(), m, work, func(string) {}); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{
		"extra/embeddings/alba.safetensors": "voice",
		"extra/xtts-v2-vocoder-f16.gguf":    "vocoder",
		"extra/.keep":                       "",
	} {
		got, err := os.ReadFile(filepath.Join(work, filepath.FromSlash(rel)))
		if err != nil || string(got) != want {
			t.Errorf("%s: %q, %v; want %q", rel, got, err, want)
		}
	}

	// No extras: extra/ still exists for the Dockerfile's COPY.
	empty := t.TempDir()
	if err := b.stageAudioCppExtras(context.Background(), catalog.Model{ID: "e"}, empty, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(empty, "extra", ".keep")); err != nil {
		t.Errorf("extra/.keep missing: %v", err)
	}
}

// The audio.cpp image is pinned to a dated, commit-tagged nightly (never a
// floating tag) and copies what the builder stages.
func TestAudioCppDockerfile(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docker", "Dockerfile.audiocpp.cpu"))
	if err != nil {
		t.Fatal(err)
	}
	code := logical(string(data))
	from := regexp.MustCompile(`(?m)^FROM ghcr\.io/0xshug0/audio\.cpp:full-cpu-(\d{8})-([0-9a-f]{7})\s*$`)
	if !from.MatchString(code) {
		t.Errorf("Dockerfile.audiocpp.cpu must build FROM a pinned full-cpu-<date>-<sha> tag:\n%s", code)
	}
	if compilesFromSource(data) {
		t.Error("the audio.cpp image reuses the published runtime; it shouldn't compile")
	}
	for _, line := range []string{
		"COPY model.gguf /models/model.gguf",
		"COPY extra/ /models/",
		"COPY audiocpp.json /models/audiocpp.json",
		`CMD ["server", "--config", "/models/audiocpp.json"]`,
	} {
		if !strings.Contains(code, line+"\n") {
			t.Errorf("Dockerfile.audiocpp.cpu lacks %q", line)
		}
	}
}
