package server

// Endpoints behind the redesigned UI's build plan, library and overview:
//
//	POST /api/build/preflight  where each planned build compiles, what stops it, what it downloads
//	GET  /api/storage          what the factory keeps on disk, and the disk's free space
//	POST /api/storage/cleanup  remove leftover build folders (refused while a build runs)

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/seanbrill/local-llm/internal/builder"
)

type preflightItemReq struct {
	ModelID string `json:"model_id"`
	Engine  string `json:"engine"`
	Compute string `json:"compute"`
}

type preflightItemResp struct {
	ModelID string `json:"model_id"`
	Name    string `json:"name"`
	builder.PreflightPlan
	WeightsGB  float64 `json:"weights_gb"`  // weights the image bakes
	DownloadGB float64 `json:"download_gb"` // still to download (0 = on disk or self-contained)
	EtaSeconds int     `json:"eta_seconds"` // last build time for this compute (0 = unknown)
	RunBlock   bool    `json:"run_block"`   // too big to run on its engine at all
	RunNote    string  `json:"run_note,omitempty"`
}

// handleBuildPreflight answers POST /api/build/preflight {"items":[{model_id,
// engine, compute}]} with one plan per item plus the engines' memory, the disk's
// free space and the builds already waiting on each engine. Nothing is started.
func (s *Server) handleBuildPreflight(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Items []preflightItemReq `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(req.Items) > 50 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "plan at most 50 builds at a time"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	items := make([]builder.PreflightItem, 0, len(req.Items))
	known := make([]bool, len(req.Items))
	for i, it := range req.Items {
		m := s.modelByID(it.ModelID)
		if m == nil {
			continue
		}
		known[i] = true
		items = append(items, builder.PreflightItem{Model: *m, Engine: normalizeEngine(it.Engine), Compute: normalizeCompute(it.Compute)})
	}
	plans, engines := s.b.Preflight(ctx, items)

	out := make([]preflightItemResp, 0, len(req.Items))
	downloads := map[string]float64{} // model id -> GB, counted once per model
	var weights float64
	k := 0
	for i, it := range req.Items {
		if !known[i] {
			out = append(out, preflightItemResp{
				ModelID:       it.ModelID,
				PreflightPlan: builder.PreflightPlan{Engine: normalizeEngine(it.Engine), Compute: normalizeCompute(it.Compute), Error: "unknown model id: " + it.ModelID},
			})
			continue
		}
		m, p := items[k].Model, plans[k]
		k++
		resp := preflightItemResp{ModelID: m.ID, Name: m.Name, PreflightPlan: p, WeightsGB: s.b.WeightsGB(m), EtaSeconds: int(s.etaFor(p.Compute))}
		if dl, _ := s.b.ModelOnDisk(m); !dl && m.File != "" {
			resp.DownloadGB = m.SizeGB
			downloads[m.ID] = m.SizeGB
		}
		weights += resp.WeightsGB
		if e := engines[p.Engine]; e.Reachable && e.GB > 0 {
			resp.RunBlock, resp.RunNote = engineFit(m.Name, p.Engine, resp.WeightsGB, e.GB, p.Compute)
		}
		out = append(out, resp)
	}

	var downloadGB float64
	for _, gb := range downloads {
		downloadGB += gb
	}
	resp := map[string]any{
		"items":       out,
		"engines":     engines,
		"queued":      s.q.activeByLane(),
		"download_gb": downloadGB,
		// The downloads land in models/ and each image is also exported to
		// images/<ref>.tar, about the size of its weights.
		"disk_need_gb": downloadGB + weights,
	}
	if total, avail, err := builder.DiskSpace(s.b.ModelsDir); err == nil {
		resp["disk_total_gb"] = float64(total) / 1e9
		resp["disk_free_gb"] = float64(avail) / 1e9
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleStorage answers GET /api/storage: weights, image tars, leftover build
// folders and saved media, plus the disk's size and free space. Leftovers are
// only listed while no build is running (a running build's folder is in use).
func (s *Server) handleStorage(w http.ResponseWriter, r *http.Request) {
	busy := s.q.running()
	resp := map[string]any{
		"weights": s.b.WeightsUsage(),
		"tars":    s.b.TarsUsage(),
		"busy":    busy,
	}
	if busy {
		resp["leftovers"] = builder.UsageGroup{Items: []builder.UsageItem{}}
	} else {
		resp["leftovers"] = s.b.LeftoversUsage()
	}
	media := builder.UsageGroup{Items: []builder.UsageItem{}}
	if entries, err := os.ReadDir(s.mediaDir); err == nil {
		var bytes int64
		for _, e := range entries {
			if e.Name() == ".gitkeep" {
				continue
			}
			if info, err := e.Info(); err == nil && info.Mode().IsRegular() {
				media.Count++
				bytes += info.Size()
			}
		}
		media.GB = float64(bytes) / 1e9
	}
	resp["media"] = media
	if total, avail, err := builder.DiskSpace(s.b.ModelsDir); err == nil {
		resp["disk_total_gb"] = float64(total) / 1e9
		resp["disk_free_gb"] = float64(avail) / 1e9
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleStorageCleanup answers POST /api/storage/cleanup by removing leftover
// build folders and transfer tars. Refused (409) while a build runs, since its
// own folder lives there too.
func (s *Server) handleStorageCleanup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var removed []string
	var freed float64
	ran := s.q.whileIdle(func() {
		before := s.b.LeftoversUsage()
		removed = s.b.CleanStaleBuildDirs()
		freed = before.GB - s.b.LeftoversUsage().GB
	})
	if !ran {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "A build is running and uses that folder. Clean up after it finishes."})
		return
	}
	if freed < 0 {
		freed = 0
	}
	if removed == nil {
		removed = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed, "freed_gb": freed})
}
