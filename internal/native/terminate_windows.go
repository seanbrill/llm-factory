//go:build windows

package native

import (
	"os"
	"syscall"
)

// terminate ends a process. Windows has no SIGTERM for console programs; a
// Ctrl-C in the console already reached llama-server (it shares the console),
// so what is left to do is end it.
func terminate(p *os.Process) error { return p.Kill() }

// StopSignals are the events that stop the launcher: Ctrl-C, and closing the
// console window (which Go reports as SIGTERM).
var StopSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}
