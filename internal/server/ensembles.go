// Ensemble endpoints: CRUD over saved Ensemble definitions, the image build,
// and the running ensemble: start, stop, status (each member's state, read
// from its Conductor), member start/stop, the voice chain, and keeping the
// Conductor's persona and voice in step with the saved persona.
package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/seanbrill/local-llm/internal/builder"
	"github.com/seanbrill/local-llm/internal/ensemble"
	"github.com/seanbrill/local-llm/internal/harness"
	"github.com/seanbrill/local-llm/internal/voice"
)

// ensembleImage is the image an ensemble builds into.
func ensembleImage(id string) string { return "local-llm/ens-" + id + ":latest" }

// imageLabels reads an image's labels (the builder's, unless a test hooks it).
func (s *Server) imageLabels(ctx context.Context, engine, ref string) (map[string]string, error) {
	if s.imageLabelsFn != nil {
		return s.imageLabelsFn(ctx, engine, ref)
	}
	return s.b.ImageLabels(ctx, engine, ref)
}

// containers lists the factory's containers (the builder's, unless hooked).
func (s *Server) containers(ctx context.Context) ([]map[string]any, error) {
	if s.containersFn != nil {
		return s.containersFn(ctx)
	}
	return s.b.Containers(ctx)
}

// errorJSON writes {"error": msg}.
func errorJSON(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// upstreamError pulls a readable message out of an error body ({"error":
// "..."}, {"error": {"message": "..."}}, or text).
func upstreamError(b []byte) string {
	var v struct {
		Error any `json:"error"`
	}
	if json.Unmarshal(b, &v) == nil {
		switch e := v.Error.(type) {
		case string:
			return e
		case map[string]any:
			if m, ok := e["message"].(string); ok {
				return m
			}
		}
	}
	s := strings.TrimSpace(string(b))
	if len(s) > 500 {
		s = s[:500] + "…"
	}
	return s
}

// ---- definitions ------------------------------------------------------------

// handleEnsembles serves GET (list) and POST (create, or update by id). A new
// ensemble whose name is taken is refused (409) instead of replacing the other.
func (s *Server) handleEnsembles(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		es, err := s.ens.List()
		if err != nil {
			errorJSON(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ensembles": es})
	case http.MethodPost:
		var e ensemble.Ensemble
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&e); err != nil {
			errorJSON(w, http.StatusBadRequest, "the ensemble isn't valid JSON: "+err.Error())
			return
		}
		s.enrichMembers(r.Context(), &e)
		saved, err := s.ens.Save(e)
		if errors.Is(err, ensemble.ErrExists) {
			errorJSON(w, http.StatusConflict, err.Error())
			return
		}
		if err != nil {
			errorJSON(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, saved)
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
	}
}

// enrichMembers fills each member from its image's labels and the catalog:
// model, modality, runtime, compute, footprint and the speech facts the voice
// contract needs (voice_clone, clone_needs_transcript, clone_only).
func (s *Server) enrichMembers(ctx context.Context, e *ensemble.Ensemble) {
	engine := normalizeEngine(e.Engine)
	for i := range e.Members {
		m := &e.Members[i]
		m.Image = strings.TrimSpace(m.Image)
		if labels, err := s.imageLabels(ctx, engine, m.Image); err == nil {
			if v := labels["local-llm.model"]; v != "" {
				m.Model = v
			}
			if v := labels["local-llm.modality"]; v != "" {
				m.Modality = v
			}
			if v := labels["local-llm.runtime"]; v != "" {
				m.Runtime = v
			}
			if v := labels["local-llm.compute"]; v != "" {
				m.Compute = v
			}
		}
		s.applyCatalog(m)
	}
}

// applyCatalog sets the catalog's facts on a member.
func (s *Server) applyCatalog(m *ensemble.Member) {
	cat := s.modelByID(m.Model)
	if cat == nil {
		// A custom image: guess from the runtime (audio.cpp may clone).
		m.VoiceClone = m.Tool == ensemble.ToolSpeak && voice.CloneCapable(m.Runtime)
		m.CloneNeedsTranscript, m.CloneOnly = false, false
		return
	}
	if m.Runtime == "" {
		m.Runtime = cat.Runtime
	}
	if m.Modality == "" {
		m.Modality = cat.Mod()
	}
	if m.VRAMGB <= 0 {
		if m.Compute == "cuda" || m.Compute == "vulkan" {
			m.VRAMGB = cat.MinVRAMGB
		} else {
			m.VRAMGB = cat.MinRAMGB
		}
	}
	m.VoiceClone, m.CloneNeedsTranscript, m.CloneOnly, m.Family = cat.VoiceClone, cat.CloneNeedsTranscript, false, ""
	if cat.AudioCpp != nil {
		m.Family = cat.AudioCpp.Family
		m.CloneOnly = cat.VoiceClone && voice.CloneOnly(cat.AudioCpp.Family, cat.AudioCpp.DefaultVoice)
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
		errorJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	// A running one would be left behind with nothing on the page to stop it.
	if runs, err := s.ensembleRuns(r.Context()); err == nil {
		if _, ok := runs[req.ID]; ok {
			errorJSON(w, http.StatusConflict, "it's still running. Stop it first")
			return
		}
	}
	if err := s.ens.Delete(req.ID); err != nil {
		errorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleEnsembleBuild queues an Ensemble image build on the shared build queue
// (so it streams like any other build) and returns {id, position}.
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
		errorJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	e, ok, err := s.ens.Get(req.ID)
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		errorJSON(w, http.StatusBadRequest, "unknown ensemble id: "+req.ID)
		return
	}
	if req.Compute != "" {
		e.Compute = normalizeCompute(req.Compute)
	}
	// Members may have been rebuilt since the ensemble was saved.
	s.enrichMembers(r.Context(), &e)
	if _, err := builder.EnsembleManifest(e); err != nil {
		errorJSON(w, http.StatusBadRequest, err.Error())
		return
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
	job := &buildJob{
		kind: "ensemble", lane: engine, ref: imageName + ":" + tag, name: e.Name, eta: 90,
		cfg: buildConfig{ImageName: imageName, Tag: tag, Engine: engine, Compute: e.Compute},
		run: func(ctx context.Context, log builder.LogFunc) error {
			_, err := s.b.BuildEnsemble(ctx, e, imageName, tag, engine, true, log)
			return err
		},
	}
	pos, err := s.q.enqueue(job)
	if err != nil {
		errorJSON(w, http.StatusConflict, err.Error())
		return
	}
	enqueueResponse(w, job, pos)
}

// ---- running ensembles ----------------------------------------------------------

// ensembleRun is a running Conductor container.
type ensembleRun struct {
	Container string `json:"container"`
	Port      int    `json:"port"`
	Engine    string `json:"engine"`
	ID        string `json:"id"`
}

// ensembleRuns maps ensemble id -> its running Conductor, from the engines'
// container lists (the image's local-llm.ensemble label).
func (s *Server) ensembleRuns(ctx context.Context) (map[string]ensembleRun, error) {
	rows, err := s.containers(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]ensembleRun{}
	for _, row := range rows {
		id := harness.RowLabel(row, ensemble.LabelEnsemble)
		if id == "" || !harness.RowRunning(row) {
			continue
		}
		name, _ := row["Names"].(string)
		cid, _ := row["ID"].(string)
		engine, _ := row["Engine"].(string)
		out[id] = ensembleRun{Container: strings.TrimPrefix(strings.Split(name, ",")[0], "/"), Port: harness.RowPort(row), Engine: normalizeEngine(engine), ID: cid}
	}
	return out, nil
}

// usedPorts are the host ports running containers publish.
func (s *Server) usedPorts(ctx context.Context) map[int]bool {
	used := map[int]bool{}
	if rows, err := s.containers(ctx); err == nil {
		for _, row := range rows {
			if p := harness.RowPort(row); p > 0 {
				used[p] = true
			}
		}
	}
	return used
}

// freeEnsemblePort picks a host port for a Conductor: 8200 and up, skipping
// ports containers publish and (natively) ports something listens on.
func (s *Server) freeEnsemblePort(ctx context.Context) int {
	used := s.usedPorts(ctx)
	for p := 8200; p < 8400; p++ {
		if used[p] {
			continue
		}
		if l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p)); err == nil {
			l.Close()
			return p
		}
	}
	return 8200
}

// gateClient talks to running Conductors (local, no proxy).
var gateClient = &http.Client{Transport: &http.Transport{Proxy: nil, MaxIdleConnsPerHost: 4, IdleConnTimeout: 60 * time.Second}}

// gateStatus reads a Conductor's GET /ensemble/status.
func (s *Server) gateStatus(ctx context.Context, port int) (*ensemble.Status, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.modelURL(port, "/ensemble/status"), nil)
	resp, err := gateClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errNotGate
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var st ensemble.Status
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&st); err != nil || st.ID == "" {
		return nil, errNotGate
	}
	return &st, nil
}

// errNotGate: something answers on the port, and it isn't a Conductor.
var errNotGate = errors.New("not an ensemble")

// forgetGatePort drops what ensembleAt remembers about a port (a new
// container was just started on it).
func (s *Server) forgetGatePort(port int) {
	s.gatePortsMu.Lock()
	delete(s.notGate, port)
	s.gatePortsMu.Unlock()
}

// ensembleAt reports whether a model port is a Conductor, and its status.
// Ports that aren't are remembered (10 minutes when something else answered,
// 15 s when nothing did), so chat with a plain model doesn't ask every time;
// running a container on a port forgets it.
func (s *Server) ensembleAt(ctx context.Context, port int) *ensemble.Status {
	s.gatePortsMu.Lock()
	if t, ok := s.notGate[port]; ok && time.Now().Before(t) {
		s.gatePortsMu.Unlock()
		return nil
	}
	s.gatePortsMu.Unlock()
	st, err := s.gateStatus(ctx, port)
	s.gatePortsMu.Lock()
	defer s.gatePortsMu.Unlock()
	if s.notGate == nil {
		s.notGate = map[int]time.Time{}
	}
	if err != nil {
		ttl := 15 * time.Second
		if errors.Is(err, errNotGate) {
			ttl = 10 * time.Minute
		}
		s.notGate[port] = time.Now().Add(ttl)
		return nil
	}
	delete(s.notGate, port)
	return st
}

// persona returns a saved persona by id.
func (s *Server) persona(id string) (Persona, bool) {
	s.personasMu.Lock()
	defer s.personasMu.Unlock()
	ps, err := s.readPersonasLocked()
	if err != nil {
		return Persona{}, false
	}
	for _, p := range ps {
		if p.ID == id {
			return p, true
		}
	}
	return Persona{}, false
}

// ensembleConfig is what a running ensemble should apply: its persona's
// prompt and voice, the clip read from the store. problem says why the voice
// (or persona) can't be used as saved, for the Ensembles page.
func (s *Server) ensembleConfig(e ensemble.Ensemble) (c ensemble.Config, problem string) {
	defer func() { c.Hash = c.ComputeHash() }()
	if e.Persona == "" {
		return c, ""
	}
	p, ok := s.persona(e.Persona)
	if !ok {
		return c, fmt.Sprintf("the persona %q no longer exists: pick another on the Ensembles page", e.Persona)
	}
	c.Persona = &ensemble.ConfigPersona{ID: p.ID, Name: strings.TrimSpace(p.Name), Prompt: strings.TrimSpace(p.Prompt)}
	if p.Voice == nil {
		return c, ""
	}
	v := p.Voice.Normalize()
	req := voice.SpeechRequest{Input: "test"}
	switch v.Kind {
	case voice.KindBuiltin:
		c.Voice = &ensemble.ConfigVoice{Kind: voice.KindBuiltin, Name: v.Name}
		req.Voice = v.Name
	case voice.KindClone:
		clip, err := s.voices.Get(v.Clip)
		var audio []byte
		if err == nil {
			audio, err = s.voices.Audio(v.Clip)
		}
		if err != nil {
			return c, fmt.Sprintf("%s's voice clip is gone (%v): record or pick another on the Personas page", p.Name, err)
		}
		c.Voice = &ensemble.ConfigVoice{Kind: voice.KindClone, Clip: v.Clip, RefAudio: base64.StdEncoding.EncodeToString(audio), RefText: clip.Transcript}
		req.RefAudio, req.RefText = c.Voice.RefAudio, c.Voice.RefText
	default:
		return c, ""
	}
	// Can the speech member speak it? The same adapter check the Conductor runs.
	m, ok := e.Member(ensemble.ToolSpeak)
	if !ok {
		return c, fmt.Sprintf("%s has a voice, but this ensemble has no speech member to speak it", p.Name)
	}
	if _, err := voice.For(voice.Speaker{Runtime: m.Runtime, Model: m.Model, Clone: m.VoiceClone,
		NeedsTranscript: m.CloneNeedsTranscript, CloneOnly: m.CloneOnly}).Call(req); err != nil {
		return c, err.Error()
	}
	return c, ""
}

// syncPersona makes a running Conductor apply its ensemble's current persona
// and voice (it holds them in memory only, so a restart loses them). st is
// updated in place; the result is any problem with the voice.
func (s *Server) syncPersona(ctx context.Context, port int, st *ensemble.Status) string {
	if s.ens == nil || s.voices == nil {
		return ""
	}
	e, ok, err := s.ens.Get(st.ID)
	if err != nil || !ok {
		return ""
	}
	want, problem := s.ensembleConfig(e)
	if st.Persona == nil || st.Persona.Hash != want.Hash {
		body, _ := json.Marshal(want)
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.modelURL(port, "/ensemble/config"), bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := gateClient.Do(req)
		if err != nil {
			return "couldn't apply the persona: " + err.Error()
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "the ensemble refused the persona: " + upstreamError(b)
		}
		var ps ensemble.PersonaStatus
		if json.Unmarshal(b, &ps) == nil {
			st.Persona = &ps
		}
	}
	if st.Persona != nil {
		st.Persona.Synced = st.Persona.Hash == want.Hash
	}
	return problem
}

// syncPersonaAt syncs the persona when port is a Conductor (before chat,
// speech and the gateway). Problems are logged; the request goes on.
func (s *Server) syncPersonaAt(ctx context.Context, port int) {
	if st := s.ensembleAt(ctx, port); st != nil {
		if problem := s.syncPersona(ctx, port, st); problem != "" {
			log.Printf("ensemble %s: %s", st.ID, problem)
		}
	}
}

// ensembleLive is one entry of GET /api/ensembles/status.
type ensembleLive struct {
	ID         string           `json:"id"`
	Image      string           `json:"image"`
	Built      bool             `json:"built"`
	Running    *ensembleRun     `json:"running"`
	Gate       *ensemble.Status `json:"gate,omitempty"`
	GateError  string           `json:"gate_error,omitempty"`
	VoiceError string           `json:"voice_error,omitempty"`
}

// handleEnsemblesStatus reports every saved ensemble: built, running, and (from
// its Conductor) each member's state. It also keeps personas in step.
func (s *Server) handleEnsemblesStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	es, err := s.ens.List()
	if err != nil {
		errorJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	runs, runErr := s.ensembleRuns(r.Context())
	out := make([]ensembleLive, len(es))
	var wg sync.WaitGroup
	for i, e := range es {
		wg.Add(1)
		go func(i int, e ensemble.Ensemble) {
			defer wg.Done()
			live := ensembleLive{ID: e.ID, Image: ensembleImage(e.ID)}
			labels, err := s.imageLabels(r.Context(), normalizeEngine(e.Engine), live.Image)
			live.Built = err == nil && len(labels) > 0
			if run, ok := runs[e.ID]; ok {
				live.Running = &run
				st, err := s.gateStatus(r.Context(), run.Port)
				if err != nil {
					live.GateError = "the Conductor isn't answering yet (" + err.Error() + "): it may still be starting; see its log"
				} else {
					live.VoiceError = s.syncPersona(r.Context(), run.Port, st)
					live.Gate = st
				}
			} else if runErr != nil {
				live.GateError = "can't list containers: " + runErr.Error()
			}
			if live.VoiceError == "" && live.Running == nil {
				_, live.VoiceError = s.ensembleConfig(e)
			}
			out[i] = live
		}(i, e)
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, map[string]any{"ensembles": out})
}

// handleEnsembleRun starts an ensemble's Conductor: its image with the host
// engine's socket and host.docker.internal, on a free port unless one is
// given. The members start when a request needs them (or now, if kept warm).
func (s *Server) handleEnsembleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID   string `json:"id"`
		Port int    `json:"port"`
		// MCP names registered MCP servers the brain gets (see mcp.go).
		MCP []string `json:"mcp"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	e, ok, err := s.ens.Get(req.ID)
	if err != nil || !ok {
		errorJSON(w, http.StatusNotFound, "unknown ensemble id: "+req.ID)
		return
	}
	engine := normalizeEngine(e.Engine)
	ref := ensembleImage(e.ID)
	labels, err := s.imageLabels(r.Context(), engine, ref)
	if err != nil || len(labels) == 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "build the ensemble first: its image " + ref + " doesn't exist yet", "code": "not_built"})
		return
	}
	if runs, err := s.ensembleRuns(r.Context()); err == nil {
		if run, ok := runs[e.ID]; ok {
			writeJSON(w, http.StatusOK, map[string]any{"id": run.ID, "port": run.Port, "engine": run.Engine, "warning": "it was already running"})
			return
		}
	}
	port := req.Port
	if port <= 0 {
		port = s.freeEnsemblePort(r.Context())
	}
	compute := labels["local-llm.compute"]
	labels["local-llm.kind"] = "ensemble" // the brain runs the agent loop
	runHarness, mcpNotes, err := s.runHarness(labels, req.MCP, engine)
	if err != nil {
		errorJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	id, err := s.b.Run(r.Context(), builder.RunOptions{Ref: ref, HostPort: port, Engine: engine, Compute: compute, Orchestrator: true, Harness: runHarness})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, engineErrPayload(err))
		return
	}
	s.rememberRun(port, runHarness)
	if err := s.b.RegenProxy(r.Context()); err != nil {
		log.Printf("regen proxy after ensemble run: %v", err)
	}
	s.forgetGatePort(port)
	// Push the persona as soon as the Conductor answers.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		for ctx.Err() == nil {
			if st, err := s.gateStatus(ctx, port); err == nil {
				if problem := s.syncPersona(ctx, port, st); problem != "" {
					log.Printf("ensemble %s: %s", e.ID, problem)
				}
				return
			}
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
		}
	}()
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "port": port, "engine": engine, "mcp": mcpNames(runHarness), "mcp_notes": mcpNotes})
}

// handleEnsembleStop stops an ensemble's Conductor and removes its members.
func (s *Server) handleEnsembleStop(w http.ResponseWriter, r *http.Request) {
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
	e, ok, _ := s.ens.Get(req.ID)
	engine := "docker"
	if ok {
		engine = normalizeEngine(e.Engine)
	}
	runs, err := s.ensembleRuns(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, engineErrPayload(err))
		return
	}
	if run, ok := runs[req.ID]; ok {
		engine = run.Engine
		if err := s.b.Stop(r.Context(), run.Engine, run.ID); err != nil {
			writeJSON(w, http.StatusInternalServerError, engineErrPayload(err))
			return
		}
	}
	if err := s.b.RemoveEnsembleMembers(r.Context(), engine, req.ID); err != nil {
		errorJSON(w, http.StatusInternalServerError, "stopped the Conductor, but its members: "+err.Error())
		return
	}
	if err := s.b.RegenProxy(r.Context()); err != nil {
		log.Printf("regen proxy after ensemble stop: %v", err)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

// runningGate finds a running ensemble's Conductor port.
func (s *Server) runningGate(ctx context.Context, id string) (int, error) {
	runs, err := s.ensembleRuns(ctx)
	if err != nil {
		return 0, err
	}
	run, ok := runs[id]
	if !ok || run.Port == 0 {
		return 0, fmt.Errorf("the ensemble %q isn't running", id)
	}
	return run.Port, nil
}

// handleEnsembleMember starts or stops one member: {id, tool, action}.
func (s *Server) handleEnsembleMember(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID     string `json:"id"`
		Tool   string `json:"tool"`
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errorJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	port, err := s.runningGate(r.Context(), req.ID)
	if err != nil {
		errorJSON(w, http.StatusConflict, err.Error())
		return
	}
	body, _ := json.Marshal(map[string]string{"tool": req.Tool, "action": req.Action})
	// A start waits for the health check: give it the Conductor's start limit.
	ctx, cancel := context.WithTimeout(r.Context(), 9*time.Minute)
	defer cancel()
	upReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.modelURL(port, "/ensemble/members"), bytes.NewReader(body))
	upReq.Header.Set("Content-Type", "application/json")
	resp, err := gateClient.Do(upReq)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, engineErrPayload(err))
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		errorJSON(w, resp.StatusCode, upstreamError(b))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- media in replies -------------------------------------------------------

// reMediaMarkdown is a markdown image or link whose target is a data URL: the
// Conductor puts generated images there for clients that read only text.
var reMediaMarkdown = regexp.MustCompile(`\n*!?\[[^\]]*\]\(data:[^)]*\)\n*`)

// saveMedia stores each data-URL item to the media folder and points it there.
func (s *Server) saveMedia(media []ensemble.Media) []ensemble.Media {
	out := make([]ensemble.Media, 0, len(media))
	for _, m := range media {
		if u, err := s.saveDataURL(m.URL); err == nil {
			m.URL = u
		} else {
			log.Printf("ensemble media: %v", err)
		}
		out = append(out, m)
	}
	return out
}

// rewriteMediaEvent rewrites one SSE data payload from a Conductor: media
// saved to /api/media, and data-URL markdown dropped from the text (the
// Playground shows the media itself). Other events come back unchanged.
func (s *Server) rewriteMediaEvent(payload string) string {
	if !strings.Contains(payload, `"media"`) && !strings.Contains(payload, "](data:") {
		return payload
	}
	var ev map[string]any
	if json.Unmarshal([]byte(payload), &ev) != nil {
		return payload
	}
	choices, _ := ev["choices"].([]any)
	for _, c := range choices {
		cm, _ := c.(map[string]any)
		delta, _ := cm["delta"].(map[string]any)
		if delta == nil {
			continue
		}
		if text, ok := delta["content"].(string); ok && strings.Contains(text, "](data:") {
			delta["content"] = reMediaMarkdown.ReplaceAllString(text, "")
		}
		if raw, ok := delta["media"]; ok {
			b, _ := json.Marshal(raw)
			var media []ensemble.Media
			if json.Unmarshal(b, &media) == nil {
				delta["media"] = s.saveMedia(media)
			}
		}
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return payload
	}
	return string(b)
}

// relaySSE copies an event stream to w line by line, rewriting media events.
func (s *Server) relaySSE(w http.ResponseWriter, body io.Reader) {
	fl, _ := w.(http.Flusher)
	br := bufio.NewReaderSize(body, 64<<10)
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			if strings.HasPrefix(line, "data: ") {
				payload := strings.TrimRight(strings.TrimPrefix(line, "data: "), "\r\n")
				line = "data: " + s.rewriteMediaEvent(payload) + "\n"
			}
			if _, werr := io.WriteString(w, line); werr != nil {
				return
			}
			if fl != nil && (line == "\n" || line == "\r\n") {
				fl.Flush()
			}
		}
		if err != nil {
			if fl != nil {
				fl.Flush()
			}
			return
		}
	}
}

// ---- the voice chain ------------------------------------------------------------

// handleEnsembleVoiceChat runs the voice chain on a running ensemble:
// multipart {file (the spoken question), port, messages (JSON history),
// language?}. It answers {transcript, route, reply: {content, media}, audio,
// speech_error, timings} with media and audio saved to /api/media.
func (s *Server) handleEnsembleVoiceChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		errorJSON(w, http.StatusBadRequest, "send multipart form data: file, port, messages")
		return
	}
	port, _ := strconv.Atoi(r.FormValue("port"))
	f, hdr, err := r.FormFile("file")
	if err != nil || port <= 0 {
		errorJSON(w, http.StatusBadRequest, "send the recorded question (file) and the ensemble's port")
		return
	}
	audio, _ := io.ReadAll(f)
	f.Close()
	st := s.ensembleAt(r.Context(), port)
	if st == nil {
		errorJSON(w, http.StatusBadRequest, fmt.Sprintf("nothing on port %d is an ensemble: the voice chain needs a running ensemble", port))
		return
	}
	if problem := s.syncPersona(r.Context(), port, st); problem != "" {
		log.Printf("ensemble %s: %s", st.ID, problem)
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", hdr.Filename)
	_, _ = fw.Write(audio)
	for _, k := range []string{"messages", "language", "speak"} {
		if v := r.FormValue(k); v != "" {
			_ = mw.WriteField(k, v)
		}
	}
	_ = mw.Close()
	// Members may need to start (minutes on a cold machine), and media take long.
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	upReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.modelURL(port, "/ensemble/voice-chat"), &buf)
	upReq.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := gateClient.Do(upReq)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, engineErrPayload(err))
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Transcript string `json:"transcript"`
		}
		_ = json.Unmarshal(b, &e)
		writeJSON(w, resp.StatusCode, map[string]string{"error": upstreamError(b), "transcript": e.Transcript})
		return
	}
	var out struct {
		Transcript string `json:"transcript"`
		Route      string `json:"route"`
		Reply      struct {
			Content string           `json:"content"`
			Media   []ensemble.Media `json:"media"`
		} `json:"reply"`
		Audio       string           `json:"audio,omitempty"`
		SpeechError string           `json:"speech_error,omitempty"`
		Timings     map[string]int64 `json:"timings"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		errorJSON(w, http.StatusBadGateway, "the ensemble's answer isn't JSON")
		return
	}
	out.Reply.Content = strings.TrimSpace(reMediaMarkdown.ReplaceAllString(out.Reply.Content, "\n\n"))
	out.Reply.Media = s.saveMedia(out.Reply.Media)
	if out.Audio != "" {
		if u, err := s.saveDataURL(out.Audio); err == nil {
			out.Audio = u
		}
	}
	writeJSON(w, http.StatusOK, out)
}
