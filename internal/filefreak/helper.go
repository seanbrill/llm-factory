package filefreak

// The Node helper (tools/filefreak/ff.mjs), run as a child process with the
// FileFreak SDK. One command per run; it answers with JSON lines on stdout
// (progress, then a result or an error). The upload key goes in its
// environment, never on its command line.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// MinNodeMajor is the oldest Node the SDK runs on.
const MinNodeMajor = 20

// Event is one line the helper prints.
type Event struct {
	Event    string          `json:"event"` // progress | verifying | begun | restarted | result | error
	Done     int64           `json:"done,omitempty"`
	Total    int64           `json:"total,omitempty"`
	UploadID string          `json:"upload_id,omitempty"`
	Reason   string          `json:"reason,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
	Code     string          `json:"code,omitempty"`
	Message  string          `json:"message,omitempty"`
	Status   int             `json:"status,omitempty"`
	Received string          `json:"received,omitempty"`
}

// HelperError is a command that failed and said why.
type HelperError struct {
	Code     string
	Message  string
	Status   int
	UploadID string
}

func (e *HelperError) Error() string { return e.Message }

// runner runs one helper command. Tests replace it.
type runner interface {
	// Check says whether the helper can run: Node 20 or later and the SDK installed.
	Check(ctx context.Context) (node string, err error)
	// Run runs a command. key, when set, is passed as FILEFREAK_KEY.
	Run(ctx context.Context, args []string, key string, onEvent func(Event)) (json.RawMessage, error)
}

// NodeHelper runs ff.mjs with Node.
type NodeHelper struct {
	Node   string // the node binary: FILEFREAK_NODE, or "node" on PATH
	Script string // ff.mjs
	// Env is added to the helper's environment (FILEFREAK_API_URL, FILEFREAK_LINKS_ORIGIN).
	Env []string

	mu        sync.Mutex
	checkedAt time.Time
	version   string
	checkErr  error
}

// NewNodeHelper finds the helper under toolsDir (tools/filefreak), unless
// FILEFREAK_HELPER names another ff.mjs.
func NewNodeHelper(toolsDir string) *NodeHelper {
	h := &NodeHelper{Node: "node", Script: filepath.Join(toolsDir, "ff.mjs")}
	if v := strings.TrimSpace(os.Getenv("FILEFREAK_NODE")); v != "" {
		h.Node = v
	}
	if v := strings.TrimSpace(os.Getenv("FILEFREAK_HELPER")); v != "" {
		h.Script = v
	}
	for _, k := range []string{"FILEFREAK_API_URL", "FILEFREAK_LINKS_ORIGIN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			h.Env = append(h.Env, k+"="+v)
		}
	}
	return h
}

var nodeVersion = regexp.MustCompile(`^v(\d+)\.`)

// Check runs `node --version` and looks for the SDK, remembering a good
// answer for ten minutes and a bad one for twenty seconds.
func (h *NodeHelper) Check(ctx context.Context) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	keep := 10 * time.Minute
	if h.checkErr != nil {
		keep = 20 * time.Second
	}
	if !h.checkedAt.IsZero() && time.Since(h.checkedAt) < keep {
		return h.version, h.checkErr
	}
	h.version, h.checkErr = h.check(ctx)
	h.checkedAt = time.Now()
	return h.version, h.checkErr
}

func (h *NodeHelper) check(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	path, err := exec.LookPath(h.Node)
	if err != nil {
		return "", fmt.Errorf("Node.js %d or later isn't installed (no %q on PATH)", MinNodeMajor, h.Node)
	}
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("%s --version failed: %v", h.Node, err)
	}
	v := strings.TrimSpace(string(out))
	m := nodeVersion.FindStringSubmatch(v)
	if m == nil {
		return v, fmt.Errorf("couldn't read the Node.js version (%q)", v)
	}
	if major, _ := strconv.Atoi(m[1]); major < MinNodeMajor {
		return v, fmt.Errorf("Node.js %s is too old: the FileFreak SDK needs %d or later", v, MinNodeMajor)
	}
	if _, err := os.Stat(h.Script); err != nil {
		return v, fmt.Errorf("the archive helper %s is missing", h.Script)
	}
	sdk := filepath.Join(filepath.Dir(h.Script), "node_modules", "@filefreak.io", "sdk", "package.json")
	if _, err := os.Stat(sdk); err != nil {
		return v, fmt.Errorf("the FileFreak SDK isn't installed: run `npm ci --omit=dev` in %s", filepath.Dir(h.Script))
	}
	return v, nil
}

// Run runs one command and waits for its answer. Stopping ctx stops the
// helper (SIGTERM, so it can tidy up; killed ten seconds later).
func (h *NodeHelper) Run(ctx context.Context, args []string, key string, onEvent func(Event)) (json.RawMessage, error) {
	cmd := exec.CommandContext(ctx, h.Node, append([]string{h.Script}, args...)...)
	cmd.Dir = filepath.Dir(h.Script)
	cmd.Env = helperEnv(os.Environ(), h.Env, key)
	cmd.Cancel = func() error {
		if runtime.GOOS == "windows" {
			return cmd.Process.Kill()
		}
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 10 * time.Second
	var stderr tailBuffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("couldn't start the FileFreak helper: %w", err)
	}
	var result json.RawMessage
	var failed *HelperError
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		var ev Event
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Event {
		case "result":
			result = ev.Data
		case "error":
			failed = &HelperError{Code: ev.Code, Message: redact(ev.Message, key), Status: ev.Status, UploadID: ev.UploadID}
		default:
			if onEvent != nil {
				onEvent(ev)
			}
		}
	}
	waitErr := cmd.Wait()
	switch {
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case failed != nil:
		return nil, failed
	case waitErr == nil && result != nil:
		return result, nil
	}
	msg := strings.TrimSpace(redact(stderr.String(), key))
	if waitErr == nil {
		waitErr = errors.New("it gave no answer")
	}
	if msg != "" {
		return nil, fmt.Errorf("the FileFreak helper failed (%v): %s", waitErr, lastLine(msg))
	}
	return nil, fmt.Errorf("the FileFreak helper failed: %v", waitErr)
}

// helperEnv is the factory's environment for the helper, with the key (or no
// key at all, so a FILEFREAK_KEY in the factory's own environment never
// reaches a read).
func helperEnv(base, extra []string, key string) []string {
	env := make([]string, 0, len(base)+len(extra)+1)
	for _, kv := range base {
		if !strings.HasPrefix(kv, "FILEFREAK_KEY=") {
			env = append(env, kv)
		}
	}
	env = append(env, extra...)
	if key != "" {
		env = append(env, "FILEFREAK_KEY="+key)
	}
	return env
}

// redact takes the key out of text bound for logs and the UI.
func redact(s, key string) string {
	if len(key) >= 8 {
		s = strings.ReplaceAll(s, key, "[key]")
	}
	return s
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// tailBuffer keeps the last 8 KB written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf.Write(p)
	if t.buf.Len() > 8<<10 {
		b := t.buf.Bytes()[t.buf.Len()-8<<10:]
		t.buf = *bytes.NewBuffer(append([]byte(nil), b...))
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.String()
}

// ---------------------------------------------------------------------------
// The commands, typed
// ---------------------------------------------------------------------------

// ArchivedFile is a file in the archive.
type ArchivedFile struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	Protected bool   `json:"protected"`
	URL       string `json:"url"`
	CreatedAt string `json:"created_at"`
}

type found struct {
	Found bool          `json:"found"`
	File  *ArchivedFile `json:"file"`
}

// Provenance is the record kept beside each archived file.
type Provenance struct {
	Schema      string   `json:"schema"`
	Source      string   `json:"source"`
	Repo        string   `json:"repo"`
	Ref         string   `json:"ref"`
	Revision    string   `json:"revision"`
	File        string   `json:"file"`
	SHA256      string   `json:"sha256"`
	Size        int64    `json:"size"`
	SourceURL   string   `json:"source_url"`
	License     string   `json:"license"`
	LicenseName string   `json:"license_name,omitempty"`
	LicenseLink string   `json:"license_link,omitempty"`
	Notices     []string `json:"notices,omitempty"`
	ArchivedAt  string   `json:"archived_at"`
	ArchivedBy  string   `json:"archived_by"`
}

// Candidate is one archived revision of a file, for a lookup without Hugging Face.
type Candidate struct {
	Revision   string       `json:"revision"`
	File       ArchivedFile `json:"file"`
	Provenance *Provenance  `json:"provenance"`
	Problem    string       `json:"problem"`
}

type downloaded struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// arg is one option as --name=value, so a value that starts with "-" stays a value.
func arg(name, value string) string { return "--" + name + "=" + value }

func decode[T any](raw json.RawMessage, err error) (T, error) {
	var v T
	if err != nil {
		return v, err
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, fmt.Errorf("the FileFreak helper's answer couldn't be read: %w", err)
	}
	return v, nil
}

func lookupFile(ctx context.Context, r runner, link, path string) (*ArchivedFile, error) {
	f, err := decode[found](r.Run(ctx, []string{"lookup", arg("archive", link), arg("path", path)}, "", nil))
	if err != nil || !f.Found {
		return nil, err
	}
	return f.File, nil
}

func listCandidates(ctx context.Context, r runner, link, repoDir, file string) ([]Candidate, error) {
	v, err := decode[struct {
		Candidates []Candidate `json:"candidates"`
	}](r.Run(ctx, []string{"candidates", arg("archive", link), arg("repo-dir", repoDir), arg("file", file)}, "", nil))
	return v.Candidates, err
}

func downloadFile(ctx context.Context, r runner, link, id, dest, sha string, onEvent func(Event)) (downloaded, error) {
	return decode[downloaded](r.Run(ctx, []string{"download", arg("archive", link), arg("id", id), arg("out", dest), arg("sha256", sha)}, "", onEvent))
}

func ensureFolder(ctx context.Context, r runner, key, name string) (KeyFolder, error) {
	return decode[KeyFolder](r.Run(ctx, []string{"ensure-folder", arg("name", name)}, key, nil))
}

func existsFile(ctx context.Context, r runner, key, path string) (*ArchivedFile, error) {
	f, err := decode[found](r.Run(ctx, []string{"exists", arg("path", path)}, key, nil))
	if err != nil || !f.Found {
		return nil, err
	}
	return f.File, nil
}

type uploadRequest struct {
	File, Parent, Dir, Name, SHA256, Resume string
	Replace                                 bool
}

func uploadFile(ctx context.Context, r runner, key string, u uploadRequest, onEvent func(Event)) (ArchivedFile, error) {
	args := []string{"upload", arg("file", u.File), arg("parent", u.Parent), arg("dir", u.Dir), arg("name", u.Name)}
	if u.SHA256 != "" {
		args = append(args, arg("sha256", u.SHA256))
	}
	if u.Resume != "" {
		args = append(args, arg("resume", u.Resume))
	}
	if u.Replace {
		args = append(args, "--replace")
	}
	v, err := decode[struct {
		File ArchivedFile `json:"file"`
	}](r.Run(ctx, args, key, onEvent))
	return v.File, err
}
