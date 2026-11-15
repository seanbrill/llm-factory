//go:build unix

package agent

import (
	"os"
	"os/exec"
	"syscall"
)

// setProcessGroup puts the command in its own process group, so a timeout
// stops everything it started.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup kills the command's whole process group.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}

func runningAsRoot() bool { return os.Geteuid() == 0 }
