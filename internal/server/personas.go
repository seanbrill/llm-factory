// Personas are named, reusable system prompts. They're stored in
// config/personas.json and can be applied when building an image (baked in) or
// when running one (a per-instance override) — so a user can curate a small
// library of behaviours ("FinBot", "Donald", "Pirate") once and reuse them.
package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Persona is one saved system prompt.
type Persona struct {
	ID     string `json:"id"`     // stable slug key (derived from the name)
	Name   string `json:"name"`   // display name
	Prompt string `json:"prompt"` // the system prompt text
}

type personaDoc struct {
	Personas []Persona `json:"personas"`
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	return strings.Trim(slugRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-"), "-")
}

func (s *Server) personasFile() string {
	return filepath.Join(s.b.BaseDir, "config", "personas.json")
}

// readPersonasLocked loads the catalog; a missing file is an empty list, not an
// error. Caller must hold personasMu.
func (s *Server) readPersonasLocked() ([]Persona, error) {
	data, err := os.ReadFile(s.personasFile())
	if os.IsNotExist(err) {
		return []Persona{}, nil
	}
	if err != nil {
		return nil, err
	}
	var doc personaDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse personas.json: %w", err)
	}
	if doc.Personas == nil {
		doc.Personas = []Persona{}
	}
	return doc.Personas, nil
}

// writePersonasLocked persists the list (pretty-printed). Caller holds personasMu.
func (s *Server) writePersonasLocked(ps []Persona) error {
	dir := filepath.Join(s.b.BaseDir, "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(personaDoc{Personas: ps}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.personasFile(), data, 0o644)
}

// handlePersonas serves GET (list) and POST (create or update by id).
func (s *Server) handlePersonas(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.personasMu.Lock()
		ps, err := s.readPersonasLocked()
		s.personasMu.Unlock()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"personas": ps})

	case http.MethodPost:
		var p Persona
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		p.Name = strings.TrimSpace(p.Name)
		p.Prompt = strings.TrimSpace(p.Prompt)
		if p.Name == "" || p.Prompt == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and prompt are required"})
			return
		}
		if p.ID == "" {
			p.ID = slugify(p.Name)
		}
		if p.ID == "" { // name was all punctuation/non-ascii
			p.ID = fmt.Sprintf("persona-%d", time.Now().UnixNano())
		}
		s.personasMu.Lock()
		defer s.personasMu.Unlock()
		ps, err := s.readPersonasLocked()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		updated := false
		for i := range ps {
			if ps[i].ID == p.ID {
				ps[i] = p
				updated = true
				break
			}
		}
		if !updated {
			ps = append(ps, p)
		}
		if err := s.writePersonasLocked(ps); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, p)

	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
	}
}

// handlePersonaDelete removes a persona by id.
func (s *Server) handlePersonaDelete(w http.ResponseWriter, r *http.Request) {
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
	s.personasMu.Lock()
	defer s.personasMu.Unlock()
	ps, err := s.readPersonasLocked()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := make([]Persona, 0, len(ps))
	for _, p := range ps {
		if p.ID != req.ID {
			out = append(out, p)
		}
	}
	if err := s.writePersonasLocked(out); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
