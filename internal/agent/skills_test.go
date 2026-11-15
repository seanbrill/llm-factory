package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, dir, folder, text string) {
	t.Helper()
	p := filepath.Join(dir, folder)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "SKILL.md"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func skillKit(t *testing.T) (string, map[string]Tool) {
	dir := t.TempDir()
	writeSkill(t, dir, "web-research", "---\nname: web-research\ndescription: Search and cite sources.\n---\n\n# Web Research\n\nSearch, then read.\n")
	writeSkill(t, dir, "folded", "---\nname: folded\ndescription: >\n  A long\n  description.\n---\nBody.\n")
	writeSkill(t, dir, "no-desc", "---\nname: no-desc\n---\nNothing.\n")
	writeSkill(t, dir, ".hidden", "---\nname: hidden\ndescription: Hidden.\n---\n")
	os.MkdirAll(filepath.Join(dir, "web-research", "scripts"), 0o755)
	os.WriteFile(filepath.Join(dir, "web-research", "scripts", "check.sh"), []byte("echo check\n"), 0o644)
	os.WriteFile(filepath.Join(filepath.Dir(dir), "outside.txt"), []byte("SECRET"), 0o644)
	tools := map[string]Tool{}
	for _, tool := range skillToolset(dir, 8192) {
		tools[tool.Name()] = tool
	}
	return dir, tools
}

func TestListAndViewSkills(t *testing.T) {
	dir, tools := skillKit(t)
	skills, err := ListSkills(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 2 || skills[0].Name != "folded" || skills[0].Description != "A long description." || skills[1].Name != "web-research" {
		t.Errorf("skills = %+v", skills)
	}
	out, err := tools[ToolListSkills].Run(context.Background(), nil)
	if err != nil || !strings.Contains(out, "- web-research: Search and cite sources.") || strings.Contains(out, "hidden") || strings.Contains(out, "no-desc") {
		t.Errorf("list = %q %v", out, err)
	}
	out, err = tools[ToolViewSkill].Run(context.Background(), json.RawMessage(`{"name":"web-research"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "Skill web-research: Search and cite sources.\n\n# Web Research") || strings.Contains(out, "---") ||
		!strings.Contains(out, "scripts/check.sh") {
		t.Errorf("view = %q", out)
	}
	out, err = tools[ToolViewSkill].Run(context.Background(), json.RawMessage(`{"name":"web-research","file":"scripts/check.sh"}`))
	if err != nil || !strings.Contains(out, "echo check") {
		t.Errorf("view file = %q %v", out, err)
	}
	for _, bad := range []string{`{"name":"../web-research"}`, `{"name":"nope"}`, `{"name":"web-research","file":"../../outside.txt"}`, `{"name":"web-research","file":"/etc/passwd"}`} {
		if out, err := tools[ToolViewSkill].Run(context.Background(), json.RawMessage(bad)); err == nil {
			t.Errorf("%s: %q", bad, out)
		}
	}
	if _, err := tools[ToolViewSkill].Run(context.Background(), json.RawMessage(`{"name":"nope"}`)); err == nil || !strings.Contains(err.Error(), "installed: folded, web-research") {
		t.Errorf("unknown skill error = %v", err)
	}
	if tt, ok := tools[ToolViewSkill].(TrustedTool); !ok || !tt.TrustedOutput() {
		t.Error("skills should be trusted output")
	}
	note := tools[ToolListSkills].(Noter).SystemNote()
	if !strings.Contains(note, "view_skill") || !strings.Contains(note, "- web-research: Search and cite sources.") {
		t.Errorf("note = %q", note)
	}
}

func TestRepoSkillsParse(t *testing.T) {
	// The repo's starter skills must all be usable by the baked harness.
	dir := filepath.Join("..", "..", "skills")
	skills, err := ListSkills(dir)
	if err != nil {
		t.Skipf("no skills folder: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	n := 0
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			n++
		}
	}
	if len(skills) != n || n == 0 {
		t.Errorf("parsed %d of %d skills: %+v", len(skills), n, skills)
	}
}

func TestBuiltins(t *testing.T) {
	dir, _ := skillKit(t)
	cfg := Config{Workspace: filepath.Join(t.TempDir(), "new-ws"), SkillsDir: dir}.WithDefaults()
	reg, un, err := Builtins(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, info := range reg.Describe() {
		names = append(names, info.Toolset+"/"+info.Name)
	}
	want := "web_search/search_web,fetch_url/fetch_url,files/list_files,files/read_file,files/write_file,files/replace_file_content,files/glob_search,files/grep_search,skills/list_skills,skills/view_skill"
	if strings.Join(names, ",") != want || len(un) != 0 {
		t.Errorf("tools = %v\nunavailable = %v", names, un)
	}
	if _, err := os.Stat(cfg.Workspace); err != nil {
		t.Error("the workspace wasn't created")
	}

	// Single tools, a missing skills folder, and brave without a key.
	cfg2 := Config{Tools: []string{"read_file", "skills", "web_search"}, Workspace: t.TempDir(), SkillsDir: filepath.Join(dir, "missing"),
		Search: SearchConfig{Backend: SearchBrave}}.WithDefaults()
	reg2, un2, err := Builtins(cfg2)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(reg2.Tools()); got != 1 {
		t.Errorf("tools = %v", reg2.Describe())
	}
	var reasons []string
	for _, u := range un2 {
		reasons = append(reasons, u.Tool+": "+u.Reason)
	}
	joined := strings.Join(reasons, "\n")
	if !strings.Contains(joined, "list_skills: the skills folder") || !strings.Contains(joined, "search_web: the brave backend needs HARNESS_BRAVE_API_KEY") {
		t.Errorf("unavailable = %s", joined)
	}
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	if err := r.Register("x", echoTool(false)); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("x", echoTool(false)); err == nil {
		t.Error("duplicate accepted")
	}
	bad := &FuncTool{ToolName: "bad name", Params: json.RawMessage(`{}`)}
	if err := r.Register("x", bad); err == nil {
		t.Error("bad name accepted")
	}
	noParams := &FuncTool{ToolName: "np"}
	if err := r.Register("x", noParams); err == nil {
		t.Error("missing parameters accepted")
	}
	var s map[string]any
	json.Unmarshal(Schema(echoTool(false)), &s)
	if s["type"] != "function" || s["function"].(map[string]any)["name"] != "echo" {
		t.Errorf("schema = %v", s)
	}
	r.Remove("echo")
	if _, ok := r.Lookup("echo"); ok {
		t.Error("remove failed")
	}
}
