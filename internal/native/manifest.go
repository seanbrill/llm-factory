package native

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/seanbrill/local-llm/internal/tuning"
)

// File and folder names inside a package (relative to its root folder).
const (
	ManifestName  = "manifest.json" // what the launcher reads at start
	ChecksumsName = "SHA256SUMS"    // sha256sum/shasum format, every regular file
	ReadmeName    = "README.txt"
	WorkspaceDir  = "workspace" // the harness's file tools work here
)

// ManifestSchema identifies the manifest format.
const ManifestSchema = "llm-factory.native-package/1"

// Engine roles. A package runs one engine per role; only the chat role
// exists today (an ensemble would add image, speech and voice engines).
const RoleChat = "chat"

// Engine is one native inference server in a package.
type Engine struct {
	// Kind is the runtime: "llama.cpp" (llama-server). Later: stable-diffusion.cpp, whisper.cpp.
	Kind string `json:"kind"`
	// Role is what the gate routes to it: "chat" (/v1/chat/completions and the rest of the OpenAI API).
	Role string `json:"role"`
	// Release and Build identify the runtime's version (llama.cpp v0.9.0 = b12110).
	Release string `json:"release,omitempty"`
	Build   string `json:"build,omitempty"`
	// Server is the server program's path in the package.
	Server string `json:"server"`
	// Accel is the GPU acceleration the build uses (metal, vulkan, cpu).
	Accel string `json:"accel"`
	// GPULayers is the default --n-gpu-layers ("all", "auto" or a number).
	GPULayers string `json:"gpu_layers,omitempty"`
	// Model and MMProj are the weights' paths in the package (MMProj: vision models only).
	Model  string `json:"model"`
	MMProj string `json:"mmproj,omitempty"`
	// CtxSize is the context window in tokens.
	CtxSize int `json:"ctx_size"`
	// Modality is the catalog modality (text, code, reasoning, vision).
	Modality string `json:"modality,omitempty"`
	// Tuning is llama-server's runtime tuning (docs/TUNING.md); nil = the
	// launcher's defaults. DraftModel is the draft model's path for
	// speculative decoding with spec "draft".
	Tuning     *tuning.Tuning `json:"tuning,omitempty"`
	DraftModel string         `json:"draft_model,omitempty"`
}

// Manifest describes a package: written by the factory, read by the launcher.
// Every path is relative to the package folder, with forward slashes.
type Manifest struct {
	Schema  string   `json:"schema"`
	Name    string   `json:"name"`     // the model's display name
	ModelID string   `json:"model_id"` // the factory catalog id
	Target  string   `json:"target"`   // Target.ID it was made for
	Engines []Engine `json:"engines"`
	// SystemPromptFile holds the baked initialization prompt ("" = none);
	// InjectMode is "missing" (default) or "always", as in images.
	SystemPromptFile string `json:"system_prompt_file,omitempty"`
	InjectMode       string `json:"inject_mode,omitempty"`
	// HarnessFile is the agent harness config ("" = no harness); its
	// workspace and skills folder are set by the launcher at start.
	HarnessFile string `json:"harness_file,omitempty"`
	SkillsDir   string `json:"skills_dir,omitempty"`
	// Provenance, for the README and the chat page.
	License   string `json:"license,omitempty"`    // e.g. "apache-2.0"; "" = unknown
	SourceURL string `json:"source_url,omitempty"` // the model's repository
	Created   string `json:"created"`              // RFC 3339
	Factory   string `json:"factory_version,omitempty"`
}

// Engine returns the package's engine for a role.
func (m *Manifest) Engine(role string) (Engine, bool) {
	for _, e := range m.Engines {
		if e.Role == role {
			return e, true
		}
	}
	return Engine{}, false
}

// Validate checks a manifest: a known schema, a chat engine, and paths that
// stay inside the package.
func (m *Manifest) Validate() error {
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }
	if m.Schema != ManifestSchema {
		add("schema %q isn't %q (made by a newer factory? Use the launcher that came with the package)", m.Schema, ManifestSchema)
	}
	if len(m.Engines) == 0 {
		add("no engines")
	}
	checkPath := func(field, p string, required bool) {
		if p == "" {
			if required {
				add("%s is missing", field)
			}
			return
		}
		if _, err := CleanRel(p); err != nil {
			add("%s: %v", field, err)
		}
	}
	roles := map[string]bool{}
	for i, e := range m.Engines {
		if roles[e.Role] {
			add("two engines for the %s role", e.Role)
		}
		roles[e.Role] = true
		checkPath(fmt.Sprintf("engines[%d].server", i), e.Server, true)
		checkPath(fmt.Sprintf("engines[%d].model", i), e.Model, true)
		checkPath(fmt.Sprintf("engines[%d].mmproj", i), e.MMProj, false)
		checkPath(fmt.Sprintf("engines[%d].draft_model", i), e.DraftModel, false)
		if e.CtxSize < 0 {
			add("engines[%d].ctx_size is negative", i)
		}
	}
	if !roles[RoleChat] {
		add("no chat engine")
	}
	checkPath("system_prompt_file", m.SystemPromptFile, false)
	checkPath("harness_file", m.HarnessFile, false)
	checkPath("skills_dir", m.SkillsDir, false)
	switch m.InjectMode {
	case "", "missing", "always":
	default:
		add("inject_mode %q isn't missing or always", m.InjectMode)
	}
	if len(errs) > 0 {
		return errors.New("manifest: " + strings.Join(errs, "; "))
	}
	return nil
}

// CleanRel checks a package path: relative, forward slashes, no "..", and
// returns it cleaned.
func CleanRel(p string) (string, error) {
	if p == "" || strings.Contains(p, `\`) || strings.HasPrefix(p, "/") || filepath.IsAbs(p) || strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("%q isn't a relative path inside the package", p)
	}
	c := path.Clean(p)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") || (len(c) > 1 && c[1] == ':') {
		return "", fmt.Errorf("%q leaves the package", p)
	}
	return c, nil
}

// Resolve turns a package path into a path on this computer under root.
func Resolve(root, rel string) (string, error) {
	c, err := CleanRel(rel)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, filepath.FromSlash(c)), nil
}

// ReadManifest reads and validates <root>/manifest.json.
func ReadManifest(root string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(root, ManifestName))
	if err != nil {
		return nil, err
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Marshal is the manifest as written into a package.
func (m *Manifest) Marshal() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
