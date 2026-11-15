//go:build unix

package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestShell(t *testing.T, cfg ShellConfig, timeout time.Duration, maxOut int) (*shellTool, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "ws")
	ws, err := OpenWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	sh, err := newShellTool(cfg, ws, maxOut, timeout)
	if err != nil {
		t.Skipf("no shell here: %v", err)
	}
	return sh, ws.real
}

func runShell(t *testing.T, sh *shellTool, args string) string {
	t.Helper()
	out, err := sh.Run(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("%s: %v", args, err)
	}
	return out
}

func TestShellRuns(t *testing.T) {
	sh, root := newTestShell(t, ShellConfig{}, 10*time.Second, 8192)
	out := runShell(t, sh, `{"command":"echo hello; pwd; echo err >&2"}`)
	if !strings.Contains(out, "[exit code 0") || !strings.Contains(out, "hello\n"+root+"\nerr") {
		t.Errorf("out = %q", out)
	}
	if out := runShell(t, sh, `{"command":"exit 3"}`); !strings.Contains(out, "[exit code 3") {
		t.Errorf("exit code: %q", out)
	}
	os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	if out := runShell(t, sh, `{"command":"pwd","cwd":"sub"}`); !strings.Contains(out, filepath.Join(root, "sub")) {
		t.Errorf("cwd: %q", out)
	}
	if _, err := sh.Run(context.Background(), json.RawMessage(`{"command":"pwd","cwd":"../.."}`)); err == nil {
		t.Error("cwd outside the workspace accepted")
	}
	if _, err := sh.Run(context.Background(), json.RawMessage(`{"command":"  "}`)); err == nil {
		t.Error("empty command accepted")
	}
}

func TestShellEnvironmentIsScrubbed(t *testing.T) {
	t.Setenv("FACTORY_TOKEN", "super-secret")
	t.Setenv("KEEP_ME", "kept")
	sh, _ := newTestShell(t, ShellConfig{Env: []string{"KEEP_ME"}}, 10*time.Second, 8192)
	out := runShell(t, sh, `{"command":"echo token=$FACTORY_TOKEN keep=$KEEP_ME home=$HOME"}`)
	if !strings.Contains(out, "token= keep=kept home="+sh.ws.Root()+"\n") {
		t.Errorf("env = %q", out)
	}
	if strings.Contains(out, "super-secret") {
		t.Error("a host secret reached the command")
	}
}

func TestShellTimeoutKillsChildren(t *testing.T) {
	sh, root := newTestShell(t, ShellConfig{}, 10*time.Second, 8192)
	start := time.Now()
	// The background child would write the marker after 3s unless the whole
	// process group is killed.
	out := runShell(t, sh, `{"command":"(sleep 3; touch marker) & sleep 30","timeout":1}`)
	if took := time.Since(start); took > 6*time.Second {
		t.Errorf("took %s", took)
	}
	if !strings.Contains(out, "timed out after 1s") {
		t.Errorf("out = %q", out)
	}
	time.Sleep(3500 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(root, "marker")); err == nil {
		t.Error("a child process survived the timeout")
	}
}

func TestShellOutputCap(t *testing.T) {
	sh, _ := newTestShell(t, ShellConfig{}, 10*time.Second, 1000)
	out := runShell(t, sh, `{"command":"echo START; i=0; while [ $i -lt 2000 ]; do echo line$i; i=$((i+1)); done; echo END"}`)
	if len(out) > 1100 || !strings.Contains(out, "START") || !strings.Contains(out, "END") || !strings.Contains(out, "bytes of output cut") {
		t.Errorf("out (%d bytes) = %q", len(out), out)
	}
}

func TestShellUnavailable(t *testing.T) {
	ws, err := OpenWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newShellTool(ShellConfig{Program: []string{"/nonexistent/shell", "-c"}}, ws, 1000, time.Second); err == nil {
		t.Error("a missing shell was accepted")
	}
}

func TestShellOffByDefault(t *testing.T) {
	cfg := Config{Workspace: t.TempDir(), SkillsDir: t.TempDir()}.WithDefaults()
	reg, _, err := Builtins(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Lookup(ToolRunCommand); ok {
		t.Fatal("run_command is on without being enabled")
	}
	cfg.Tools = append(cfg.Tools, ToolsetShell)
	reg, un, err := Builtins(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Lookup(ToolRunCommand); !ok && !(os.Geteuid() == 0 && len(un) > 0) {
		t.Errorf("run_command missing when enabled: %v", un)
	}
}
