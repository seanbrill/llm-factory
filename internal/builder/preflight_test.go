package builder

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seanbrill/local-llm/internal/catalog"
)

func TestPreflightOne(t *testing.T) {
	dir := t.TempDir()
	b := &Builder{BaseDir: dir}
	m := catalog.Model{ID: "q", Name: "Qwen", File: "q.gguf", SizeGB: 5}
	fam := modelFamily(m)
	if err := os.MkdirAll(filepath.Join(dir, "docker"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"cpu", "vulkan"} {
		body := "FROM x\nRUN cmake --build build\n"
		if err := os.WriteFile(filepath.Join(dir, "docker", dockerfileName(fam, c)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	refused := errors.New("dial unix /run/podman/podman.sock: connect: connection refused")

	cases := []struct {
		name            string
		engine, compute string
		mem             map[string]engineMem
		wantBuild       string
		wantTransfer    bool
		wantCompute     string
		wantErr         string // substring; "" = no error
	}{
		{"fits on target", "podman", "vulkan",
			map[string]engineMem{"podman": {GB: 12}, "docker": {GB: 18}}, "podman", false, "vulkan", ""},
		{"borrows docker", "podman", "vulkan",
			map[string]engineMem{"podman": {GB: 2}, "docker": {GB: 18}}, "docker", true, "vulkan", ""},
		{"nobody has enough", "podman", "vulkan",
			map[string]engineMem{"podman": {GB: 2}, "docker": {GB: 4}}, "podman", false, "vulkan", "Not enough memory"},
		{"target down", "podman", "vulkan",
			map[string]engineMem{"podman": {Err: refused}, "docker": {GB: 18}}, "podman", false, "vulkan", "odman"},
		{"not installed", "podman", "cpu",
			map[string]engineMem{"podman": {Err: errNotInstalled}, "docker": {GB: 18}}, "podman", false, "cpu", "isn't installed"},
		{"no cuda variant falls back to cpu", "docker", "cuda",
			map[string]engineMem{"docker": {GB: 18}}, "docker", false, "cpu", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := preflightOne(b, PreflightItem{Model: m, Engine: c.engine, Compute: c.compute}, c.mem)
			if p.BuildEngine != c.wantBuild || p.Transfer != c.wantTransfer || p.Compute != c.wantCompute {
				t.Fatalf("got build=%s transfer=%v compute=%s, want %s %v %s (plan %+v)",
					p.BuildEngine, p.Transfer, p.Compute, c.wantBuild, c.wantTransfer, c.wantCompute, p)
			}
			if c.wantErr == "" && p.Error != "" {
				t.Fatalf("unexpected error %q", p.Error)
			}
			if c.wantErr != "" && !strings.Contains(p.Error, c.wantErr) {
				t.Fatalf("error %q, want it to mention %q", p.Error, c.wantErr)
			}
			if !p.Compiles || p.NeedGB == 0 && p.Error == "" {
				t.Fatalf("a cmake Dockerfile should compile with a memory floor: %+v", p)
			}
		})
	}

	// A family that doesn't compile builds where it runs, whatever the memory.
	if err := os.WriteFile(filepath.Join(dir, "docker", dockerfileName(fam, "cpu")), []byte("FROM x\nRUN pip install y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := preflightOne(b, PreflightItem{Model: m, Engine: "podman", Compute: "cpu"}, map[string]engineMem{"podman": {GB: 2}})
	if p.Compiles || p.BuildEngine != "podman" || p.Transfer || p.Error != "" || p.NeedGB != 0 {
		t.Fatalf("non-compiling build: %+v", p)
	}
}
