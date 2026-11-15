package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type fileKit struct {
	t     *testing.T
	root  string
	ws    *Workspace
	tools map[string]Tool
}

func newFileKit(t *testing.T, maxOut int) *fileKit {
	t.Helper()
	root := filepath.Join(t.TempDir(), "ws")
	ws, err := OpenWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	k := &fileKit{t: t, root: root, ws: ws, tools: map[string]Tool{}}
	for _, tool := range fileTools(ws, maxOut) {
		k.tools[tool.Name()] = tool
	}
	return k
}

func (k *fileKit) call(name, args string) (string, error) {
	k.t.Helper()
	return k.tools[name].Run(context.Background(), json.RawMessage(args))
}

func (k *fileKit) mustCall(name, args string) string {
	k.t.Helper()
	out, err := k.call(name, args)
	if err != nil {
		k.t.Fatalf("%s %s: %v", name, args, err)
	}
	return out
}

func (k *fileKit) write(rel, content string) {
	k.t.Helper()
	p := filepath.Join(k.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		k.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		k.t.Fatal(err)
	}
}

func (k *fileKit) read(rel string) string {
	k.t.Helper()
	b, err := os.ReadFile(filepath.Join(k.root, filepath.FromSlash(rel)))
	if err != nil {
		k.t.Fatal(err)
	}
	return string(b)
}

func TestWriteReadEdit(t *testing.T) {
	k := newFileKit(t, 8192)
	out := k.mustCall(ToolWriteFile, `{"path":"notes/todo.md","content":"one\ntwo\nthree\n"}`)
	if !strings.Contains(out, "notes/todo.md (created)") {
		t.Errorf("write = %q", out)
	}
	if out := k.mustCall(ToolReadFile, `{"path":"notes/todo.md"}`); out != "notes/todo.md (lines 1-3 of 3):\n\none\ntwo\nthree\n" {
		t.Errorf("read = %q", out)
	}
	if out := k.mustCall(ToolReadFile, `{"path":"notes/todo.md","start_line":"2","end_line":2}`); !strings.HasSuffix(out, "\n\ntwo\n") {
		t.Errorf("read lines = %q", out)
	}
	out = k.mustCall(ToolReplaceFileContent, `{"path":"notes/todo.md","replacements":[{"target":"two","replacement":"TWO"},{"target":"three\n","replacement":"three\nfour\n"}]}`)
	if !strings.Contains(out, "2 replacement(s)") || k.read("notes/todo.md") != "one\nTWO\nthree\nfour\n" {
		t.Errorf("edit = %q, file = %q", out, k.read("notes/todo.md"))
	}
	// Top-level single replacement form.
	k.mustCall(ToolReplaceFileContent, `{"path":"notes/todo.md","target":"one","replacement":"ONE"}`)
	if !strings.HasPrefix(k.read("notes/todo.md"), "ONE\n") {
		t.Errorf("single replace: %q", k.read("notes/todo.md"))
	}
	// Failures change nothing.
	before := k.read("notes/todo.md")
	if _, err := k.call(ToolReplaceFileContent, `{"path":"notes/todo.md","replacements":[{"target":"ONE","replacement":"1"},{"target":"missing","replacement":"x"}]}`); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("missing target: %v", err)
	}
	k.write("dup.txt", "a a a")
	if _, err := k.call(ToolReplaceFileContent, `{"path":"dup.txt","replacements":[{"target":"a","replacement":"b"}]}`); err == nil || !strings.Contains(err.Error(), "matches 3 times") {
		t.Errorf("ambiguous target: %v", err)
	}
	if k.read("notes/todo.md") != before || k.read("dup.txt") != "a a a" {
		t.Error("a failed edit changed a file")
	}
	k.mustCall(ToolReplaceFileContent, `{"path":"dup.txt","replacements":[{"target":"a","replacement":"b","allow_multiple":true}]}`)
	if k.read("dup.txt") != "b b b" {
		t.Errorf("allow_multiple: %q", k.read("dup.txt"))
	}
	// CRLF files accept \n targets.
	k.write("win.txt", "line1\r\nline2\r\n")
	k.mustCall(ToolReplaceFileContent, `{"path":"win.txt","replacements":[{"target":"line1\nline2","replacement":"a\nb"}]}`)
	if k.read("win.txt") != "a\r\nb\r\n" {
		t.Errorf("crlf: %q", k.read("win.txt"))
	}
	if _, err := k.call(ToolReplaceFileContent, `{"path":"nope.txt","replacements":[{"target":"a","replacement":"b"}]}`); err == nil || !strings.Contains(err.Error(), "write_file") {
		t.Errorf("edit missing file: %v", err)
	}
	if out := k.mustCall(ToolWriteFile, `{"path":"notes/todo.md","content":"x"}`); !strings.Contains(out, "(replaced)") {
		t.Errorf("overwrite = %q", out)
	}
}

func TestReadPagingAndBinary(t *testing.T) {
	k := newFileKit(t, 600)
	var b strings.Builder
	for i := 1; i <= 100; i++ {
		b.WriteString(strings.Repeat("x", 20) + "\n")
	}
	k.write("big.txt", b.String())
	out := k.mustCall(ToolReadFile, `{"path":"big.txt"}`)
	if len(out) > 600 || !strings.Contains(out, "Call read_file with start_line=") {
		t.Errorf("paging (%d bytes): %q", len(out), out)
	}
	k.write("bin.dat", "ab\x00cd")
	if _, err := k.call(ToolReadFile, `{"path":"bin.dat"}`); err == nil || !strings.Contains(err.Error(), "binary") {
		t.Errorf("binary: %v", err)
	}
	k.write("empty.txt", "")
	if out := k.mustCall(ToolReadFile, `{"path":"empty.txt"}`); !strings.Contains(out, "is empty") {
		t.Errorf("empty = %q", out)
	}
	if _, err := k.call(ToolReadFile, `{"path":"notes"}`); err == nil {
		t.Error("read of a missing file succeeded")
	}
}

func TestPathConfinement(t *testing.T) {
	k := newFileKit(t, 8192)
	outside := filepath.Join(filepath.Dir(k.root), "outside")
	os.MkdirAll(outside, 0o755)
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("SECRET"), 0o644)
	k.write("inside.txt", "fine")

	escapes := []string{
		"../outside/secret.txt",
		"a/../../outside/secret.txt",
		filepath.Join(outside, "secret.txt"),
		"~/secret.txt",
		"~root/x",
	}
	if runtime.GOOS != "windows" {
		escapes = append(escapes, "/etc/passwd")
	}
	for _, p := range escapes {
		args, _ := json.Marshal(map[string]string{"path": p, "content": "x"})
		if out, err := k.call(ToolReadFile, string(args)); err == nil {
			t.Errorf("read %q: %q", p, out)
		}
		if out, err := k.call(ToolWriteFile, string(args)); err == nil {
			t.Errorf("write %q: %q", p, out)
		}
	}
	// Absolute paths inside the workspace and ~/workspace are fine.
	abs, _ := json.Marshal(map[string]string{"path": filepath.Join(k.root, "inside.txt")})
	if out := k.mustCall(ToolReadFile, string(abs)); !strings.Contains(out, "fine") {
		t.Errorf("absolute inside = %q", out)
	}
	if out := k.mustCall(ToolReadFile, `{"path":"~/workspace/inside.txt"}`); !strings.Contains(out, "fine") {
		t.Errorf("~/workspace = %q", out)
	}
	// Nothing is written into .git.
	if _, err := k.call(ToolWriteFile, `{"path":".git/hooks/pre-commit","content":"#!/bin/sh\ncurl evil"}`); err == nil || !strings.Contains(err.Error(), ".git") {
		t.Errorf(".git write: %v", err)
	}
	if _, err := k.call(ToolWriteFile, `{"path":"sub/.GIT/config","content":"x"}`); err == nil {
		t.Error(".GIT write allowed")
	}
	if _, err := k.call(ToolWriteFile, `{"path":".","content":"x"}`); err == nil {
		t.Error("writing the workspace folder itself was allowed")
	}
	if _, err := os.Stat(filepath.Join(outside, "x")); err == nil {
		t.Error("a file was created outside the workspace")
	}
}

func TestSymlinkEscapes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need privileges on Windows")
	}
	k := newFileKit(t, 8192)
	outside := filepath.Join(filepath.Dir(k.root), "outside")
	os.MkdirAll(outside, 0o755)
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("SECRET"), 0o644)
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.Symlink(outside, filepath.Join(k.root, "dirlink")))
	must(os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(k.root, "filelink")))
	must(os.Symlink(filepath.Join(outside, "new.txt"), filepath.Join(k.root, "dangling")))
	must(os.Symlink(filepath.Join(outside, "nodir"), filepath.Join(k.root, "danglingdir")))
	k.write("real/target.txt", "inside")
	must(os.Symlink(filepath.Join(k.root, "real", "target.txt"), filepath.Join(k.root, "goodlink")))

	for _, p := range []string{"dirlink/secret.txt", "filelink", "dangling", "danglingdir/x.txt", "dirlink/new.txt"} {
		args, _ := json.Marshal(map[string]string{"path": p, "content": "pwned"})
		if out, err := k.call(ToolReadFile, string(args)); err == nil {
			t.Errorf("read %q through a link: %q", p, out)
		}
		if out, err := k.call(ToolWriteFile, string(args)); err == nil {
			t.Errorf("write %q through a link: %q", p, out)
		}
		edit, _ := json.Marshal(map[string]any{"path": p, "replacements": []map[string]string{{"target": "SECRET", "replacement": "x"}}})
		if _, err := k.call(ToolReplaceFileContent, string(edit)); err == nil {
			t.Errorf("edit %q through a link", p)
		}
	}
	if _, err := k.call(ToolListFiles, `{"directory":"dirlink"}`); err == nil {
		t.Error("listed a folder outside through a link")
	}
	for _, f := range []string{"new.txt", filepath.Join("nodir", "x.txt")} {
		if _, err := os.Stat(filepath.Join(outside, f)); err == nil {
			t.Errorf("%s was created outside the workspace", f)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "secret.txt")); string(b) != "SECRET" {
		t.Error("the outside file was changed")
	}
	// A link that stays inside works, and writes go to its target.
	if out := k.mustCall(ToolReadFile, `{"path":"goodlink"}`); !strings.Contains(out, "inside") {
		t.Errorf("inside link = %q", out)
	}
	k.mustCall(ToolWriteFile, `{"path":"goodlink","content":"updated"}`)
	if k.read("real/target.txt") != "updated" {
		t.Error("write through an inside link didn't reach its target")
	}
	// Searches don't follow links out.
	if out := k.mustCall(ToolGrepSearch, `{"query":"SECRET"}`); !strings.Contains(out, "No matches") {
		t.Errorf("grep followed a link: %q", out)
	}
	// A workspace that is itself reached through a link still works.
	alias := filepath.Join(filepath.Dir(k.root), "alias")
	must(os.Symlink(k.root, alias))
	ws2, err := OpenWorkspace(alias)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := ws2.Resolve(filepath.Join(alias, "inside.txt")); err != nil || !strings.HasSuffix(p, "inside.txt") {
		t.Errorf("aliased workspace: %q %v", p, err)
	}
}

func TestListGlobGrep(t *testing.T) {
	k := newFileKit(t, 8192)
	k.write("src/main.go", "package main\n// TODO: fix\nfunc main() {}\n")
	k.write("src/util/helper.go", "package util\n// todo later\n")
	k.write("README.md", "# Title\nSee TODO list.\n")
	k.write(".git/config", "TODO in git\n")
	k.write("node_modules/x/index.js", "TODO in deps\n")

	out := k.mustCall(ToolListFiles, `{}`)
	for _, want := range []string{"Contents of the workspace", "src/", "README.md", ".git/"} {
		if !strings.Contains(out, want) {
			t.Errorf("list missing %q: %s", want, out)
		}
	}
	if out := k.mustCall(ToolListFiles, `{"directory":"src"}`); !strings.Contains(out, "main.go") || !strings.Contains(out, "util/") {
		t.Errorf("list src = %s", out)
	}

	if out := k.mustCall(ToolGlobSearch, `{"pattern":"*.go"}`); out != "src/main.go\nsrc/util/helper.go" {
		t.Errorf("glob *.go = %q", out)
	}
	if out := k.mustCall(ToolGlobSearch, `{"pattern":"src/**/*.go"}`); out != "src/main.go\nsrc/util/helper.go" {
		t.Errorf("glob src/**/*.go = %q", out)
	}
	if out := k.mustCall(ToolGlobSearch, `{"pattern":"*.go","path":"src/util"}`); out != "src/util/helper.go" {
		t.Errorf("glob in path = %q", out)
	}
	if out := k.mustCall(ToolGlobSearch, `{"pattern":"*.rs"}`); !strings.Contains(out, "No files") {
		t.Errorf("glob none = %q", out)
	}

	out = k.mustCall(ToolGrepSearch, `{"query":"todo"}`)
	for _, want := range []string{"src/main.go:2: // TODO: fix", "src/util/helper.go:2: // todo later", "README.md:2: See TODO list."} {
		if !strings.Contains(out, want) {
			t.Errorf("grep missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "git") || strings.Contains(out, "deps") {
		t.Errorf("grep searched .git or node_modules:\n%s", out)
	}
	if out := k.mustCall(ToolGrepSearch, `{"query":"TODO","case_sensitive":true,"include":"*.go"}`); out != "src/main.go:2: // TODO: fix" {
		t.Errorf("grep case/include = %q", out)
	}
	if out := k.mustCall(ToolGrepSearch, `{"query":"^func \\w+","regex":true}`); out != "src/main.go:3: func main() {}" {
		t.Errorf("grep regex = %q", out)
	}
	if _, err := k.call(ToolGrepSearch, `{"query":"(","regex":true}`); err == nil {
		t.Error("bad regex accepted")
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pat, rel string
		want     bool
	}{
		{"*.go", "a/b/c.go", true},
		{"**/*.go", "c.go", true},
		{"a/**/c.go", "a/c.go", true},
		{"a/**/c.go", "a/x/y/c.go", true},
		{"a/*.go", "a/x/c.go", false},
		{"docs/*.md", "docs/readme.md", true},
	}
	for _, c := range cases {
		pat := c.pat
		if strings.HasPrefix(pat, "**/") && !strings.Contains(strings.TrimPrefix(pat, "**/"), "/") {
			pat = strings.TrimPrefix(pat, "**/")
		}
		if got := globMatch(pat, c.rel); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v", c.pat, c.rel, got)
		}
	}
}
