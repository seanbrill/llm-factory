package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// defaultShell is the shell run_command uses when shell.program is unset.
func defaultShell() []string {
	if runtime.GOOS == "windows" {
		return []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command"}
	}
	return []string{"/bin/sh", "-c"}
}

// baseEnv are the variables commands inherit from the host process. Everything
// else is dropped so a host's secrets (tokens, keys) never reach a command the
// model chose; shell.env adds more names.
var baseEnv = []string{
	"PATH", "LANG", "LC_ALL", "TZ",
	// Windows needs these for programs to start at all.
	"SystemRoot", "SYSTEMROOT", "ComSpec", "COMSPEC", "PATHEXT", "WINDIR", "TEMP", "TMP",
	"PROGRAMFILES", "ProgramFiles", "ProgramData", "PROGRAMDATA", "USERPROFILE", "APPDATA", "LOCALAPPDATA",
}

type shellTool struct {
	ws      *Workspace
	program []string
	env     []string
	maxOut  int
	timeout time.Duration
}

// newShellTool builds run_command, or explains why it can't run here.
func newShellTool(cfg ShellConfig, ws *Workspace, maxOut int, timeout time.Duration) (*shellTool, error) {
	prog := cfg.Program
	if len(prog) == 0 {
		prog = defaultShell()
	}
	if _, err := exec.LookPath(prog[0]); err != nil {
		return nil, fmt.Errorf("the shell %s isn't available on this host", prog[0])
	}
	if runningAsRoot() && !cfg.AllowRoot {
		return nil, errors.New("refusing to run commands as root; run the host as a normal user (or set shell.allow_root)")
	}
	return &shellTool{ws: ws, program: prog, env: commandEnv(cfg.Env, ws.Root()), maxOut: maxOut, timeout: timeout}, nil
}

// commandEnv is the minimal environment commands run with.
func commandEnv(extra []string, home string) []string {
	var env []string
	seen := map[string]bool{}
	add := func(k, v string) {
		if seen[k] {
			return
		}
		seen[k] = true
		env = append(env, k+"="+v)
	}
	for _, k := range append(append([]string{}, baseEnv...), extra...) {
		if v, ok := os.LookupEnv(k); ok {
			add(k, v)
		}
	}
	if !seen["PATH"] && runtime.GOOS != "windows" {
		add("PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	}
	if !seen["LANG"] {
		add("LANG", "C.UTF-8")
	}
	add("HOME", home)
	add("TERM", "dumb")
	add("PAGER", "cat")
	add("GIT_PAGER", "cat")
	add("GIT_TERMINAL_PROMPT", "0")
	add("NO_COLOR", "1")
	add("DEBIAN_FRONTEND", "noninteractive")
	return env
}

func (t *shellTool) Name() string { return ToolRunCommand }

func (t *shellTool) Description() string {
	return "Run a shell command (" + strings.Join(t.program, " ") + ") in the workspace and return its exit code and output " +
		"(stdout and stderr together). Commands can't read input; pass flags like -y. Long output is cut in the middle."
}

func (t *shellTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` +
		`"command":{"type":"string","description":"The command line to run."},` +
		`"cwd":{"type":"string","description":"Folder to run in, relative to the workspace. Default: the workspace."},` +
		`"timeout":{"type":"integer","description":"Seconds before the command is stopped (at most ` + fmt.Sprint(int(t.timeout.Seconds())) + `)."}` +
		`},"required":["command"]}`)
}

func (t *shellTool) SystemNote() string {
	return "Shell: run_command runs commands in the workspace folder as an unprivileged user, with no input and a time limit."
}

func (t *shellTool) Run(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Command string  `json:"command"`
		Cwd     string  `json:"cwd"`
		Timeout flexInt `json:"timeout"`
		Wait    flexInt `json:"wait"` // Open Terminal's name for it
	}
	if err := decodeArgs(args, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Command) == "" {
		return "", errors.New("command is required")
	}
	dir, err := t.ws.Resolve(a.Cwd)
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("cwd %s is not a folder", t.ws.Rel(dir))
	}
	limit := t.timeout
	secs := int(a.Timeout)
	if secs <= 0 {
		secs = int(a.Wait)
	}
	if secs > 0 && time.Duration(secs)*time.Second < limit {
		limit = time.Duration(secs) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()

	argv := append(append([]string{}, t.program[1:]...), a.Command)
	cmd := exec.CommandContext(ctx, t.program[0], argv...)
	cmd.Dir = dir
	cmd.Env = t.env
	out := &headTail{max: t.maxOut - 300}
	cmd.Stdout = out
	cmd.Stderr = out
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	cmd.WaitDelay = 3 * time.Second

	start := time.Now()
	runErr := cmd.Run()
	took := time.Since(start).Round(10 * time.Millisecond)

	var b strings.Builder
	fmt.Fprintf(&b, "$ %s\n", a.Command)
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		fmt.Fprintf(&b, "[timed out after %s; the command was stopped]\n", limit)
	case runErr != nil:
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			fmt.Fprintf(&b, "[exit code %d after %s]\n", ee.ExitCode(), took)
		} else {
			fmt.Fprintf(&b, "[failed after %s: %v]\n", took, runErr)
		}
	default:
		fmt.Fprintf(&b, "[exit code 0 after %s]\n", took)
	}
	output := out.String()
	if output == "" {
		b.WriteString("(no output)")
	} else {
		b.WriteString(output)
	}
	return b.String(), nil
}

// headTail keeps the first and last parts of a stream that exceeds max bytes:
// the start shows what ran, the end usually holds the error.
type headTail struct {
	mu      sync.Mutex
	max     int
	head    []byte
	tail    []byte
	dropped int64
}

func (h *headTail) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := len(p)
	half := h.max / 2
	if half < 64 {
		half = 64
	}
	if room := half - len(h.head); room > 0 {
		take := room
		if take > len(p) {
			take = len(p)
		}
		h.head = append(h.head, p[:take]...)
		p = p[take:]
	}
	if len(p) > 0 {
		h.tail = append(h.tail, p...)
		if over := len(h.tail) - half; over > 0 {
			h.dropped += int64(over)
			h.tail = append(h.tail[:0], h.tail[over:]...)
		}
	}
	return n, nil
}

func (h *headTail) String() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	head := strings.ToValidUTF8(string(h.head), "�")
	tail := strings.ToValidUTF8(string(h.tail), "�")
	if h.dropped == 0 {
		return head + tail
	}
	return head + fmt.Sprintf("\n[... %d bytes of output cut ...]\n", h.dropped) + tail
}
