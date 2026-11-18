package builder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/seanbrill/local-llm/internal/catalog"
	"github.com/seanbrill/local-llm/internal/ensemble"
)

func TestMountMembers(t *testing.T) {
	dir := t.TempDir()
	b := &Builder{BaseDir: filepath.Join("..", ".."), ModelsDir: dir, ConfigDir: dir}
	weights := []byte("GGUF weights")
	os.WriteFile(filepath.Join(dir, "qwen.gguf"), weights, 0o644)
	os.WriteFile(filepath.Join(dir, "gemma.gguf"), []byte("GGUF gemma"), 0o644)
	os.WriteFile(filepath.Join(dir, "mmproj-gemma.gguf"), []byte("GGUF mmproj"), 0o644)
	models := map[string]catalog.Model{
		"qwen":    {ID: "qwen", File: "qwen.gguf", URL: "https://hf/qwen.gguf", Modality: "code"},
		"gemma":   {ID: "gemma", File: "gemma.gguf", Modality: "vision", MMProjFile: "mmproj-gemma.gguf", MMProjURL: "https://hf/mmproj"},
		"whisper": {ID: "whisper", File: "w.bin", Modality: "audio-stt"},
	}
	lookup := func(id string) (catalog.Model, bool) { m, ok := models[id]; return m, ok }
	e := ensemble.Ensemble{Name: "Fred", Weights: ensemble.WeightsMounted, Compute: "vulkan", Members: []ensemble.Member{
		{Tool: ensemble.ToolChat, Image: "local-llm/qwen:latest", Model: "qwen"},
		{Tool: ensemble.ToolSeeImage, Image: "local-llm/gemma:latest", Model: "gemma", Compute: "cpu"},
		{Tool: ensemble.ToolTranscribe, Image: "local-llm/whisper:latest", Model: "whisper"},
	}}
	if err := b.MountMembers(context.Background(), &e, lookup, func(s string) { t.Log(s) }); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(weights)
	chat, vision, stt := e.Members[0], e.Members[1], e.Members[2]
	if chat.Weights == nil || chat.Weights.File != "qwen.gguf" || chat.Weights.SHA256 != hex.EncodeToString(sum[:]) ||
		chat.Weights.Size != int64(len(weights)) || chat.Weights.URL != "https://hf/qwen.gguf" || chat.MMProj != nil {
		t.Errorf("chat weights = %+v %+v", chat.Weights, chat.MMProj)
	}
	if !strings.HasPrefix(chat.Image, "local-llm/runtime:vulkan-") || !strings.HasSuffix(chat.Image, "-"+goruntime.GOARCH) || chat.Compute != "vulkan" {
		t.Errorf("chat runs %s (%s)", chat.Image, chat.Compute)
	}
	if !strings.HasPrefix(vision.Image, "local-llm/runtime:cpu-") || vision.MMProj == nil || vision.MMProj.File != "mmproj-gemma.gguf" || vision.MMProj.SHA256 == "" {
		t.Errorf("vision = %s %+v", vision.Image, vision.MMProj)
	}
	if stt.Weights != nil || stt.Image != "local-llm/whisper:latest" {
		t.Errorf("a whisper member keeps its image: %+v", stt)
	}
	if _, err := os.Stat(filepath.Join(dir, "weights-sha256.json")); err != nil {
		t.Errorf("the hashes should be remembered: %v", err)
	}

	// Published by registry address.
	e.Registry = "ghcr.io/filefreak/"
	mani, err := EnsembleManifest(e)
	if err != nil {
		t.Fatal(err)
	}
	if img := mani.Members[0].Image; !strings.HasPrefix(img, "ghcr.io/filefreak/runtime:vulkan-") {
		t.Errorf("registry image = %s", img)
	}
	if img := mani.Members[2].Image; img != "ghcr.io/filefreak/whisper:latest" {
		t.Errorf("registry image = %s", img)
	}
}

func TestOrchestratorArgs(t *testing.T) {
	if got := strings.Join(orchestratorArgs(engineDocker, ""), " "); got != "-v /var/run/docker.sock:/var/run/docker.sock --add-host host.docker.internal:host-gateway" {
		t.Errorf("docker: %s", got)
	}
	got := strings.Join(orchestratorArgs(enginePodman, "/run/user/501/podman/podman.sock"), " ")
	for _, want := range []string{"-v /run/user/501/podman/podman.sock:/var/run/docker.sock", "label=disable", "MODEL_HOST=host.containers.internal"} {
		if !strings.Contains(got, want) {
			t.Errorf("podman: missing %q in %s", want, got)
		}
	}
	if got := strings.Join(orchestratorArgs(enginePodman, ""), " "); !strings.Contains(got, defaultPodmanSocket+":/var/run/docker.sock") {
		t.Errorf("podman default socket: %s", got)
	}
}
