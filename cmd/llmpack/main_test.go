package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/seanbrill/local-llm/internal/agent"
	"github.com/seanbrill/local-llm/internal/native"
)

func TestParseFlags(t *testing.T) {
	o, err := parseFlags(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if o.host != "127.0.0.1" || o.port != 8080 || o.portSet || o.noBrowser || o.gpuLayers != "" {
		t.Errorf("defaults = %+v", o)
	}
	o, err = parseFlags([]string{"--port", "9000", "--no-browser", "--gpu-layers", "20", "--allow-origin", "https://a.example/, https://b.example", "--", "--flash-attn", "on"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !o.portSet || o.port != 9000 || !o.noBrowser || o.gpuLayers != "20" ||
		strings.Join(o.allowOrigins, " ") != "https://a.example https://b.example" || strings.Join(o.llamaArgs, " ") != "--flash-attn on" {
		t.Errorf("parsed = %+v", o)
	}
	for _, bad := range [][]string{{"--port", "70000"}, {"--gpu-layers", "most"}, {"--gpu-layers", "-1"}, {"--ctx-size", "-5"}, {"--nope"}} {
		if _, err := parseFlags(bad, io.Discard); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestRunVersionAndHelp(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--version"}, &out, &errOut, func(string) string { return "" }); code != 0 {
		t.Fatalf("--version exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "llmpack ") || !strings.Contains(out.String(), native.LlamaCppBuild) {
		t.Errorf("--version = %q", out.String())
	}
	errOut.Reset()
	if code := run([]string{"--help"}, &out, &errOut, func(string) string { return "" }); code != 0 || !strings.Contains(errOut.String(), "-no-browser") {
		t.Errorf("--help exit %d: %s", code, errOut.String())
	}
	if code := run([]string{"--package", t.TempDir()}, &out, &errOut, func(string) string { return "" }); code != 1 {
		t.Errorf("a folder without a manifest: exit %d", code)
	}
}

// hostTarget is the target of this computer, if packages can be made for it.
func hostTarget(t *testing.T) native.Target {
	t.Helper()
	for _, tg := range native.Targets() {
		if tg.GOOS == runtime.GOOS && tg.GOARCH == runtime.GOARCH {
			return tg
		}
	}
	t.Skipf("no package target for %s/%s", runtime.GOOS, runtime.GOARCH)
	return native.Target{}
}

// writePackage lays out a package folder for tg (with stand-in programs).
func writePackage(t *testing.T, tg native.Target, harness string) string {
	t.Helper()
	root := t.TempDir()
	m := &native.Manifest{
		Schema: native.ManifestSchema, Name: "Tiny Chat", ModelID: "tiny", Target: tg.ID, Created: "2026-11-15T00:00:00Z",
		License: "apache-2.0", InjectMode: "missing", SystemPromptFile: "config/system_prompt.txt",
		Engines: []native.Engine{{Kind: "llama.cpp", Role: native.RoleChat, Server: "bin/llama/" + tg.ServerName(),
			Accel: tg.Accel, GPULayers: tg.GPULayers, Model: "model/tiny.gguf", CtxSize: 4096, Modality: "text"}},
	}
	files := map[string]string{
		"bin/llama/" + tg.ServerName(): "server",
		"model/tiny.gguf":              "GGUF",
		"config/system_prompt.txt":     "You are Tiny.\n",
	}
	if harness != "" {
		m.HarnessFile, m.SkillsDir = "harness/harness.json", "harness/skills"
		files["harness/harness.json"] = harness
		files["harness/skills/notes/SKILL.md"] = "---\nname: notes\ndescription: Keeps notes.\n---\n"
	}
	data, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	files[native.ManifestName] = string(data)
	var sums []native.Checksum
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		sum, _ := native.FileSHA256(p)
		sums = append(sums, native.Checksum{SHA256: sum, Path: rel})
	}
	if err := os.WriteFile(filepath.Join(root, native.ChecksumsName), native.FormatChecksums(sums), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestRunVerify(t *testing.T) {
	tg, _ := native.TargetByID("macos-arm64")
	root := writePackage(t, tg, "")
	var out, errOut bytes.Buffer
	if code := run([]string{"--package", root, "--verify"}, &out, &errOut, os.Getenv); code != 0 {
		t.Fatalf("verify exit %d: %s %s", code, out.String(), errOut.String())
	}
	if err := os.WriteFile(filepath.Join(root, "model", "tiny.gguf"), []byte("damaged"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"--package", root, "--verify"}, &out, &errOut, os.Getenv); code != 1 || !strings.Contains(errOut.String(), "model/tiny.gguf") {
		t.Errorf("damaged package: exit %d: %s", code, errOut.String())
	}
}

func TestPrepare(t *testing.T) {
	tg := hostTarget(t)
	root := writePackage(t, tg, `{"tools": ["files", "skills", "shell"]}`)
	env := map[string]string{"HARNESS_TOOLS": "+shell", "HARNESS_WORKSPACE": "/elsewhere", "HARNESS_SKILLS_DIR": "/elsewhere"}
	getenv := func(k string) string { return env[k] }
	var logs bytes.Buffer
	logger := log.New(&logs, "", 0)
	m, err := native.ReadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	o, _ := parseFlags([]string{"--ctx-size", "2048", "--", "--flash-attn", "on"}, io.Discard)
	l, err := prepare(root, m, o, getenv, logger)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(l.llama.Args(), " ")
	for _, want := range []string{"--model " + filepath.Join(root, "model", "tiny.gguf"), "--host 127.0.0.1", "--ctx-size 2048", "--no-webui", "--n-gpu-layers " + tg.GPULayers, "--jinja", "--flash-attn on"} {
		if !strings.Contains(args, want) {
			t.Errorf("args lack %q: %s", want, args)
		}
	}
	if l.prompt != "You are Tiny." || l.server != filepath.Join(root, "bin", "llama", tg.ServerName()) {
		t.Errorf("prompt %q server %s", l.prompt, l.server)
	}
	if l.harness == nil || l.harness.agent == nil {
		t.Fatalf("no harness:\n%s", logs.String())
	}
	cfg := l.harness.cfg
	if cfg.EnabledTools()[agent.ToolRunCommand] || strings.Join(cfg.Tools, ",") != "files,skills" {
		t.Errorf("tools = %v", cfg.Tools)
	}
	if cfg.Workspace != filepath.Join(root, native.WorkspaceDir) || cfg.SkillsDir != filepath.Join(root, "harness", "skills") {
		t.Errorf("workspace %s skills %s", cfg.Workspace, cfg.SkillsDir)
	}
	if fi, err := os.Stat(cfg.Workspace); err != nil || !fi.IsDir() {
		t.Error("the workspace folder wasn't made")
	}
	if !strings.Contains(logs.String(), "shell tool stays off") {
		t.Errorf("log:\n%s", logs.String())
	}

	// /v1/package and the chat page, through the guard.
	h := l.handler(http.NotFoundHandler(), l.harnessAgent(), 8080)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/v1/package", nil))
	var info packageInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil || rec.Code != 200 {
		t.Fatalf("/v1/package %d: %s", rec.Code, rec.Body.String())
	}
	if info.Name != "Tiny Chat" || info.Target != tg.ID || info.CtxSize != 2048 || !info.Prompt || !info.Harness.Enabled ||
		info.Harness.Web || strings.Join(info.Harness.Toolsets, ",") != "files,skills" || info.Ready {
		t.Errorf("info = %+v", info)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://localhost:8080/", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<html") || rec.Header().Get("Content-Security-Policy") == "" {
		t.Errorf("chat page %d %q", rec.Code, rec.Header())
	}

	// --no-harness and HARNESS=off serve the plain model.
	o.noHarness = true
	if l, err := prepare(root, m, o, getenv, logger); err != nil || l.harness != nil {
		t.Errorf("--no-harness: %v %v", l.harness, err)
	}
	o.noHarness = false
	env["HARNESS"] = "off"
	if l, err := prepare(root, m, o, getenv, logger); err != nil || l.harness != nil {
		t.Errorf("HARNESS=off: %v %v", l.harness, err)
	}
}

func TestPrepareRefusesOtherSystems(t *testing.T) {
	var other native.Target
	for _, tg := range native.Targets() {
		if tg.GOOS != runtime.GOOS || tg.GOARCH != runtime.GOARCH {
			other = tg
			break
		}
	}
	root := writePackage(t, other, "")
	m, err := native.ReadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	o, _ := parseFlags(nil, io.Discard)
	if _, err := prepare(root, m, o, os.Getenv, log.New(io.Discard, "", 0)); err == nil || !strings.Contains(err.Error(), other.Label) {
		t.Errorf("a package for %s: %v", other.Label, err)
	}
}

func TestPrepareMissingFiles(t *testing.T) {
	tg := hostTarget(t)
	root := writePackage(t, tg, "")
	m, _ := native.ReadManifest(root)
	o, _ := parseFlags(nil, io.Discard)
	if err := os.Remove(filepath.Join(root, "model", "tiny.gguf")); err != nil {
		t.Fatal(err)
	}
	if _, err := prepare(root, m, o, os.Getenv, log.New(io.Discard, "", 0)); err == nil || !strings.Contains(err.Error(), "model file is missing") {
		t.Errorf("no weights: %v", err)
	}
}

func TestGuard(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	local := &guard{next: ok, loopbackOnly: true, origins: map[string]bool{"https://app.example": true}}
	open := &guard{next: ok, loopbackOnly: false, origins: map[string]bool{}}
	cases := []struct {
		g            *guard
		host, origin string
		pass         bool
	}{
		{local, "127.0.0.1:8080", "", true},                        // curl, an SDK
		{local, "localhost:8080", "http://localhost:8080", true},   // the chat page
		{local, "[::1]:8080", "http://[::1]:8080", true},           // the chat page over IPv6
		{local, "127.0.0.1:8080", "https://evil.example", false},   // another site in the browser
		{local, "127.0.0.1:8080", "http://localhost:8080", false},  // a different origin
		{local, "127.0.0.1:8080", "null", false},                   // a sandboxed frame or file:// page
		{local, "evil.example:8080", "http://evil.example", false}, // DNS rebinding
		{local, "evil.example:8080", "", false},
		{local, "127.0.0.1:8080", "https://app.example/", true}, // --allow-origin
		{open, "192.168.1.5:8080", "", true},                    // --host 0.0.0.0
		{open, "192.168.1.5:8080", "http://192.168.1.5:8080", true},
		{open, "192.168.1.5:8080", "http://evil.example", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodPost, "http://"+c.host+"/v1/chat/completions", strings.NewReader("{}"))
		r.Host = c.host
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		rec := httptest.NewRecorder()
		c.g.ServeHTTP(rec, r)
		if got := rec.Code == http.StatusTeapot; got != c.pass {
			t.Errorf("host %s origin %q: passed %v (%d)", c.host, c.origin, got, rec.Code)
		}
	}
}

func TestHosts(t *testing.T) {
	for host, want := range map[string]bool{"127.0.0.1": true, "localhost": true, "::1": true, "[::1]": true, "127.0.0.2": true, "0.0.0.0": false, "192.168.1.5": false, "": false} {
		if isLoopback(host) != want {
			t.Errorf("isLoopback(%q) = %v", host, !want)
		}
	}
	for host, want := range map[string]string{"127.0.0.1": "127.0.0.1", "0.0.0.0": "127.0.0.1", "": "127.0.0.1", "::": "127.0.0.1", "::1": "[::1]", "192.168.1.5": "192.168.1.5"} {
		if got := browseHost(host); got != want {
			t.Errorf("browseHost(%q) = %q", host, got)
		}
	}
}

func TestListenFallsBackToTheNextPort(t *testing.T) {
	first, err := listen("127.0.0.1", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	taken := first.Addr().(*net.TCPAddr).Port
	if ln, err := listen("127.0.0.1", taken, true); err == nil {
		ln.Close()
		t.Error("a taken port was listened on with --port set")
	}
	ln, err := listen("127.0.0.1", taken, false)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if ln.Addr().(*net.TCPAddr).Port == taken {
		t.Error("listened on the taken port")
	}
}

func TestWithoutShell(t *testing.T) {
	if got, dropped := withoutShell(nil); got != nil || dropped {
		t.Errorf("nil = %v %v", got, dropped)
	}
	got, dropped := withoutShell([]string{"files", "shell", "run_command", "web_search"})
	if !dropped || strings.Join(got, ",") != "files,web_search" {
		t.Errorf("got %v %v", got, dropped)
	}
}
