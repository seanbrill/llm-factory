package server

// The FileFreak archive's settings and upload queue, for the System page.
// The logic is internal/filefreak; see docs/FILEFREAK.md. Every route is
// behind the access token (auth.go) and the harness's local-request guard
// (same-origin JSON POSTs). The upload key goes in and never comes back out:
// the status carries only a hint of it.

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/seanbrill/local-llm/internal/filefreak"
	"github.com/seanbrill/local-llm/internal/version"
)

// openArchive sets up the archive and hands it to the builder, which asks it
// before every weight download. A config that can't be read leaves the
// archive off (downloads go to Hugging Face as before) and the routes say why.
func (s *Server) openArchive() {
	arc, err := filefreak.Open(filefreak.Options{
		ConfigDir:  s.b.ConfigDir,
		StagingDir: filepath.Join(s.b.ModelsDir, ".filefreak"),
		ToolsDir:   filepath.Join(s.b.BaseDir, "tools", "filefreak"),
		Version:    version.Get().Version,
	})
	if err != nil {
		log.Printf("filefreak: %v: the archive is off until it is fixed (or deleted to start fresh)", err)
		s.ffErr = err
		return
	}
	s.ff = arc
	s.b.Archive = arc
	// Weights already on disk that the archive may take are queued too, with
	// an upload key: soon after start, every few hours, and right after a key
	// is set (filefreak/scan.go).
	arc.StartScans(2*time.Minute, 6*time.Hour, s.localWeights)
}

// localWeights lists the catalog's weight files that are on disk, with the
// Hugging Face URL each came from: the model, its vision projector and its
// extra files. Several models can share a file; it's listed once.
func (s *Server) localWeights() []filefreak.LocalFile {
	var out []filefreak.LocalFile
	seen := map[string]bool{}
	add := func(file, url, label string) {
		if file == "" || url == "" {
			return
		}
		p := filepath.Join(s.b.ModelsDir, file)
		if seen[p] {
			return
		}
		if fi, err := os.Stat(p); err != nil || fi.Size() == 0 {
			return
		}
		seen[p] = true
		out = append(out, filefreak.LocalFile{URL: url, Path: p, Label: label})
	}
	for _, m := range s.cat.Models {
		add(m.File, m.URL, m.Name)
		add(m.MMProjFile, m.MMProjURL, m.Name+" (vision projector)")
		for _, w := range m.ExtraFiles {
			add(w.File, w.URL, m.Name+" ("+w.File+")")
		}
	}
	return out
}

func (s *Server) registerFileFreak() {
	s.mux.HandleFunc("/api/filefreak/status", guarded(http.MethodGet, s.handleFileFreakStatus))
	s.mux.HandleFunc("/api/filefreak/settings", guarded(http.MethodPost, s.handleFileFreakSettings))
	s.mux.HandleFunc("/api/filefreak/key", guarded(http.MethodPost, s.handleFileFreakKey))
	s.mux.HandleFunc("/api/filefreak/key/remove", guarded(http.MethodPost, s.handleFileFreakKeyRemove))
	s.mux.HandleFunc("/api/filefreak/folder/check", guarded(http.MethodPost, s.handleFileFreakFolderCheck))
	s.mux.HandleFunc("/api/filefreak/queue/retry", guarded(http.MethodPost, s.handleFileFreakRetry))
	s.mux.HandleFunc("/api/filefreak/queue/remove", guarded(http.MethodPost, s.handleFileFreakRemove))
}

func (s *Server) archiveOK(w http.ResponseWriter) bool {
	if s.ff != nil {
		return true
	}
	msg := "the FileFreak archive isn't set up"
	if s.ffErr != nil {
		msg = "config/filefreak.json or config/filefreak-queue.json can't be read (" + s.ffErr.Error() + "): fix it, or delete it to start fresh"
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": msg})
	return false
}

func (s *Server) writeArchiveStatus(w http.ResponseWriter, r *http.Request, extra map[string]any) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	out := map[string]any{"status": s.ff.Status(ctx)}
	for k, v := range extra {
		out[k] = v
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleFileFreakStatus(w http.ResponseWriter, r *http.Request) {
	if !s.archiveOK(w) {
		return
	}
	s.writeArchiveStatus(w, r, nil)
}

func (s *Server) handleFileFreakSettings(w http.ResponseWriter, r *http.Request) {
	if !s.archiveOK(w) {
		return
	}
	var req struct {
		ReadFirst  *bool   `json:"read_from_archive"`
		ArchiveURL *string `json:"archive_url"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if req.ArchiveURL != nil {
		if err := s.ff.SetArchiveURL(*req.ArchiveURL); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	if req.ReadFirst != nil {
		if err := s.ff.SetReadFirst(*req.ReadFirst); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}
	s.writeArchiveStatus(w, r, nil)
}

func (s *Server) handleFileFreakKey(w http.ResponseWriter, r *http.Request) {
	if !s.archiveOK(w) {
		return
	}
	var req struct {
		Key string `json:"key"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the key couldn't be read from the request"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	folderErr, err := s.ff.SetKey(ctx, req.Key)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	extra := map[string]any{}
	if folderErr != nil {
		extra["folder_error"] = folderErr.Error()
	}
	s.writeArchiveStatus(w, r, extra)
}

func (s *Server) handleFileFreakKeyRemove(w http.ResponseWriter, r *http.Request) {
	if !s.archiveOK(w) {
		return
	}
	if err := s.ff.RemoveKey(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.writeArchiveStatus(w, r, nil)
}

func (s *Server) handleFileFreakFolderCheck(w http.ResponseWriter, r *http.Request) {
	if !s.archiveOK(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	extra := map[string]any{}
	if err := s.ff.CheckFolder(ctx); err != nil {
		extra["folder_error"] = err.Error()
	}
	s.writeArchiveStatus(w, r, extra)
}

func (s *Server) handleFileFreakRetry(w http.ResponseWriter, r *http.Request) {
	s.fileFreakJob(w, r, s.ff.Retry)
}

func (s *Server) handleFileFreakRemove(w http.ResponseWriter, r *http.Request) {
	s.fileFreakJob(w, r, s.ff.Remove)
}

func (s *Server) fileFreakJob(w http.ResponseWriter, r *http.Request, act func(id string) error) {
	if !s.archiveOK(w) {
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := decodeBody(r, &req); err != nil || req.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "say which upload: {\"id\": …}"})
		return
	}
	if err := act(req.ID); err != nil {
		code := http.StatusConflict
		if errors.Is(err, filefreak.ErrNoJob) {
			code = http.StatusNotFound
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	s.writeArchiveStatus(w, r, nil)
}
