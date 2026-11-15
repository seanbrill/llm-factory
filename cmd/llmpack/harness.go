package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/seanbrill/local-llm/internal/agent"
	"github.com/seanbrill/local-llm/internal/gate"
	"github.com/seanbrill/local-llm/internal/native"
)

// harnessSetup is the package's agent harness as this launcher runs it.
type harnessSetup struct {
	agent *agent.Agent // nil when none of its tools can run here
	cfg   agent.Config
}

func (l *launcher) harnessAgent() *agent.Agent {
	if l.harness == nil {
		return nil
	}
	return l.harness.agent
}

// loadHarness reads the package's harness.json and sets it up for this
// computer: the workspace is a real folder here (the package's workspace
// folder unless --workspace says otherwise), the skills are the package's,
// and the shell tool is removed whatever the file or HARNESS_TOOLS say,
// because no container sandboxes it. The other HARNESS_* variables work as
// in a container (HARNESS=off, HARNESS_TOOLS, HARNESS_BRAVE_API_KEY, ...).
// It returns nil when the package has no harness.
func loadHarness(root string, m *native.Manifest, workspace string, getenv func(string) string, logf func(string, ...any)) (*harnessSetup, error) {
	if m.HarnessFile == "" || isOff(getenv("HARNESS")) {
		return nil, nil
	}
	p, err := native.Resolve(root, m.HarnessFile)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	cfg, err := agent.ParseConfig(data)
	if err != nil {
		return nil, err
	}
	if cfg, err = cfg.WithEnv(packageEnv(getenv)); err != nil {
		return nil, err
	}
	ws, err := absPath(workspace)
	if err != nil {
		return nil, err
	}
	cfg.Workspace = ws
	cfg.SkillsDir = ""
	if m.SkillsDir != "" {
		if cfg.SkillsDir, err = native.Resolve(root, m.SkillsDir); err != nil {
			return nil, err
		}
	}
	var dropped bool
	if cfg.Tools, dropped = withoutShell(cfg.Tools); dropped {
		logf("harness: the shell tool stays off in a native package: no container keeps it away from the rest of this computer")
	}
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	on := cfg.EnabledTools()
	files := on[agent.ToolListFiles] || on[agent.ToolReadFile] || on[agent.ToolWriteFile]
	if files {
		// Some unzip tools leave empty folders out.
		if err := os.MkdirAll(ws, 0o755); err != nil {
			logf("harness: can't make the workspace folder %s: %v", ws, err)
		}
	}
	a, err := gate.NewHarness(cfg, "llmpack", logf)
	if err != nil {
		return nil, err
	}
	if a != nil && files {
		logf("harness: the model's files are in %s", ws)
	}
	return &harnessSetup{agent: a, cfg: cfg}, nil
}

// packageEnv hides the variables that would move the workspace or skills
// folder: the launcher's flags and the package decide those.
func packageEnv(getenv func(string) string) func(string) string {
	return func(k string) string {
		switch k {
		case "HARNESS_WORKSPACE", "HARNESS_SKILLS_DIR":
			return ""
		}
		return getenv(k)
	}
}

// withoutShell removes the shell toolset (and its one tool) from a tools list.
// nil stays nil: the default tools never include the shell.
func withoutShell(tools []string) ([]string, bool) {
	if tools == nil {
		return nil, false
	}
	out := make([]string, 0, len(tools))
	dropped := false
	for _, t := range tools {
		if t == agent.ToolsetShell || t == agent.ToolRunCommand {
			dropped = true
			continue
		}
		out = append(out, t)
	}
	return out, dropped
}

func isOff(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "off", "false", "no", "disabled":
		return true
	}
	return false
}

func absPath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("no workspace folder")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("workspace %s: %w", p, err)
	}
	return abs, nil
}
