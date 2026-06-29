// Package ensemble holds the shared model for an "Ensemble" — a multimodal
// super-model made of a tiny Conductor (router) plus a roster of specialist model
// images. It is consumed by three callers: the server (CRUD over
// config/ensembles.json), the builder (assembling an Ensemble image), and the
// baked manifest the ensemblegate Conductor reads at run time.
//
// Two package modes (see docs/ENSEMBLE.md):
//   - "orchestrated": the Ensemble image is just the Conductor + manifest; it
//     starts/stops sibling specialist containers via the host engine.
//   - "embedded": (mega) everything baked into one image; the Conductor supervises
//     internal processes. Orchestrated is the implemented path; embedded is staged.
package ensemble

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Member is one specialist in an Ensemble, identified by the tool it backs.
type Member struct {
	Tool     string  `json:"tool"`     // chat|see_image|transcribe|speak|generate_image|generate_video|watch_video
	Modality string  `json:"modality"` // text|vision|audio-stt|tts|image|video
	Image    string  `json:"image"`    // built image ref the specialist runs from
	Port     int     `json:"port"`     // host port the Conductor publishes this specialist on
	VRAMGB   float64 `json:"vram_gb"`  // estimated VRAM cost, for the budget/eviction
}

// Ensemble is one saved multimodal super-model definition.
type Ensemble struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	PackageMode  string   `json:"package_mode"`   // orchestrated|embedded
	Routing      string   `json:"routing"`        // heuristic|tool-calling
	Conductor    string   `json:"conductor"`      // image ref of the tiny router model (tool-calling only)
	VRAMBudgetGB float64  `json:"vram_budget_gb"` // total GPU budget the Conductor stays under
	Engine       string   `json:"engine"`         // docker|podman
	Compute      string   `json:"compute"`        // cpu|cuda|vulkan (applied when starting specialists)
	Members      []Member `json:"members"`
}

// Manifest is the resolved definition baked into an Ensemble image and read by the
// ensemblegate Conductor at startup. It is just an Ensemble plus the modelHost the
// specialists are reachable on (the gate runs in a container; siblings publish to
// the host, reached via host.docker.internal).
type Manifest struct {
	Ensemble
	ModelHost string `json:"model_host"` // host where started specialists are reachable
}

type doc struct {
	Ensembles []Ensemble `json:"ensembles"`
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// Slug derives a stable id from a name.
func Slug(s string) string {
	return strings.Trim(slugRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-"), "-")
}

// Store persists Ensembles to config/ensembles.json.
type Store struct {
	mu   sync.Mutex
	path string
}

func NewStore(baseDir string) *Store {
	return &Store{path: filepath.Join(baseDir, "config", "ensembles.json")}
}

// List returns all saved ensembles (a missing file is an empty list).
func (s *Store) List() ([]Ensemble, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readLocked()
}

func (s *Store) readLocked() ([]Ensemble, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return []Ensemble{}, nil
	}
	if err != nil {
		return nil, err
	}
	var d doc
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("parse ensembles.json: %w", err)
	}
	if d.Ensembles == nil {
		d.Ensembles = []Ensemble{}
	}
	return d.Ensembles, nil
}

func (s *Store) writeLocked(es []Ensemble) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(doc{Ensembles: es}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o644)
}

// Get returns the ensemble with id, or ok=false.
func (s *Store) Get(id string) (Ensemble, bool, error) {
	es, err := s.List()
	if err != nil {
		return Ensemble{}, false, err
	}
	for _, e := range es {
		if e.ID == id {
			return e, true, nil
		}
	}
	return Ensemble{}, false, nil
}

// Save upserts an ensemble (creating an id from the name when absent) and returns
// the stored value.
func (s *Store) Save(e Ensemble) (Ensemble, error) {
	e.Name = strings.TrimSpace(e.Name)
	if e.Name == "" {
		return Ensemble{}, fmt.Errorf("name is required")
	}
	if e.ID == "" {
		e.ID = Slug(e.Name)
	}
	if e.PackageMode == "" {
		e.PackageMode = "orchestrated"
	}
	if e.Routing == "" {
		e.Routing = "heuristic"
	}
	if e.VRAMBudgetGB == 0 {
		e.VRAMBudgetGB = 12
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	es, err := s.readLocked()
	if err != nil {
		return Ensemble{}, err
	}
	updated := false
	for i := range es {
		if es[i].ID == e.ID {
			es[i] = e
			updated = true
			break
		}
	}
	if !updated {
		es = append(es, e)
	}
	if err := s.writeLocked(es); err != nil {
		return Ensemble{}, err
	}
	return e, nil
}

// Delete removes an ensemble by id.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	es, err := s.readLocked()
	if err != nil {
		return err
	}
	out := make([]Ensemble, 0, len(es))
	for _, e := range es {
		if e.ID != id {
			out = append(out, e)
		}
	}
	return s.writeLocked(out)
}
