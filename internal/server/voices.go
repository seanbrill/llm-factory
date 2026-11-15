// Voice clips for cloned persona voices: a short recording of someone speaking
// (mono WAV, 3–30 s) plus what they said. Stored in config/voices (0600 files in
// a 0700 folder, git-ignored) by internal/voice; nothing here sends a clip off
// this machine. Saving one requires the consent statement.
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/seanbrill/local-llm/internal/builder"
	"github.com/seanbrill/local-llm/internal/harness"
	"github.com/seanbrill/local-llm/internal/voice"
)

// handleVoices serves GET (list) and POST (multipart: file, name, transcript,
// consent=true).
func (s *Server) handleVoices(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		clips, err := s.voices.List()
		if err != nil {
			errorJSON(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"voices": clips, "consent": voice.ConsentStatement})
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, voice.MaxClipBytes+1<<20)
		if err := r.ParseMultipartForm(voice.MaxClipBytes + 1<<20); err != nil {
			errorJSON(w, http.StatusBadRequest, "send the clip as multipart form data (file, name, transcript, consent), at most 8 MB")
			return
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			errorJSON(w, http.StatusBadRequest, "send the recording as 'file' (a WAV)")
			return
		}
		wav, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			errorJSON(w, http.StatusBadRequest, "couldn't read the recording: "+err.Error())
			return
		}
		consent, _ := strconv.ParseBool(r.FormValue("consent"))
		c, err := s.voices.Add(r.FormValue("name"), wav, r.FormValue("transcript"), consent)
		if err != nil {
			errorJSON(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, c)
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
	}
}

// handleVoiceUpdate renames a clip or edits its transcript: {id, name?, transcript?}.
func (s *Server) handleVoiceUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID         string  `json:"id"`
		Name       *string `json:"name"`
		Transcript *string `json:"transcript"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		errorJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := s.voices.Update(req.ID, req.Name, req.Transcript)
	if errors.Is(err, voice.ErrNoClip) {
		errorJSON(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// handleVoiceDelete removes a clip. Personas that used it fall back to their
// speech member's default voice (their voice is cleared, so nothing points at
// a missing clip).
func (s *Server) handleVoiceDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.voices.Delete(req.ID); err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, voice.ErrNoClip) {
			code = http.StatusNotFound
		}
		errorJSON(w, code, err.Error())
		return
	}
	cleared := s.clearClip(req.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "personas_cleared": cleared})
}

// clearClip drops a deleted clip from the personas that used it.
func (s *Server) clearClip(id string) []string {
	s.personasMu.Lock()
	defer s.personasMu.Unlock()
	ps, err := s.readPersonasLocked()
	if err != nil {
		return nil
	}
	var names []string
	for i := range ps {
		if v := ps[i].Voice; v != nil && v.Kind == voice.KindClone && v.Clip == id {
			ps[i].Voice = nil
			names = append(names, ps[i].Name)
		}
	}
	if len(names) > 0 {
		_ = s.writePersonasLocked(ps)
	}
	return names
}

// handleVoiceAudio plays a clip back (GET ?id=), to this browser only.
func (s *Server) handleVoiceAudio(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	b, err := s.voices.Audio(r.URL.Query().Get("id"))
	if err != nil {
		http.Error(w, "no such voice clip", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	_, _ = w.Write(b)
}

// handleVoiceTranscribe fills in a clip's transcript with speech-to-text:
// {id, ensemble?} -> {text, source}. It asks the ensemble's Conductor when that
// ensemble runs, else any running speech-to-text model; with none, 409 (type
// the transcript instead). The clip goes only to a model on this machine.
func (s *Server) handleVoiceTranscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID       string `json:"id"`
		Ensemble string `json:"ensemble"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	wav, err := s.voices.Audio(req.ID)
	if err != nil {
		errorJSON(w, http.StatusNotFound, "no such voice clip")
		return
	}
	type source struct {
		name string
		port int
	}
	var sources []source
	if req.Ensemble != "" {
		if port, err := s.runningGate(r.Context(), req.Ensemble); err == nil {
			sources = append(sources, source{"the ensemble " + req.Ensemble, port})
		}
	}
	if rows, err := s.b.Containers(r.Context()); err == nil {
		for _, row := range rows {
			if harness.RowRunning(row) && harness.RowLabel(row, "local-llm.modality") == "audio-stt" && harness.RowPort(row) > 0 {
				name := harness.RowLabel(row, "local-llm.model")
				if name == "" {
					name = "port " + strconv.Itoa(harness.RowPort(row))
				}
				sources = append(sources, source{name, harness.RowPort(row)})
			}
		}
	}
	if len(sources) == 0 {
		errorJSON(w, http.StatusConflict, "no speech-to-text model is running: run this ensemble (or a speech-to-text model) to fill it in, or type what the clip says")
		return
	}
	var problems []string
	for _, src := range sources {
		text, err := s.transcribeAt(r.Context(), src.port, "voice.wav", wav, nil)
		if err == nil {
			writeJSON(w, http.StatusOK, map[string]string{"text": text, "source": src.name})
			return
		}
		problems = append(problems, src.name+": "+err.Error())
	}
	errorJSON(w, http.StatusBadGateway, "speech-to-text failed ("+strings.Join(problems, "; ")+"): type what the clip says")
}

// sttError is a speech-to-text failure with the model's status.
type sttError struct {
	status int
	msg    string
}

func (e *sttError) Error() string { return e.msg }

// transcribeAt sends audio to the speech-to-text model (or ensemble) on port:
// whisper.cpp's /inference, or the OpenAI-style /v1/audio/transcriptions when
// that 404s (audio.cpp's Moonshine). extra adds form fields (language, ...).
func (s *Server) transcribeAt(ctx context.Context, port int, filename string, audio []byte, extra map[string]string) (string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", filename)
	_, _ = fw.Write(audio)
	_ = mw.WriteField("response_format", "json")
	// audio.cpp images (Moonshine) need the id they serve their model under;
	// whisper-server ignores the field.
	_ = mw.WriteField("model", builder.AudioCppSTTModelID)
	for k, v := range extra {
		if v != "" {
			_ = mw.WriteField(k, v)
		}
	}
	_ = mw.Close()
	payload := buf.Bytes()

	// An ensemble may have to start its speech-to-text member first.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	post := func(path string) (*http.Response, error) {
		upReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.modelURL(port, path), bytes.NewReader(payload))
		upReq.Header.Set("Content-Type", mw.FormDataContentType())
		return gateClient.Do(upReq)
	}
	resp, err := post("/inference")
	if err == nil && resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		resp, err = post("/v1/audio/transcriptions")
	}
	if err != nil {
		return "", &sttError{http.StatusBadGateway, err.Error()}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return "", &sttError{resp.StatusCode, fmt.Sprintf("HTTP %d: %s", resp.StatusCode, upstreamError(b))}
	}
	var out struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(b, &out) == nil && out.Text != "" {
		return strings.TrimSpace(out.Text), nil
	}
	return strings.TrimSpace(string(b)), nil
}
