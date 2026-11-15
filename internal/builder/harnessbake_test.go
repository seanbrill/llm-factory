package builder

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/seanbrill/local-llm/internal/agent"
)

// llmgateDockerfiles returns the Dockerfiles that ship llmgate, keyed by name.
func llmgateDockerfiles(t *testing.T) map[string]string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "..", filepath.FromSlash(RuntimesDir), "Dockerfile*"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(logical(string(data)), "/llmgate") && strings.Contains(string(data), "COPY gate/") {
			out[filepath.Base(p)] = string(data)
		}
	}
	return out
}

func TestLlmgateDockerfilesBakeHarness(t *testing.T) {
	dfs := llmgateDockerfiles(t)
	for _, want := range []string{"Dockerfile.cpu", "Dockerfile.cuda", "Dockerfile.vulkan"} {
		if _, ok := dfs[want]; !ok {
			t.Errorf("%s doesn't build llmgate", want)
		}
	}
	if len(dfs) != 3 {
		t.Errorf("llmgate Dockerfiles = %d; a new one needs the harness lines too", len(dfs))
	}
	for name, df := range dfs {
		code := logical(df)
		checks := map[string]bool{
			"builds ./cmd/llmgate from the staged module": strings.Contains(code, `go build -trimpath -ldflags "-s -w" -o /llmgate ./cmd/llmgate`),
			"copies harness/ to /etc/llmgate/":            strings.Contains(code, "COPY harness/           /etc/llmgate/\n"),
			"sets HARNESS_FILE":                           strings.Contains(code, "HARNESS_FILE=/etc/llmgate/harness.json"),
			"creates /workspace for the llama user":       strings.Contains(code, "mkdir -p /etc/llmgate /workspace") && strings.Contains(code, "chown llama /workspace"),
			"runs as the non-root llama user":             strings.Contains(code, "\nUSER llama\n") && strings.Contains(code, "useradd --system --uid 10001 llama"),
		}
		for what, ok := range checks {
			if !ok {
				t.Errorf("%s: doesn't %s", name, what)
			}
		}
		// The harness COPY has to land in the runtime stage, after llmgate.
		if strings.Index(code, "COPY harness/") < strings.Index(code, "COPY --from=gate") {
			t.Errorf("%s: harness/ is copied before the runtime stage", name)
		}
		if strings.Contains(code, "VOLUME") {
			t.Errorf("%s: a VOLUME would leave an anonymous volume behind every run", name)
		}
	}
}

func TestStageGateCompiles(t *testing.T) {
	work := t.TempDir()
	if err := stageGate(filepath.Join("..", ".."), work); err != nil {
		t.Fatal(err)
	}
	gate := filepath.Join(work, "gate")
	mod, err := os.ReadFile(filepath.Join(gate, "go.mod"))
	if err != nil || !strings.HasPrefix(string(mod), "module "+gateModule+"\n") {
		t.Fatalf("go.mod = %q, %v", mod, err)
	}
	for _, f := range []string{"cmd/llmgate/main.go", "internal/gate/gate.go", "internal/gate/llama.go", "internal/agent/agent.go", "internal/agent/netguard.go", "internal/agent/shell_unix.go", "internal/agent/shell_windows.go"} {
		if _, err := os.Stat(filepath.Join(gate, filepath.FromSlash(f))); err != nil {
			t.Errorf("not staged: %s", f)
		}
	}
	_ = filepath.WalkDir(gate, func(p string, d os.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, "_test.go") {
			t.Errorf("test file staged: %s", p)
		}
		return nil
	})
	if testing.Short() {
		t.Skip("compiling the staged gate")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go toolchain")
	}
	// Build it the way the image does: offline, no cgo, Linux. Any import
	// outside the standard library fails here (GOPROXY=off).
	cmd := exec.Command(goBin, "build", "-trimpath", "-o", filepath.Join(work, "llmgate"), "./cmd/llmgate")
	cmd.Dir = gate
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH, "GOFLAGS=-mod=mod", "GOPROXY=off", "GOTOOLCHAIN=local", "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("staged llmgate doesn't build: %v\n%s", err, out)
	}
}

func TestNormalizeHarness(t *testing.T) {
	if c, err := NormalizeHarness(nil); c != nil || err != nil {
		t.Errorf("nil = %v %v", c, err)
	}
	in := &agent.Config{Tools: []string{"web_search", "files"}, Workspace: "/home/me", SkillsDir: "/x"}
	in.Search.APIKey = "secret"
	c, err := NormalizeHarness(in)
	if err != nil {
		t.Fatal(err)
	}
	if c.Workspace != ImageWorkspace || c.SkillsDir != ImageSkillsDir || c.Search.APIKey != "" || c.Limits.MaxSteps == 0 {
		t.Errorf("normalized = %+v", c)
	}
	if in.Workspace != "/home/me" {
		t.Error("the request's config was modified")
	}
	if _, err := NormalizeHarness(&agent.Config{Tools: []string{"teleport"}}); err == nil {
		t.Error("invalid config accepted")
	}
}

func TestHarnessLabels(t *testing.T) {
	if l := harnessLabels(nil); l != nil {
		t.Errorf("no harness: %v", l)
	}
	c, _ := NormalizeHarness(&agent.Config{Tools: []string{"web_search", "read_file", "shell"}})
	l := strings.Join(harnessLabels(c), " ")
	for _, want := range []string{"local-llm.harness=1", "local-llm.harness_tools=web_search,files,shell", `local-llm.harness_config={"version":1,"tools":["web_search","read_file","shell"]`} {
		if !strings.Contains(l, want) {
			t.Errorf("labels %q lack %q", l, want)
		}
	}
}

func TestStageHarness(t *testing.T) {
	base := t.TempDir()
	skills := filepath.Join(base, "skills")
	mk := func(rel, content string) {
		p := filepath.Join(skills, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("web-research/SKILL.md", "---\nname: web-research\ndescription: Research.\n---\nBody\n")
	mk("web-research/scripts/run.sh", "echo hi\n")
	mk("web-research/.secret", "x")
	mk("broken/SKILL.md", "no front matter\n")
	mk(".hidden/SKILL.md", "---\nname: hidden\ndescription: H.\n---\n")
	if runtime.GOOS != "windows" {
		outside := filepath.Join(base, "outside.txt")
		os.WriteFile(outside, []byte("SECRET"), 0o644)
		os.Symlink(outside, filepath.Join(skills, "web-research", "leak.txt"))
	}
	var logs []string
	log := func(s string) { logs = append(logs, s) }

	// No harness: placeholders only.
	work := t.TempDir()
	if err := stageHarness(base, work, nil, log); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(work, "harness", "harness.json")); err != nil || len(b) != 0 {
		t.Errorf("placeholder harness.json = %q %v", b, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(work, "harness", "skills")); len(entries) != 1 || entries[0].Name() != ".keep" {
		t.Errorf("placeholder skills = %v", entries)
	}

	// A harness with skills.
	c, err := NormalizeHarness(&agent.Config{Tools: []string{"web_search", "skills", "shell"}})
	if err != nil {
		t.Fatal(err)
	}
	work = t.TempDir()
	if err := stageHarness(base, work, c, log); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(work, "harness", "harness.json"))
	if err != nil {
		t.Fatal(err)
	}
	baked, err := agent.ParseConfig(data)
	if err != nil {
		t.Fatalf("baked config doesn't parse: %v\n%s", err, data)
	}
	if baked.Workspace != ImageWorkspace || baked.SkillsDir != ImageSkillsDir || baked.WithDefaults().Validate() != nil {
		t.Errorf("baked = %+v", baked)
	}
	got := map[string]bool{}
	root := filepath.Join(work, "harness", "skills")
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			got[filepath.ToSlash(rel)] = true
		}
		return nil
	})
	want := map[string]bool{".keep": true, "web-research/SKILL.md": true, "web-research/scripts/run.sh": true}
	if len(got) != len(want) {
		t.Errorf("staged skills = %v, want %v", got, want)
	}
	for k := range want {
		if !got[k] {
			t.Errorf("missing %s (got %v)", k, got)
		}
	}
	last := logs[len(logs)-1]
	if !strings.Contains(last, "1 skills") || !strings.Contains(last, "shell tool is ON") {
		t.Errorf("log = %q", last)
	}

	// Without the skills tools, no skills are baked.
	c2, _ := NormalizeHarness(&agent.Config{Tools: []string{"web_search"}})
	work = t.TempDir()
	if err := stageHarness(base, work, c2, log); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(filepath.Join(work, "harness", "skills")); len(entries) != 1 {
		t.Errorf("skills baked without the skills tools: %v", entries)
	}
	var m map[string]any
	data, _ = os.ReadFile(filepath.Join(work, "harness", "harness.json"))
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
}

func TestRepoSkillsStage(t *testing.T) {
	// The repo's own skills bake cleanly.
	n, err := stageSkills(filepath.Join("..", "..", "skills"), t.TempDir())
	if err != nil || n == 0 {
		t.Errorf("staged %d repo skills: %v", n, err)
	}
}
