package server

// Agent harness: the model gateway (/v1/*), the workspace terminal (Open
// Terminal) and the Open WebUI connector. Pure logic lives in internal/harness;
// this file is the HTTP surface. See docs/HARNESS.md.

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/seanbrill/local-llm/internal/builder"
	"github.com/seanbrill/local-llm/internal/harness"
)

const (
	defaultOpenWebUIURL = "http://host.docker.internal:3000" // as a containerized factory reaches it
	nativeOpenWebUIURL  = "http://localhost:3000"            // as a factory on the host reaches it
	defaultFactoryPort  = 8799
	maxGatewayBody      = 100 << 20 // chat requests carry base64 images
	modelsCacheTTL      = 3 * time.Second
	terminalStartLimit  = 45 * time.Minute // a first pull of the full image is several GB
	terminalHealthWait  = 90 * time.Second
)

// harnessAPI holds the harness's runtime state. registerHarness creates one and
// its handlers capture it, so Server needs no new fields.
type harnessAPI struct {
	s        *Server
	store    *harness.Store
	storeErr error
	paths    harness.Paths

	modelsMu   sync.Mutex
	models     []harness.GatewayModel
	modelsAt   time.Time
	containers func(context.Context) ([]map[string]any, error) // model container rows (builder.Containers)

	eng helperEngine // the terminal container's engine calls (the builder)
	op  terminalOp

	upstream *http.Client // gateway -> model containers (no timeout: streams run long)
	probe    *http.Client // quick health checks
}

// helperEngine is the slice of *builder.Builder the terminal lifecycle uses.
type helperEngine interface {
	HelperEngine() string
	FindHelper(ctx context.Context, name string) (builder.HelperContainer, error)
	InspectHelper(ctx context.Context, engine, name string) (builder.HelperContainer, error)
	ImageExists(ctx context.Context, engine, ref string) bool
	PullImage(ctx context.Context, engine, ref string, log builder.LogFunc) error
	RunHelper(ctx context.Context, engine string, args []string) (string, error)
	StartHelper(ctx context.Context, engine, name string) error
	StopHelper(ctx context.Context, engine, name string) error
	RemoveHelper(ctx context.Context, engine, name string) error
	ContainerLogs(ctx context.Context, engine, id string, tail int) (string, error)
}

// registerHarness mounts the model gateway and the harness API.
func (s *Server) registerHarness() {
	newHarnessAPI(s, filepath.Join(s.b.ConfigDir, "harness.json")).mount(s.mux)
}

func (h *harnessAPI) mount(mux interface {
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}) {
	mux.HandleFunc("/v1/", h.handleGateway)
	mux.HandleFunc("/api/harness/status", guarded(http.MethodGet, h.handleStatus))
	mux.HandleFunc("/api/harness/gateway/key", guarded(http.MethodPost, h.handleGatewayKey))
	mux.HandleFunc("/api/harness/terminal/config", guarded(http.MethodPost, h.handleTerminalConfig))
	mux.HandleFunc("/api/harness/terminal/start", guarded(http.MethodPost, h.handleTerminalStart))
	mux.HandleFunc("/api/harness/terminal/stop", guarded(http.MethodPost, h.handleTerminalStop))
	mux.HandleFunc("/api/harness/terminal/key", guarded(http.MethodPost, h.handleTerminalKey))
	mux.HandleFunc("/api/harness/terminal/logs", guarded(http.MethodGet, h.handleTerminalLogs))
	mux.HandleFunc("/api/harness/openwebui/check", guarded(http.MethodPost, h.handleOpenWebUI(false)))
	mux.HandleFunc("/api/harness/openwebui/connect", guarded(http.MethodPost, h.handleOpenWebUI(true)))
}

func newHarnessAPI(s *Server, configPath string) *harnessAPI {
	h := &harnessAPI{
		s:          s,
		paths:      harness.DetectPaths(s.b.BaseDir, s.b.HostDir),
		containers: s.b.Containers,
		eng:        s.b,
		upstream: &http.Client{
			Transport: &http.Transport{
				Proxy:               nil, // model traffic never goes through an env proxy
				DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				MaxIdleConnsPerHost: 8,
				IdleConnTimeout:     90 * time.Second,
				DisableCompression:  true, // keep SSE chunks flowing as they arrive
			},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		probe: &http.Client{
			Timeout:       2 * time.Second,
			Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
	st, err := harness.OpenStore(configPath)
	h.store, h.storeErr = st, err
	if err != nil {
		if st == nil {
			log.Printf("harness: %v — the gateway and terminal are disabled until it is fixed (or deleted to start fresh)", err)
		} else {
			log.Printf("harness: %v (using in-memory settings for now)", err)
		}
	}
	return h
}

// ---------------------------------------------------------------------------
// Request guard
// ---------------------------------------------------------------------------

// guarded wraps a harness handler with its HTTP method and a guard against
// other web pages. The harness API hands out keys and starts a container, so:
// the Host header must be a local name or an IP literal (defeats DNS
// rebinding), and state-changing calls must be same-origin JSON POSTs (a
// cross-site form can't send JSON, and a cross-site fetch of JSON needs a CORS
// preflight the factory never grants).
func guarded(method string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method && !(method == http.MethodGet && r.Method == http.MethodHead) {
			w.Header().Set("Allow", method)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": method + " only"})
			return
		}
		if err := checkLocalRequest(r); err != nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
			return
		}
		next(w, r)
	}
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return strings.Trim(hostport, "[]")
}

func isLoopbackName(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// allowedHost accepts localhost names, IP literals (a rebinding attack needs a
// DNS name) and names listed in FACTORY_ALLOWED_HOSTS (comma-separated).
func allowedHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" {
		return false
	}
	if isLoopbackName(host) || net.ParseIP(host) != nil {
		return true
	}
	for _, h := range strings.Split(os.Getenv("FACTORY_ALLOWED_HOSTS"), ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" && h == host {
			return true
		}
	}
	return false
}

func checkLocalRequest(r *http.Request) error {
	host := hostOnly(r.Host)
	if !allowedHost(host) {
		return fmt.Errorf("refusing a request addressed to %q: open the factory at http://localhost:<port> (or add the name to FACTORY_ALLOWED_HOSTS)", host)
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return nil
	}
	if r.Method != http.MethodPost {
		return fmt.Errorf("%s is not allowed here", r.Method)
	}
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		return errors.New("send JSON (Content-Type: application/json)")
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return errors.New("cross-site requests are refused")
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" {
			return errors.New("cross-site requests are refused")
		}
		same := strings.EqualFold(u.Host, r.Host)
		// The Vite dev server proxies the API from another loopback port.
		if !same && !(isLoopbackName(hostOnly(u.Host)) && isLoopbackName(host)) {
			return errors.New("cross-site requests are refused")
		}
	}
	return nil
}

// decodeBody reads a small JSON request body (an empty body decodes as {}).
func decodeBody(r *http.Request, v any) error {
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("invalid JSON: %v", err)
	}
	return nil
}

func (h *harnessAPI) storeOK(w http.ResponseWriter) bool {
	if h.store != nil {
		return true
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{
		"error": fmt.Sprintf("config/harness.json can't be read (%v); fix it or delete it to start fresh", h.storeErr),
	})
	return false
}

// redact hides the harness keys in text bound for the UI (logs, errors).
func (h *harnessAPI) redact(s string) string {
	if h.store == nil {
		return s
	}
	cfg := h.store.Get()
	for _, k := range []string{cfg.GatewayKey, cfg.Terminal.APIKey} {
		if len(k) >= 8 {
			s = strings.ReplaceAll(s, k, "[redacted]")
		}
	}
	return s
}

// ---------------------------------------------------------------------------
// Model gateway: /v1/models, /v1/chat/completions, /v1/completions, /v1/embeddings
// ---------------------------------------------------------------------------

func oaiError(w http.ResponseWriter, status int, msg, typ, code string) {
	e := map[string]any{"message": msg, "type": typ, "param": nil, "code": nil}
	if code != "" {
		e["code"] = code
	}
	writeJSON(w, status, map[string]any{"error": e})
}

func (h *harnessAPI) gatewayAuthorized(r *http.Request) bool {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	const prefix = "bearer "
	if len(auth) <= len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) {
		return false
	}
	got := strings.TrimSpace(auth[len(prefix):])
	want := h.store.Get().GatewayKey
	return want != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func (h *harnessAPI) modalityOf(modelID string) string {
	if m := h.s.modelByID(modelID); m != nil {
		return m.Mod()
	}
	return ""
}

// gatewayModels lists the exposed models, reusing a listing younger than maxAge.
func (h *harnessAPI) gatewayModels(ctx context.Context, maxAge time.Duration) ([]harness.GatewayModel, error) {
	h.modelsMu.Lock()
	defer h.modelsMu.Unlock()
	if maxAge > 0 && !h.modelsAt.IsZero() && time.Since(h.modelsAt) < maxAge {
		return h.models, nil
	}
	rows, err := h.containers(ctx)
	if err != nil {
		return nil, err
	}
	h.models = harness.ModelsFromContainers(rows, h.modalityOf)
	if h.models == nil {
		h.models = []harness.GatewayModel{}
	}
	h.modelsAt = time.Now()
	return h.models, nil
}

type oaiModel struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	OwnedBy string         `json:"owned_by"`
	Name    string         `json:"name"`
	Info    map[string]any `json:"local_llm"`
}

func toOAIModel(m harness.GatewayModel) oaiModel {
	return oaiModel{
		ID: m.ID, Object: "model", Created: m.Created, OwnedBy: "llm-factory", Name: m.Name,
		Info: map[string]any{"modality": m.Modality, "image": m.Ref, "port": m.Port, "engine": m.Engine, "container": m.Container},
	}
}

func (h *harnessAPI) handleGateway(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		oaiError(w, http.StatusServiceUnavailable, "the factory's harness config (config/harness.json) can't be read; fix it or delete it", "server_error", "")
		return
	}
	if !h.gatewayAuthorized(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="llm-factory"`)
		oaiError(w, http.StatusUnauthorized, "missing or wrong API key: use the gateway key shown on the factory's Harness page", "invalid_request_error", "invalid_api_key")
		return
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	switch {
	case path == "/v1/models":
		if r.Method != http.MethodGet {
			oaiError(w, http.StatusMethodNotAllowed, "use GET", "invalid_request_error", "")
			return
		}
		models, err := h.gatewayModels(r.Context(), modelsCacheTTL)
		if err != nil {
			oaiError(w, http.StatusBadGateway, "can't list model containers: "+engineMessage(err), "server_error", "")
			return
		}
		data := make([]oaiModel, 0, len(models))
		for _, m := range models {
			data = append(data, toOAIModel(m))
		}
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
	case strings.HasPrefix(path, "/v1/models/"):
		if r.Method != http.MethodGet {
			oaiError(w, http.StatusMethodNotAllowed, "use GET", "invalid_request_error", "")
			return
		}
		models, err := h.gatewayModels(r.Context(), modelsCacheTTL)
		if err != nil {
			oaiError(w, http.StatusBadGateway, "can't list model containers: "+engineMessage(err), "server_error", "")
			return
		}
		id := strings.TrimPrefix(path, "/v1/models/")
		for _, m := range models {
			if m.ID == id {
				writeJSON(w, http.StatusOK, toOAIModel(m))
				return
			}
		}
		oaiError(w, http.StatusNotFound, fmt.Sprintf("model %q is not running on the factory", id), "invalid_request_error", "model_not_found")
	case path == "/v1/chat/completions" || path == "/v1/completions" || path == "/v1/embeddings":
		if r.Method != http.MethodPost {
			oaiError(w, http.StatusMethodNotAllowed, "use POST", "invalid_request_error", "")
			return
		}
		h.proxyToModel(w, r, path)
	default:
		oaiError(w, http.StatusNotFound, "the factory gateway serves /v1/models, /v1/chat/completions, /v1/completions and /v1/embeddings", "invalid_request_error", "not_found")
	}
}

func engineMessage(err error) string {
	if hint := builder.ClassifyEngineError(err); hint != "" {
		return hint
	}
	return err.Error()
}

func modelIDs(models []harness.GatewayModel) string {
	if len(models) == 0 {
		return "none (start a model on the factory's Containers page)"
	}
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	return strings.Join(ids, ", ")
}

// proxyToModel routes a request to the container named by its "model" field.
// The body is forwarded byte-for-byte; the response (SSE included) is streamed
// back unbuffered. The caller's Authorization header is never forwarded.
func (h *harnessAPI) proxyToModel(w http.ResponseWriter, r *http.Request, path string) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxGatewayBody))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			oaiError(w, http.StatusRequestEntityTooLarge, "request body is too large", "invalid_request_error", "")
			return
		}
		oaiError(w, http.StatusBadRequest, "can't read the request body", "invalid_request_error", "")
		return
	}
	var probe struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		oaiError(w, http.StatusBadRequest, "the request body must be a JSON object", "invalid_request_error", "")
		return
	}
	models, err := h.gatewayModels(r.Context(), modelsCacheTTL)
	if err != nil {
		oaiError(w, http.StatusBadGateway, "can't list model containers: "+engineMessage(err), "server_error", "")
		return
	}
	m, ok := harness.ResolveModel(models, probe.Model)
	if !ok { // the cached listing may predate a container that just started
		if models, err = h.gatewayModels(r.Context(), 0); err == nil {
			m, ok = harness.ResolveModel(models, probe.Model)
		}
	}
	if !ok {
		msg := fmt.Sprintf("model %q is not running on the factory. Running: %s", probe.Model, modelIDs(models))
		if probe.Model == "" {
			msg = "the request has no \"model\". Running: " + modelIDs(models)
		}
		oaiError(w, http.StatusNotFound, msg, "invalid_request_error", "model_not_found")
		return
	}
	switch {
	case path == "/v1/embeddings" && m.Modality != "embedding":
		oaiError(w, http.StatusBadRequest, fmt.Sprintf("%s is a %s model; embeddings need an embedding model", m.ID, m.Modality), "invalid_request_error", "")
		return
	case path != "/v1/embeddings" && m.Modality == "embedding":
		oaiError(w, http.StatusBadRequest, m.ID+" is an embedding model; use it through /v1/embeddings (e.g. as Open WebUI's embedding model), not for chat", "invalid_request_error", "")
		return
	}
	target := h.s.modelURL(m.Port, path)
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		oaiError(w, http.StatusInternalServerError, err.Error(), "server_error", "")
		return
	}
	upReq.ContentLength = int64(len(body))
	upReq.Header.Set("Content-Type", "application/json")
	if a := r.Header.Get("Accept"); a != "" {
		upReq.Header.Set("Accept", a)
	}
	resp, err := h.upstream.Do(upReq)
	if err != nil {
		if r.Context().Err() != nil {
			return // the caller went away
		}
		oaiError(w, http.StatusBadGateway, fmt.Sprintf("model %s (port %d) isn't answering: %v", m.ID, m.Port, err), "server_error", "")
		return
	}
	defer resp.Body.Close()
	for _, k := range []string{"Content-Type", "Content-Length", "Cache-Control"} {
		if v := resp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		w.Header().Set("X-Accel-Buffering", "no")
	}
	w.WriteHeader(resp.StatusCode)
	flushCopy(w, resp.Body)
}

// flushCopy copies src to w, flushing after every read so tokens stream through.
func flushCopy(w http.ResponseWriter, src io.Reader) {
	fl, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Status
// ---------------------------------------------------------------------------

type gatewayStatus struct {
	BaseURL    string                 `json:"base_url"`  // as Open WebUI reaches it
	LocalURL   string                 `json:"local_url"` // from this computer
	Port       int                    `json:"port"`
	Key        string                 `json:"key"`
	Models     []harness.GatewayModel `json:"models"`
	Modalities []string               `json:"modalities"`
	Error      string                 `json:"error,omitempty"`
}

type terminalStatus struct {
	Variant           string                   `json:"variant"`
	Port              int                      `json:"port"`
	Workspace         string                   `json:"workspace"` // as configured ("" = default)
	ShareClaudeSkills bool                     `json:"share_claude_skills"`
	MemoryMB          int                      `json:"memory_mb"`
	Variants          []harness.Variant        `json:"variants"`
	Image             string                   `json:"image"`
	ImagePresent      bool                     `json:"image_present"`
	Engine            string                   `json:"engine"`
	Container         *builder.HelperContainer `json:"container"`
	Healthy           bool                     `json:"healthy"`
	RestartRequired   bool                     `json:"restart_required"`
	SpecError         string                   `json:"spec_error,omitempty"`
	Op                terminalOpSnapshot       `json:"op"`
	URL               string                   `json:"url"`       // as Open WebUI reaches it
	LocalURL          string                   `json:"local_url"` // from this computer
	Key               string                   `json:"key"`
	WorkspacePath     string                   `json:"workspace_path"` // effective host folder
	DefaultWorkspace  string                   `json:"default_workspace"`
	SkillsPath        string                   `json:"skills_path"`        // host folder mounted read-only
	ClaudeSkillsPath  string                   `json:"claude_skills_path"` // "" when the host home is unknown
	Error             string                   `json:"error,omitempty"`
	Mounts            []map[string]string      `json:"mounts"`
}

type skillsStatus struct {
	Path  string          `json:"path"` // host folder
	Items []harness.Skill `json:"items"`
	Error string          `json:"error,omitempty"`
}

type openWebUIStatus struct {
	URL        string `json:"url"`
	DefaultURL string `json:"default_url"`
	ReachHost  string `json:"reach_host"`
}

type harnessStatus struct {
	ConfigPath  string          `json:"config_path"`
	ConfigError string          `json:"config_error,omitempty"`
	InContainer bool            `json:"in_container"` // the factory runs in a container
	Gateway     gatewayStatus   `json:"gateway"`
	Terminal    terminalStatus  `json:"terminal"`
	Skills      skillsStatus    `json:"skills"`
	OpenWebUI   openWebUIStatus `json:"openwebui"`
}

// gatewayPort is the host port Open WebUI reaches the factory on: FACTORY_PORT
// (set by the start scripts), else the port this request came in on.
func gatewayPort(r *http.Request) int {
	if p, err := strconv.Atoi(strings.TrimSpace(os.Getenv("FACTORY_PORT"))); err == nil && p > 0 && p < 65536 {
		return p
	}
	if r != nil {
		if _, port, err := net.SplitHostPort(r.Host); err == nil {
			if p, err := strconv.Atoi(port); err == nil && p > 0 {
				return p
			}
		}
	}
	return defaultFactoryPort
}

func urlHost(h string) string {
	if strings.Contains(h, ":") && !strings.HasPrefix(h, "[") {
		return "[" + h + "]" // IPv6 literal
	}
	return h
}

func (h *harnessAPI) planURLs(cfg harness.Config, r *http.Request) (gatewayURL, terminalURL string) {
	reach := urlHost(cfg.ReachHost())
	return fmt.Sprintf("http://%s:%d/v1", reach, gatewayPort(r)), fmt.Sprintf("http://%s:%d", reach, cfg.Terminal.Port)
}

func (h *harnessAPI) handleStatus(w http.ResponseWriter, r *http.Request) {
	st := harnessStatus{
		ConfigPath:  "config/harness.json",
		InContainer: filepath.Clean(h.s.b.BaseDir) != filepath.Clean(h.s.b.HostDir),
		Skills:      h.skillsStatus(),
	}
	if h.store == nil {
		st.ConfigError = fmt.Sprintf("config/harness.json can't be read (%v); fix it or delete it to start fresh", h.storeErr)
		writeJSON(w, http.StatusOK, st)
		return
	}
	if h.storeErr != nil {
		st.ConfigError = fmt.Sprintf("settings can't be saved: %v", h.storeErr)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	cfg := h.store.Get()
	gwURL, termURL := h.planURLs(cfg, r)

	port := gatewayPort(r)
	st.Gateway = gatewayStatus{
		BaseURL: gwURL, LocalURL: fmt.Sprintf("http://localhost:%d/v1", port), Port: port,
		Key: cfg.GatewayKey, Modalities: harness.GatewayModalities(), Models: []harness.GatewayModel{},
	}
	if models, err := h.gatewayModels(ctx, modelsCacheTTL); err != nil {
		st.Gateway.Error = engineMessage(err)
	} else {
		st.Gateway.Models = models
	}

	st.Terminal = h.terminalStatus(ctx, cfg, termURL)
	st.OpenWebUI = openWebUIStatus{URL: cfg.OpenWebUI.URL, DefaultURL: h.defaultOpenWebUI(), ReachHost: cfg.ReachHost()}
	if st.OpenWebUI.URL == "" {
		st.OpenWebUI.URL = st.OpenWebUI.DefaultURL
	}
	writeJSON(w, http.StatusOK, st)
}

func (h *harnessAPI) skillsStatus() skillsStatus {
	ss := skillsStatus{Path: h.paths.HostSkillsDir(), Items: []harness.Skill{}}
	items, err := harness.ListSkills(h.paths.SkillsDir())
	switch {
	case errors.Is(err, harness.ErrNoSkillsDir):
		ss.Error = "The skills/ folder isn't visible to the factory. Restart it with the current start script (it mounts ./skills)."
	case err != nil:
		ss.Error = err.Error()
	default:
		ss.Items = items
	}
	return ss
}

func (h *harnessAPI) terminalStatus(ctx context.Context, cfg harness.Config, termURL string) terminalStatus {
	t := cfg.Terminal
	ts := terminalStatus{
		Variant: t.Variant, Port: t.Port, Workspace: t.Workspace, ShareClaudeSkills: t.ShareClaudeSkills,
		MemoryMB: t.Memory(), Variants: harness.Variants, Image: harness.TerminalImage(t.Variant),
		URL: termURL, LocalURL: fmt.Sprintf("http://localhost:%d", t.Port), Key: t.APIKey,
		WorkspacePath: h.paths.Workspace(t), DefaultWorkspace: h.paths.DefaultWorkspace(),
		SkillsPath: h.paths.HostSkillsDir(), ClaudeSkillsPath: h.paths.ClaudeSkillsDir(),
		Op: h.op.snapshot(),
	}
	hc, err := h.eng.FindHelper(ctx, harness.TerminalContainer)
	ts.Engine = hc.Engine
	if err != nil {
		ts.Error = engineMessage(err)
	}
	ts.Container = &hc
	ts.ImagePresent = h.eng.ImageExists(ctx, h.eng.HelperEngine(), ts.Image)

	spec, err := harness.BuildTerminalSpec(t, h.paths, h.eng.HelperEngine())
	if err != nil {
		ts.SpecError = err.Error()
	} else {
		ts.Mounts = []map[string]string{
			{"host": spec.Workspace, "container": "/home/user/workspace", "mode": "read-write"},
			{"host": spec.Skills, "container": "/home/user/.agents/skills", "mode": "read-only"},
		}
		if spec.ClaudeSkills != "" {
			ts.Mounts = append(ts.Mounts, map[string]string{"host": spec.ClaudeSkills, "container": "/home/user/.claude/skills", "mode": "read-only"})
		}
		if hc.Exists {
			ts.RestartRequired = hc.Labels[harness.SpecLabel] != spec.Hash() || hc.Engine != spec.Engine
		}
	}
	if hc.Running {
		ts.Healthy = h.terminalHealthy(ctx, t.Port)
	}
	return ts
}

func (h *harnessAPI) terminalHealthy(ctx context.Context, port int) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.s.modelURL(port, "/health"), nil)
	if err != nil {
		return false
	}
	resp, err := h.probe.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode == http.StatusOK
}

// ---------------------------------------------------------------------------
// Keys
// ---------------------------------------------------------------------------

func (h *harnessAPI) handleGatewayKey(w http.ResponseWriter, r *http.Request) {
	if !h.storeOK(w) {
		return
	}
	key, err := harness.NewGatewayKey()
	if err == nil {
		_, err = h.store.Update(func(c *harness.Config) error { c.GatewayKey = key; return nil })
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	log.Printf("harness: gateway key regenerated")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "key": key,
		"note": "Open WebUI still has the old key: run Connect again (or paste the new key into its connection)."})
}

func (h *harnessAPI) handleTerminalKey(w http.ResponseWriter, r *http.Request) {
	if !h.storeOK(w) {
		return
	}
	key, err := harness.NewTerminalKey()
	if err == nil {
		_, err = h.store.Update(func(c *harness.Config) error { c.Terminal.APIKey = key; return nil })
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	log.Printf("harness: terminal key regenerated")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "key": key,
		"note": "Restart the terminal to apply the new key, then run Connect again so Open WebUI has it."})
}

// ---------------------------------------------------------------------------
// Terminal settings
// ---------------------------------------------------------------------------

type terminalConfigRequest struct {
	Variant           *string `json:"variant"`
	Port              *int    `json:"port"`
	Workspace         *string `json:"workspace"`
	ShareClaudeSkills *bool   `json:"share_claude_skills"`
	MemoryMB          *int    `json:"memory_mb"`
}

func (h *harnessAPI) handleTerminalConfig(w http.ResponseWriter, r *http.Request) {
	if !h.storeOK(w) {
		return
	}
	var req terminalConfigRequest
	if err := decodeBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	factoryPort := gatewayPort(r)
	cfg, err := h.store.Update(func(c *harness.Config) error {
		t := &c.Terminal
		if req.Variant != nil {
			if _, ok := harness.VariantByID(*req.Variant); !ok {
				return fmt.Errorf("unknown image variant %q", *req.Variant)
			}
			t.Variant = *req.Variant
		}
		if req.Port != nil {
			if err := harness.ValidatePort(*req.Port); err != nil {
				return err
			}
			if *req.Port == factoryPort {
				return fmt.Errorf("port %d is the factory's own port", *req.Port)
			}
			t.Port = *req.Port
		}
		if req.Workspace != nil {
			ws := strings.TrimSpace(*req.Workspace)
			if ws != "" && harness.NormalizeHostPath(ws) == harness.NormalizeHostPath(h.paths.DefaultWorkspace()) {
				ws = "" // the default; keep following the repo if it moves
			}
			if err := harness.ValidateWorkspace(ws, h.paths.HostDir, h.paths.HostHome); err != nil {
				return err
			}
			t.Workspace = ws
		}
		if req.ShareClaudeSkills != nil {
			t.ShareClaudeSkills = *req.ShareClaudeSkills
		}
		if req.MemoryMB != nil {
			if m := *req.MemoryMB; m != 0 && (m < 512 || m > 262144) {
				return fmt.Errorf("memory limit must be between 512 and 262144 MB (got %d)", m)
			}
			t.MemoryMB = *req.MemoryMB
		}
		_, err := harness.BuildTerminalSpec(*t, h.paths, h.eng.HelperEngine())
		return err
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "workspace_path": h.paths.Workspace(cfg.Terminal)})
}

// ---------------------------------------------------------------------------
// Terminal lifecycle
// ---------------------------------------------------------------------------

// terminalOp tracks the (asynchronous) terminal start: pulling the image can
// take minutes, so the UI polls the status for progress.
type terminalOp struct {
	mu      sync.Mutex
	active  bool
	phase   string // pulling | creating | starting | waiting | running | error | canceled
	message string
	lines   []string
	err     string
	detail  string
	started time.Time
	ended   time.Time
	cancel  context.CancelFunc
}

type terminalOpSnapshot struct {
	Active    bool     `json:"active"`
	Phase     string   `json:"phase"`
	Message   string   `json:"message"`
	Lines     []string `json:"lines"`
	Error     string   `json:"error,omitempty"`
	Detail    string   `json:"detail,omitempty"`
	ElapsedMs int64    `json:"elapsed_ms"`
}

func (o *terminalOp) begin(cancel context.CancelFunc) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.active {
		return false
	}
	o.active, o.phase, o.message = true, "starting", "Starting…"
	o.lines, o.err, o.detail = nil, "", ""
	o.started, o.ended, o.cancel = time.Now(), time.Time{}, cancel
	return true
}

func (o *terminalOp) set(phase, msg string) {
	o.mu.Lock()
	o.phase, o.message = phase, msg
	o.mu.Unlock()
}

func (o *terminalOp) line(l string) {
	o.mu.Lock()
	o.lines = append(o.lines, l)
	if len(o.lines) > 200 {
		o.lines = o.lines[len(o.lines)-200:]
	}
	o.mu.Unlock()
}

func (o *terminalOp) finish(phase, msg, errMsg, detail string) {
	o.mu.Lock()
	o.active = false
	o.phase, o.message, o.err, o.detail = phase, msg, errMsg, detail
	o.ended = time.Now()
	o.cancel = nil
	o.mu.Unlock()
}

// abort cancels an in-flight start and reports whether one was running.
func (o *terminalOp) abort() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.active || o.cancel == nil {
		return false
	}
	o.cancel()
	return true
}

func (o *terminalOp) snapshot() terminalOpSnapshot {
	o.mu.Lock()
	defer o.mu.Unlock()
	s := terminalOpSnapshot{Active: o.active, Phase: o.phase, Message: o.message, Lines: append([]string{}, o.lines...), Error: o.err, Detail: o.detail}
	if !o.started.IsZero() {
		end := time.Now()
		if !o.active && !o.ended.IsZero() {
			end = o.ended
		}
		s.ElapsedMs = end.Sub(o.started).Milliseconds()
	}
	return s
}

func (h *harnessAPI) handleTerminalStart(w http.ResponseWriter, r *http.Request) {
	if !h.storeOK(w) {
		return
	}
	var req struct {
		Update bool `json:"update"` // pull the newest image for the tag first
	}
	if err := decodeBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	cfg := h.store.Get()
	engine := h.eng.HelperEngine()
	spec, err := harness.BuildTerminalSpec(cfg.Terminal, h.paths, engine)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), terminalStartLimit)
	if !h.op.begin(cancel) {
		cancel()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the terminal is already starting"})
		return
	}
	go func() {
		defer cancel()
		h.runTerminalStart(ctx, spec, req.Update)
	}()
	writeJSON(w, http.StatusAccepted, h.op.snapshot())
}

func (h *harnessAPI) failOp(ctx context.Context, err error, what string) {
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		h.op.finish("canceled", "Start canceled.", "", "")
		return
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		h.op.finish("error", "", fmt.Sprintf("gave up after %s", terminalStartLimit), h.redact(err.Error()))
		return
	}
	msg := what + ": " + h.redact(err.Error())
	if hint := builder.ClassifyEngineError(err); hint != "" {
		h.op.finish("error", "", hint, msg)
		return
	}
	h.op.finish("error", "", msg, "")
}

func (h *harnessAPI) runTerminalStart(ctx context.Context, spec harness.TerminalSpec, update bool) {
	b := h.eng
	// The default workspace lives in the repo: create it (the factory can see it).
	// A custom folder must already exist on the host.
	if local, ok := h.paths.LocalPath(spec.Workspace); ok {
		if err := os.MkdirAll(local, 0o755); err != nil {
			log.Printf("harness: create workspace %s: %v", local, err)
		}
	}
	image := spec.Image()
	if update || !b.ImageExists(ctx, spec.Engine, image) {
		h.op.set("pulling", "Downloading "+image+"…")
		if err := b.PullImage(ctx, spec.Engine, image, h.op.line); err != nil {
			h.failOp(ctx, err, "Couldn't download "+image)
			return
		}
	}

	existing, err := b.FindHelper(ctx, harness.TerminalContainer)
	if err != nil && !existing.Exists {
		log.Printf("harness: inspect terminal: %v", err)
	}
	if existing.Exists && existing.Engine == spec.Engine && existing.Labels[harness.SpecLabel] == spec.Hash() && !update {
		if !existing.Running {
			h.op.set("starting", "Starting the existing terminal container…")
			if err := b.StartHelper(ctx, spec.Engine, harness.TerminalContainer); err != nil {
				h.failOp(ctx, err, "Couldn't start the terminal")
				return
			}
		}
	} else {
		if existing.Exists {
			h.op.set("creating", "Replacing the old terminal container…")
			if err := b.RemoveHelper(ctx, existing.Engine, harness.TerminalContainer); err != nil {
				h.failOp(ctx, err, "Couldn't remove the old terminal container")
				return
			}
		}
		h.op.set("creating", "Creating the terminal container…")
		if _, err := b.RunHelper(ctx, spec.Engine, spec.RunArgs()); err != nil {
			h.failOp(ctx, err, "Couldn't start the terminal")
			return
		}
		log.Printf("harness: terminal started (%s, %s, port %d)", spec.Engine, image, spec.Port)
	}

	h.op.set("waiting", "Waiting for the terminal to answer…")
	deadline := time.Now().Add(terminalHealthWait)
	for {
		if h.terminalHealthy(ctx, spec.Port) {
			h.op.finish("running", "The terminal is running.", "", "")
			return
		}
		hc, _ := b.InspectHelper(ctx, spec.Engine, harness.TerminalContainer)
		if hc.Exists && !hc.Running && hc.Status != "restarting" && hc.Status != "created" {
			logs, _ := b.ContainerLogs(ctx, spec.Engine, harness.TerminalContainer, 20)
			reason := fmt.Sprintf("the terminal container stopped (exit code %d)", hc.ExitCode)
			if hc.OOMKilled {
				reason = "the terminal ran out of memory; raise its memory limit"
			}
			h.op.finish("error", "", reason, h.redact(strings.TrimSpace(logs)))
			return
		}
		if time.Now().After(deadline) {
			h.op.finish("error", "", fmt.Sprintf("the terminal container is up but doesn't answer on %s yet", h.s.modelURL(spec.Port, "/health")),
				"Check its logs below. If Open WebUI and the factory run on different networks, see docs/HARNESS.md › Troubleshooting.")
			return
		}
		select {
		case <-ctx.Done():
			h.failOp(ctx, ctx.Err(), "Start canceled")
			return
		case <-time.After(time.Second):
		}
	}
}

func (h *harnessAPI) handleTerminalStop(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Remove bool `json:"remove"` // delete the container (a fresh one is created on the next start)
	}
	if err := decodeBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	h.op.abort()
	hc, err := h.eng.FindHelper(r.Context(), harness.TerminalContainer)
	if err != nil && !hc.Exists {
		writeJSON(w, http.StatusBadGateway, engineErrPayload(err))
		return
	}
	if !hc.Exists {
		writeJSON(w, http.StatusOK, map[string]string{"status": "absent"})
		return
	}
	if req.Remove {
		err = h.eng.RemoveHelper(r.Context(), hc.Engine, harness.TerminalContainer)
	} else {
		err = h.eng.StopHelper(r.Context(), hc.Engine, harness.TerminalContainer)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, engineErrPayload(err))
		return
	}
	status := "stopped"
	if req.Remove {
		status = "removed"
	}
	log.Printf("harness: terminal %s", status)
	writeJSON(w, http.StatusOK, map[string]string{"status": status})
}

func (h *harnessAPI) handleTerminalLogs(w http.ResponseWriter, r *http.Request) {
	tail, _ := strconv.Atoi(r.URL.Query().Get("tail"))
	if tail <= 0 || tail > 2000 {
		tail = 200
	}
	hc, err := h.eng.FindHelper(r.Context(), harness.TerminalContainer)
	if !hc.Exists {
		msg := "the terminal container doesn't exist yet"
		if err != nil {
			msg = engineMessage(err)
		}
		writeJSON(w, http.StatusOK, map[string]string{"logs": "", "note": msg})
		return
	}
	logs, err := h.eng.ContainerLogs(r.Context(), hc.Engine, harness.TerminalContainer, tail)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": h.redact(err.Error())})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"logs": h.redact(logs)})
}

// ---------------------------------------------------------------------------
// Open WebUI connector
// ---------------------------------------------------------------------------

type openWebUIRequest struct {
	URL       string `json:"url"`
	APIKey    string `json:"api_key"` // used for this call only; never stored or logged
	ReachHost string `json:"reach_host"`
	Models    *bool  `json:"models"`
	Terminal  *bool  `json:"terminal"`
	WebSearch *bool  `json:"web_search"`
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// defaultOpenWebUI is Open WebUI's usual address (Docker, port 3000) as this
// factory reaches it: through host.docker.internal from the factory's
// container, or plain localhost when the factory runs on the host itself
// (where host.docker.internal often doesn't resolve).
func (h *harnessAPI) defaultOpenWebUI() string {
	if isLoopbackName(h.s.modelHost) {
		return nativeOpenWebUIURL
	}
	return defaultOpenWebUIURL
}

// factorySideURL rewrites a localhost Open WebUI URL to the host the factory
// reaches host ports through (inside the factory's container, localhost is the
// factory itself).
func (h *harnessAPI) factorySideURL(raw string) (string, string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || !isLoopbackName(u.Hostname()) || isLoopbackName(h.s.modelHost) {
		return raw, ""
	}
	orig := u.String()
	if port := u.Port(); port != "" {
		u.Host = net.JoinHostPort(h.s.modelHost, port)
	} else {
		u.Host = urlHost(h.s.modelHost)
	}
	return u.String(), fmt.Sprintf("Using %s for %s: inside the factory's container, localhost is the factory itself.", u.String(), orig)
}

func (h *harnessAPI) handleOpenWebUI(connect bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.storeOK(w) {
			return
		}
		var req openWebUIRequest
		if err := decodeBody(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		cfg := h.store.Get()
		rawURL := strings.TrimSpace(req.URL)
		if rawURL == "" {
			rawURL = cfg.OpenWebUI.URL
		}
		if rawURL == "" {
			rawURL = h.defaultOpenWebUI()
		}
		reach := strings.TrimSpace(req.ReachHost)
		if reach == "" {
			reach = cfg.ReachHost()
		}
		if err := harness.ValidateReachHost(reach); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		useURL, note := h.factorySideURL(rawURL)
		client, err := harness.NewOWUIClient(useURL, req.APIKey)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		// Remember the (non-secret) inputs for next time.
		cfg, err = h.store.Update(func(c *harness.Config) error {
			c.OpenWebUI.URL = rawURL
			c.OpenWebUI.ReachHost = ""
			if reach != harness.DefaultReachHost {
				c.OpenWebUI.ReachHost = reach
			}
			return nil
		})
		if err != nil {
			log.Printf("harness: save Open WebUI prefs: %v", err)
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		gwURL, termURL := h.planURLs(cfg, r)
		term, _ := h.eng.FindHelper(ctx, harness.TerminalContainer)
		plan := harness.Plan{
			GatewayURL: gwURL, GatewayKey: cfg.GatewayKey,
			TerminalURL: termURL, TerminalKey: cfg.Terminal.APIKey,
			OpenAI: boolOr(req.Models, true), Terminal: boolOr(req.Terminal, true), WebSearch: boolOr(req.WebSearch, true),
			TerminalRunning: term.Running,
		}
		var rep harness.Report
		if connect {
			rep = client.Connect(ctx, plan)
			log.Printf("harness: Open WebUI connect at %s: changed=%v errors=%v", client.BaseURL, rep.Changed, rep.HasErrors())
		} else {
			rep = client.Check(ctx, plan)
		}
		if note != "" {
			rep.Items = append([]harness.Item{{Area: "openwebui", Status: "info", Detail: note}}, rep.Items...)
		}
		if rep.Admin && plan.Terminal && !term.Running {
			rep.Items = append(rep.Items, harness.Item{Area: "terminal", Status: "info",
				Detail: "The workspace terminal isn't running, so Open WebUI's access to it wasn't tested. Start it on this page, then Check again."})
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"report": rep, "url": client.BaseURL, "gateway_url": plan.GatewayURL, "terminal_url": plan.TerminalURL,
		})
	}
}
