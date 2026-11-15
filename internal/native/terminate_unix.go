//go:build !windows

package native

import (
	"os"
	"syscall"
)

// terminate asks a process to stop (llama-server shuts down cleanly on SIGTERM).
func terminate(p *os.Process) error { return p.Signal(syscall.SIGTERM) }

// StopSignals are the signals that stop the launcher: Ctrl-C, a plain kill,
// and the hang-up a closed Terminal window sends.
var StopSignals = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
