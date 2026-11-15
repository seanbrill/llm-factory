package builder

import (
	"context"
	"testing"
)

func TestIsNoSuchContainer(t *testing.T) {
	yes := []string{
		"Error: No such container: local-llm-terminal",
		"Error response from daemon: No such container: x",
		"Error: no such object: x",
		"Error: no container with name or ID \"x\" found: no such container",
	}
	for _, s := range yes {
		if !isNoSuchContainer(s) {
			t.Errorf("%q not recognized", s)
		}
	}
	for _, s := range []string{"", "permission denied", "Cannot connect to the Docker daemon"} {
		if isNoSuchContainer(s) {
			t.Errorf("%q misread as missing", s)
		}
	}
}

func TestRunHelperOnlyRuns(t *testing.T) {
	b := &Builder{}
	for _, args := range [][]string{nil, {"rm", "-f", "x"}, {"exec", "x", "sh"}} {
		if _, err := b.RunHelper(context.Background(), "docker", args); err == nil {
			t.Errorf("RunHelper(%v) accepted", args)
		}
	}
}
