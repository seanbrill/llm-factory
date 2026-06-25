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
