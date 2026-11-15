package builder

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/seanbrill/local-llm/internal/agent"
	"github.com/seanbrill/local-llm/internal/catalog"
	"github.com/seanbrill/local-llm/internal/filefreak"
	"github.com/seanbrill/local-llm/internal/native"
)

func nopLog(string) {}

func mustTarget(t *testing.T, id string) native.Target {
	t.Helper()
	tg, ok := native.TargetByID(id)
	if !ok {
		t.Fatalf("no target %s", id)
	}
	return tg
}

func TestPackageNames(t *testing.T) {
	if got := DefaultPackageName("Qwen2.5 Coder/7B", "macos-arm64"); got != "qwen2.5-coder-7b-macos-arm64" {
		t.Errorf("DefaultPackageName = %q", got)
	}
	if got := DefaultPackageName("!!", "linux-x64"); got != "model-linux-x64" {
		t.Errorf("DefaultPackageName(!!) = %q", got)
	}
	for name, ok := range map[string]bool{
		"qwen-macos-arm64": true, "a.b_c-1": true,
		"": false, ".hidden": false, "a..b": false, "a/b": false, "trailing.": false, "-x": false, strings.Repeat("a", 121): false,
	} {
		if ValidPackageName(name) != ok {
			t.Errorf("ValidPackageName(%q) = %v", name, !ok)
		}
	}
}

func TestNativeCapable(t *testing.T) {
	if why := NativeCapable(catalog.Model{Name: "Chat", Modality: "vision"}); why != "" {
		t.Errorf("vision: %s", why)
	}
	if why := NativeCapable(catalog.Model{Name: "Emb", Modality: "embedding"}); why == "" {
		t.Error("an embedding model is packaged")
	}
	if why := NativeCapable(catalog.Model{Name: "Flux", Modality: "image", Runtime: "python"}); !strings.Contains(why, "Python-runtime") {
		t.Errorf("python: %q", why)
	}
}

func TestNormalizeNativeHarness(t *testing.T) {
	if c, dropped, err := NormalizeNativeHarness(nil); c != nil || dropped || err != nil {
		t.Errorf("nil = %v %v %v", c, dropped, err)
	}
	in := &agent.Config{Tools: []string{"web_search", "files", "shell", "run_command"}, Workspace: "/home/me", SkillsDir: "/x"}
	in.Search.APIKey = "secret"
	in.Shell.Program, in.Shell.AllowRoot = []string{"/bin/bash", "-c"}, true
	c, dropped, err := NormalizeNativeHarness(in)
	if err != nil {
		t.Fatal(err)
	}
	if !dropped || strings.Join(c.Tools, ",") != "web_search,files" {
		t.Errorf("tools = %v, dropped %v", c.Tools, dropped)
	}
	if c.Workspace != "" || c.SkillsDir != "" || c.Search.APIKey != "" || len(c.Shell.Program) > 0 || c.Shell.AllowRoot || c.Limits.MaxSteps == 0 {
		t.Errorf("normalized = %+v", c)
	}
	if len(in.Tools) != 4 || in.Search.APIKey != "secret" {
		t.Error("the input was changed")
	}
	// The default tools never include the shell.
	c, dropped, err = NormalizeNativeHarness(&agent.Config{})
	if err != nil || dropped || c.EnabledTools()[agent.ToolRunCommand] {
		t.Errorf("defaults = %+v %v %v", c, dropped, err)
	}
	if _, _, err := NormalizeNativeHarness(&agent.Config{Tools: []string{"no-such-tool"}}); err == nil {
		t.Error("an unknown tool was accepted")
	}
}

type tarMember struct {
	name, link string
	mode       int64
	body       string
}

func writeTarGz(t *testing.T, p string, members []tarMember) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, m := range members {
		h := &tar.Header{Name: m.name, Mode: m.mode, Size: int64(len(m.body)), Typeflag: tar.TypeReg}
		switch {
		case strings.HasSuffix(m.name, "/"):
			h.Typeflag, h.Size = tar.TypeDir, 0
		case m.link != "":
			h.Typeflag, h.Linkname, h.Size = tar.TypeSymlink, m.link, 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(m.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestUnpackRuntimeTarGz(t *testing.T) {
	tg := mustTarget(t, "macos-arm64")
	dir := t.TempDir()
	archive := filepath.Join(dir, tg.Asset)
	writeTarGz(t, archive, []tarMember{
		{name: "llama-b12110/", mode: 0o755},
		{name: "llama-b12110/llama-server", mode: 0o755, body: "server"},
		{name: "llama-b12110/llama-cli", mode: 0o755, body: "cli"},
		{name: "llama-b12110/libllama.0.29.0.dylib", mode: 0o644, body: "lib"},
		{name: "llama-b12110/libllama.0.dylib", link: "libllama.0.29.0.dylib"},
		{name: "llama-b12110/libllama.dylib", link: "libllama.0.dylib"},
		{name: "llama-b12110/libescape.dylib", link: "../../etc/passwd"},
		{name: "llama-b12110/LICENSE", mode: 0o644, body: "MIT"},
		{name: "llama-b12110/deeper/libx.dylib", mode: 0o755, body: "x"},
		{name: "../evil.dylib", mode: 0o755, body: "x"},
	})
	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	rt, err := unpackRuntime(archive, out, tg)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range rt.Files {
		got = append(got, f.Name+">"+f.Link)
		if f.Link == "" && f.Mode&0o111 == 0 {
			t.Errorf("%s isn't executable: %v", f.Name, f.Mode)
		}
	}
	if strings.Join(got, " ") != "llama-server> libllama.0.29.0.dylib> libllama.0.dylib>libllama.0.29.0.dylib libllama.dylib>libllama.0.dylib" {
		t.Errorf("files = %v", got)
	}
	if len(rt.Licenses) != 1 || rt.Licenses[0].Name != "LICENSE" {
		t.Errorf("licenses = %+v", rt.Licenses)
	}
	entries, _ := os.ReadDir(out)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, " ") != "LICENSE libllama.0.29.0.dylib llama-server" {
		t.Errorf("unpacked = %v", names)
	}
	if _, err := os.Stat(filepath.Join(dir, "evil.dylib")); err == nil {
		t.Error("a member outside the archive's folder was written")
	}

	// A link to a file that isn't there, or no server, fails.
	writeTarGz(t, archive, []tarMember{
		{name: "llama-server", mode: 0o755, body: "server"},
		{name: "libggml.dylib", link: "libggml.0.dylib"},
	})
	if _, err := unpackRuntime(archive, t.TempDir(), tg); err == nil {
		t.Error("a dangling link was accepted")
	}
	writeTarGz(t, archive, []tarMember{{name: "libggml.dylib", mode: 0o755, body: "lib"}})
	if _, err := unpackRuntime(archive, t.TempDir(), tg); err == nil || !strings.Contains(err.Error(), "no llama-server") {
		t.Errorf("no server: %v", err)
	}
}

func TestUnpackRuntimeZip(t *testing.T) {
	tg := mustTarget(t, "windows-x64")
	dir := t.TempDir()
	archive := filepath.Join(dir, tg.Asset)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"llama-server.exe": "server", "llama-cli.exe": "cli", "ggml-vulkan.dll": "dll", "llama.dll": "dll",
		"LICENSE-LLVM-OpenMP": "openmp", "README.md": "readme",
	} {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()
	if err := os.WriteFile(archive, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	rt, err := unpackRuntime(archive, out, tg)
	if err != nil {
		t.Fatal(err)
	}
	if len(rt.Files) != 3 || len(rt.Licenses) != 1 || rt.Licenses[0].Name != "LICENSE-LLVM-OpenMP" {
		t.Errorf("runtime = %+v", rt)
	}
	if _, err := os.Stat(filepath.Join(out, "llama-cli.exe")); err == nil {
		t.Error("llama-cli was unpacked")
	}
}

func TestDownloadVerified(t *testing.T) {
	body := []byte("llama.cpp release archive")
	sum := sha256.Sum256(body)
	want := hex.EncodeToString(sum[:])
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "a.tar.gz")
	if err := downloadVerified(context.Background(), srv.URL+"/a.tar.gz", dest, want, "a.tar.gz", nopLog); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); !bytes.Equal(got, body) {
		t.Errorf("saved %q", got)
	}
	// Already there with the right SHA-256: no second download.
	if err := downloadVerified(context.Background(), srv.URL+"/a.tar.gz", dest, want, "a.tar.gz", nopLog); err != nil || hits.Load() != 1 {
		t.Errorf("second call: %v, %d requests", err, hits.Load())
	}
	other := filepath.Join(t.TempDir(), "b.tar.gz")
	err := downloadVerified(context.Background(), srv.URL+"/b.tar.gz", other, strings.Repeat("0", 64), "b.tar.gz", nopLog)
	if err == nil || !strings.Contains(err.Error(), "doesn't match") {
		t.Errorf("bad SHA-256: %v", err)
	}
	if _, err := os.Stat(other); err == nil {
		t.Error("a download with the wrong SHA-256 was kept")
	}
	if _, err := os.Stat(other + ".part"); err == nil {
		t.Error("the .part file was left behind")
	}
}

func TestCheckBinaryRefusesOtherFormats(t *testing.T) {
	p := filepath.Join(t.TempDir(), "llama-server")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"macos-arm64", "linux-x64", "windows-x64"} {
		if err := checkBinary(p, mustTarget(t, id)); err == nil {
			t.Errorf("%s: a shell script passed", id)
		}
	}
}

// testLauncher cross-compiles cmd/llmpack for darwin/arm64 the way the
// factory does (compileLauncher).
func testLauncher(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("cross-compiling the launcher")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	b := &Builder{BaseDir: root}
	p, err := b.compileLauncher(context.Background(), mustTarget(t, "macos-arm64"), t.TempDir(), nopLog)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCompiledLauncherIsSignedForAppleSilicon(t *testing.T) {
	p := testLauncher(t)
	tg := mustTarget(t, "macos-arm64")
	if err := checkBinary(p, tg); err != nil {
		t.Fatalf("the Go linker's darwin/arm64 build: %v", err)
	}
	if err := checkBinary(p, mustTarget(t, "linux-x64")); err == nil {
		t.Error("a macOS program passed as a Linux one")
	}
}

// fakeHF is a Hugging Face with one repository, org/tiny, at one commit.
func fakeHF(t *testing.T, license string) *httptest.Server {
	t.Helper()
	commit := strings.Repeat("ab", 20)
	mux := http.NewServeMux()
	mux.HandleFunc("/org/tiny/resolve/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Repo-Commit", commit)
		switch {
		case r.Method == http.MethodHead:
		case strings.HasSuffix(r.URL.Path, "/LICENSE"):
			_, _ = io.WriteString(w, "Apache License 2.0\n")
		case strings.HasSuffix(r.URL.Path, "/README.md"):
			_, _ = io.WriteString(w, "# Tiny\n")
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/api/models/org/tiny/revision/"+commit, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"private":  false,
			"gated":    false,
			"cardData": map[string]any{"license": license},
			"siblings": []map[string]string{{"rfilename": "LICENSE"}, {"rfilename": "README.md"}, {"rfilename": "tiny.gguf"}},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	old := newHF
	newHF = func() *filefreak.HF { return &filefreak.HF{Base: srv.URL, Client: srv.Client()} }
	t.Cleanup(func() { newHF = old })
	return srv
}

// fakeRuntimeCache puts an unpacked llama.cpp release for tg in the cache.
func fakeRuntimeCache(t *testing.T, b *Builder, tg native.Target) {
	t.Helper()
	dir := filepath.Join(b.llamaCacheDir(), tg.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"llama-server": "server", "libllama.0.29.0.dylib": "lib", "LICENSE": "MIT License"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rt := llamaRuntime{SHA256: tg.AssetSHA256,
		Files: []runtimeFile{
			{Name: "llama-server", Mode: 0o755},
			{Name: "libllama.0.29.0.dylib", Mode: 0o755},
			{Name: "libllama.0.dylib", Mode: 0o755, Link: "libllama.0.29.0.dylib"},
		},
		Licenses: []runtimeFile{{Name: "LICENSE", Mode: 0o644}},
	}
	data, _ := json.Marshal(rt)
	if err := os.WriteFile(filepath.Join(dir, runtimeIndex), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// unzipTo extracts a package zip the way unzip does, symlinks included.
func unzipTo(t *testing.T, zipPath, dest string) {
	t.Helper()
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		p := filepath.Join(dest, filepath.FromSlash(f.Name))
		if f.Mode().IsDir() {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		if f.Mode()&fs.ModeSymlink != 0 {
			if err := os.Symlink(string(data), p); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(p, data, f.Mode().Perm()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBuildNativePackage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	launcher := testLauncher(t)
	hf := fakeHF(t, "apache-2.0")
	b, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tg := mustTarget(t, "macos-arm64")
	fakeRuntimeCache(t, b, tg)
	// The factory image's prebuilt launcher.
	if err := os.MkdirAll(b.LaunchersDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(launcher)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.LaunchersDir(), LauncherFileName(tg)), data, 0o755); err != nil {
		t.Fatal(err)
	}
	// One skill, and the weights already downloaded.
	skill := filepath.Join(b.BaseDir, "skills", "notes")
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: notes\ndescription: Keeps notes.\n---\n\n# Notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := catalog.Model{ID: "tiny-chat", Name: "Tiny Chat", File: "tiny.gguf", URL: hf.URL + "/org/tiny/resolve/main/tiny.gguf"}
	weights := bytes.Repeat([]byte("GGUF"), 50_000)
	if err := os.WriteFile(b.ModelPath(m), weights, 0o644); err != nil {
		t.Fatal(err)
	}

	var logs []string
	pkg, err := b.BuildNativePackage(context.Background(), NativeOptions{
		Model: m, Target: tg.ID, SystemPrompt: "You are Tiny.", CtxSize: 4096,
		Harness: &agent.Config{Tools: []string{"files", "skills", "shell"}},
	}, func(s string) { logs = append(logs, s) })
	if err != nil {
		t.Fatalf("%v\n%s", err, strings.Join(logs, "\n"))
	}
	log := strings.Join(logs, "\n")
	for _, want := range []string{"from the cache", "Model license: apache-2.0", "shell tool is left out", "Package ready"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	if pkg.Name != "tiny-chat-macos-arm64" || !pkg.Shareable || pkg.License != "apache-2.0" || strings.Join(pkg.Toolsets, ",") != "files,skills" {
		t.Errorf("package = %+v", pkg)
	}
	zipPath := filepath.Join(b.PackagesDir(), pkg.File)
	if sum, _ := native.FileSHA256(zipPath); sum != pkg.SHA256 {
		t.Errorf("zip SHA-256 %s, reported %s", sum, pkg.SHA256)
	}
	if side, _ := os.ReadFile(zipPath + ".sha256"); string(side) != pkg.SHA256+"  "+pkg.File+"\n" {
		t.Errorf(".sha256 = %q", side)
	}

	// The archive: README first, SHA256SUMS last, the weights stored.
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
		if f.Name == pkg.Name+"/model/tiny.gguf" && f.Method != zip.Store {
			t.Error("the weights are deflated")
		}
	}
	zr.Close()
	if names[0] != pkg.Name+"/" || names[len(names)-1] != pkg.Name+"/SHA256SUMS" {
		t.Errorf("entries = %v", names)
	}

	root := filepath.Join(t.TempDir(), "x")
	unzipTo(t, zipPath, root)
	root = filepath.Join(root, pkg.Name)
	problems, err := native.VerifyPackage(root, nil)
	if err != nil || len(problems) > 0 {
		t.Errorf("verify: %v %v", problems, err)
	}
	man, err := native.ReadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := man.Engine(native.RoleChat)
	if e.Server != "bin/llama/llama-server" || e.Model != "model/tiny.gguf" || e.GPULayers != "all" || e.Accel != "metal" || e.CtxSize != 4096 ||
		man.SystemPromptFile != "config/system_prompt.txt" || man.HarnessFile != "harness/harness.json" || man.SkillsDir != "harness/skills" {
		t.Errorf("manifest = %+v %+v", man, e)
	}
	for rel, mode := range map[string]fs.FileMode{
		"Start.command": 0o755, "bin/llmpack": 0o755, "bin/llama/llama-server": 0o755, "README.txt": 0o644,
	} {
		fi, err := os.Stat(filepath.Join(root, rel))
		if err != nil || fi.Mode().Perm() != mode {
			t.Errorf("%s: %v %v", rel, fi, err)
		}
	}
	if target, err := os.Readlink(filepath.Join(root, "bin/llama/libllama.0.dylib")); err != nil || target != "libllama.0.29.0.dylib" {
		t.Errorf("link = %q %v", target, err)
	}
	if fi, err := os.Stat(filepath.Join(root, native.WorkspaceDir)); err != nil || !fi.IsDir() {
		t.Error("no workspace folder")
	}
	for _, rel := range []string{"licenses/model/LICENSE", "licenses/model/README.md", "licenses/llama.cpp/LICENSE", "harness/skills/notes/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("missing %s", rel)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(root, "model/tiny.gguf")); !bytes.Equal(got, weights) {
		t.Error("the weights changed")
	}
	hdata, _ := os.ReadFile(filepath.Join(root, "harness/harness.json"))
	cfg, err := agent.ParseConfig(hdata)
	if err != nil || strings.Join(cfg.Tools, ",") != "files,skills" || cfg.Workspace != "" {
		t.Errorf("harness.json = %s %v", hdata, err)
	}
	readme, _ := os.ReadFile(filepath.Join(root, "README.txt"))
	for _, want := range []string{"Start.command", "Open Anyway", "apache-2.0", native.LlamaCppBuild, "workspace"} {
		if !strings.Contains(string(readme), want) {
			t.Errorf("README lacks %q", want)
		}
	}
	if s, _ := os.ReadFile(filepath.Join(root, "config/system_prompt.txt")); string(s) != "You are Tiny." {
		t.Errorf("prompt = %q", s)
	}

	// The packages folder.
	list := b.NativePackages()
	if len(list) != 1 || list[0].Name != pkg.Name || list[0].SizeBytes != pkg.SizeBytes || list[0].ModelName != "Tiny Chat" {
		t.Errorf("list = %+v", list)
	}
	if _, err := b.NativePackagePath("../etc"); err == nil {
		t.Error("a path outside packages/ was accepted")
	}
	if err := b.RemoveNativePackage(pkg.Name); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{zipPath, zipPath + ".sha256", filepath.Join(b.PackagesDir(), pkg.Name+".json")} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s is still there", p)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(b.PackagesDir(), ".work")); len(entries) > 0 {
		t.Errorf("work folders left: %v", entries)
	}
}

func TestBuildNativePackageRefuses(t *testing.T) {
	b, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := catalog.Model{ID: "tiny", Name: "Tiny", File: "tiny.gguf"}
	for name, o := range map[string]NativeOptions{
		"unknown target": {Model: m, Target: "plan9-mips"},
		"bad name":       {Model: m, Target: "macos-arm64", Name: "../x"},
		"embedding":      {Model: catalog.Model{ID: "e", Name: "E", Modality: "embedding"}, Target: "macos-arm64"},
		"bad harness":    {Model: m, Target: "macos-arm64", Harness: &agent.Config{Tools: []string{"nope"}}},
	} {
		if _, err := b.BuildNativePackage(context.Background(), o, nopLog); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
