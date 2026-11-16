package builder

import (
	"encoding/json"
	"path"

	"github.com/seanbrill/local-llm/internal/agent"
)

// RunHarness is what a run gives an image's harness (a baked harness, or an
// ensemble's brain): MCP servers to connect and the admin token that lets
// the factory change them while it runs.
type RunHarness struct {
	// AdminToken turns on the harness's admin API (/v1/harness/mcp).
	AdminToken string
	// MCP servers, passed as HARNESS_MCP. A loopback URL should carry
	// connect_via (the engine's host alias); the harness adds it otherwise.
	MCP []agent.MCPServer
}

// HostAlias is how a container on engine reaches its host.
func HostAlias(engine string) string {
	if resolveEngine(engine) == enginePodman {
		return "host.containers.internal"
	}
	return "host.docker.internal"
}

// harnessRunArgs are the `run` flags for o.Harness: HARNESS_ADMIN_TOKEN,
// HARNESS_HOST_ALIAS, HARNESS_STATE_DIR (in the mounted workspace, so
// run-time MCP changes survive a restart) and HARNESS_MCP. Docker also gets
// the host alias added (Podman provides host.containers.internal itself).
func harnessRunArgs(engine string, o RunOptions) []string {
	h := o.Harness
	if h == nil || (h.AdminToken == "" && len(h.MCP) == 0) {
		return nil
	}
	alias := HostAlias(engine)
	args := []string{"-e", "HARNESS_HOST_ALIAS=" + alias}
	if h.AdminToken != "" {
		args = append(args, "-e", "HARNESS_ADMIN_TOKEN="+h.AdminToken)
	}
	if o.Workspace != "" {
		args = append(args, "-e", "HARNESS_STATE_DIR="+path.Join(ImageWorkspace, agent.StateDirName))
	}
	if len(h.MCP) > 0 {
		b, _ := json.Marshal(h.MCP)
		args = append(args, "-e", "HARNESS_MCP="+string(b))
	}
	if resolveEngine(engine) != enginePodman && !o.Orchestrator {
		args = append(args, "--add-host", "host.docker.internal:host-gateway")
	}
	return args
}
