package server

import (
	"context"
	"net/http"
	"time"
)

// handleGPUProbe runs the GPU access check on every engine — POST /api/gpu/probe
// -> {"engines": [builder.GPUProbe...], "checked_at": unix ms}. User-triggered
// (System → "Check GPU access"), because it starts a throwaway container per
// engine. Each reachable engine's answer is saved (config/gpu-probe.json) and
// shows in /api/sysinfo as that engine's gpu_check: an engine whose container
// got a usable /dev/dri gives Vulkan from then on, Docker included.
func (s *Server) handleGPUProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	engines := s.b.ProbeGPU(ctx)
	writeJSON(w, http.StatusOK, map[string]any{"engines": engines, "checked_at": time.Now().UnixMilli()})
}
