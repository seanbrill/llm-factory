package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	c := Config{Workspace: "/workspace", SkillsDir: "/etc/llmgate/skills"}.WithDefaults()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.Tools, ",") != "web_search,fetch_url,files,skills" {
		t.Errorf("default tools = %v", c.Tools)
	}
	if c.EnabledTools()[ToolRunCommand] {
		t.Error("the shell must be off by default")
	}
	if c.Limits.MaxSteps != DefaultMaxSteps || c.Limits.ToolTimeout.D() != DefaultToolTimeout ||
		c.Limits.MaxOutputBytes != DefaultMaxOutputBytes || c.Search.Backend != SearchDuckDuckGo {
		t.Errorf("defaults = %+v", c)
	}
	// An explicit empty list means no tools.
	empty := Config{Tools: []string{}}.WithDefaults()
	if len(empty.Tools) != 0 || len(empty.EnabledTools()) != 0 {
		t.Errorf("tools [] = %v", empty.Tools)
	}
}

func TestParseConfig(t *testing.T) {
	c, err := ParseConfig([]byte(`{
		"version": 1,
		"tools": ["web_search", "read_file", "list_files", "shell"],
		"limits": {"max_steps": 4, "tool_timeout": "90s", "max_output_bytes": 4096},
		"search": {"backend": "searxng", "searxng_url": "http://searxng:8080"},
		"shell": {"env": ["GOPATH"]},
		"workspace": "/workspace"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	c = c.WithDefaults()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Limits.ToolTimeout.D() != 90*time.Second || c.Limits.MaxSteps != 4 {
		t.Errorf("limits = %+v", c.Limits)
	}
	on := c.EnabledTools()
	if !on[ToolReadFile] || on[ToolWriteFile] || !on[ToolRunCommand] {
		t.Errorf("enabled = %v", sortedKeys(on))
	}
	if got := strings.Join(c.EnabledToolsets(), ","); got != "web_search,files,shell" {
		t.Errorf("toolsets = %s", got)
	}

	// Seconds as a number.
	c2, err := ParseConfig([]byte(`{"limits":{"tool_timeout":45}}`))
	if err != nil || c2.Limits.ToolTimeout.D() != 45*time.Second {
		t.Errorf("numeric timeout = %v, %v", c2.Limits.ToolTimeout.D(), err)
	}

	for name, bad := range map[string]string{
		"unknown field": `{"tools":[],"api_key":"sk-123"}`,
		"trailing data": `{"tools":[]} {}`,
		"bad duration":  `{"limits":{"tool_timeout":"soon"}}`,
		"not an object": `[1,2]`,
	} {
		if _, err := ParseConfig([]byte(bad)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]struct {
		c    Config
		want string
	}{
		"unknown tool":       {Config{Tools: []string{"teleport"}}, "unknown tools"},
		"files no workspace": {Config{Tools: []string{"files"}}, "workspace is required"},
		"shell no workspace": {Config{Tools: []string{"run_command"}}, "workspace is required"},
		"relative workspace": {Config{Tools: []string{"files"}, Workspace: "work"}, "absolute"},
		"skills no dir":      {Config{Tools: []string{"skills"}}, "skills_dir is required"},
		"steps":              {Config{Tools: []string{}, Limits: Limits{MaxSteps: 500}}, "max_steps"},
		"timeout":            {Config{Tools: []string{}, Limits: Limits{ToolTimeout: Duration(time.Hour)}}, "tool_timeout"},
		"output":             {Config{Tools: []string{}, Limits: Limits{MaxOutputBytes: 10}}, "max_output_bytes"},
		"backend":            {Config{Tools: []string{}, Search: SearchConfig{Backend: "google"}}, "search.backend"},
		"searxng no url":     {Config{Tools: []string{}, Search: SearchConfig{Backend: "searxng"}}, "searxng_url is required"},
		"searxng bad url":    {Config{Tools: []string{}, Search: SearchConfig{Backend: "searxng", SearXNGURL: "ftp://x"}}, "http(s) URL"},
		"version":            {Config{Tools: []string{}, Version: 9}, "version 9"},
		"shell env":          {Config{Tools: []string{}, Shell: ShellConfig{Env: []string{"A=B"}}}, "shell.env"},
	}
	for name, tc := range cases {
		err := tc.c.WithDefaults().Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

func TestWithEnv(t *testing.T) {
	base := Config{Tools: []string{"web_search", "fetch_url", "files"}, Workspace: "/workspace"}
	c, err := base.WithEnv(envMap(map[string]string{"HARNESS_TOOLS": "+shell,-fetch_url"}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.Tools, ",") != "web_search,files,shell" {
		t.Errorf("edited tools = %v", c.Tools)
	}
	c, _ = base.WithEnv(envMap(map[string]string{"HARNESS_TOOLS": "files"}))
	if strings.Join(c.Tools, ",") != "files" {
		t.Errorf("replaced tools = %v", c.Tools)
	}
	c, _ = base.WithEnv(envMap(map[string]string{"HARNESS_TOOLS": "none"}))
	if len(c.Tools) != 0 || c.Tools == nil {
		t.Errorf("none = %#v", c.Tools)
	}
	c, _ = Config{}.WithEnv(envMap(map[string]string{"HARNESS_TOOLS": "+shell"}))
	if strings.Join(c.Tools, ",") != "web_search,fetch_url,files,skills,shell" {
		t.Errorf("+shell on defaults = %v", c.Tools)
	}

	c, _ = base.WithEnv(envMap(map[string]string{"HARNESS_SEARXNG_URL": "http://sx:8080"}))
	if c.Search.Backend != SearchSearXNG || c.Search.SearXNGURL != "http://sx:8080" {
		t.Errorf("searxng = %+v", c.Search)
	}
	c, _ = base.WithEnv(envMap(map[string]string{"HARNESS_BRAVE_API_KEY": "k"}))
	if c.Search.Backend != SearchBrave || c.Search.APIKey != "k" {
		t.Errorf("brave = %+v", c.Search)
	}
	c, _ = base.WithEnv(envMap(map[string]string{"HARNESS_BRAVE_API_KEY": "k", "HARNESS_SEARCH_BACKEND": "DuckDuckGo"}))
	if c.Search.Backend != SearchDuckDuckGo {
		t.Errorf("explicit backend lost: %+v", c.Search)
	}
	c, _ = base.WithEnv(envMap(map[string]string{"HARNESS_MAX_STEPS": "3", "HARNESS_TOOL_TIMEOUT": "2m", "HARNESS_MAX_OUTPUT_BYTES": "2048", "HARNESS_WORKSPACE": "/w", "HARNESS_SKILLS_DIR": "/s"}))
	if c.Limits.MaxSteps != 3 || c.Limits.ToolTimeout.D() != 2*time.Minute || c.Limits.MaxOutputBytes != 2048 || c.Workspace != "/w" || c.SkillsDir != "/s" {
		t.Errorf("overrides = %+v", c)
	}
	if _, err := base.WithEnv(envMap(map[string]string{"HARNESS_MAX_STEPS": "many"})); err == nil {
		t.Error("bad HARNESS_MAX_STEPS accepted")
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	none := envMap(nil)
	if _, ok, err := Load("", none); ok || err != nil {
		t.Errorf("no path: ok=%v err=%v", ok, err)
	}
	if _, ok, err := Load(filepath.Join(dir, "missing.json"), none); ok || err != nil {
		t.Errorf("missing file: ok=%v err=%v", ok, err)
	}
	empty := filepath.Join(dir, "empty.json")
	os.WriteFile(empty, []byte("  \n"), 0o644)
	if _, ok, err := Load(empty, none); ok || err != nil {
		t.Errorf("placeholder file: ok=%v err=%v", ok, err)
	}
	good := filepath.Join(dir, "harness.json")
	os.WriteFile(good, []byte(`{"tools":["web_search"]}`), 0o644)
	c, ok, err := Load(good, none)
	if !ok || err != nil || c.Limits.MaxSteps != DefaultMaxSteps {
		t.Errorf("good file: %+v ok=%v err=%v", c, ok, err)
	}
	if _, ok, _ := Load(good, envMap(map[string]string{"HARNESS": "off"})); ok {
		t.Error("HARNESS=off didn't turn the harness off")
	}
	if _, _, err := Load(good, envMap(map[string]string{"HARNESS_TOOLS": "+files"})); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("env making the config invalid: %v", err)
	}
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte(`{"tools":`), 0o644)
	if _, _, err := Load(bad, none); err == nil {
		t.Error("corrupt file accepted")
	}
}

func TestDurationJSON(t *testing.T) {
	b, err := Duration(90 * time.Second).MarshalJSON()
	if err != nil || string(b) != `"1m30s"` {
		t.Errorf("marshal = %s %v", b, err)
	}
}
