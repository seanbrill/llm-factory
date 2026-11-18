package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// Engine is the slice of a container engine the member lifecycle uses. The
// Conductor drives the host's engine through its CLI (cliEngine); tests swap in
// a fake CLI.
type Engine interface {
	// Run starts a detached container.
	Run(ctx context.Context, spec RunSpec) error
	// Remove force-removes a container; a missing one is not an error.
	Remove(ctx context.Context, name string) error
	// HostPort is the host port the container's 8080 is published on.
	HostPort(ctx context.Context, name string) (int, error)
	// State is the container's state ("running", "exited", …), "" when there is
	// no such container.
	State(ctx context.Context, name string) (string, error)
	// List returns the containers (running or not) carrying label (k=v).
	List(ctx context.Context, label string) ([]Container, error)
	// Logs is the tail of a container's output, for failure messages.
	Logs(ctx context.Context, name string, tail int) string
}

// RunSpec is one member container to start.
type RunSpec struct {
	Name   string
	Image  string
	Labels map[string]string
	Port   int    // host port to publish 8080 on; 0 = any free one
	GPU    string // "cuda" (--gpus all) | "vulkan" (--device /dev/dri) | ""
	// Mounts are -v host:container[:ro] binds (a member's mounted weights),
	// Env KEY=VALUE variables.
	Mounts []string
	Env    []string
}

// Container is a row from List.
type Container struct {
	Name   string
	Image  string
	State  string
	Labels map[string]string
}

// cliEngine runs the docker (or podman) CLI.
type cliEngine struct{ bin string }

func (e cliEngine) out(ctx context.Context, args ...string) (string, error) {
	b, err := exec.CommandContext(ctx, e.bin, args...).CombinedOutput()
	s := strings.TrimSpace(string(b))
	if err != nil {
		return s, fmt.Errorf("%s %s: %v: %s", e.bin, args[0], err, s)
	}
	return s, nil
}

func noSuchContainer(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "no such container") || strings.Contains(s, "no such object")
}

// runArgs builds `run` arguments: loopback-only publishing (the members answer
// only the Conductor and this machine), and GPU access for a GPU image.
func runArgs(spec RunSpec) []string {
	args := []string{"run", "-d", "--name", spec.Name}
	keys := make([]string, 0, len(spec.Labels))
	for k := range spec.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--label", k+"="+spec.Labels[k])
	}
	if spec.Port > 0 {
		args = append(args, "-p", fmt.Sprintf("127.0.0.1:%d:8080", spec.Port))
	} else {
		args = append(args, "-p", "127.0.0.1::8080")
	}
	switch spec.GPU {
	case "cuda":
		args = append(args, "--gpus", "all")
	case "vulkan":
		args = append(args, "--device", "/dev/dri")
	}
	for _, m := range spec.Mounts {
		args = append(args, "-v", m)
	}
	for _, e := range spec.Env {
		args = append(args, "-e", e)
	}
	return append(args, spec.Image)
}

func (e cliEngine) Run(ctx context.Context, spec RunSpec) error {
	_, err := e.out(ctx, runArgs(spec)...)
	return err
}

func (e cliEngine) Remove(ctx context.Context, name string) error {
	out, err := e.out(ctx, "rm", "-f", name)
	if err != nil && noSuchContainer(out) {
		return nil
	}
	return err
}

// parseHostPort reads `docker port <name> 8080/tcp` output ("127.0.0.1:55012",
// possibly one line per address family).
func parseHostPort(out string) (int, error) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if i := strings.LastIndexByte(line, ':'); i >= 0 {
			if p, err := strconv.Atoi(line[i+1:]); err == nil && p > 0 {
				return p, nil
			}
		}
	}
	return 0, fmt.Errorf("no published port in %q", out)
}

func (e cliEngine) HostPort(ctx context.Context, name string) (int, error) {
	out, err := e.out(ctx, "port", name, "8080/tcp")
	if err != nil {
		return 0, err
	}
	return parseHostPort(out)
}

func (e cliEngine) State(ctx context.Context, name string) (string, error) {
	out, err := e.out(ctx, "inspect", "-f", "{{.State.Status}}", name)
	if err != nil {
		if noSuchContainer(out) {
			return "", nil
		}
		return "", err
	}
	return out, nil
}

func (e cliEngine) List(ctx context.Context, label string) ([]Container, error) {
	out, err := e.out(ctx, "ps", "-a", "--filter", "label="+label, "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	var cs []Container
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row map[string]any
		if json.Unmarshal([]byte(line), &row) != nil {
			continue
		}
		c := Container{Labels: map[string]string{}}
		c.Name, _ = row["Names"].(string)
		c.Image, _ = row["Image"].(string)
		c.State, _ = row["State"].(string)
		switch l := row["Labels"].(type) {
		case string: // docker: "k=v,k=v"
			for _, part := range strings.Split(l, ",") {
				if i := strings.IndexByte(part, '='); i > 0 {
					c.Labels[part[:i]] = part[i+1:]
				}
			}
		case map[string]any: // podman
			for k, v := range l {
				c.Labels[k] = fmt.Sprint(v)
			}
		}
		c.Name = strings.TrimPrefix(strings.Split(c.Name, ",")[0], "/")
		cs = append(cs, c)
	}
	return cs, nil
}

func (e cliEngine) Logs(ctx context.Context, name string, tail int) string {
	out, _ := exec.CommandContext(ctx, e.bin, "logs", "--tail", strconv.Itoa(tail), name).CombinedOutput()
	return strings.TrimSpace(string(out))
}
