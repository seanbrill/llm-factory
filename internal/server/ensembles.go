// Ensemble endpoints: CRUD over saved Ensemble definitions plus a build trigger
// that assembles an Ensemble image (the Conductor + manifest) using the shared
// build-progress state, so the UI streams its log like any other build.
package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/yourorg/local-llm/internal/ensemble"
)

// handleEnsembles serves GET (list) and POST (create/update).
func (s *Server) handleEnsembles(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		es, err := s.ens.List()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ensembles": es})
	case http.MethodPost:
		var e ensemble.Ensemble
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		saved, err := s.ens.Save(e)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, saved)
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
	}
}

// handleEnsembleDelete removes an ensemble by id.
func (s *Server) handleEnsembleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.ens.Delete(req.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleEnsembleBuild assembles an Ensemble image, streaming progress through the
// shared build state (so the Build page's progress UI shows it).
func (s *Server) handleEnsembleBuild(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID        string `json:"id"`
		ImageName string `json:"image_name"`
		Tag       string `json:"tag"`
		Compute   string `json:"compute"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	e, ok, err := s.ens.Get(req.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown ensemble id: " + req.ID})
		return
	}
	if req.Compute != "" {
		e.Compute = normalizeCompute(req.Compute)
	}
	imageName := strings.TrimSpace(req.ImageName)
	if imageName == "" {
		imageName = "local-llm/ens-" + e.ID
	}
	tag := strings.TrimSpace(req.Tag)
	if tag == "" {
		tag = "latest"
	}
	engine := normalizeEngine(e.Engine)
	ref := imageName + ":" + tag
	cfg := buildConfig{ImageName: imageName, Tag: tag, Engine: engine, Compute: e.Compute}
	if !s.bs.tryStart(ref, 90, cfg) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a build is already in progress"})
		return
	}
	go func() {
		log := func(line string) { s.bs.append(line) }
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		if _, err := s.b.BuildEnsemble(ctx, e, imageName, tag, engine, true, s.modelHost, log); err != nil {
			s.bs.append("ERROR: " + err.Error())
			s.bs.finish("error")
			return
		}
		s.bs.append("DONE")
		s.bs.finish("done")
	}()
	writeJSON(w, http.StatusOK, map[string]string{"status": "started"})
}
