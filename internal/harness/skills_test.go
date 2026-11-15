package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseFrontmatter(t *testing.T) {
	text := "---\nname: demo\ndescription: >\n  Folded line one\n  and two.\nnotes: |\n  a\n  b\nquoted: \"hi\"\n# comment\n---\n# Body\ntext\n"
	fm, body := ParseFrontmatter(text)
	if fm["name"] != "demo" || fm["description"] != "Folded line one and two." || fm["notes"] != "a\nb" || fm["quoted"] != "hi" {
		t.Fatalf("frontmatter %#v", fm)
	}
	if body != "# Body\ntext" {
		t.Fatalf("body %q", body)
	}
	if fm, _ := ParseFrontmatter("no frontmatter"); len(fm) != 0 {
		t.Fatal("parsed frontmatter from plain text")
	}
	if fm, _ := ParseFrontmatter("---\nname: x\n"); len(fm) != 0 {
		t.Fatal("parsed unterminated frontmatter")
	}
}

func TestParseSkillWarnings(t *testing.T) {
	good := ParseSkill("demo", "---\nname: demo\ndescription: Does a thing.\n---\n# Demo\n")
	if !good.Listed || len(good.Warnings) != 0 || good.Description != "Does a thing." {
		t.Fatalf("good skill: %+v", good)
	}
	cases := map[string]Skill{
		"crlf":       ParseSkill("demo", "---\r\nname: demo\r\ndescription: Does a thing.\r\n---\r\n"),
		"no desc":    ParseSkill("demo", "---\nname: demo\n---\n"),
		"no period":  ParseSkill("demo", "---\nname: demo\ndescription: Does a thing\n---\n"),
		"bad name":   ParseSkill("Demo Skill", "---\nname: Demo Skill\ndescription: Does a thing.\n---\n"),
		"mismatch":   ParseSkill("demo", "---\nname: other\ndescription: Does a thing.\n---\n"),
		"too long":   ParseSkill("demo", "---\nname: demo\ndescription: "+strings.Repeat("x", 1100)+".\n---\n"),
		"no frontmt": ParseSkill("demo", "# Demo\n"),
	}
	for name, s := range cases {
		if len(s.Warnings) == 0 {
			t.Errorf("%s: no warning", name)
		}
	}
	if cases["crlf"].Listed || cases["no desc"].Listed || cases["no frontmt"].Listed {
		t.Error("unlistable skills marked listed")
	}
}

func TestListSkills(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, text string) {
		p := filepath.Join(dir, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("zeta/SKILL.md", "---\nname: zeta\ndescription: Z.\n---\n")
	write("alpha/SKILL.md", "---\nname: alpha\ndescription: A.\n---\n")
	write(".hidden/SKILL.md", "---\nname: hidden\ndescription: H.\n---\n")
	write("empty/README.md", "not a skill")
	write("README.md", "top-level file")
	skills, err := ListSkills(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 2 || skills[0].Name != "alpha" || skills[1].Name != "zeta" {
		t.Fatalf("skills %+v", skills)
	}
	if _, err := ListSkills(filepath.Join(dir, "missing")); err != ErrNoSkillsDir {
		t.Fatalf("missing dir: %v", err)
	}
}

// TestRepoSkills holds the shipped skills to the authoring standard Open WebUI
// uses: lowercase-hyphenated names matching the folder, a description of at
// most 1024 characters ending in a period, LF endings, and the section order.
func TestRepoSkills(t *testing.T) {
	dir := filepath.Join("..", "..", "skills")
	skills, err := ListSkills(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) < 3 {
		t.Fatalf("expected at least 3 shipped skills, found %d", len(skills))
	}
	sections := []string{"## When to Use", "## Prerequisites", "## How to Run", "## Quick Reference", "## Procedure", "## Pitfalls", "## Verification"}
	marketing := []string{"powerful", "comprehensive", "seamless", "advanced", "robust"}
	for _, s := range skills {
		if !s.Listed || len(s.Warnings) > 0 {
			t.Errorf("%s: listed=%v warnings=%v", s.Dir, s.Listed, s.Warnings)
		}
		if len(s.Name) > 64 {
			t.Errorf("%s: name longer than 64 characters", s.Name)
		}
		lower := strings.ToLower(s.Description)
		for _, w := range marketing {
			if strings.Contains(lower, w) {
				t.Errorf("%s: description uses %q", s.Name, w)
			}
		}
		data, err := os.ReadFile(filepath.Join(dir, s.Dir, "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if strings.Contains(text, "\r") {
			t.Errorf("%s: CRLF line endings", s.Name)
		}
		_, body := ParseFrontmatter(text)
		if !strings.HasPrefix(body, "# ") {
			t.Errorf("%s: body must start with a '# Title'", s.Name)
		}
		last := -1
		for _, sec := range sections {
			i := strings.Index(body, "\n"+sec+"\n")
			if i < 0 {
				t.Errorf("%s: missing section %q", s.Name, sec)
				continue
			}
			if i < last {
				t.Errorf("%s: section %q out of order", s.Name, sec)
			}
			last = i
		}
		if s.Lines < 40 || s.Lines > 120 {
			t.Errorf("%s: %d lines; keep skills between 40 and 120 lines", s.Name, s.Lines)
		}
	}
}
