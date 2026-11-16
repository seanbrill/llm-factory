package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seanbrill/local-llm/internal/builder"
	"github.com/seanbrill/local-llm/internal/catalog"
	"github.com/seanbrill/local-llm/internal/harness"
)

// fakeEngine stands in for the container engine so the terminal lifecycle can
// be tested without Docker.
type fakeEngine struct {
	mu        sync.Mutex
	image     bool
	container builder.HelperContainer
	pulls     int
	runs      int
	starts    int
	stops     int
	removes   int
	runArgs   []string
	logs      string
}

func (f *fakeEngine) HelperEngine() string { return "docker" }

func (f *fakeEngine) FindHelper(ctx context.Context, name string) (builder.HelperContainer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.container
	if !c.Exists {
		c.Engine = "docker"
	}
	return c, nil
}

func (f *fakeEngine) InspectHelper(ctx context.Context, engine, name string) (builder.HelperContainer, error) {
	return f.FindHelper(ctx, name)
}

func (f *fakeEngine) ImageExists(ctx context.Context, engine, ref string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.image
}

func (f *fakeEngine) PullImage(ctx context.Context, engine, ref string, log builder.LogFunc) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulls++
	log("slim: Pulling from open-webui/open-terminal")
	f.image = true
	return nil
}

func (f *fakeEngine) RunHelper(ctx context.Context, engine string, args []string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs++
	f.runArgs = append([]string{}, args...)
	labels := map[string]string{}
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--label" {
			k, v, _ := strings.Cut(args[i+1], "=")
			labels[k] = v
		}
	}
	f.container = builder.HelperContainer{Engine: engine, Exists: true, Running: true, Status: "running", Labels: labels}
	return "abc123", nil
}

func (f *fakeEngine) StartHelper(ctx context.Context, engine, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	f.container.Running, f.container.Status = true, "running"
	return nil
}

func (f *fakeEngine) StopHelper(ctx context.Context, engine, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	f.container.Running, f.container.Status = false, "exited"
	return nil
}

func (f *fakeEngine) RemoveHelper(ctx context.Context, engine, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removes++
	f.container = builder.HelperContainer{}
	return nil
}

func (f *fakeEngine) ContainerLogs(ctx context.Context, engine, id string, tail int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logs, nil
}

type testHarness struct {
	h    *harnessAPI
	eng  *fakeEngine
	srv  *httptest.Server
	rows []map[string]any
	mu   sync.Mutex
}

func newTestHarness(t *testing.T) *testHarness {
	t.Helper()
	t.Setenv("FACTORY_PORT", "8799")
	dir := t.TempDir()
	b, err := builder.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{b: b, cat: &catalog.Catalog{}, mux: newRouteMux(), modelHost: "127.0.0.1"}
	h := newHarnessAPI(s, filepath.Join(dir, "config", "harness.json"))
	if h.store == nil {
		t.Fatal(h.storeErr)
	}
	th := &testHarness{h: h, eng: &fakeEngine{}}
	h.eng = th.eng
	h.containers = func(context.Context) ([]map[string]any, error) {
		th.mu.Lock()
		defer th.mu.Unlock()
		return th.rows, nil
	}
	mux := http.NewServeMux()
	h.mount(mux)
	th.srv = httptest.NewServer(mux)
	t.Cleanup(th.srv.Close)
	return th
}

func (th *testHarness) setRows(rows ...map[string]any) {
	th.mu.Lock()
	th.rows = rows
	th.mu.Unlock()
	th.h.modelsMu.Lock()
	th.h.modelsAt = time.Time{} // drop the cache
	th.h.modelsMu.Unlock()
}

func modelRow(port int, ref, modality string) map[string]any {
	return map[string]any{
		"Names": "localllm-" + strconv.Itoa(port), "State": "running", "Image": ref, "Engine": "docker",
		"Labels": "local-llm.model=m,local-llm.modality=" + modality + ",local-llm.ref=" + ref,
	}
}

func upstreamPort(t *testing.T, u string) int {
	t.Helper()
	_, p, err := net.SplitHostPort(strings.TrimPrefix(u, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(p)
	return n
}

func (th *testHarness) gw(t *testing.T, method, path, key string, body []byte) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, th.srv.URL+path, bytes.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func decodeErr(t *testing.T, resp *http.Response) (msg, code string) {
	t.Helper()
	defer resp.Body.Close()
	var e struct {
		Error struct {
			Message string  `json:"message"`
			Code    *string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatal(err)
	}
	if e.Error.Code != nil {
		code = *e.Error.Code
	}
	return e.Error.Message, code
}

func TestGatewayAuth(t *testing.T) {
	th := newTestHarness(t)
	key := th.h.store.Get().GatewayKey
	th.setRows(modelRow(18080, "local-llm/demo:latest", "text"))

	resp := th.gw(t, "GET", "/v1/models", "", nil)
	if resp.StatusCode != 401 || resp.Header.Get("WWW-Authenticate") == "" {
		t.Fatalf("no key: %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = th.gw(t, "GET", "/v1/models", key+"x", nil)
	if resp.StatusCode != 401 {
		t.Fatalf("wrong key: %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = th.gw(t, "GET", "/v1/models", key, nil)
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("models: %d", resp.StatusCode)
	}
	var list struct {
		Object string `json:"object"`
		Data   []struct {
			ID, Object, Name, OwnedBy string `json:"-"`
		} `json:"data"`
	}
	raw, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(raw, &list); err != nil || list.Object != "list" || len(list.Data) != 1 || !strings.Contains(string(raw), `"id":"demo"`) {
		t.Fatalf("models body %s", raw)
	}

	resp = th.gw(t, "GET", "/v1/models/demo", key, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("model by id: %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = th.gw(t, "GET", "/v1/files", key, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("unknown route: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestGatewayStreamsUnbufferedAndForwardsBodyUnchanged(t *testing.T) {
	th := newTestHarness(t)
	key := th.h.store.Get().GatewayKey
	release := make(chan struct{})
	var gotBody []byte
	var gotAuth, gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer up.Close()
	port := upstreamPort(t, up.URL)
	th.setRows(modelRow(port, "local-llm/demo:latest", "text"))

	body := []byte(`{"model":"demo","stream":true,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"run_command"}}]}`)
	resp := th.gw(t, "POST", "/v1/chat/completions", key, body)
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	// The first event must arrive while the model is still "generating".
	first := make(chan string, 1)
	rd := bufio.NewReader(resp.Body)
	go func() {
		line, _ := rd.ReadString('\n')
		first <- line
	}()
	select {
	case line := <-first:
		if !strings.Contains(line, "Hel") {
			t.Fatalf("first line %q", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the gateway buffered the stream")
	}
	close(release)
	rest, _ := io.ReadAll(rd)
	if !strings.Contains(string(rest), "[DONE]") {
		t.Fatalf("rest %q", rest)
	}
	if !bytes.Equal(gotBody, body) {
		t.Fatalf("body changed:\n got %s\nwant %s", gotBody, body)
	}
	if gotAuth != "" {
		t.Fatalf("the gateway key was forwarded to the model: %q", gotAuth)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path %q", gotPath)
	}
}

func TestGatewayRoutingErrors(t *testing.T) {
	th := newTestHarness(t)
	key := th.h.store.Get().GatewayKey
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"embedding":[0.1]}]}`)
	}))
	defer up.Close()
	port := upstreamPort(t, up.URL)
	th.setRows(modelRow(port, "local-llm/nomic-embed:latest", "embedding"), modelRow(port+1, "local-llm/chat:latest", "text"))

	msg, code := decodeErr(t, th.gw(t, "POST", "/v1/chat/completions", key, []byte(`{"model":"nope"}`)))
	if code != "model_not_found" || !strings.Contains(msg, "chat") || !strings.Contains(msg, "nomic-embed") {
		t.Fatalf("unknown model: %q %q", msg, code)
	}
	if msg, _ := decodeErr(t, th.gw(t, "POST", "/v1/chat/completions", key, []byte(`{"model":"nomic-embed"}`))); !strings.Contains(msg, "embedding model") {
		t.Fatalf("embedding model for chat: %q", msg)
	}
	if msg, _ := decodeErr(t, th.gw(t, "POST", "/v1/embeddings", key, []byte(`{"model":"chat","input":"x"}`))); !strings.Contains(msg, "embedding model") {
		t.Fatalf("chat model for embeddings: %q", msg)
	}
	resp := th.gw(t, "POST", "/v1/embeddings", key, []byte(`{"model":"nomic-embed","input":"x"}`))
	if resp.StatusCode != 200 {
		t.Fatalf("embeddings: %d", resp.StatusCode)
	}
	resp.Body.Close()
	if msg, _ := decodeErr(t, th.gw(t, "POST", "/v1/chat/completions", key, []byte(`not json`))); !strings.Contains(msg, "JSON") {
		t.Fatalf("bad body: %q", msg)
	}
	resp = th.gw(t, "GET", "/v1/chat/completions", key, nil)
	if resp.StatusCode != 405 {
		t.Fatalf("GET chat: %d", resp.StatusCode)
	}
	resp.Body.Close()
	// A model that's listed but not answering.
	if msg, _ := decodeErr(t, th.gw(t, "POST", "/v1/chat/completions", key, []byte(`{"model":"chat"}`))); !strings.Contains(msg, "isn't answering") {
		t.Fatalf("dead upstream: %q", msg)
	}
}

func TestGatewayRefreshesModelsOnMiss(t *testing.T) {
	th := newTestHarness(t)
	key := th.h.store.Get().GatewayKey
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer up.Close()
	resp := th.gw(t, "GET", "/v1/models", key, nil) // caches an empty list
	resp.Body.Close()
	th.mu.Lock()
	th.rows = []map[string]any{modelRow(upstreamPort(t, up.URL), "local-llm/fresh:latest", "text")}
	th.mu.Unlock()
	resp = th.gw(t, "POST", "/v1/chat/completions", key, []byte(`{"model":"fresh"}`))
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("a just-started model wasn't found: %d", resp.StatusCode)
	}
}

func TestCheckLocalRequest(t *testing.T) {
	t.Setenv("FACTORY_ALLOWED_HOSTS", "factory.lan")
	type tc struct {
		method, host, ctype, origin, site string
		ok                                bool
	}
	cases := []tc{
		{"GET", "localhost:8799", "", "", "", true},
		{"GET", "127.0.0.1:8799", "", "", "", true},
		{"GET", "[::1]:8799", "", "", "", true},
		{"GET", "192.168.1.20:8799", "", "", "", true},
		{"GET", "factory.lan:8799", "", "", "", true},
		{"GET", "attacker.example:8799", "", "", "", false}, // DNS rebinding
		{"GET", "", "", "", "", false},
		{"POST", "localhost:8799", "application/json", "", "", true},
		{"POST", "localhost:8799", "application/json; charset=utf-8", "http://localhost:8799", "same-origin", true},
		{"POST", "localhost:5173", "application/json", "http://localhost:5173", "same-origin", true},
		{"POST", "localhost:8799", "application/json", "http://localhost:5173", "", true}, // Vite dev proxy
		{"POST", "localhost:8799", "text/plain", "", "", false},
		{"POST", "localhost:8799", "application/x-www-form-urlencoded", "", "", false},
		{"POST", "localhost:8799", "application/json", "http://attacker.example", "", false},
		{"POST", "localhost:8799", "application/json", "null", "", false},
		{"POST", "localhost:8799", "application/json", "", "cross-site", false},
		{"POST", "localhost:8799", "application/json", "", "same-site", false},
		{"PUT", "localhost:8799", "application/json", "", "", true}, // same-origin JSON PUT/PATCH/DELETE (the MCP registry)
		{"PUT", "localhost:8799", "text/plain", "", "", false},
		{"OPTIONS", "localhost:8799", "application/json", "", "", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, "http://x/api/harness/status", nil)
		r.Host = c.host
		if c.ctype != "" {
			r.Header.Set("Content-Type", c.ctype)
		}
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if c.site != "" {
			r.Header.Set("Sec-Fetch-Site", c.site)
		}
		if err := checkLocalRequest(r); (err == nil) != c.ok {
			t.Errorf("%+v: err=%v", c, err)
		}
	}
}

func (th *testHarness) api(t *testing.T, method, path string, body any, hdr map[string]string) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rd = bytes.NewReader(data)
	}
	req, _ := http.NewRequest(method, th.srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v // Go sends req.Host, not a Host header
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestRegenerateGatewayKey(t *testing.T) {
	th := newTestHarness(t)
	old := th.h.store.Get().GatewayKey
	if code, _ := th.api(t, "GET", "/api/harness/gateway/key", nil, nil); code != 405 {
		t.Fatalf("GET regenerates: %d", code)
	}
	if code, _ := th.api(t, "POST", "/api/harness/gateway/key", map[string]any{}, map[string]string{"Origin": "http://evil.example"}); code != 403 {
		t.Fatalf("cross-site regenerate: %d", code)
	}
	if code, _ := th.api(t, "POST", "/api/harness/gateway/key", map[string]any{}, map[string]string{"Content-Type": "text/plain"}); code != 403 {
		t.Fatalf("form regenerate: %d", code)
	}
	code, out := th.api(t, "POST", "/api/harness/gateway/key", map[string]any{}, map[string]string{"Origin": th.srv.URL})
	if code != 200 || out["key"] == old || !strings.HasPrefix(out["key"].(string), "sk-llmf-") {
		t.Fatalf("regenerate: %d %v", code, out)
	}
	resp := th.gw(t, "GET", "/v1/models", old, nil)
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("old key still works")
	}
	resp = th.gw(t, "GET", "/v1/models", out["key"].(string), nil)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("new key rejected")
	}
}

func TestTerminalConfigValidation(t *testing.T) {
	th := newTestHarness(t)
	bad := []map[string]any{
		{"workspace": "/"},
		{"workspace": "relative/dir"},
		{"workspace": "/etc/x"},
		{"port": 80},
		{"port": 8799}, // the factory's own port
		{"variant": "latest; rm -rf /"},
		{"memory_mb": 64},
	}
	for _, b := range bad {
		if code, out := th.api(t, "POST", "/api/harness/terminal/config", b, nil); code != 400 {
			t.Errorf("%v accepted: %d %v", b, code, out)
		}
	}
	code, out := th.api(t, "POST", "/api/harness/terminal/config", map[string]any{
		"variant": "latest", "port": 9100, "workspace": "/srv/agent-work", "memory_mb": 3072,
	}, nil)
	if code != 200 || out["workspace_path"] != "/srv/agent-work" {
		t.Fatalf("valid config: %d %v", code, out)
	}
	c := th.h.store.Get().Terminal
	if c.Variant != "latest" || c.Port != 9100 || c.Workspace != "/srv/agent-work" || c.MemoryMB != 3072 {
		t.Fatalf("stored %+v", c)
	}
	// Setting the default path stores "" so it follows the repo.
	code, _ = th.api(t, "POST", "/api/harness/terminal/config", map[string]any{"workspace": th.h.paths.DefaultWorkspace()}, nil)
	if code != 200 || th.h.store.Get().Terminal.Workspace != "" {
		t.Fatalf("default workspace: %d %q", code, th.h.store.Get().Terminal.Workspace)
	}
}

func waitOp(t *testing.T, h *harnessAPI) terminalOpSnapshot {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s := h.op.snapshot(); !s.Active {
			return s
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("terminal start didn't finish")
	return terminalOpSnapshot{}
}

func TestTerminalLifecycle(t *testing.T) {
	th := newTestHarness(t)
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = io.WriteString(w, `{"status":"ok"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer health.Close()
	port := upstreamPort(t, health.URL)
	if code, out := th.api(t, "POST", "/api/harness/terminal/config", map[string]any{"port": port}, nil); code != 200 {
		t.Fatalf("config: %d %v", code, out)
	}
	key := th.h.store.Get().Terminal.APIKey

	// First start: pull, run, wait for health.
	if code, out := th.api(t, "POST", "/api/harness/terminal/start", map[string]any{}, nil); code != 202 {
		t.Fatalf("start: %d %v", code, out)
	}
	snap := waitOp(t, th.h)
	if snap.Phase != "running" || snap.Error != "" {
		t.Fatalf("op %+v", snap)
	}
	e := th.eng
	if e.pulls != 1 || e.runs != 1 {
		t.Fatalf("pulls=%d runs=%d", e.pulls, e.runs)
	}
	args := strings.Join(e.runArgs, " ")
	for _, want := range []string{
		"-p 127.0.0.1:" + strconv.Itoa(port) + ":8000",
		"-e OPEN_TERMINAL_API_KEY=" + key,
		"--label local-llm.tool=harness",
		"ghcr.io/open-webui/open-terminal:slim",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("run args lack %q: %s", want, args)
		}
	}

	// Status reflects it; nothing to restart.
	code, st := th.api(t, "GET", "/api/harness/status", nil, nil)
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	term := st["terminal"].(map[string]any)
	if term["healthy"] != true || term["restart_required"] != false || term["key"] != key {
		t.Fatalf("terminal status %v", term)
	}
	gw := st["gateway"].(map[string]any)
	if gw["base_url"] != "http://host.docker.internal:8799/v1" || gw["key"] != th.h.store.Get().GatewayKey {
		t.Fatalf("gateway status %v", gw)
	}

	// Starting again with the same settings is a no-op.
	th.api(t, "POST", "/api/harness/terminal/start", map[string]any{}, nil)
	waitOp(t, th.h)
	if e.runs != 1 || e.removes != 0 {
		t.Fatalf("restart without changes: runs=%d removes=%d", e.runs, e.removes)
	}

	// Stop keeps the container; start resumes it.
	if code, out := th.api(t, "POST", "/api/harness/terminal/stop", map[string]any{}, nil); code != 200 || out["status"] != "stopped" {
		t.Fatalf("stop: %d %v", code, out)
	}
	th.api(t, "POST", "/api/harness/terminal/start", map[string]any{}, nil)
	if s := waitOp(t, th.h); s.Phase != "running" || e.starts != 1 || e.runs != 1 {
		t.Fatalf("resume: %+v starts=%d runs=%d", s, e.starts, e.runs)
	}

	// A new key changes the spec: the status asks for a restart, and start
	// replaces the container.
	th.api(t, "POST", "/api/harness/terminal/key", map[string]any{}, nil)
	_, st = th.api(t, "GET", "/api/harness/status", nil, nil)
	if st["terminal"].(map[string]any)["restart_required"] != true {
		t.Fatal("restart_required not set after a key change")
	}
	th.api(t, "POST", "/api/harness/terminal/start", map[string]any{}, nil)
	waitOp(t, th.h)
	if e.removes != 1 || e.runs != 2 {
		t.Fatalf("recreate: removes=%d runs=%d", e.removes, e.runs)
	}

	// Logs are redacted.
	e.logs = "started with key " + th.h.store.Get().Terminal.APIKey
	_, out := th.api(t, "GET", "/api/harness/terminal/logs", nil, nil)
	if strings.Contains(out["logs"].(string), "otk-") {
		t.Fatalf("logs leak the key: %v", out["logs"])
	}

	if code, out := th.api(t, "POST", "/api/harness/terminal/stop", map[string]any{"remove": true}, nil); code != 200 || out["status"] != "removed" {
		t.Fatalf("remove: %d %v", code, out)
	}
}

func TestStatusWithoutEngine(t *testing.T) {
	th := newTestHarness(t)
	th.setRows(modelRow(18081, "local-llm/demo:latest", "text"))
	code, st := th.api(t, "GET", "/api/harness/status", nil, nil)
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	gw := st["gateway"].(map[string]any)
	if models := gw["models"].([]any); len(models) != 1 {
		t.Fatalf("models %v", gw["models"])
	}
	skills := st["skills"].(map[string]any)
	if skills["error"] == nil || skills["error"] == "" {
		t.Fatal("a missing skills/ folder should be explained")
	}
	if code, _ := th.api(t, "GET", "/api/harness/status", nil, map[string]string{"Host": "attacker.example"}); code != 403 {
		t.Fatalf("rebinding host allowed: %d", code)
	}
}

func TestFactorySideURL(t *testing.T) {
	th := newTestHarness(t)
	th.h.s.modelHost = "host.docker.internal"
	got, note := th.h.factorySideURL("http://localhost:3000")
	if got != "http://host.docker.internal:3000" || note == "" {
		t.Fatalf("got %q %q", got, note)
	}
	if got, note := th.h.factorySideURL("http://open-webui:8080"); got != "http://open-webui:8080" || note != "" {
		t.Fatalf("non-local URL rewritten: %q", got)
	}
	if d := th.h.defaultOpenWebUI(); d != "http://host.docker.internal:3000" {
		t.Fatalf("containerized default %q", d)
	}
	th.h.s.modelHost = "127.0.0.1" // a native factory reaches localhost directly
	if got, _ := th.h.factorySideURL("http://localhost:3000"); got != "http://localhost:3000" {
		t.Fatalf("native factory rewrote %q", got)
	}
	if d := th.h.defaultOpenWebUI(); d != "http://localhost:3000" {
		t.Fatalf("native default %q", d)
	}
}

// The gateway's model id must survive the ServeMux (dots, colons, @).
func TestGatewayModelIDPath(t *testing.T) {
	th := newTestHarness(t)
	key := th.h.store.Get().GatewayKey
	th.setRows(modelRow(18082, "org/llama-3.1-8b:q4_k_m", "text"))
	resp := th.gw(t, "GET", "/v1/models/llama-3.1-8b:q4_k_m", key, nil)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	var m harness.GatewayModel
	_ = json.NewDecoder(resp.Body).Decode(&m)
	if m.ID != "llama-3.1-8b:q4_k_m" {
		t.Fatalf("id %q", m.ID)
	}
}
