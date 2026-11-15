package builder

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/seanbrill/local-llm/internal/agent"
	"github.com/seanbrill/local-llm/internal/catalog"
)

// The baked agent harness: a llama.cpp image can carry a harness.json and a
// skills folder, which llmgate reads at start to run the tool loop of
// internal/agent itself (see docs/HARNESS.md, "Baked harness").

// Paths inside llmgate images. The Dockerfiles create them.
const (
	ImageWorkspace   = "/workspace"
	ImageSkillsDir   = "/etc/llmgate/skills"
	ImageHarnessFile = "/etc/llmgate/harness.json"
)

// Image labels for a baked harness. The factory gateway reads LabelHarness to
// pass such models through instead of running a second loop around them.
const (
	LabelHarness       = "local-llm.harness"        // "1" when baked
	LabelHarnessTools  = "local-llm.harness_tools"  // enabled toolsets, comma-separated
	LabelHarnessConfig = "local-llm.harness_config" // the baked harness.json (compact), for cloning
)

const (
	maxSkillFile  = 1 << 20  // largest skill file baked
	maxSkillsSize = 16 << 20 // all skill files together
)

// NormalizeHarness turns a requested harness config into the one an image
// bakes: the workspace and skills folder are the image's own paths, defaults
// are filled in, and the result is validated. nil stays nil (no harness).
func NormalizeHarness(h *agent.Config) (*agent.Config, error) {
	if h == nil {
		return nil, nil
	}
	c := *h
	c.Workspace = ImageWorkspace
	c.SkillsDir = ImageSkillsDir
	c.Search.APIKey = "" // keys come from the environment at run time only
	c = c.WithDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// CanHarness reports whether m can take a baked harness. The tool loop runs in
// llmgate, which fronts the llama.cpp chat models (text, code, reasoning and
// vision); embedding models and the other runtimes have no tool loop.
func CanHarness(m catalog.Model) bool {
	return modelFamily(m) == "llama" && m.Mod() != "embedding"
}

// HarnessModelError says why m can't take a baked harness (nil when it can).
// The build API checks it before queueing, the builder again before building.
func HarnessModelError(m catalog.Model) error {
	if CanHarness(m) {
		return nil
	}
	return fmt.Errorf("the agent harness needs a llama.cpp chat model; %s is a %s model", m.Name, m.Mod())
}

// BakedHarness returns the harness config an image was built with, read from
// its labels, or nil when it has none. When the config label is missing or
// unreadable, the tools label stands in, with defaults for the rest.
func BakedHarness(labels map[string]string) *agent.Config {
	if labels[LabelHarness] != "1" {
		return nil
	}
	if raw := strings.TrimSpace(labels[LabelHarnessConfig]); raw != "" {
		if c, err := agent.ParseConfig([]byte(raw)); err == nil {
			c = c.WithDefaults()
			return &c
		}
	}
	tools := []string{}
	known := agent.Toolsets()
	for _, t := range strings.Split(labels[LabelHarnessTools], ",") {
		if t = strings.TrimSpace(t); slices.Contains(known, t) {
			tools = append(tools, t)
		}
	}
	c := agent.Config{Tools: tools, Workspace: ImageWorkspace, SkillsDir: ImageSkillsDir}.WithDefaults()
	return &c
}

// HarnessUsesWorkspace reports whether c turns on tools that work in the
// workspace (the files or the shell tools), so a run mounts a folder there.
func HarnessUsesWorkspace(c *agent.Config) bool {
	if c == nil {
		return false
	}
	sets := c.EnabledToolsets()
	return slices.Contains(sets, agent.ToolsetFiles) || slices.Contains(sets, agent.ToolsetShell)
}

// ImageHarness is what the Library shows of a baked harness (an image's
// "Harness" in GET /api/images): the toolsets that are on and how it searches.
type ImageHarness struct {
	Tools      []string `json:"tools"`                 // enabled toolsets, in order
	Search     string   `json:"search,omitempty"`      // search backend, when web_search is on
	SearXNGURL string   `json:"searxng_url,omitempty"` // with the searxng backend
	MaxSteps   int      `json:"max_steps"`
	Workspace  bool     `json:"workspace"` // a run mounts a workspace folder at /workspace
}

// HarnessInfo summarizes c for the Library (nil for no harness).
func HarnessInfo(c *agent.Config) *ImageHarness {
	if c == nil {
		return nil
	}
	h := &ImageHarness{
		Tools:     append([]string{}, c.EnabledToolsets()...),
		MaxSteps:  c.Limits.MaxSteps,
		Workspace: HarnessUsesWorkspace(c),
	}
	if slices.Contains(h.Tools, agent.ToolsetWebSearch) {
		h.Search = c.Search.Backend
		if h.Search == agent.SearchSearXNG {
			h.SearXNGURL = c.Search.SearXNGURL
		}
	}
	return h
}

// harnessLabels are the image labels describing a baked harness.
func harnessLabels(c *agent.Config) []string {
	if c == nil {
		return nil
	}
	compact, _ := json.Marshal(c)
	return []string{
		"--label", LabelHarness + "=1",
		"--label", LabelHarnessTools + "=" + strings.Join(c.EnabledToolsets(), ","),
		"--label", LabelHarnessConfig + "=" + string(compact),
	}
}

// stageHarness writes <work>/harness/harness.json and <work>/harness/skills/,
// which the Dockerfiles COPY to /etc/llmgate/. Without a harness both are
// placeholders (an empty harness.json, which llmgate reads as "no harness",
// and an empty skills folder), so the COPY never fails. With the skills tools
// on, the repo's skills/ folder is baked in. c must be normalized.
func stageHarness(baseDir, work string, c *agent.Config, log LogFunc) error {
	dir := filepath.Join(work, "harness")
	skillsDst := filepath.Join(dir, "skills")
	if err := os.MkdirAll(skillsDst, 0o755); err != nil {
		return err
	}
	// A file in the folder keeps the COPY's source non-empty for every builder.
	if err := os.WriteFile(filepath.Join(skillsDst, ".keep"), nil, 0o644); err != nil {
		return err
	}
	if c == nil {
		return os.WriteFile(filepath.Join(dir, "harness.json"), nil, 0o644)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "harness.json"), append(data, '\n'), 0o644); err != nil {
		return err
	}
	on := c.EnabledTools()
	skills := 0
	if on[agent.ToolListSkills] || on[agent.ToolViewSkill] {
		if skills, err = stageSkills(filepath.Join(baseDir, "skills"), skillsDst); err != nil {
			return err
		}
	}
	msg := fmt.Sprintf("Baking the agent harness: %s", strings.Join(c.EnabledToolsets(), ", "))
	if skills > 0 {
		msg += fmt.Sprintf(" (%d skills)", skills)
	}
	if on[agent.ToolRunCommand] {
		msg += "; the shell tool is ON"
	}
	log(msg)
	return nil
}

// stageSkills copies each skill folder (one holding SKILL.md) from src to dst:
// regular files only, no hidden entries, no symbolic links, within size caps.
// A missing src bakes no skills. It returns the number of skills copied.
func stageSkills(src, dst string) (int, error) {
	skills, err := agent.ListSkills(src)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read skills: %w", err)
	}
	total := int64(0)
	n := 0
	for _, sk := range skills {
		root := filepath.Join(src, sk.Dir)
		if fi, err := os.Lstat(root); err != nil || !fi.IsDir() {
			continue // a linked skill folder isn't baked
		}
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if p != root && strings.HasPrefix(d.Name(), ".") {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			rel, _ := filepath.Rel(src, p)
			target := filepath.Join(dst, rel)
			switch {
			case d.IsDir():
				return os.MkdirAll(target, 0o755)
			case !d.Type().IsRegular():
				return nil // links and devices stay behind
			}
			fi, err := d.Info()
			if err != nil {
				return err
			}
			if fi.Size() > maxSkillFile {
				return fmt.Errorf("skill file %s is larger than %d MB", rel, maxSkillFile>>20)
			}
			if total += fi.Size(); total > maxSkillsSize {
				return fmt.Errorf("the skills folder is larger than %d MB", maxSkillsSize>>20)
			}
			return copyFile(p, target)
		})
		if err != nil {
			return 0, fmt.Errorf("stage skill %s: %w", sk.Name, err)
		}
		n++
	}
	return n, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// gateModule is the module path staged gate sources build under, so a gate
// can import the repo's stdlib-only internal packages (llmgate uses
// internal/agent).
const gateModule = "github.com/seanbrill/local-llm"

// stageGoPackages copies the non-test Go files of each package folder (given
// relative to baseDir, e.g. "cmd/llmgate") to the same place under
// <work>/gate, and writes a go.mod for gateModule. The packages must be
// stdlib-only: the image build has no network.
func stageGoPackages(baseDir, work string, pkgs ...string) error {
	gateDir := filepath.Join(work, "gate")
	for _, pkg := range pkgs {
		src := filepath.Join(baseDir, filepath.FromSlash(pkg))
		entries, err := os.ReadDir(src)
		if err != nil {
			return fmt.Errorf("read %s source (%s): %w", pkg, src, err)
		}
		dst := filepath.Join(gateDir, filepath.FromSlash(pkg))
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
		copied := 0
		for _, e := range entries {
			name := e.Name()
			if !e.Type().IsRegular() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			if err := copyFile(filepath.Join(src, name), filepath.Join(dst, name)); err != nil {
				return err
			}
			copied++
		}
		if copied == 0 {
			return fmt.Errorf("no Go source in %s", src)
		}
	}
	return os.WriteFile(filepath.Join(gateDir, "go.mod"), []byte("module "+gateModule+"\n\ngo 1.22\n"), 0o644)
}
