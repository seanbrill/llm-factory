package server

import (
	"fmt"
	"os"
	"strings"

	"github.com/seanbrill/local-llm/internal/builder"
	"github.com/seanbrill/local-llm/internal/harness"
)

// runWorkspace picks the host folder a run mounts read-write at /workspace.
// Only an image whose baked harness has the files or shell tools gets one
// (labels are the image's); for any other image it returns "".
//
// requested "" means the factory's own workspace/ folder, which is created
// when missing. Another folder must exist on the host already. Either one is
// checked like the workspace terminal's (harness.ValidateWorkspace): never a
// system, hidden or whole home folder, nor anything else in the factory.
func (s *Server) runWorkspace(labels map[string]string, requested string) (string, error) {
	if !builder.HarnessUsesWorkspace(builder.BakedHarness(labels)) {
		return "", nil
	}
	p := harness.DetectPaths(s.b.BaseDir, s.b.HostDir)
	ws := strings.TrimSpace(requested)
	if ws == "" {
		ws = p.DefaultWorkspace()
	}
	if err := harness.ValidateWorkspace(ws, p.HostDir, p.HostHome); err != nil {
		return "", fmt.Errorf("can't mount %s at /workspace: %w", ws, err)
	}
	ws = harness.NormalizeHostPath(ws)
	if local, ok := p.LocalPath(ws); ok {
		if err := os.MkdirAll(local, 0o755); err != nil {
			return "", fmt.Errorf("create the workspace folder: %w", err)
		}
	}
	return ws, nil
}
