package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	maxWriteBytes   = 4 << 20  // largest file write_file writes
	maxEditBytes    = 8 << 20  // largest file replace_file_content edits
	maxReadBytes    = 16 << 20 // largest file read_file reads
	maxListEntries  = 500
	maxGlobResults  = 200
	maxGrepMatches  = 100
	maxGrepFileSize = 2 << 20
	maxWalkEntries  = 20000
)

// skipDirs are not descended into by glob_search and grep_search.
var skipDirs = map[string]bool{StateDirName: true, ".git": true, "node_modules": true, ".venv": true, "venv": true, "__pycache__": true, ".cache": true}

// Workspace confines file paths to one folder. Paths may be relative to it,
// absolute inside it, or start with ~/workspace (the name the starter skills
// use). A path that leaves the folder, directly or through a symbolic link, is
// refused.
//
// The check resolves symbolic links before each operation. Without a shell
// tool nothing the model does can create a link, so links come only from the
// operator; with a shell the model can read anything its user can anyway.
type Workspace struct {
	dir  string // absolute, as configured
	real string // dir with symbolic links resolved
}

// OpenWorkspace returns the workspace at dir, creating the folder if needed.
func OpenWorkspace(dir string) (*Workspace, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("workspace %s: %w", abs, err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("workspace %s: %w", abs, err)
	}
	fi, err := os.Stat(real)
	if err != nil {
		return nil, fmt.Errorf("workspace %s: %w", abs, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("workspace %s is not a folder", abs)
	}
	return &Workspace{dir: abs, real: real}, nil
}

// Root is the workspace folder.
func (w *Workspace) Root() string { return w.dir }

// within returns p relative to base when p is inside base.
func within(base, p string) (string, bool) {
	rel, err := filepath.Rel(base, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}

// Resolve maps a path the model gave to the real path inside the workspace.
func (w *Workspace) Resolve(p string) (string, error) {
	orig := p
	p = strings.TrimSpace(p)
	p = filepath.FromSlash(p)
	sep := string(filepath.Separator)
	switch {
	case p == "" || p == "." || p == "~" || p == filepath.FromSlash("~/workspace"):
		return w.real, nil
	case strings.HasPrefix(p, filepath.FromSlash("~/workspace/")):
		p = strings.TrimPrefix(p, filepath.FromSlash("~/workspace/"))
	case strings.HasPrefix(p, "~"):
		return "", fmt.Errorf("%q is outside the workspace; give a path relative to the workspace", orig)
	}
	var rel string
	if filepath.IsAbs(p) || strings.HasPrefix(p, sep) {
		clean := filepath.Clean(p)
		r, ok := within(w.dir, clean)
		if !ok {
			r, ok = within(w.real, clean)
		}
		if !ok {
			return "", fmt.Errorf("%q is outside the workspace; give a path relative to the workspace", orig)
		}
		rel = r
	} else {
		rel = filepath.Clean(p)
	}
	if rel == "." {
		return w.real, nil
	}
	if !filepath.IsLocal(rel) {
		return "", fmt.Errorf("%q is outside the workspace", orig)
	}
	real, err := resolveExisting(filepath.Join(w.real, rel))
	if err != nil {
		return "", fmt.Errorf("%q: %v", orig, err)
	}
	inner, ok := within(w.real, real)
	if !ok {
		return "", fmt.Errorf("%q leads outside the workspace through a symbolic link", orig)
	}
	for _, part := range strings.Split(inner, string(filepath.Separator)) {
		if strings.EqualFold(part, StateDirName) {
			// The harness's own state (MCP servers and their headers) is
			// never the model's to read or change.
			return "", fmt.Errorf("%q is the harness's private folder; the files tools don't go there", orig)
		}
	}
	return real, nil
}

// resolveExisting resolves the symbolic links in the longest existing prefix
// of p and appends the rest. A dangling link in the path is refused, since
// writing through it would create its target wherever it points.
func resolveExisting(p string) (string, error) {
	cur := p
	var rest []string
	for {
		r, err := filepath.EvalSymlinks(cur)
		if err == nil {
			if len(rest) > 0 {
				if _, lerr := os.Lstat(filepath.Join(r, rest[len(rest)-1])); lerr == nil {
					return "", errors.New("the path goes through a broken symbolic link")
				}
			}
			for i := len(rest) - 1; i >= 0; i-- {
				r = filepath.Join(r, rest[i])
			}
			return r, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", err
		}
		rest = append(rest, filepath.Base(cur))
		cur = parent
	}
}

// Rel returns a real path relative to the workspace, with forward slashes.
func (w *Workspace) Rel(real string) string {
	r, ok := within(w.real, real)
	if !ok {
		return real
	}
	if r == "." {
		return "."
	}
	return filepath.ToSlash(r)
}

// resolveWrite is Resolve for paths that will be written: it also refuses any
// path inside a .git folder, so a web page can't steer the model into
// planting git hooks that would run on the host.
func (w *Workspace) resolveWrite(p string) (string, error) {
	real, err := w.Resolve(p)
	if err != nil {
		return "", err
	}
	rel, _ := within(w.real, real)
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if strings.EqualFold(part, ".git") {
			return "", fmt.Errorf("%q is inside a .git folder; the files tools don't write there", p)
		}
	}
	if real == w.real {
		return "", errors.New("give a file path, not the workspace folder")
	}
	return real, nil
}

// writeFileAtomic writes data through a temporary file and a rename, keeping
// an existing file's permissions.
func writeFileAtomic(p string, data []byte) error {
	mode := fs.FileMode(0o644)
	if fi, err := os.Stat(p); err == nil {
		if fi.IsDir() {
			return errors.New("it is a folder")
		}
		mode = fi.Mode().Perm()
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".harness-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(name, mode)
	}
	if werr == nil {
		werr = os.Rename(name, p)
	}
	if werr != nil {
		os.Remove(name)
	}
	return werr
}

func isBinary(b []byte) bool {
	if len(b) > 8000 {
		b = b[:8000]
	}
	return bytes.IndexByte(b, 0) >= 0
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// fileTools builds the files toolset.
func fileTools(ws *Workspace, maxOut int) []Tool {
	f := &files{ws: ws, maxOut: maxOut}
	pathProp := func(desc string) string {
		return `{"type":"string","description":"` + desc + `"}`
	}
	return []Tool{
		&noteTool{FuncTool: FuncTool{
			ToolName: ToolListFiles,
			Desc:     "List a folder in the workspace: sub-folders (ending in /) and files with sizes.",
			Params:   json.RawMessage(`{"type":"object","properties":{"directory":` + pathProp("Folder relative to the workspace. Default: the workspace itself.") + `}}`),
			Fn:       f.list,
		}, note: "Files: the file tools work inside the workspace folder. Give paths relative to it (for example notes/todo.md)."},
		&FuncTool{
			ToolName: ToolReadFile,
			Desc:     "Read a text file from the workspace, optionally only some lines. Long files come back in parts.",
			Params: json.RawMessage(`{"type":"object","properties":{"path":` + pathProp("File path relative to the workspace.") + `,` +
				`"start_line":{"type":"integer","description":"First line to read (1-based). Default 1."},` +
				`"end_line":{"type":"integer","description":"Last line to read. Default: the end."}},"required":["path"]}`),
			Fn: f.read,
		},
		&FuncTool{
			ToolName: ToolWriteFile,
			Desc:     "Create a file in the workspace, or replace a whole file. Parent folders are created. Prefer replace_file_content for changes to an existing file.",
			Params: json.RawMessage(`{"type":"object","properties":{"path":` + pathProp("File path relative to the workspace.") + `,` +
				`"content":{"type":"string","description":"The complete new file content."}},"required":["path","content"]}`),
			Fn: f.write,
		},
		&FuncTool{
			ToolName: ToolReplaceFileContent,
			Desc: "Change a workspace file with exact find-and-replace. Each target must be copied exactly from the file " +
				"(indentation included) and match once, unless allow_multiple is set. Nothing changes if any target fails.",
			Params: json.RawMessage(`{"type":"object","properties":{"path":` + pathProp("File path relative to the workspace.") + `,` +
				`"replacements":{"type":"array","items":{"type":"object","properties":{` +
				`"target":{"type":"string","description":"Exact text to find."},` +
				`"replacement":{"type":"string","description":"Text to put in its place."},` +
				`"allow_multiple":{"type":"boolean","description":"Replace every match instead of requiring exactly one."}},` +
				`"required":["target","replacement"]}}},"required":["path","replacements"]}`),
			Fn: f.replace,
		},
		&FuncTool{
			ToolName: ToolGlobSearch,
			Desc:     "Find files in the workspace by name pattern, such as *.py or src/**/*.go.",
			Params: json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"File name pattern; ** matches any folders."},` +
				`"path":` + pathProp("Folder to search, relative to the workspace. Default: all of it.") + `},"required":["pattern"]}`),
			Fn: f.glob,
		},
		&FuncTool{
			ToolName: ToolGrepSearch,
			Desc:     "Search the text of workspace files. Returns matching lines as path:line: text.",
			Params: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Text to find (or a regular expression with regex=true)."},` +
				`"path":` + pathProp("Folder or file to search, relative to the workspace. Default: all of it.") + `,` +
				`"include":{"type":"string","description":"Only files whose name matches this pattern, such as *.md."},` +
				`"regex":{"type":"boolean","description":"Treat query as a regular expression."},` +
				`"case_sensitive":{"type":"boolean","description":"Match case exactly. Default false."}},"required":["query"]}`),
			Fn: f.grep,
		},
	}
}

// noteTool is a FuncTool that adds a line to the system note.
type noteTool struct {
	FuncTool
	note string
}

func (t *noteTool) SystemNote() string { return t.note }

type files struct {
	ws     *Workspace
	maxOut int
}

func (f *files) list(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Directory string `json:"directory"`
		Path      string `json:"path"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return "", err
	}
	if a.Directory == "" {
		a.Directory = a.Path
	}
	dir, err := f.ws.Resolve(a.Directory)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%s doesn't exist", f.ws.Rel(dir))
		}
		return "", fmt.Errorf("can't list %s: %v", f.ws.Rel(dir), errUnwrapPath(err))
	}
	var b strings.Builder
	label := f.ws.Rel(dir)
	if label == "." {
		label = "the workspace"
	}
	fmt.Fprintf(&b, "Contents of %s (%d entries):\n", label, len(entries))
	for i, e := range entries {
		if i == maxListEntries {
			fmt.Fprintf(&b, "... and %d more\n", len(entries)-maxListEntries)
			break
		}
		switch {
		case e.Type()&fs.ModeSymlink != 0:
			fmt.Fprintf(&b, "%s -> (symbolic link)\n", e.Name())
		case e.IsDir():
			fmt.Fprintf(&b, "%s/\n", e.Name())
		default:
			size := ""
			if fi, err := e.Info(); err == nil {
				size = "  " + humanSize(fi.Size())
			}
			fmt.Fprintf(&b, "%s%s\n", e.Name(), size)
		}
	}
	if len(entries) == 0 {
		b.WriteString("(empty)\n")
	}
	return b.String(), nil
}

// errUnwrapPath drops the absolute path from an *fs.PathError.
func errUnwrapPath(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

func (f *files) read(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Path      string  `json:"path"`
		StartLine flexInt `json:"start_line"`
		EndLine   flexInt `json:"end_line"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Path) == "" {
		return "", errors.New("path is required")
	}
	p, err := f.ws.Resolve(a.Path)
	if err != nil {
		return "", err
	}
	rel := f.ws.Rel(p)
	fi, err := os.Stat(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%s doesn't exist", rel)
		}
		return "", fmt.Errorf("can't read %s: %v", rel, errUnwrapPath(err))
	}
	if fi.IsDir() {
		return "", fmt.Errorf("%s is a folder; use list_files", rel)
	}
	if fi.Size() > maxReadBytes {
		return "", fmt.Errorf("%s is %s, larger than read_file reads (%s)", rel, humanSize(fi.Size()), humanSize(maxReadBytes))
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("can't read %s: %v", rel, errUnwrapPath(err))
	}
	if len(data) == 0 {
		return fmt.Sprintf("%s is empty.", rel), nil
	}
	if isBinary(data) {
		return "", fmt.Errorf("%s is a binary file (%s); read_file shows text files", rel, humanSize(int64(len(data))))
	}
	text := strings.ToValidUTF8(string(data), "�")
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	total := len(lines)
	start, end := int(a.StartLine), int(a.EndLine)
	if start < 1 {
		start = 1
	}
	if end < 1 || end > total {
		end = total
	}
	if start > total {
		return "", fmt.Errorf("%s has %d lines; start_line %d is past the end", rel, total, start)
	}
	if end < start {
		return "", fmt.Errorf("end_line %d is before start_line %d", end, start)
	}
	header := fmt.Sprintf("%s (lines %d-%d of %d):\n\n", rel, start, end, total)
	budget := f.maxOut - len(header) - 200
	var body strings.Builder
	last := start - 1
	for i := start; i <= end; i++ {
		l := lines[i-1]
		if body.Len()+len(l) > budget {
			if body.Len() == 0 { // one huge line: show what fits
				body.WriteString(truncateUTF8(l, budget))
				last = i
			}
			break
		}
		body.WriteString(l)
		last = i
	}
	out := body.String()
	if last < end {
		header = fmt.Sprintf("%s (lines %d-%d of %d):\n\n", rel, start, last, total)
		next := last + 1
		return header + out + fmt.Sprintf("\n[Showing lines %d-%d of %d. Call read_file with start_line=%d to read more.]", start, last, total, next), nil
	}
	return header + out, nil
}

func (f *files) write(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Path    string  `json:"path"`
		Content *string `json:"content"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Path) == "" {
		return "", errors.New("path is required")
	}
	if a.Content == nil {
		return "", errors.New("content is required")
	}
	if len(*a.Content) > maxWriteBytes {
		return "", fmt.Errorf("content is %s; write_file writes at most %s", humanSize(int64(len(*a.Content))), humanSize(maxWriteBytes))
	}
	p, err := f.ws.resolveWrite(a.Path)
	if err != nil {
		return "", err
	}
	rel := f.ws.Rel(p)
	_, statErr := os.Stat(p)
	if err := writeFileAtomic(p, []byte(*a.Content)); err != nil {
		return "", fmt.Errorf("can't write %s: %v", rel, errUnwrapPath(err))
	}
	what := "created"
	if statErr == nil {
		what = "replaced"
	}
	return fmt.Sprintf("Wrote %d bytes to %s (%s).", len(*a.Content), rel, what), nil
}

type replacement struct {
	Target        *string  `json:"target"`
	Replacement   *string  `json:"replacement"`
	AllowMultiple flexBool `json:"allow_multiple"`
}

func (f *files) replace(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Path         string        `json:"path"`
		Replacements []replacement `json:"replacements"`
		replacement
	}
	if err := decodeArgs(args, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Path) == "" {
		return "", errors.New("path is required")
	}
	reps := a.Replacements
	if a.Target != nil { // a single replacement given at the top level
		reps = append(reps, a.replacement)
	}
	if len(reps) == 0 {
		return "", errors.New("replacements is required: a list of {target, replacement}")
	}
	p, err := f.ws.resolveWrite(a.Path)
	if err != nil {
		return "", err
	}
	rel := f.ws.Rel(p)
	fi, err := os.Stat(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%s doesn't exist; use write_file to create it", rel)
		}
		return "", fmt.Errorf("can't read %s: %v", rel, errUnwrapPath(err))
	}
	if fi.IsDir() {
		return "", fmt.Errorf("%s is a folder", rel)
	}
	if fi.Size() > maxEditBytes {
		return "", fmt.Errorf("%s is too large to edit (%s)", rel, humanSize(fi.Size()))
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("can't read %s: %v", rel, errUnwrapPath(err))
	}
	if isBinary(data) {
		return "", fmt.Errorf("%s is a binary file", rel)
	}
	content := string(data)
	crlf := strings.Contains(content, "\r\n")
	firstLine, lastLine := 0, 0
	count := 0
	for i, r := range reps {
		if r.Target == nil || *r.Target == "" {
			return "", fmt.Errorf("replacement %d: target is empty", i+1)
		}
		if r.Replacement == nil {
			return "", fmt.Errorf("replacement %d: replacement is missing", i+1)
		}
		target, repl := *r.Target, *r.Replacement
		n := strings.Count(content, target)
		if n == 0 && crlf && !strings.Contains(target, "\r\n") {
			// The model usually sends \n line endings for a \r\n file.
			target = strings.ReplaceAll(target, "\n", "\r\n")
			repl = strings.ReplaceAll(repl, "\n", "\r\n")
			n = strings.Count(content, target)
		}
		switch {
		case n == 0:
			return "", fmt.Errorf("replacement %d: target not found in %s; nothing was changed. Read the file again and copy the text exactly", i+1, rel)
		case n > 1 && !bool(r.AllowMultiple):
			return "", fmt.Errorf("replacement %d: target matches %d times in %s; nothing was changed. Include more surrounding text, or set allow_multiple", i+1, n, rel)
		}
		idx := strings.Index(content, target)
		line := strings.Count(content[:idx], "\n") + 1
		if firstLine == 0 || line < firstLine {
			firstLine = line
		}
		if bool(r.AllowMultiple) {
			content = strings.ReplaceAll(content, target, repl)
		} else {
			content = content[:idx] + repl + content[idx+len(target):]
		}
		if end := line + strings.Count(repl, "\n"); end > lastLine {
			lastLine = end
		}
		count += n
	}
	if content == string(data) {
		return fmt.Sprintf("%s already had that content; nothing changed.", rel), nil
	}
	if err := writeFileAtomic(p, []byte(content)); err != nil {
		return "", fmt.Errorf("can't write %s: %v", rel, errUnwrapPath(err))
	}
	return fmt.Sprintf("Edited %s: %d replacement(s), around lines %d-%d.", rel, count, firstLine, lastLine), nil
}

// walk visits the files and folders under root (a real path), skipping
// skipDirs, symbolic links and anything past maxWalkEntries.
func (f *files) walk(ctx context.Context, root string, visit func(p string, d fs.DirEntry) error) error {
	seen := 0
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		seen++
		if seen > maxWalkEntries {
			return fs.SkipAll
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() && p != root && skipDirs[d.Name()] {
			return fs.SkipDir
		}
		return visit(p, d)
	})
}

func (f *files) glob(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return "", err
	}
	pat := strings.TrimSpace(filepath.ToSlash(a.Pattern))
	if pat == "" {
		return "", errors.New("pattern is required")
	}
	if _, err := path.Match(strings.ReplaceAll(pat, "**", "*"), ""); err != nil {
		return "", fmt.Errorf("bad pattern %q", a.Pattern)
	}
	root, err := f.ws.Resolve(a.Path)
	if err != nil {
		return "", err
	}
	var found []string
	more := false
	err = f.walk(ctx, root, func(p string, d fs.DirEntry) error {
		if p == root {
			return nil
		}
		rel, _ := within(root, p)
		if globMatch(pat, filepath.ToSlash(rel)) {
			if len(found) == maxGlobResults {
				more = true
				return fs.SkipAll
			}
			name := f.ws.Rel(p)
			if d.IsDir() {
				name += "/"
			}
			found = append(found, name)
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return "", fmt.Errorf("search failed: %v", errUnwrapPath(err))
	}
	if len(found) == 0 {
		return fmt.Sprintf("No files match %s.", a.Pattern), nil
	}
	sort.Strings(found)
	out := strings.Join(found, "\n")
	if more {
		out += fmt.Sprintf("\n[Stopped at %d matches; narrow the pattern or path.]", maxGlobResults)
	}
	return out, nil
}

// globMatch matches a slash path against a pattern where ** spans folders. A
// pattern without a slash matches the file name at any depth.
func globMatch(pat, rel string) bool {
	if !strings.Contains(pat, "/") {
		ok, _ := path.Match(pat, path.Base(rel))
		return ok
	}
	return matchSegs(strings.Split(pat, "/"), strings.Split(rel, "/"))
}

func matchSegs(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for i := 0; i <= len(segs); i++ {
				if matchSegs(pat[1:], segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		if ok, _ := path.Match(pat[0], segs[0]); !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}

func (f *files) grep(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Query         string   `json:"query"`
		Pattern       string   `json:"pattern"`
		Path          string   `json:"path"`
		Include       string   `json:"include"`
		Regex         flexBool `json:"regex"`
		CaseSensitive flexBool `json:"case_sensitive"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return "", err
	}
	if a.Query == "" {
		a.Query = a.Pattern
	}
	if a.Query == "" {
		return "", errors.New("query is required")
	}
	var match func(string) bool
	if a.Regex {
		expr := a.Query
		if !a.CaseSensitive {
			expr = "(?i)" + expr
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return "", fmt.Errorf("bad regular expression: %v", err)
		}
		match = re.MatchString
	} else if a.CaseSensitive {
		q := a.Query
		match = func(s string) bool { return strings.Contains(s, q) }
	} else {
		q := strings.ToLower(a.Query)
		match = func(s string) bool { return strings.Contains(strings.ToLower(s), q) }
	}
	include := strings.TrimSpace(a.Include)
	root, err := f.ws.Resolve(a.Path)
	if err != nil {
		return "", err
	}
	var out []string
	more := false
	err = f.walk(ctx, root, func(p string, d fs.DirEntry) error {
		if d.IsDir() {
			return nil
		}
		if include != "" {
			if ok, _ := path.Match(include, d.Name()); !ok {
				return nil
			}
		}
		fi, err := d.Info()
		if err != nil || fi.Size() > maxGrepFileSize || !fi.Mode().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil || isBinary(data) {
			return nil
		}
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 64<<10), maxGrepFileSize)
		for n := 1; sc.Scan(); n++ {
			line := sc.Text()
			if !match(line) {
				continue
			}
			if len(out) == maxGrepMatches {
				more = true
				return fs.SkipAll
			}
			line = strings.TrimSpace(line)
			if len(line) > 200 {
				line = truncateUTF8(line, 200) + "..."
			}
			out = append(out, fmt.Sprintf("%s:%d: %s", f.ws.Rel(p), n, line))
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return "", fmt.Errorf("search failed: %v", errUnwrapPath(err))
	}
	if len(out) == 0 {
		return fmt.Sprintf("No matches for %q.", a.Query), nil
	}
	res := strings.Join(out, "\n")
	if more {
		res += fmt.Sprintf("\n[Stopped at %d matches; narrow the query, path or include.]", maxGrepMatches)
	}
	return res, nil
}
