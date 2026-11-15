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
	"github.com/seanbrill/local-llm/internal/ensemble"
)

func TestEnsembleManifest(t *testing.T) {
	e := ensemble.Ensemble{Name: "My Helper", Routing: "tool-calling", Conductor: "local-llm/small", Members: []ensemble.Member{
		{Tool: "chat", Modality: "text", Image: "local-llm/qwen", VRAMGB: 6},
		{Tool: "speak", Modality: "tts", Image: "local-llm/pocket", Port: 18090, VRAMGB: 1},
	}, Harness: &agent.Config{Tools: []string{"web_search"}}}
	m, err := EnsembleManifest(e)
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "my-helper" || m.ModelHost != ensemble.DefaultModelHost || len(m.Members) != 3 {
		t.Fatalf("manifest = %+v", m)
	}
	if m.Members[0].Port != 0 || m.Members[1].Port != 18090 {
		t.Errorf("ports = %d, %d (only pinned ports are kept; others are picked at start)", m.Members[0].Port, m.Members[1].Port)
	}
	if c := m.Members[2]; c.Tool != ensemble.ToolConductor || c.Image != "local-llm/small" {
		t.Errorf("conductor member = %+v", c)
	}
	if m.Harness == nil || m.Harness.Workspace != ensemble.HarnessWorkspace || strings.Join(m.Harness.Tools, ",") != "web_search" {
		t.Errorf("harness = %+v", m.Harness)
	}
	e.Routing = ""
	if m, _ := EnsembleManifest(e); m.Routing != ensemble.RoutingAgent || len(m.Members) != 2 {
		t.Errorf("agent routing manifest = %s, %d members", m.Routing, len(m.Members))
	}
	e.Harness = &agent.Config{Tools: []string{"shell"}}
	if _, err := EnsembleManifest(e); err == nil {
		t.Error("a shell tool was accepted for an ensemble")
	}
}

// TestStageEnsemblegateCompiles stages the Conductor the way an Ensemble image
// build does and compiles it offline for Linux, plus vets it (which also
// catches standard-library APIs newer than the image's Go 1.22).
func TestStageEnsemblegateCompiles(t *testing.T) {
	work := t.TempDir()
	if err := stageEnsemblegate(filepath.Join("..", ".."), work); err != nil {
		t.Fatal(err)
	}
	if err := stageHarness(filepath.Join("..", ".."), work, nil, func(string) {}); err != nil {
		t.Fatal(err)
	}
	gate := filepath.Join(work, "gate")
	for _, f := range []string{"go.mod", "cmd/ensemblegate/main.go", "cmd/ensemblegate/agent.go", "internal/ensemble/ensemble.go",
		"internal/voice/adapters.go", "internal/agent/agent.go", "../harness/harness.json"} {
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
		t.Skip("compiling the staged Conductor")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go toolchain")
	}
	env := append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH, "GOFLAGS=-mod=mod", "GOPROXY=off", "GOTOOLCHAIN=local", "GOWORK=off")
	for _, args := range [][]string{
		{"build", "-trimpath", "-o", filepath.Join(work, "ensemblegate"), "./cmd/ensemblegate"},
		{"vet", "./..."},
	} {
		cmd := exec.Command(goBin, args...)
		cmd.Dir = gate
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("staged Conductor: go %s: %v\n%s", args[0], err, out)
		}
	}
}

func TestDockerfileEnsembleBuildsTheStagedModule(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docker", "Dockerfile.ensemble"))
	if err != nil {
		t.Fatal(err)
	}
	df := string(b)
	for _, want := range []string{"COPY gate/ ./", "./cmd/ensemblegate", "COPY manifest.json", "COPY harness/ /etc/ensemblegate/", "HARNESS_FILE=/etc/ensemblegate/harness.json"} {
		if !strings.Contains(df, want) {
			t.Errorf("Dockerfile.ensemble lacks %q", want)
		}
	}
	// The manifest the gate reads parses as the gate expects.
	m, _ := EnsembleManifest(ensemble.Ensemble{Name: "x", Members: []ensemble.Member{{Tool: "chat", Image: "local-llm/q"}}})
	raw, _ := json.Marshal(m)
	var back ensemble.Manifest
	if err := json.Unmarshal(raw, &back); err != nil || back.ID != "x" || back.ModelHost == "" {
		t.Errorf("manifest round trip = %+v, %v", back, err)
	}
}
