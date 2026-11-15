package native

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Proc is one engine process the launcher runs (llama-server today; an
// ensemble would add stable-diffusion.cpp's and whisper.cpp's servers).
type Proc struct {
	Name   string // for logs, e.g. "llama-server"
	Path   string
	Args   []string
	Env    []string // added to the launcher's environment
	Dir    string
	Stdout io.Writer
	Stderr io.Writer
}

// Exit is a process that stopped, and how.
type Exit struct {
	Name string
	Err  error // nil: exit status 0
}

// Code is the exit status (-1 when it was killed or never ran).
func (e Exit) Code() int {
	if e.Err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(e.Err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// Supervisor runs several processes as one unit: Exited reports the first
// that stops (the launcher then stops the rest), and Stop ends them all, asking
// politely first (SIGTERM; on Windows, where a console program can't be asked
// that way, it is ended at once).
type Supervisor struct {
	mu     sync.Mutex
	procs  []*supervised
	first  chan Exit
	closed bool
}

type supervised struct {
	name string
	cmd  *exec.Cmd
	done chan struct{}
	exit Exit
}

// NewSupervisor makes an empty supervisor.
func NewSupervisor() *Supervisor { return &Supervisor{first: make(chan Exit, 1)} }

// Start starts p. It fails after Stop.
func (s *Supervisor) Start(p Proc) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("supervisor stopped")
	}
	cmd := exec.Command(p.Path, p.Args...)
	cmd.Dir = p.Dir
	cmd.Env = append(os.Environ(), p.Env...)
	cmd.Stdout, cmd.Stderr = p.Stdout, p.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", p.Name, err)
	}
	sp := &supervised{name: p.Name, cmd: cmd, done: make(chan struct{})}
	s.procs = append(s.procs, sp)
	go func() {
		err := cmd.Wait()
		sp.exit = Exit{Name: sp.name, Err: err}
		close(sp.done)
		select {
		case s.first <- sp.exit:
		default: // another one stopped first
		}
	}()
	return nil
}

// Exited delivers the first process that stops.
func (s *Supervisor) Exited() <-chan Exit { return s.first }

// Running reports whether a named process is still running.
func (s *Supervisor) Running(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.procs {
		if p.name == name {
			select {
			case <-p.done:
			default:
				return true
			}
		}
	}
	return false
}

// Stop ends every process: a terminate request first, then a kill for those
// still running after grace. It returns once all have exited.
func (s *Supervisor) Stop(grace time.Duration) {
	s.mu.Lock()
	s.closed = true
	procs := append([]*supervised(nil), s.procs...)
	s.mu.Unlock()
	for _, p := range procs {
		select {
		case <-p.done:
		default:
			_ = terminate(p.cmd.Process)
		}
	}
	deadline := time.After(grace)
	for _, p := range procs {
		select {
		case <-p.done:
		case <-deadline:
			for _, q := range procs {
				select {
				case <-q.done:
				default:
					_ = q.cmd.Process.Kill()
				}
			}
			for _, q := range procs {
				<-q.done
			}
			return
		}
	}
}
