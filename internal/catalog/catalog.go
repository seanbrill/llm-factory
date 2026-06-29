// Package catalog holds the curated, config-driven list of local models.
//
// The catalog is loaded from <baseDir>/config/models.json. If that file does
// not exist yet it is seeded from the embedded default below — so adding or
// editing models is just editing JSON, no recompile. This is the project's
// "optional config settings to build from multiple models".
package catalog

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed models.default.json
var defaultJSON []byte

// Model is one entry in the catalog.
type Model struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Repo        string  `json:"repo"`
	File        string  `json:"file"`
	URL         string  `json:"url"`
	SizeGB      float64 `json:"size_gb"`
	Tier        string  `json:"tier"`   // tiny | small | mid | large
	Params      string  `json:"params"` // e.g. "3B"
	MinRAMGB    float64 `json:"min_ram_gb"`
	MinVRAMGB   float64 `json:"min_vram_gb"`
	Recommended bool    `json:"recommended"`
	Description string  `json:"description"`
	// Modality + optional multi-file fields. All omitempty so existing single-file
	// text entries parse byte-for-byte unchanged. Modality is treated as "text"
	// when empty. MMProjFile/MMProjURL carry a vision projector for VLM models,
	// which bake a 2nd GGUF alongside the model and pass llama-server --mmproj.
	// This is the keystone every later modality (vision/embedding/audio) reuses.
	Modality   string `json:"modality,omitempty"`    // text|code|reasoning|vision|embedding|audio-stt|tts|image|video (default text)
	MMProjFile string `json:"mmproj_file,omitempty"` // vision projector filename (VLMs)
	MMProjURL  string `json:"mmproj_url,omitempty"`  // download URL for the projector
	// ExtraFiles are additional weights a model needs beyond File — a video model
	// (Wan/LTX) bakes a VAE, a T5 text encoder, and a second (high-noise) diffusion
	// model alongside the main one. Each is downloaded into ./models and baked; the
	// runtime maps each file's Role to its CLI flag.
	ExtraFiles []WeightFile `json:"extra_files,omitempty"`
}

// WeightFile is one extra weight a model needs, with the Role the runtime uses to
// map it to a flag (e.g. "vae", "t5", "high_noise_diffusion").
type WeightFile struct {
	File string `json:"file"`
	URL  string `json:"url"`
	Role string `json:"role,omitempty"`
}

// Mod returns the model's modality, defaulting to "text" when unset.
func (m Model) Mod() string {
	if m.Modality == "" {
		return "text"
	}
	return m.Modality
}

// Catalog is the loaded set of models plus the path it came from.
type Catalog struct {
	Models []Model `json:"models"`
	Path   string  `json:"-"`
}

// Load reads (or seeds) <baseDir>/config/models.json.
func Load(baseDir string) (*Catalog, error) {
	cfgDir := filepath.Join(baseDir, "config")
	path := filepath.Join(cfgDir, "models.json")

	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.MkdirAll(cfgDir, 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, defaultJSON, 0o644); err != nil {
			return nil, err
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Catalog
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(c.Models) == 0 {
		return nil, fmt.Errorf("%s contains no models", path)
	}
	c.Path = path
	return &c, nil
}

// Get returns the model with the given id.
func (c *Catalog) Get(id string) (Model, bool) {
	for _, m := range c.Models {
		if m.ID == id {
			return m, true
		}
	}
	return Model{}, false
}
