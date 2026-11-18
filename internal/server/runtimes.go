package server

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/seanbrill/local-llm/internal/builder"
)

// handleRuntimeBuild queues a model-less llama.cpp runtime (docs/RUNTIME.md):
// POST /api/runtimes/build {compute, engine, platform, image_name, tag}.
func (s *Server) handleRuntimeBuild(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Compute   string `json:"compute"`
		Engine    string `json:"engine"`
		Platform  string `json:"platform"`
		ImageName string `json:"image_name"`
		Tag       string `json:"tag"`
		Export    bool   `json:"export"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request: " + err.Error()})
		return
	}
	engine, compute := normalizeEngine(req.Engine), normalizeCompute(req.Compute)
	o := builder.RuntimeOptions{Engine: engine, Compute: compute, Platform: req.Platform,
		ImageName: req.ImageName, Tag: req.Tag, ExportTar: req.Export}
	ref, err := s.b.RuntimeRef(o)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	arch, _ := builder.PlatformArch(req.Platform)
	name := "llama.cpp runtime (" + compute + ", " + arch + ", no model)"
	job := &buildJob{
		kind: "runtime", lane: engine, ref: ref, name: name,
		cfg: buildConfig{ImageName: req.ImageName, Tag: req.Tag, Engine: engine, Compute: compute, Platform: req.Platform},
		eta: int(s.etaFor(compute, 0)),
		run: func(ctx context.Context, log builder.LogFunc) error {
			_, err := s.b.BuildRuntime(ctx, o, log)
			return err
		},
	}
	pos, err := s.q.enqueue(job)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	enqueueResponse(w, job, pos)
}
