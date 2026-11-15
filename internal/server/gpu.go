package server

import (
	"context"
	"net/http"
	"time"
)

// handleGPUProbe runs the GPU access check on every engine — POST /api/gpu/probe
// -> {"engines": [builder.GPUProbe...]}. User-triggered (Settings → "Check GPU
// access"), because it starts a throwaway container per engine.
func (s *Server) handleGPUProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	writeJSON(w, http.StatusOK, map[string]any{"engines": s.b.ProbeGPU(ctx)})
}
