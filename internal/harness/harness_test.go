package harness

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewKey(t *testing.T) {
	a, err := NewKey("sk-x-")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewKey("sk-x-")
	if !strings.HasPrefix(a, "sk-x-") || len(a) != len("sk-x-")+48 {
		t.Fatalf("bad key %q", a)
	}
	if a == b {
		t.Fatal("two keys are equal")
	}
	if g, _ := NewGatewayKey(); !strings.HasPrefix(g, "sk-llmf-") {
		t.Fatalf("gateway key prefix: %q", g)
	}
	if k, _ := NewTerminalKey(); !strings.HasPrefix(k, "otk-") {
		t.Fatalf("terminal key prefix: %q", k)
	}
}

func TestOpenStoreCreatesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "harness.json")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	c := s.Get()
	if c.GatewayKey == "" || c.Terminal.APIKey == "" {
		t.Fatal("keys not generated")
	}
	if c.Terminal.Variant != DefaultVariant || c.Terminal.Port != DefaultTerminalPort || c.Terminal.Workspace != "" {
		t.Fatalf("defaults: %+v", c.Terminal)
	}
	if c.ReachHost() != DefaultReachHost {
		t.Fatalf("reach host %q", c.ReachHost())
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", fi.Mode().Perm())
	}
	// Reopening keeps the same keys.
	s2, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Get().GatewayKey != c.GatewayKey || s2.Get().Terminal.APIKey != c.Terminal.APIKey {
		t.Fatal("keys changed on reopen")
	}
}

func TestOpenStoreTightensPermissionsAndFillsGaps(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	path := filepath.Join(t.TempDir(), "harness.json")
	if err := os.WriteFile(path, []byte(`{"gateway_key":"sk-llmf-keep","terminal":{"port":9100}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	c := s.Get()
	if c.GatewayKey != "sk-llmf-keep" || c.Terminal.Port != 9100 || c.Terminal.APIKey == "" || c.Terminal.Variant != "slim" {
		t.Fatalf("config %+v", c)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", fi.Mode().Perm())
	}
}

func TestOpenStoreRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harness.json")
	_ = os.WriteFile(path, []byte("{not json"), 0o600)
	s, err := OpenStore(path)
	if err == nil || s != nil {
		t.Fatalf("want error and no store, got %v, %v", s, err)
	}
	if data, _ := os.ReadFile(path); string(data) != "{not json" {
		t.Fatal("corrupt file was overwritten")
	}
}

func TestStoreUpdateIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harness.json")
	s, _ := OpenStore(path)
	before := s.Get()
	_, err := s.Update(func(c *Config) error {
		c.Terminal.Port = 9999
		return errors.New("nope")
	})
	if err == nil || s.Get().Terminal.Port != before.Terminal.Port {
		t.Fatal("failed update leaked")
	}
	if _, err := s.Update(func(c *Config) error { c.Terminal.Port = 9001; return nil }); err != nil {
		t.Fatal(err)
	}
	var onDisk Config
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &onDisk); err != nil || onDisk.Terminal.Port != 9001 {
		t.Fatalf("not persisted: %s", data)
	}
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".harness-*"))
	if len(matches) != 0 {
		t.Fatalf("temp files left: %v", matches)
	}
}

func TestValidatePortAndReachHost(t *testing.T) {
	for _, p := range []int{1024, 8798, 65535} {
		if err := ValidatePort(p); err != nil {
			t.Errorf("port %d: %v", p, err)
		}
	}
	for _, p := range []int{0, 80, 1023, 65536, -1} {
		if ValidatePort(p) == nil {
			t.Errorf("port %d accepted", p)
		}
	}
	for _, h := range []string{"host.docker.internal", "127.0.0.1", "::1", "my-box.local", "10.0.0.5"} {
		if err := ValidateReachHost(h); err != nil {
			t.Errorf("%q: %v", h, err)
		}
	}
	for _, h := range []string{"", "a b", "evil.com/x", "x:80", "-bad", "a\nb", "http://x"} {
		if ValidateReachHost(h) == nil {
			t.Errorf("%q accepted", h)
		}
	}
}

func TestNormalizeHostPath(t *testing.T) {
	cases := map[string]string{
		"/Users/me/a/../b/":     "/Users/me/b",
		`C:\Users\me\proj\..\x`: "C:/Users/me/x",
		"  /tmp/x  ":            "/tmp/x",
		"C:/":                   "C:/",
	}
	for in, want := range cases {
		if got := NormalizeHostPath(in); got != want {
			t.Errorf("NormalizeHostPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateWorkspace(t *testing.T) {
	const repo, home = "/Users/me/llm-factory", "/Users/me"
	ok := []string{
		"",
		"/Users/me/projects/demo",
		"/Users/me/llm-factory/workspace",
		"/Users/me/llm-factory/workspace/demo",
		"/Volumes/Data/projects",
		"/home/me/code",
		"/tmp/scratch",
		"/mnt/c/Users/me/proj",
		`C:\Users\me\work\proj`,
	}
	for _, p := range ok {
		if err := ValidateWorkspace(p, repo, home); err != nil {
			t.Errorf("%q rejected: %v", p, err)
		}
	}
	bad := []string{
		"workspace",              // relative
		"~/workspace",            // relative (no shell expansion)
		"/",                      // root
		`C:\`,                    // drive root
		"/Users",                 // all users
		"/Users/me",              // home
		"/Users/other",           // another home
		"/home/me",               // a home on Linux
		`C:\Users\me`,            // a home on Windows
		"/Volumes/Data",          // a whole disk
		"/Users/me/.ssh",         // hidden
		"/Users/me/proj/../.aws", // hidden after cleaning
		"/Users/me/Library/Mail", // app data
		"/etc",                   // system
		"/usr/local/src",         // inside system
		"/var",                   // contains /var/run (docker.sock)
		"/private",               // contains /private/etc
		"/var/run/docker",        // inside system
		"c:/windows/system32",    // system, case-insensitive
		"/Users/me/llm-factory",  // the repo (holds config/)
		"/Users/me/llm-factory/config/x",
		"/Users/me/llm-factory/skills",  // shared read-only on purpose
		"/Users/me/llm-factory/scripts", // run on the host
		"/Users/me/llm-factory/workspace-old",
		"/Users/me/a:b",      // breaks -v
		"/Users/me/proj\nX",  // newline
		"/Users/me/proj\x00", // NUL
	}
	for _, p := range bad {
		if ValidateWorkspace(p, repo, home) == nil {
			t.Errorf("%q accepted", p)
		}
	}
	// Without a known home, a whole home folder is still refused.
	if ValidateWorkspace("/Users/someone", repo, "") == nil {
		t.Error("home folder accepted without HOST_HOME")
	}
	// A Windows repo.
	if ValidateWorkspace(`C:\src\llm-factory\config`, `C:\src\llm-factory`, `C:\Users\me`) == nil {
		t.Error("windows config dir accepted")
	}
	if err := ValidateWorkspace(`C:\src\llm-factory\workspace`, `C:\src\llm-factory`, `C:\Users\me`); err != nil {
		t.Errorf("windows default workspace rejected: %v", err)
	}
}

func testPaths() Paths {
	return Paths{BaseDir: "/app", HostDir: "/Users/me/llm-factory", HostHome: "/Users/me"}
}

func defaultTerminal() TerminalConfig {
	return TerminalConfig{Variant: "slim", Port: 8798, APIKey: "otk-test"}
}

func argPairs(args []string, flag string) []string {
	var out []string
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag {
			out = append(out, args[i+1])
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestTerminalRunArgsSlim(t *testing.T) {
	spec, err := BuildTerminalSpec(defaultTerminal(), testPaths(), "docker")
	if err != nil {
		t.Fatal(err)
	}
	args := spec.RunArgs()
	if args[0] != "run" || args[len(args)-1] != "ghcr.io/open-webui/open-terminal:slim" {
		t.Fatalf("args %v", args)
	}
	if got := argPairs(args, "--name"); len(got) != 1 || got[0] != TerminalContainer {
		t.Fatalf("name %v", got)
	}
	if got := argPairs(args, "-p"); len(got) != 1 || got[0] != "127.0.0.1:8798:8000" {
		t.Fatalf("publish %v (must be loopback)", got)
	}
	if got := argPairs(args, "--memory"); len(got) != 1 || got[0] != "2048m" {
		t.Fatalf("memory %v", got)
	}
	if !contains(argPairs(args, "--restart"), "unless-stopped") || !contains(argPairs(args, "--pids-limit"), "1024") {
		t.Fatalf("restart/pids missing: %v", args)
	}
	labels := argPairs(args, "--label")
	if !contains(labels, "local-llm.tool=harness") || !contains(labels, SpecLabel+"="+spec.Hash()) {
		t.Fatalf("labels %v", labels)
	}
	env := argPairs(args, "-e")
	for _, want := range []string{
		"OPEN_TERMINAL_API_KEY=otk-test",
		"OPEN_TERMINAL_START_DIR=~/workspace",
		"OPEN_TERMINAL_FILE_BROWSER_ROOT={{home}}/workspace",
		"OPEN_TERMINAL_ENABLE_BROWSER=false",
		"OPEN_TERMINAL_ENABLE_DESKTOP=false",
	} {
		if !contains(env, want) {
			t.Errorf("env %q missing from %v", want, env)
		}
	}
	vols := argPairs(args, "-v")
	want := []string{
		"/Users/me/llm-factory/workspace:/home/user/workspace",
		"/Users/me/llm-factory/skills:/home/user/.agents/skills:ro",
	}
	if strings.Join(vols, "|") != strings.Join(want, "|") {
		t.Fatalf("volumes %v, want %v", vols, want)
	}
	if contains(args, "--security-opt") {
		t.Fatal("docker run shouldn't disable labels")
	}
}

func TestTerminalRunArgsVariants(t *testing.T) {
	cfg := defaultTerminal()
	cfg.Variant = "latest"
	cfg.ShareClaudeSkills = true
	cfg.Workspace = "/Users/me/projects"
	cfg.MemoryMB = 3000
	spec, err := BuildTerminalSpec(cfg, testPaths(), "podman")
	if err != nil {
		t.Fatal(err)
	}
	args := spec.RunArgs()
	env := argPairs(args, "-e")
	if contains(env, "OPEN_TERMINAL_ENABLE_BROWSER=false") {
		t.Fatal("full image shouldn't disable the browser")
	}
	vols := argPairs(args, "-v")
	if !contains(vols, "/Users/me/projects:/home/user/workspace") ||
		!contains(vols, "/Users/me/.claude/skills:/home/user/.claude/skills:ro") {
		t.Fatalf("volumes %v", vols)
	}
	if !contains(argPairs(args, "--security-opt"), "label=disable") {
		t.Fatal("podman needs label=disable")
	}
	if !contains(argPairs(args, "--memory"), "3000m") {
		t.Fatal("custom memory ignored")
	}

	// The hash tracks everything that changes the container.
	base, _ := BuildTerminalSpec(defaultTerminal(), testPaths(), "docker")
	other := defaultTerminal()
	other.Port = 8797
	moved, _ := BuildTerminalSpec(other, testPaths(), "docker")
	rekeyed := defaultTerminal()
	rekeyed.APIKey = "otk-other"
	rk, _ := BuildTerminalSpec(rekeyed, testPaths(), "docker")
	if base.Hash() == moved.Hash() || base.Hash() == rk.Hash() || base.Hash() == spec.Hash() {
		t.Fatal("hash doesn't track changes")
	}
	again, _ := BuildTerminalSpec(defaultTerminal(), testPaths(), "docker")
	if base.Hash() != again.Hash() {
		t.Fatal("hash isn't stable")
	}
}

func TestBuildTerminalSpecRejects(t *testing.T) {
	p := testPaths()
	cases := map[string]func(*TerminalConfig){
		"variant":   func(c *TerminalConfig) { c.Variant = "evil; rm -rf /" },
		"port":      func(c *TerminalConfig) { c.Port = 80 },
		"key":       func(c *TerminalConfig) { c.APIKey = "" },
		"workspace": func(c *TerminalConfig) { c.Workspace = "/Users/me" },
	}
	for name, mut := range cases {
		cfg := defaultTerminal()
		mut(&cfg)
		if _, err := BuildTerminalSpec(cfg, p, "docker"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	noHome := p
	noHome.HostHome = ""
	cfg := defaultTerminal()
	cfg.ShareClaudeSkills = true
	if _, err := BuildTerminalSpec(cfg, noHome, "docker"); err == nil {
		t.Error("sharing ~/.claude/skills without a known home accepted")
	}
	weird := p
	weird.HostDir = "/Users/me/odd:dir"
	if _, err := BuildTerminalSpec(defaultTerminal(), weird, "docker"); err == nil {
		t.Error("repo path with ':' accepted as a mount source")
	}
}

func TestWindowsPaths(t *testing.T) {
	p := Paths{BaseDir: "/app", HostDir: `C:\Users\me\llm-factory`, HostHome: `C:\Users\me`}
	cfg := defaultTerminal()
	cfg.ShareClaudeSkills = true
	spec, err := BuildTerminalSpec(cfg, p, "docker")
	if err != nil {
		t.Fatal(err)
	}
	vols := argPairs(spec.RunArgs(), "-v")
	want := []string{
		"C:/Users/me/llm-factory/workspace:/home/user/workspace",
		"C:/Users/me/llm-factory/skills:/home/user/.agents/skills:ro",
		"C:/Users/me/.claude/skills:/home/user/.claude/skills:ro",
	}
	if strings.Join(vols, "|") != strings.Join(want, "|") {
		t.Fatalf("volumes %v, want %v", vols, want)
	}
	if local, ok := p.LocalPath(`c:\users\me\llm-factory\workspace`); !ok || local != filepath.Join("/app", "workspace") {
		t.Fatalf("LocalPath = %q, %v", local, ok)
	}
	if _, ok := p.LocalPath(`D:\elsewhere`); ok {
		t.Fatal("a path outside the repo mapped locally")
	}
}
