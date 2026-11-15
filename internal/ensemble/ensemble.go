// Package ensemble holds the shared model for an "Ensemble": several built model
// images behind one OpenAI-compatible endpoint, with a Conductor
// (cmd/ensemblegate) that routes each request to the right member and starts,
// health-checks and evicts members within a memory budget.
//
// It is consumed by the server (CRUD over config/ensembles.json, status and
// persona sync), the builder (baking the manifest into an Ensemble image) and
// the Conductor itself (manifest, config and status wire types). Stdlib only:
// the Conductor's image build compiles this package (builder.stageEnsemblegate).
//
// Package modes (docs/ENSEMBLE.md): "orchestrated" — the image is the Conductor
// plus a manifest, and members run as sibling containers through the host
// engine — is the one that's built. "embedded" (every runtime in one image) is
// reserved; an ensemble saved with it runs orchestrated.
package ensemble

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Member tools: what a member does for the ensemble. One member per tool.
const (
	ToolChat          = "chat"           // text, code, reasoning (or a vision model)
	ToolSeeImage      = "see_image"      // vision: images in messages
	ToolTranscribe    = "transcribe"     // speech to text (whisper.cpp)
	ToolSpeak         = "speak"          // text to speech
	ToolGenerateImage = "generate_image" // image generation (stable-diffusion.cpp)
	ToolGenerateVideo = "generate_video" // video generation (videogate)
	// ToolConductor is the small router model for tool-calling routing. The
	// builder adds it to the manifest from Ensemble.Conductor.
	ToolConductor = "_conductor"
)

// Tools lists the member tools in display order.
var Tools = []string{ToolChat, ToolSeeImage, ToolTranscribe, ToolSpeak, ToolGenerateImage, ToolGenerateVideo}

// ToolFor returns the tool a member of the given modality backs ("" when none:
// embeddings have no place in an ensemble).
func ToolFor(modality string) string {
	switch modality {
	case "", "text", "code", "reasoning":
		return ToolChat
	case "vision":
		return ToolSeeImage
	case "audio-stt":
		return ToolTranscribe
	case "tts":
		return ToolSpeak
	case "image":
		return ToolGenerateImage
	case "video":
		return ToolGenerateVideo
	}
	return ""
}

// ToolAccepts reports whether a member of modality can back tool. Chat also
// takes a vision model (it chats too).
func ToolAccepts(tool, modality string) bool {
	if tool == ToolChat && modality == "vision" {
		return true
	}
	if tool == ToolConductor {
		return ToolFor(modality) == ToolChat || modality == "vision"
	}
	return ToolFor(modality) == tool
}

// Member is one specialist in an Ensemble, identified by the tool it backs.
type Member struct {
	Tool     string `json:"tool"`     // one of Tools
	Modality string `json:"modality"` // text|code|reasoning|vision|audio-stt|tts|image|video
	Image    string `json:"image"`    // built image ref the member runs from
	// From the image's labels (and the catalog), filled by the factory.
	Model   string `json:"model,omitempty"`   // catalog model id
	Runtime string `json:"runtime,omitempty"` // catalog runtime: cpp | python | audiocpp (picks the speech adapter)
	Compute string `json:"compute,omitempty"` // the image's compute: cpu | cuda | vulkan (GPU flags when started)
	// Port pins the host port the member is published on; 0 (the default) lets
	// the engine pick a free one.
	Port int `json:"port,omitempty"`
	// VRAMGB is the member's memory footprint toward the budget: VRAM for a GPU
	// member, RAM for a CPU one (the JSON name predates CPU accounting).
	VRAMGB float64 `json:"vram_gb"`
	// Pinned members ("keep warm") start with the ensemble and are never evicted.
	Pinned bool `json:"pinned,omitempty"`
	// VoiceClone: a speech member that can speak a cloned voice.
	VoiceClone bool `json:"voice_clone,omitempty"`
}

// Ensemble is one saved multimodal super-model definition.
type Ensemble struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	PackageMode  string   `json:"package_mode"`      // orchestrated (embedded is reserved; runs orchestrated)
	Routing      string   `json:"routing"`           // heuristic | tool-calling
	Conductor    string   `json:"conductor"`         // image ref of the small router model (tool-calling only)
	VRAMBudgetGB float64  `json:"vram_budget_gb"`    // memory budget the members stay under together
	Engine       string   `json:"engine"`            // docker (members are started with Docker)
	Compute      string   `json:"compute"`           // fallback compute for members without their own
	Persona      string   `json:"persona,omitempty"` // persona id: its prompt and voice apply
	Members      []Member `json:"members"`
}

// Member returns the member backing tool.
func (e Ensemble) Member(tool string) (Member, bool) {
	for _, m := range e.Members {
		if m.Tool == tool {
			return m, true
		}
	}
	return Member{}, false
}

// Manifest is the definition baked into an Ensemble image and read by the
// Conductor at startup: the Ensemble plus where its members are reachable
// from inside the Conductor's container.
type Manifest struct {
	Ensemble
	ModelHost string `json:"model_host"` // host the members' published ports are reached on
}

// DefaultModelHost is where a containerized Conductor reaches ports its sibling
// members publish on the host's loopback (Docker Desktop routes it there; the
// factory runs the Conductor with --add-host host.docker.internal:host-gateway).
const DefaultModelHost = "host.docker.internal"

// Normalize fills defaults and tidies fields. It doesn't validate.
func (e *Ensemble) Normalize() {
	e.Name = strings.TrimSpace(e.Name)
	e.PackageMode = "orchestrated"
	if e.Routing != "tool-calling" {
		e.Routing = "heuristic"
	}
	if e.Routing == "heuristic" {
		e.Conductor = ""
	}
	if e.VRAMBudgetGB <= 0 {
		e.VRAMBudgetGB = 12
	}
	if e.Engine == "" {
		e.Engine = "docker"
	}
	e.Persona = strings.TrimSpace(e.Persona)
	if e.Members == nil {
		e.Members = []Member{}
	}
	for i := range e.Members {
		m := &e.Members[i]
		m.Image = strings.TrimSpace(m.Image)
		if m.Tool == "" {
			m.Tool = ToolFor(m.Modality)
		}
		if m.VRAMGB < 0 {
			m.VRAMGB = 0
		}
	}
}

// Validate reports a definition the Conductor can't run, as a sentence.
func (e Ensemble) Validate() error {
	if e.Name == "" {
		return errors.New("name the ensemble")
	}
	if Slug(e.Name) == "" {
		return errors.New("use at least one letter or digit in the name")
	}
	if e.ID != "" && !slugOnlyRe.MatchString(e.ID) {
		return fmt.Errorf("the id %q has characters an image name can't hold (use a-z, 0-9 and -)", e.ID)
	}
	seen := map[string]bool{}
	for _, m := range e.Members {
		if m.Image == "" {
			return fmt.Errorf("the %s member has no image", m.Tool)
		}
		known := false
		for _, t := range Tools {
			known = known || t == m.Tool
		}
		if !known {
			return fmt.Errorf("%s: %q isn't a member tool (use one of %s)", m.Image, m.Tool, strings.Join(Tools, ", "))
		}
		if m.Modality != "" && !ToolAccepts(m.Tool, m.Modality) {
			return fmt.Errorf("%s is a %s model; it can't be the %s member", m.Image, m.Modality, m.Tool)
		}
		if seen[m.Tool] {
			return fmt.Errorf("two members are set for %s: pick one", m.Tool)
		}
		seen[m.Tool] = true
	}
	if len(e.Members) == 0 {
		return errors.New("pick at least one member")
	}
	return nil
}

type doc struct {
	Ensembles []Ensemble `json:"ensembles"`
}

var (
	slugRe     = regexp.MustCompile(`[^a-z0-9]+`)
	slugOnlyRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
)

// Slug derives a stable id from a name.
func Slug(s string) string {
	return strings.Trim(slugRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-"), "-")
}

// ErrExists is returned when a new ensemble's name slugs to an existing id.
var ErrExists = errors.New("an ensemble with that name already exists")

// ExistsError names the ensemble a new one would have overwritten.
type ExistsError struct{ Name string }

func (e *ExistsError) Error() string {
	return fmt.Sprintf("there's already an ensemble called “%s”: pick another name, or edit that one", e.Name)
}
func (e *ExistsError) Unwrap() error { return ErrExists }

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
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
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

// Save stores an ensemble and returns the stored value. Without an id it
// creates one from the name and refuses (ExistsError) a name whose id is taken,
// so saving never silently replaces another ensemble; with an id it updates
// that ensemble (or creates it under that id).
func (s *Store) Save(e Ensemble) (Ensemble, error) {
	e.Normalize()
	if err := e.Validate(); err != nil {
		return Ensemble{}, err
	}
	create := e.ID == ""
	if create {
		e.ID = Slug(e.Name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	es, err := s.readLocked()
	if err != nil {
		return Ensemble{}, err
	}
	for i := range es {
		if es[i].ID != e.ID {
			continue
		}
		if create {
			return Ensemble{}, &ExistsError{Name: es[i].Name}
		}
		es[i] = e
		return e, s.writeLocked(es)
	}
	es = append(es, e)
	return e, s.writeLocked(es)
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
