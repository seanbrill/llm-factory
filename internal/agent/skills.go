package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Skills are folders holding a SKILL.md: YAML front matter with a name and a
// one-line description, then instructions (the format of the repo's skills/
// folder, Open Terminal and Claude). The model sees the list in the system
// note and loads a skill's instructions with view_skill when a task matches.

const (
	maxSkillsListed   = 50
	maxSkillNoteDesc  = 300
	maxSkillFileBytes = 256 << 10
)

// SkillInfo is one installed skill.
type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Dir         string `json:"dir"` // folder name
}

// ParseFrontmatter reads SKILL.md front matter the way Open Terminal's
// parse_skill_frontmatter does: the file must start with "---\n"; "key: value"
// lines (quotes stripped) and folded (>) or literal (|) blocks are read until
// the closing "\n---". It returns the fields and the body after it.
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

// ListSkills returns the usable skills in dir: visible folders with a SKILL.md
// that has a description, sorted by name.
func ListSkills(dir string) ([]SkillInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []SkillInfo
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") || !isDirEntry(dir, e) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		fm, _ := ParseFrontmatter(strings.ReplaceAll(string(data), "\r\n", "\n"))
		desc := strings.TrimSpace(fm["description"])
		if desc == "" {
			continue
		}
		name := strings.TrimSpace(fm["name"])
		if name == "" {
			name = e.Name()
		}
		out = append(out, SkillInfo{Name: name, Description: desc, Dir: e.Name()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// isDirEntry follows a symbolic link to see whether an entry is a folder.
func isDirEntry(dir string, e fs.DirEntry) bool {
	if e.IsDir() {
		return true
	}
	if e.Type()&fs.ModeSymlink != 0 {
		fi, err := os.Stat(filepath.Join(dir, e.Name()))
		return err == nil && fi.IsDir()
	}
	return false
}

var skillArgRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type skillTools struct {
	dir    string
	maxOut int
}

// skillToolset builds list_skills and view_skill over dir.
func skillToolset(dir string, maxOut int) []Tool {
	s := &skillTools{dir: dir, maxOut: maxOut}
	return []Tool{
		&skillTool{FuncTool: FuncTool{
			ToolName: ToolListSkills,
			Desc:     "List the installed skills: written instructions for particular kinds of task.",
			Params:   json.RawMessage(`{"type":"object","properties":{}}`),
			Fn:       s.list,
			Trusted:  true,
		}, s: s},
		&FuncTool{
			ToolName: ToolViewSkill,
			Desc:     "Load a skill's instructions by name. Do this before a task the skill covers, then follow them.",
			Params: json.RawMessage(`{"type":"object","properties":{` +
				`"name":{"type":"string","description":"The skill's name, from the list of skills."},` +
				`"file":{"type":"string","description":"Optional: another file inside the skill's folder to read."}` +
				`},"required":["name"]}`),
			Fn:      s.view,
			Trusted: true,
		},
	}
}

// skillTool is list_skills; it also puts the skill list in the system note.
type skillTool struct {
	FuncTool
	s *skillTools
}

func (t *skillTool) SystemNote() string {
	skills, err := ListSkills(t.s.dir)
	if err != nil || len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Skills: before a task one of these covers, call view_skill with its name and follow its instructions.")
	for i, sk := range skills {
		if i == maxSkillsListed {
			fmt.Fprintf(&b, "\n- ... %d more (call list_skills)", len(skills)-maxSkillsListed)
			break
		}
		desc := sk.Description
		if len(desc) > maxSkillNoteDesc {
			desc = truncateUTF8(desc, maxSkillNoteDesc) + "..."
		}
		fmt.Fprintf(&b, "\n- %s: %s", sk.Name, desc)
	}
	return b.String()
}

func (s *skillTools) list(ctx context.Context, _ json.RawMessage) (string, error) {
	skills, err := ListSkills(s.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "No skills are installed.", nil
		}
		return "", fmt.Errorf("can't list skills: %v", errUnwrapPath(err))
	}
	if len(skills) == 0 {
		return "No skills are installed.", nil
	}
	var b strings.Builder
	b.WriteString("Installed skills (load one with view_skill):\n")
	for _, sk := range skills {
		fmt.Fprintf(&b, "- %s: %s\n", sk.Name, sk.Description)
	}
	return b.String(), nil
}

// find returns the folder of the skill called name (folder or front-matter name).
func (s *skillTools) find(name string) (SkillInfo, error) {
	skills, err := ListSkills(s.dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return SkillInfo{}, fmt.Errorf("can't list skills: %v", errUnwrapPath(err))
	}
	var names []string
	for _, sk := range skills {
		if sk.Name == name || sk.Dir == name {
			return sk, nil
		}
		names = append(names, sk.Name)
	}
	if len(names) == 0 {
		return SkillInfo{}, errors.New("no skills are installed")
	}
	return SkillInfo{}, fmt.Errorf("there is no skill %q; installed: %s", name, strings.Join(names, ", "))
}

func (s *skillTools) view(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Name string `json:"name"`
		File string `json:"file"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return "", err
	}
	name := strings.TrimSpace(a.Name)
	if !skillArgRe.MatchString(name) {
		return "", fmt.Errorf("%q is not a skill name", a.Name)
	}
	sk, err := s.find(name)
	if err != nil {
		return "", err
	}
	root := filepath.Join(s.dir, sk.Dir)
	if f := strings.TrimSpace(a.File); f != "" && f != "SKILL.md" {
		ws := &Workspace{dir: root, real: root}
		if real, err := filepath.EvalSymlinks(root); err == nil {
			ws.real = real
		}
		p, err := ws.Resolve(f)
		if err != nil {
			return "", err
		}
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() {
			return "", fmt.Errorf("skill %s has no file %s", sk.Name, f)
		}
		if fi.Size() > maxSkillFileBytes {
			return "", fmt.Errorf("%s is too large to load (%s)", f, humanSize(fi.Size()))
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return "", fmt.Errorf("can't read %s: %v", f, errUnwrapPath(err))
		}
		if isBinary(data) {
			return "", fmt.Errorf("%s is a binary file", f)
		}
		return truncateUTF8(fmt.Sprintf("Skill %s, file %s:\n\n%s", sk.Name, ws.Rel(p), data), s.maxOut), nil
	}
	data, err := os.ReadFile(filepath.Join(root, "SKILL.md"))
	if err != nil {
		return "", fmt.Errorf("can't read skill %s: %v", sk.Name, errUnwrapPath(err))
	}
	_, body := ParseFrontmatter(strings.ReplaceAll(string(data), "\r\n", "\n"))
	var b strings.Builder
	fmt.Fprintf(&b, "Skill %s: %s\n\n%s\n", sk.Name, sk.Description, body)
	if extra := skillFiles(root); len(extra) > 0 {
		fmt.Fprintf(&b, "\nOther files in this skill (read one with view_skill and file): %s\n", strings.Join(extra, ", "))
	}
	return b.String(), nil
}

// skillFiles lists a skill folder's other files (at most 50).
func skillFiles(root string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "SKILL.md" {
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		if len(out) >= 50 {
			return fs.SkipAll
		}
		return nil
	})
	return out
}
