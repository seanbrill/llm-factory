package native

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTargets(t *testing.T) {
	list := Targets()
	if len(list) == 0 || list[0].ID != "macos-arm64" {
		t.Fatalf("targets = %+v", list)
	}
	seen := map[string]bool{}
	for _, tg := range list {
		if seen[tg.ID] {
			t.Errorf("two %s targets", tg.ID)
		}
		seen[tg.ID] = true
		if len(tg.AssetSHA256) != 64 || !strings.Contains(tg.Asset, LlamaCppBuild) {
			t.Errorf("%s: asset %s %s", tg.ID, tg.Asset, tg.AssetSHA256)
		}
		if !strings.HasPrefix(tg.AssetURL(), "https://github.com/ggml-org/llama.cpp/releases/download/"+LlamaCppBuild+"/") {
			t.Errorf("%s: %s", tg.ID, tg.AssetURL())
		}
		if got, ok := TargetByID(tg.ID); !ok || got.ID != tg.ID {
			t.Errorf("TargetByID(%s)", tg.ID)
		}
	}
	mac, _ := TargetByID("macos-arm64")
	if mac.Accel != AccelMetal || mac.GPULayers != "all" || mac.Preview || mac.ServerName() != "llama-server" {
		t.Errorf("mac = %+v", mac)
	}
	win, _ := TargetByID("windows-x64")
	if win.ServerName() != "llama-server.exe" || win.LauncherName() != "llmpack.exe" {
		t.Errorf("windows names: %s %s", win.ServerName(), win.LauncherName())
	}
	if _, ok := TargetByID("plan9"); ok {
		t.Error("plan9 found")
	}
}

func validManifest() *Manifest {
	return &Manifest{
		Schema: ManifestSchema, Name: "Tiny", ModelID: "tiny", Target: "macos-arm64", Created: "2026-11-15T00:00:00Z",
		Engines: []Engine{{Kind: "llama.cpp", Role: RoleChat, Server: "bin/llama/llama-server", Model: "model/tiny.gguf", CtxSize: 4096}},
	}
}

func TestManifestValidate(t *testing.T) {
	if err := validManifest().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Manifest){
		"schema":      func(m *Manifest) { m.Schema = "other/2" },
		"no engines":  func(m *Manifest) { m.Engines = nil },
		"no chat":     func(m *Manifest) { m.Engines[0].Role = "image" },
		"two chats":   func(m *Manifest) { m.Engines = append(m.Engines, m.Engines[0]) },
		"escape":      func(m *Manifest) { m.Engines[0].Model = "../../etc/passwd" },
		"absolute":    func(m *Manifest) { m.Engines[0].Server = "/usr/bin/llama-server" },
		"backslash":   func(m *Manifest) { m.HarnessFile = `harness\harness.json` },
		"drive":       func(m *Manifest) { m.SkillsDir = "C:/skills" },
		"no server":   func(m *Manifest) { m.Engines[0].Server = "" },
		"inject mode": func(m *Manifest) { m.InjectMode = "sometimes" },
		"ctx":         func(m *Manifest) { m.Engines[0].CtxSize = -1 },
	} {
		m := validManifest()
		change(m)
		if err := m.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestManifestRoundTrip(t *testing.T) {
	root := t.TempDir()
	m := validManifest()
	m.SystemPromptFile = "config/system_prompt.txt"
	data, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ManifestName), data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := got.Engine(RoleChat)
	if !ok || e.Model != "model/tiny.gguf" || got.SystemPromptFile != m.SystemPromptFile {
		t.Errorf("read back %+v", got)
	}
	if _, ok := got.Engine("image"); ok {
		t.Error("an image engine was found")
	}
	p, err := Resolve(root, "model/./tiny.gguf")
	if err != nil || p != filepath.Join(root, "model", "tiny.gguf") {
		t.Errorf("Resolve = %s %v", p, err)
	}
	if _, err := Resolve(root, "model/../../x"); err == nil {
		t.Error("Resolve left the package")
	}
}

func TestChecksums(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{"README.txt": "hello\n", "bin/llmpack": "launcher", "model/m.gguf": "weights"}
	var list []Checksum
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		sum, err := FileSHA256(p)
		if err != nil {
			t.Fatal(err)
		}
		list = append(list, Checksum{SHA256: sum, Path: rel})
	}
	data := FormatChecksums(list)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 || !strings.HasSuffix(lines[0], "  README.txt") || !strings.HasSuffix(lines[2], "  model/m.gguf") {
		t.Errorf("SHA256SUMS =\n%s", data)
	}
	// sha256sum's own tool reads it, where there is one.
	if err := os.WriteFile(filepath.Join(root, ChecksumsName), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if tool, err := exec.LookPath("shasum"); err == nil && runtime.GOOS != "windows" {
		cmd := exec.Command(tool, "-a", "256", "-c", ChecksumsName)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("shasum -c: %v\n%s", err, out)
		}
	}
	if problems, err := VerifyPackage(root, nil); err != nil || len(problems) > 0 {
		t.Fatalf("verify: %v %v", problems, err)
	}
	if err := os.WriteFile(filepath.Join(root, "model", "m.gguf"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "README.txt")); err != nil {
		t.Fatal(err)
	}
	var checked []string
	problems, err := VerifyPackage(root, func(p string) { checked = append(checked, p) })
	if err != nil || len(problems) != 2 || len(checked) != 3 {
		t.Errorf("problems = %v, checked %v, %v", problems, checked, err)
	}

	parsed, err := ParseChecksums([]byte(strings.Repeat("a", 64) + " *bin/x\r\n\n"))
	if err != nil || len(parsed) != 1 || parsed[0].Path != "bin/x" {
		t.Errorf("binary-mode line: %+v %v", parsed, err)
	}
	for _, bad := range []string{"abc  x", strings.Repeat("g", 64) + "  x", strings.Repeat("a", 64) + "  ../x"} {
		if _, err := ParseChecksums([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// TestHelperProcess is a stand-in engine for the supervisor tests.
func TestHelperProcess(t *testing.T) {
	switch os.Getenv("NATIVE_TEST_HELPER") {
	case "exit3":
		os.Exit(3)
	case "sleep":
		time.Sleep(time.Minute)
		os.Exit(0)
	}
}

func helper(mode string) Proc {
	return Proc{Name: mode, Path: os.Args[0], Args: []string{"-test.run=TestHelperProcess"}, Env: []string{"NATIVE_TEST_HELPER=" + mode}}
}

func TestSupervisor(t *testing.T) {
	s := NewSupervisor()
	var out bytes.Buffer
	sleeper := helper("sleep")
	sleeper.Stdout = &out
	if err := s.Start(sleeper); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(helper("exit3")); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-s.Exited():
		if e.Name != "exit3" || e.Code() != 3 {
			t.Errorf("first exit = %+v code %d", e, e.Code())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("nothing exited")
	}
	if !s.Running("sleep") || s.Running("exit3") {
		t.Errorf("running: sleep %v exit3 %v", s.Running("sleep"), s.Running("exit3"))
	}
	start := time.Now()
	s.Stop(5 * time.Second)
	if s.Running("sleep") {
		t.Error("still running after Stop")
	}
	if time.Since(start) > 20*time.Second {
		t.Error("Stop took too long")
	}
	if err := s.Start(helper("sleep")); err == nil {
		t.Error("Start after Stop worked")
	}
	if err := NewSupervisor().Start(Proc{Name: "missing", Path: filepath.Join(t.TempDir(), "nope")}); err == nil {
		t.Error("a missing program started")
	}
	if (Exit{}).Code() != 0 {
		t.Error("a clean exit isn't 0")
	}
}
