package builder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Helper containers (the harness's workspace terminal).
//
// These are long-lived sidecars the factory manages by name, like the Caddy
// proxy: they run on the first available engine (docker preferred) and are
// reached through their published host ports. Arguments are always passed as
// separate argv elements (exec, never a shell).

// HelperEngine is the engine helper containers run on.
func (b *Builder) HelperEngine() string { return proxyEngine() }

// HelperContainer is the inspected state of a named helper container.
type HelperContainer struct {
	Engine    string            `json:"engine"`
	Exists    bool              `json:"exists"`
	Running   bool              `json:"running"`
	Status    string            `json:"status"` // created | running | exited | restarting | paused | ...
	Image     string            `json:"image"`
	StartedAt string            `json:"started_at,omitempty"`
	ExitCode  int               `json:"exit_code"`
	OOMKilled bool              `json:"oom_killed"`
	Labels    map[string]string `json:"-"`
}

type inspectRecord struct {
	ImageName string `json:"ImageName"` // podman
	State     struct {
		Status    string `json:"Status"`
		Running   bool   `json:"Running"`
		StartedAt string `json:"StartedAt"`
		ExitCode  int    `json:"ExitCode"`
		OOMKilled bool   `json:"OOMKilled"`
	} `json:"State"`
	Config struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

func isNoSuchContainer(text string) bool {
	t := strings.ToLower(text)
	return strings.Contains(t, "no such container") || strings.Contains(t, "no such object") ||
		strings.Contains(t, "no container with name or id")
}

// InspectHelper inspects a container by name on one engine. A container that
// doesn't exist is reported as Exists=false with a nil error.
func (b *Builder) InspectHelper(ctx context.Context, engine, name string) (HelperContainer, error) {
	engine = resolveEngine(engine)
	hc := HelperContainer{Engine: engine}
	var stdout, stderr bytes.Buffer
	cmd := engineCmd(ctx, engine, "container", "inspect", name)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if isNoSuchContainer(stderr.String()) || isNoSuchContainer(stdout.String()) {
			return hc, nil
		}
		return hc, fmt.Errorf("%s container inspect: %v: %s", engine, err, strings.TrimSpace(stderr.String()))
	}
	var recs []inspectRecord
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &recs); err != nil {
		return hc, fmt.Errorf("%s container inspect: unexpected output: %w", engine, err)
	}
	if len(recs) == 0 {
		return hc, nil
	}
	r := recs[0]
	hc.Exists = true
	hc.Running = r.State.Running
	hc.Status = strings.ToLower(r.State.Status)
	hc.Image = r.Config.Image
	if r.ImageName != "" {
		hc.Image = r.ImageName
	}
	hc.StartedAt = r.State.StartedAt
	hc.ExitCode = r.State.ExitCode
	hc.OOMKilled = r.State.OOMKilled
	hc.Labels = r.Config.Labels
	return hc, nil
}

// FindHelper looks for a named container on every available engine and returns
// the first one found (Exists=false, Engine=HelperEngine when there is none).
// The error is non-nil only when no engine could be asked at all.
func (b *Builder) FindHelper(ctx context.Context, name string) (HelperContainer, error) {
	var firstErr error
	asked := false
	for _, engine := range candidateEngines() {
		if engineIsDown(engine) && engine != b.HelperEngine() {
			continue
		}
		hc, err := b.InspectHelper(ctx, engine, name)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		asked = true
		if hc.Exists {
			return hc, nil
		}
	}
	if asked {
		firstErr = nil
	}
	return HelperContainer{Engine: b.HelperEngine()}, firstErr
}

// PullImage pulls ref on engine, forwarding progress lines to log.
func (b *Builder) PullImage(ctx context.Context, engine, ref string, log LogFunc) error {
	if log == nil {
		log = func(string) {}
	}
	var last []string
	err := runStreaming(ctx, b.BaseDir, engine, func(line string) {
		last = append(last, line)
		if len(last) > 5 {
			last = last[1:]
		}
		log(line)
	}, "pull", ref)
	if err != nil {
		return fmt.Errorf("%s pull %s: %v: %s", resolveEngine(engine), ref, err, strings.Join(last, " | "))
	}
	return nil
}

// RunHelper runs `<engine> <args...>` (args start with "run") and returns the
// new container's id.
func (b *Builder) RunHelper(ctx context.Context, engine string, args []string) (string, error) {
	if len(args) == 0 || args[0] != "run" {
		return "", errors.New("RunHelper: args must start with \"run\"")
	}
	var stdout, stderr bytes.Buffer
	cmd := engineCmd(ctx, engine, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s run: %v: %s", resolveEngine(engine), err, strings.TrimSpace(stderr.String()))
	}
	id := strings.TrimSpace(stdout.String())
	if i := strings.LastIndexByte(id, '\n'); i >= 0 { // pull chatter before the id
		id = strings.TrimSpace(id[i+1:])
	}
	return id, nil
}

// StartHelper starts an existing (stopped) container.
func (b *Builder) StartHelper(ctx context.Context, engine, name string) error {
	out, err := engineCmd(ctx, engine, "start", name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s start: %v: %s", resolveEngine(engine), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// StopHelper stops a container (keeping it, so a later start resumes it). A
// container that doesn't exist is not an error.
func (b *Builder) StopHelper(ctx context.Context, engine, name string) error {
	out, err := engineCmd(ctx, engine, "stop", "-t", "10", name).CombinedOutput()
	if err != nil && !isNoSuchContainer(string(out)) {
		return fmt.Errorf("%s stop: %v: %s", resolveEngine(engine), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RemoveHelper force-removes a container. A container that doesn't exist is
// not an error.
func (b *Builder) RemoveHelper(ctx context.Context, engine, name string) error {
	if err := b.Stop(ctx, engine, name); err != nil && !isNoSuchContainer(err.Error()) {
		return err
	}
	return nil
}
