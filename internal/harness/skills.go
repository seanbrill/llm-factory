package harness

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Skill is one skill folder (a directory holding SKILL.md).
type Skill struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Dir         string   `json:"dir"`                // folder name under skills/
	Lines       int      `json:"lines"`              // SKILL.md length
	Listed      bool     `json:"listed"`             // Open Terminal will list it
	Warnings    []string `json:"warnings,omitempty"` // authoring problems
}

// MaxDescription is the longest description Open Terminal keeps.
const MaxDescription = 1024

var skillNameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ParseFrontmatter mirrors Open Terminal's parse_skill_frontmatter: the file
// must start with "---\n"; "key: value" lines (quotes stripped) and folded (>)
// or literal (|) blocks are read until the closing "\n---". It returns the
// fields and the body after the frontmatter.
func ParseFrontmatter(text string) (map[string]string, string) {
	if !strings.HasPrefix(text, "---\n") {
		return map[string]string{}, strings.TrimSpace(text)
	}
	end := strings.Index(text[4:], "\n---")
	if end < 0 {
		return map[string]string{}, strings.TrimSpace(text)
	}
	end += 4
	fm := map[string]string{}
	lines := strings.Split(text[4:end], "\n")
	for i := 0; i < len(lines); {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		colon := strings.IndexByte(line, ':')
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || colon < 0 {
			i++
			continue
		}
		key := strings.TrimSpace(line[:colon])
		value := strings.TrimSpace(line[colon+1:])
		if value == ">" || value == "|" {
			var block []string
			i++
			for i < len(lines) && (strings.HasPrefix(lines[i], " ") || strings.HasPrefix(lines[i], "\t")) {
				block = append(block, strings.TrimSpace(lines[i]))
				i++
			}
			if value == "|" {
				fm[key] = strings.Join(block, "\n")
			} else {
				fm[key] = strings.Join(block, " ")
			}
			continue
		}
		fm[key] = strings.Trim(value, `"'`)
		i++
	}
	body := ""
	if nl := strings.IndexByte(text[end+1:], '\n'); nl >= 0 {
		body = strings.TrimSpace(text[end+1+nl+1:])
	}
	return fm, body
}

// ErrNoSkillsDir means the skills folder isn't there (e.g. not mounted).
var ErrNoSkillsDir = errors.New("skills folder not found")

// ListSkills reads every <dir>/<name>/SKILL.md, the way Open Terminal does:
// hidden folders are skipped, and a skill with no description isn't listed
// (it is still returned here, with Listed=false and a warning, so the UI can
// say why).
func ListSkills(dir string) ([]Skill, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoSkillsDir
	}
	if err != nil {
		return nil, err
	}
	out := []Skill{}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		out = append(out, ParseSkill(e.Name(), string(data)))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ParseSkill parses one SKILL.md and checks it against the authoring rules.
func ParseSkill(dirName, text string) Skill {
	s := Skill{Dir: dirName, Lines: strings.Count(text, "\n") + 1}
	if strings.HasPrefix(text, "---\r\n") {
		s.Warnings = append(s.Warnings, "SKILL.md has Windows (CRLF) line endings; Open Terminal only reads LF frontmatter, so this skill won't be listed. Convert it to LF.")
		text = strings.ReplaceAll(text, "\r\n", "\n")
	}
	fm, _ := ParseFrontmatter(text)
	s.Name = strings.TrimSpace(fm["name"])
	if s.Name == "" {
		s.Name = dirName
	}
	s.Description = strings.TrimSpace(fm["description"])
	s.Listed = s.Description != "" && len(s.Warnings) == 0
	switch {
	case s.Description == "":
		s.Warnings = append(s.Warnings, "missing description: Open Terminal skips skills without one")
	case len(s.Description) > MaxDescription:
		s.Warnings = append(s.Warnings, "description is longer than 1024 characters and will be cut off")
	case !strings.HasSuffix(s.Description, "."):
		s.Warnings = append(s.Warnings, "description should end with a period")
	}
	if !skillNameRe.MatchString(s.Name) {
		s.Warnings = append(s.Warnings, "name should be lowercase words joined by hyphens")
	}
	if s.Name != dirName {
		s.Warnings = append(s.Warnings, "name doesn't match its folder name")
	}
	return s
}
