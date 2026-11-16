package builder

import (
	"strings"
	"testing"

	"github.com/seanbrill/local-llm/internal/agent"
)

func TestHarnessRunArgs(t *testing.T) {
	o := RunOptions{Ref: "img", HostPort: 8090, Workspace: "/w", Harness: &RunHarness{AdminToken: "tok",
		MCP: []agent.MCPServer{{Name: "k", URL: "http://127.0.0.1:1/x", ConnectVia: "host.docker.internal"}}}}
	got := strings.Join(runArgs("docker", "localllm-8090", o, -1), " ")
	for _, want := range []string{"HARNESS_ADMIN_TOKEN=tok", "HARNESS_HOST_ALIAS=host.docker.internal",
		"HARNESS_STATE_DIR=/workspace/.llm-harness", `HARNESS_MCP=[{"name":"k"`, "--add-host host.docker.internal:host-gateway"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if !strings.HasSuffix(got, " img") {
		t.Errorf("the image must stay last: %s", got)
	}
	if a := harnessRunArgs("docker", RunOptions{}); a != nil {
		t.Errorf("no harness: %v", a)
	}
}
