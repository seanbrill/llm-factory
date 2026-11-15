package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
)

// builtinDeps are the outside connections of the built-in tools; tests
// replace them with fakes.
type builtinDeps struct {
	searchClient *http.Client
	ddgURL       string
	braveURL     string
	guard        *Guard
}

// Builtins builds a registry with the built-in tools cfg turns on (cfg must
// be validated). A tool that can't work on this host (no shell, a workspace
// that can't be created, no skills folder) is left out and reported in the
// second result instead of failing the whole harness.
func Builtins(cfg Config) (*Registry, []Unavailable, error) {
	return builtinsWith(cfg, builtinDeps{})
}

func builtinsWith(cfg Config, d builtinDeps) (*Registry, []Unavailable, error) {
	reg := NewRegistry()
	on := cfg.EnabledTools()
	var unavailable []Unavailable
	skip := func(set string, err error) {
		for _, n := range toolsetTools[set] {
			if on[n] {
				unavailable = append(unavailable, Unavailable{Tool: n, Reason: err.Error()})
			}
		}
	}
	add := func(set string, t Tool) error {
		if !on[t.Name()] {
			return nil
		}
		return reg.Register(set, t)
	}
	maxOut := cfg.Limits.MaxOutputBytes

	for _, set := range toolsetOrder {
		wanted := false
		for _, n := range toolsetTools[set] {
			wanted = wanted || on[n]
		}
		if !wanted {
			continue
		}
		var tools []Tool
		switch set {
		case ToolsetWebSearch:
			st := newSearchTool(cfg.Search, d.searchClient)
			if d.ddgURL != "" {
				st.ddgURL = d.ddgURL
			}
			if d.braveURL != "" {
				st.braveURL = d.braveURL
			}
			if cfg.Search.Backend == SearchBrave && cfg.Search.APIKey == "" {
				skip(set, errors.New("the brave backend needs HARNESS_BRAVE_API_KEY"))
				continue
			}
			tools = []Tool{st}
		case ToolsetFetchURL:
			g := d.guard
			if g == nil {
				g = &Guard{}
			}
			tools = []Tool{newFetchTool(cfg.Fetch, maxOut, g)}
		case ToolsetFiles:
			ws, err := OpenWorkspace(cfg.Workspace)
			if err != nil {
				skip(set, err)
				continue
			}
			tools = fileTools(ws, maxOut)
		case ToolsetSkills:
			fi, err := os.Stat(cfg.SkillsDir)
			if err == nil && !fi.IsDir() {
				err = fmt.Errorf("%s is not a folder", cfg.SkillsDir)
			}
			if errors.Is(err, fs.ErrNotExist) {
				err = fmt.Errorf("the skills folder %s doesn't exist", cfg.SkillsDir)
			}
			if err != nil {
				skip(set, err)
				continue
			}
			tools = skillToolset(cfg.SkillsDir, maxOut)
		case ToolsetShell:
			ws, err := OpenWorkspace(cfg.Workspace)
			if err != nil {
				skip(set, err)
				continue
			}
			sh, err := newShellTool(cfg.Shell, ws, maxOut, cfg.Limits.ToolTimeout.D())
			if err != nil {
				skip(set, err)
				continue
			}
			tools = []Tool{sh}
		}
		for _, t := range tools {
			if err := add(set, t); err != nil {
				return nil, nil, err
			}
		}
	}
	return reg, unavailable, nil
}
