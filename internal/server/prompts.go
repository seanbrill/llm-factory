package server

// Saved prompts, for benchmarks: the same prompt sent to different models
// (or the same model built or tuned differently) and the answers compared.
// The prompts live in config/prompts.json and every measured run in
// config/prompt-results.json, so they outlast a browser's storage and a
// factory restart. The browser does the sending (the Playground's own
// stream, which measures time to the first token and the speed) and posts
// what it measured here.
//
//	GET    /api/prompts                 the prompts, newest first
//	POST   /api/prompts                 add one (or several: {"prompts": [...]}, an import)
//	PUT    /api/prompts/{id}            change one
//	DELETE /api/prompts/{id}            remove one and its results
//	GET    /api/prompts/results         the results (?prompt=<id> for one prompt's)
//	POST   /api/prompts/results         record one
//	DELETE /api/prompts/results/{id}    remove one

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	maxPrompts       = 500
	maxPromptResults = 1000
	maxPromptText    = 200 << 10 // bytes of a prompt or system prompt
	maxResultOutput  = 32 << 10  // bytes of an answer kept with its result
	maxPromptName    = 120
)

// SavedPrompt is one reusable prompt.
type SavedPrompt struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Text   string   `json:"text"`
	System string   `json:"system,omitempty"` // sent as the system message when set
	Tags   []string `json:"tags,omitempty"`
	// What a good answer has, for whoever compares them (not checked).
	Expect      string   `json:"expect,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"` // nil: the Playground's setting
	TopP        *float64 `json:"top_p,omitempty"`
	Created     string   `json:"created"`
	Updated     string   `json:"updated"`
}

// PromptResult is one measured answer to a saved prompt.
type PromptResult struct {
	ID       string `json:"id"`
	PromptID string `json:"prompt_id"`
	Batch    string `json:"batch,omitempty"` // results from one benchmark run share it
	// The model that answered.
	Model   string `json:"model"`              // its display name
	ModelID string `json:"model_id,omitempty"` // catalog id
	Ref     string `json:"ref,omitempty"`      // the image
	Engine  string `json:"engine,omitempty"`
	Compute string `json:"compute,omitempty"`
	// How it was asked.
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	// What was measured, in the browser.
	MS     float64  `json:"ms"`
	TTFT   *float64 `json:"ttft,omitempty"`
	Tokens int      `json:"tokens,omitempty"`
	TPS    *float64 `json:"tps,omitempty"`
	// The answer (cut at maxResultOutput) and the thinking's length.
	Output         string `json:"output,omitempty"`
	OutputBytes    int    `json:"output_bytes,omitempty"`
	ReasoningBytes int    `json:"reasoning_bytes,omitempty"`
	Error          string `json:"error,omitempty"`
	Source         string `json:"source,omitempty"` // "playground" | "bench"
	At             string `json:"at"`
}

type promptStore struct {
	mu          sync.Mutex
	path        string // config/prompts.json
	resultsPath string // config/prompt-results.json
}

func (s *Server) prompts() *promptStore {
	s.promptOnce.Do(func() {
		s.promptSt = &promptStore{
			path:        filepath.Join(s.b.ConfigDir, "prompts.json"),
			resultsPath: filepath.Join(s.b.ConfigDir, "prompt-results.json"),
		}
	})
	return s.promptSt
}

func (s *Server) registerPrompts() {
	s.mux.HandleFunc("/api/prompts", s.handlePrompts)
	s.mux.HandleFunc("/api/prompts/{id}", s.handlePrompt)
	s.mux.HandleFunc("/api/prompts/results", s.handlePromptResults)
	s.mux.HandleFunc("/api/prompts/results/{id}", s.handlePromptResult)
}

func newPromptID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func nowStamp() string { return time.Now().UTC().Format(time.RFC3339) }

func readJSONFile[T any](path string) ([]T, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []T
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func writeJSONFile(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(data, '\n'))
}

// clean checks a prompt from a client and fills what it may leave out.
func (p *SavedPrompt) clean() error {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		p.Name = firstLine(p.Text, 60)
	}
	if utf8.RuneCountInString(p.Name) > maxPromptName {
		p.Name = string([]rune(p.Name)[:maxPromptName])
	}
	if strings.TrimSpace(p.Text) == "" {
		return errors.New("the prompt has no text")
	}
	if len(p.Text) > maxPromptText || len(p.System) > maxPromptText {
		return errors.New("the prompt is too long (200 KB at most)")
	}
	if p.Temperature != nil && (*p.Temperature < 0 || *p.Temperature > 2) {
		return errors.New("temperature is between 0 and 2")
	}
	if p.TopP != nil && (*p.TopP <= 0 || *p.TopP > 1) {
		return errors.New("top_p is above 0 and at most 1")
	}
	var tags []string
	seen := map[string]bool{}
	for _, t := range p.Tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" && !seen[t] && len(tags) < 12 {
			seen[t] = true
			tags = append(tags, t)
		}
	}
	p.Tags = tags
	return nil
}

func firstLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if utf8.RuneCountInString(s) > n {
		s = strings.TrimSpace(string([]rune(s)[:n])) + "…"
	}
	return s
}

func (ps *promptStore) list() ([]SavedPrompt, error) {
	all, err := readJSONFile[SavedPrompt](ps.path)
	sort.SliceStable(all, func(i, j int) bool { return all[i].Updated > all[j].Updated })
	return all, err
}

func (s *Server) handlePrompts(w http.ResponseWriter, r *http.Request) {
	ps := s.prompts()
	switch r.Method {
	case http.MethodGet:
		ps.mu.Lock()
		all, err := ps.list()
		ps.mu.Unlock()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config/prompts.json can't be read: " + err.Error()})
			return
		}
		if all == nil {
			all = []SavedPrompt{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"prompts": all})
	case http.MethodPost:
		var in struct {
			SavedPrompt
			Prompts []SavedPrompt `json:"prompts"` // an import
		}
		if err := decodeBodyMax(r, &in, 8<<20); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		add := in.Prompts
		if len(add) == 0 {
			add = []SavedPrompt{in.SavedPrompt}
		}
		ps.mu.Lock()
		defer ps.mu.Unlock()
		all, err := ps.list()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config/prompts.json can't be read: " + err.Error()})
			return
		}
		ids := map[string]bool{}
		for _, p := range all {
			ids[p.ID] = true
		}
		now := nowStamp()
		var added []SavedPrompt
		for _, p := range add {
			if err := p.clean(); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			// An import keeps its ids unless they are taken here.
			if p.ID == "" || ids[p.ID] || len(p.ID) > 40 {
				p.ID = newPromptID()
			}
			ids[p.ID] = true
			if p.Created == "" {
				p.Created = now
			}
			p.Updated = now
			added = append(added, p)
		}
		if len(all)+len(added) > maxPrompts {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "that would be more than 500 saved prompts; delete some first"})
			return
		}
		if err := writeJSONFile(ps.path, append(added, all...)); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if len(in.Prompts) > 0 {
			writeJSON(w, http.StatusOK, map[string]any{"prompts": added})
			return
		}
		writeJSON(w, http.StatusOK, added[0])
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET or POST"})
	}
}

func (s *Server) handlePrompt(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ps := s.prompts()
	switch r.Method {
	case http.MethodPut:
		var in SavedPrompt
		if err := decodeBodyMax(r, &in, 1<<20); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := in.clean(); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		ps.mu.Lock()
		defer ps.mu.Unlock()
		all, err := ps.list()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		for i, p := range all {
			if p.ID != id {
				continue
			}
			in.ID, in.Created, in.Updated = id, p.Created, nowStamp()
			all[i] = in
			if err := writeJSONFile(ps.path, all); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, in)
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no saved prompt " + id})
	case http.MethodDelete:
		ps.mu.Lock()
		defer ps.mu.Unlock()
		all, err := ps.list()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		keep := all[:0]
		for _, p := range all {
			if p.ID != id {
				keep = append(keep, p)
			}
		}
		if len(keep) == len(all) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no saved prompt " + id})
			return
		}
		if err := writeJSONFile(ps.path, keep); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		// Its results go with it.
		if res, err := readJSONFile[PromptResult](ps.resultsPath); err == nil && len(res) > 0 {
			kr := res[:0]
			for _, x := range res {
				if x.PromptID != id {
					kr = append(kr, x)
				}
			}
			if len(kr) != len(res) {
				_ = writeJSONFile(ps.resultsPath, kr)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		w.Header().Set("Allow", "PUT, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "PUT or DELETE"})
	}
}

func (s *Server) handlePromptResults(w http.ResponseWriter, r *http.Request) {
	ps := s.prompts()
	switch r.Method {
	case http.MethodGet:
		ps.mu.Lock()
		all, err := readJSONFile[PromptResult](ps.resultsPath)
		ps.mu.Unlock()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config/prompt-results.json can't be read: " + err.Error()})
			return
		}
		out := []PromptResult{}
		pid := r.URL.Query().Get("prompt")
		for _, x := range all {
			if pid == "" || x.PromptID == pid {
				out = append(out, x)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"results": out})
	case http.MethodPost:
		var in PromptResult
		if err := decodeBodyMax(r, &in, 1<<20); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if in.PromptID == "" || strings.TrimSpace(in.Model) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a result needs prompt_id and model"})
			return
		}
		if in.OutputBytes == 0 {
			in.OutputBytes = len(in.Output)
		}
		if len(in.Output) > maxResultOutput {
			in.Output = truncateUTF8Str(in.Output, maxResultOutput)
		}
		if len(in.Error) > 2000 {
			in.Error = truncateUTF8Str(in.Error, 2000)
		}
		in.ID, in.At = newPromptID(), nowStamp()
		ps.mu.Lock()
		defer ps.mu.Unlock()
		prompts, err := ps.list()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		found := false
		for _, p := range prompts {
			found = found || p.ID == in.PromptID
		}
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no saved prompt " + in.PromptID})
			return
		}
		all, err := readJSONFile[PromptResult](ps.resultsPath)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		all = append(all, in)
		if len(all) > maxPromptResults { // the oldest go first
			all = all[len(all)-maxPromptResults:]
		}
		if err := writeJSONFile(ps.resultsPath, all); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, in)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET or POST"})
	}
}

func (s *Server) handlePromptResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", "DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "DELETE"})
		return
	}
	id := r.PathValue("id")
	ps := s.prompts()
	ps.mu.Lock()
	defer ps.mu.Unlock()
	all, err := readJSONFile[PromptResult](ps.resultsPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	keep := all[:0]
	for _, x := range all {
		if x.ID != id {
			keep = append(keep, x)
		}
	}
	if len(keep) == len(all) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no result " + id})
		return
	}
	if err := writeJSONFile(ps.resultsPath, keep); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// decodeBodyMax is decodeBody with a size limit of its own.
func decodeBodyMax(r *http.Request, v any, max int64) error {
	r.Body = http.MaxBytesReader(nil, r.Body, max)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		var mb *http.MaxBytesError
		if errors.As(err, &mb) {
			return errors.New("the request is too large")
		}
		if err.Error() == "EOF" {
			return errors.New("send a JSON body")
		}
		return errors.New("invalid JSON: " + err.Error())
	}
	return nil
}

func truncateUTF8Str(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
