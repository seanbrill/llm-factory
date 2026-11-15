package harness

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const (
	testAdminKey = "sk-admin-0123456789"
	testUserKey  = "sk-user-0123456789"
	testGWKey    = "sk-llmf-gateway-0123456789"
	testTermKey  = "otk-terminal-0123456789"
	testGWURL    = "http://host.docker.internal:8799/v1"
	testTermURL  = "http://host.docker.internal:8798"
)

// fakeOWUI mimics the Open WebUI 0.13.3 admin endpoints the connector uses,
// including their quirks (index-keyed configs, null terminal list, slim's DDG
// refusal, exclude_unset web updates).
type fakeOWUI struct {
	mu              sync.Mutex
	apiKeysDisabled bool
	slim            bool
	fail422         bool // /openai/config/update answers a validation error echoing the input

	enable    *bool
	urls      []string
	keys      []string
	configs   map[string]any
	terminals []map[string]any
	web       map[string]any
	reachable map[string]string // url -> key that works (verify endpoints)

	writes   []string
	webPosts []map[string]any
}

func newFake() *fakeOWUI {
	t := true
	return &fakeOWUI{
		enable:  &t,
		urls:    []string{"https://api.openai.com/v1"},
		keys:    []string{"sk-user-openai-secret"},
		configs: map[string]any{"0": map[string]any{"enable": true, "prefix_id": "oa"}},
		web: map[string]any{
			"ENABLE_WEB_SEARCH": false, "WEB_SEARCH_ENGINE": "",
			"WEB_SEARCH_DOMAIN_FILTER_LIST": []any{"example.com"}, "BRAVE_SEARCH_API_KEY": "brave-secret",
		},
		reachable: map[string]string{testGWURL: testGWKey, testTermURL: testTermKey},
	}
}

func writeFakeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeOWUI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/api/version" {
		writeFakeJSON(w, 200, map[string]string{"version": "0.13.3"})
		return
	}
	switch auth := r.Header.Get("Authorization"); {
	case f.apiKeysDisabled:
		writeFakeJSON(w, 403, map[string]string{"detail": "Use of API key is not enabled in the environment."})
		return
	case auth == "Bearer "+testUserKey:
		writeFakeJSON(w, 401, map[string]string{"detail": "You do not have permission to access this resource. Please contact your administrator for assistance."})
		return
	case auth != "Bearer "+testAdminKey:
		writeFakeJSON(w, 401, map[string]string{"detail": "Not authenticated"})
		return
	}
	var body map[string]any
	if r.Method == http.MethodPost {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /openai/config":
		writeFakeJSON(w, 200, map[string]any{"ENABLE_OPENAI_API": f.enable, "OPENAI_API_BASE_URLS": f.urls, "OPENAI_API_KEYS": f.keys, "OPENAI_API_CONFIGS": f.configs})
	case "POST /openai/config/update":
		if f.fail422 {
			writeFakeJSON(w, 422, map[string]any{"detail": []any{map[string]any{"loc": []any{"body", "OPENAI_API_KEYS"}, "msg": "Input should be a valid list", "input": body["OPENAI_API_KEYS"]}}})
			return
		}
		f.writes = append(f.writes, r.URL.Path)
		var urls, keys []string
		for _, u := range body["OPENAI_API_BASE_URLS"].([]any) {
			urls = append(urls, u.(string))
		}
		for _, k := range body["OPENAI_API_KEYS"].([]any) {
			keys = append(keys, k.(string))
		}
		for len(keys) < len(urls) {
			keys = append(keys, "")
		}
		keys = keys[:len(urls)]
		cfgs := map[string]any{}
		for k, v := range body["OPENAI_API_CONFIGS"].(map[string]any) {
			if i, err := strconv.Atoi(k); err == nil && i < len(urls) {
				cfgs[k] = v
			}
		}
		en, _ := body["ENABLE_OPENAI_API"].(bool)
		f.enable, f.urls, f.keys, f.configs = &en, urls, keys, cfgs
		writeFakeJSON(w, 200, body)
	case "POST /openai/verify":
		u, _ := body["url"].(string)
		k, _ := body["key"].(string)
		want, ok := f.reachable[u]
		switch {
		case !ok:
			writeFakeJSON(w, 500, map[string]string{"detail": "Server Connection Error"})
		case k != want:
			writeFakeJSON(w, 401, map[string]any{"error": map[string]any{"message": "missing or wrong API key"}})
		default:
			writeFakeJSON(w, 200, map[string]any{"object": "list", "data": []any{map[string]any{"id": "m1"}, map[string]any{"id": "m2"}}})
		}
	case "GET /api/v1/configs/terminal_servers":
		writeFakeJSON(w, 200, map[string]any{"TERMINAL_SERVER_CONNECTIONS": f.terminals}) // null when unset
	case "POST /api/v1/configs/terminal_servers":
		f.writes = append(f.writes, r.URL.Path)
		var list []map[string]any
		for _, c := range body["TERMINAL_SERVER_CONNECTIONS"].([]any) {
			list = append(list, c.(map[string]any))
		}
		f.terminals = list
		writeFakeJSON(w, 200, map[string]any{"TERMINAL_SERVER_CONNECTIONS": list})
	case "POST /api/v1/configs/terminal_servers/verify":
		u, _ := body["url"].(string)
		k, _ := body["key"].(string)
		if want, ok := f.reachable[u]; ok && k == want {
			writeFakeJSON(w, 200, map[string]any{"status": true, "type": "terminal"})
			return
		}
		writeFakeJSON(w, 400, map[string]string{"detail": "Failed to connect to the terminal server"})
	case "GET /api/v1/retrieval/config":
		writeFakeJSON(w, 200, map[string]any{"status": true, "RAG_TEMPLATE": "t", "web": f.web})
	case "POST /api/v1/retrieval/config/update":
		web, _ := body["web"].(map[string]any)
		f.webPosts = append(f.webPosts, web)
		if f.slim && web["WEB_SEARCH_ENGINE"] == "duckduckgo" && f.web["WEB_SEARCH_ENGINE"] != "duckduckgo" {
			writeFakeJSON(w, 400, map[string]string{"detail": "DDGS is unavailable in slim. Configure another web search provider in Admin Settings > Web Search."})
			return
		}
		f.writes = append(f.writes, r.URL.Path)
		for k, v := range web { // exclude_unset: only submitted fields change
			f.web[k] = v
		}
		writeFakeJSON(w, 200, map[string]any{"status": true, "web": f.web})
	default:
		writeFakeJSON(w, 404, map[string]string{"detail": "Not Found"})
	}
}

func testPlan() Plan {
	return Plan{
		GatewayURL: testGWURL, GatewayKey: testGWKey,
		TerminalURL: testTermURL, TerminalKey: testTermKey,
		OpenAI: true, Terminal: true, WebSearch: true, TerminalRunning: true,
	}
}

func startFake(t *testing.T, f *fakeOWUI) *OWUIClient {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c, err := NewOWUIClient(srv.URL+"/", testAdminKey)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func statuses(r Report, area string) []string {
	var out []string
	for _, it := range r.Items {
		if it.Area == area {
			out = append(out, it.Status)
		}
	}
	return out
}

func assertNoSecrets(t *testing.T, r Report, secrets ...string) {
	t.Helper()
	data, _ := json.Marshal(r)
	for _, s := range secrets {
		if strings.Contains(string(data), s) {
			t.Fatalf("report leaks a secret (%s…): %s", s[:6], data)
		}
	}
}

func TestConnectFreshInstall(t *testing.T) {
	f := newFake()
	c := startFake(t, f)
	r := c.Connect(context.Background(), testPlan())
	if !r.Reachable || !r.Admin || !r.Changed || r.HasErrors() || r.Version != "0.13.3" {
		t.Fatalf("report %+v", r)
	}
	if got := statuses(r, "models"); strings.Join(got, ",") != "added,ok" {
		t.Errorf("models items %v", got)
	}
	if got := statuses(r, "terminal"); strings.Join(got, ",") != "added,ok" {
		t.Errorf("terminal items %v", got)
	}
	if got := statuses(r, "web_search"); strings.Join(got, ",") != "updated" {
		t.Errorf("web items %v", got)
	}
	// The user's existing connection is untouched; ours is appended.
	if strings.Join(f.urls, "|") != "https://api.openai.com/v1|"+testGWURL || strings.Join(f.keys, "|") != "sk-user-openai-secret|"+testGWKey {
		t.Fatalf("openai urls %v keys %v", f.urls, f.keys)
	}
	if cfg := f.configs["0"].(map[string]any); cfg["prefix_id"] != "oa" {
		t.Fatalf("existing config changed: %v", cfg)
	}
	ours := f.configs["1"].(map[string]any)
	if ours["connection_type"] != "local" || ours["enable"] != true || ours["auth_type"] != "bearer" {
		t.Fatalf("gateway config %v", ours)
	}
	if len(f.terminals) != 1 {
		t.Fatalf("terminals %v", f.terminals)
	}
	term := f.terminals[0]
	if term["id"] != TerminalID || term["url"] != testTermURL || term["key"] != testTermKey ||
		term["auth_type"] != "bearer" || term["path"] != "/openapi.json" || term["enabled"] != true {
		t.Fatalf("terminal %v", term)
	}
	if grants := term["config"].(map[string]any)["access_grants"].([]any); len(grants) != 0 {
		t.Fatalf("terminal should start admin-only, got grants %v", grants)
	}
	if f.web["ENABLE_WEB_SEARCH"] != true || f.web["WEB_SEARCH_ENGINE"] != "duckduckgo" || len(f.web["WEB_SEARCH_DOMAIN_FILTER_LIST"].([]any)) != 1 {
		t.Fatalf("web %v", f.web)
	}
	if r.WebSearch == nil || !r.WebSearch.Enabled || r.WebSearch.Engine != "duckduckgo" {
		t.Fatalf("web state %+v", r.WebSearch)
	}
	assertNoSecrets(t, r, testAdminKey, testGWKey, testTermKey, "sk-user-openai-secret", "brave-secret")

	// Connecting again changes nothing.
	f.writes = nil
	r2 := c.Connect(context.Background(), testPlan())
	if r2.Changed || r2.HasErrors() || len(f.writes) != 0 {
		t.Fatalf("second connect wrote %v: %+v", f.writes, r2)
	}
	for _, it := range r2.Items {
		if it.Status != "ok" && it.Status != "info" {
			t.Errorf("second connect item %+v", it)
		}
	}
}

func TestConnectUpdatesLegacyAndRotatedKeys(t *testing.T) {
	f := newFake()
	f.urls = []string{"http://other.example/v1", testGWURL + "/"}
	f.keys = []string{"sk-other"} // shorter than urls, as old configs can be
	f.configs = map[string]any{
		"0":              map[string]any{"enable": true},
		testGWURL + "/":  map[string]any{"enable": false, "prefix_id": "factory"}, // legacy URL-keyed
		"http://gone/v1": map[string]any{"enable": true},
	}
	f.terminals = []map[string]any{
		{"id": "mine", "url": "http://elsewhere:8000", "key": "keep-me", "name": "Mine"},
		{"id": TerminalID, "name": "Renamed by user", "url": "http://old-host:8798", "key": "old-key", "enabled": false,
			"auth_type": "bearer", "path": "/openapi.json", "server_type": "terminal",
			"config": map[string]any{"access_grants": []any{map[string]any{"principal_type": "group", "principal_id": "g1"}}}},
	}
	c := startFake(t, f)
	r := c.Connect(context.Background(), testPlan())
	if r.HasErrors() || !r.Changed {
		t.Fatalf("report %+v", r)
	}
	if strings.Join(f.keys, "|") != "sk-other|"+testGWKey {
		t.Fatalf("keys %v", f.keys)
	}
	cfg1, ok := f.configs["1"].(map[string]any)
	if !ok || cfg1["enable"] != true || cfg1["prefix_id"] != "factory" {
		t.Fatalf("legacy config not migrated: %v", f.configs)
	}
	if len(f.urls) != 2 {
		t.Fatalf("a duplicate connection was added: %v", f.urls)
	}
	if got := statuses(r, "models"); strings.Join(got, ",") != "updated,ok" {
		t.Errorf("models items %v", got)
	}
	mine, ours := f.terminals[0], f.terminals[1]
	if mine["key"] != "keep-me" || mine["url"] != "http://elsewhere:8000" {
		t.Fatalf("user's terminal changed: %v", mine)
	}
	if ours["url"] != testTermURL || ours["key"] != testTermKey || ours["enabled"] != true || ours["name"] != "Renamed by user" {
		t.Fatalf("our terminal %v", ours)
	}
	if grants := ours["config"].(map[string]any)["access_grants"].([]any); len(grants) != 1 {
		t.Fatalf("access grants lost: %v", ours["config"])
	}
}

func TestWebSearchRespectsChosenEngine(t *testing.T) {
	f := newFake()
	f.web["WEB_SEARCH_ENGINE"] = "searxng"
	c := startFake(t, f)
	plan := testPlan()
	plan.OpenAI, plan.Terminal = false, false
	r := c.Connect(context.Background(), plan)
	if r.HasErrors() {
		t.Fatalf("report %+v", r)
	}
	if len(f.webPosts) != 1 || len(f.webPosts[0]) != 1 || f.webPosts[0]["ENABLE_WEB_SEARCH"] != true {
		t.Fatalf("web update should only switch search on: %v", f.webPosts)
	}
	if f.web["WEB_SEARCH_ENGINE"] != "searxng" {
		t.Fatal("engine was changed")
	}
	if got := statuses(r, "models"); strings.Join(got, ",") != "skipped" {
		t.Errorf("models items %v", got)
	}

	// Already on: nothing is sent.
	f.webPosts = nil
	r = c.Connect(context.Background(), plan)
	if len(f.webPosts) != 0 || strings.Join(statuses(r, "web_search"), ",") != "ok" {
		t.Fatalf("posts %v, items %+v", f.webPosts, r.Items)
	}
}

func TestWebSearchSlimRefusal(t *testing.T) {
	f := newFake()
	f.slim = true
	c := startFake(t, f)
	r := c.Connect(context.Background(), testPlan())
	if !r.HasErrors() {
		t.Fatal("expected an error item")
	}
	var detail string
	for _, it := range r.Items {
		if it.Area == "web_search" {
			detail = it.Detail
		}
	}
	if !strings.Contains(detail, "Admin Panel → Settings → Web Search") {
		t.Fatalf("unhelpful slim message: %q", detail)
	}
	// The other parts still went through.
	if len(f.terminals) != 1 || len(f.urls) != 2 {
		t.Fatal("models/terminal not connected")
	}
}

func TestConnectAuthFailures(t *testing.T) {
	f := newFake()
	srv := httptest.NewServer(f)
	defer srv.Close()

	user, _ := NewOWUIClient(srv.URL, testUserKey)
	r := user.Connect(context.Background(), testPlan())
	if r.Admin || !r.Reachable || len(r.Items) != 1 || !strings.Contains(r.Items[0].Detail, "admin") {
		t.Fatalf("non-admin key: %+v", r)
	}

	f.apiKeysDisabled = true
	admin, _ := NewOWUIClient(srv.URL, testAdminKey)
	r = admin.Check(context.Background(), testPlan())
	if r.Admin || len(r.Items) != 1 || !strings.Contains(r.Items[0].Detail, "Enable API Keys") {
		t.Fatalf("api keys disabled: %+v", r)
	}
	if len(f.writes) != 0 {
		t.Fatalf("wrote %v without admin rights", f.writes)
	}
	assertNoSecrets(t, r, testAdminKey, testUserKey)
}

func TestCheckReportsWithoutWriting(t *testing.T) {
	f := newFake()
	f.reachable = map[string]string{} // nothing reachable from Open WebUI
	c := startFake(t, f)
	r := c.Check(context.Background(), testPlan())
	if len(f.writes) != 0 || r.Changed {
		t.Fatalf("check wrote %v", f.writes)
	}
	if got := strings.Join(statuses(r, "models"), ","); got != "missing,error" {
		t.Errorf("models %v", got)
	}
	if got := strings.Join(statuses(r, "terminal"), ","); got != "missing,error" {
		t.Errorf("terminal %v", got)
	}
	if got := strings.Join(statuses(r, "web_search"), ","); got != "missing" {
		t.Errorf("web %v", got)
	}
	for _, it := range r.Items {
		if it.Area == "models" && it.Status == "error" && !strings.Contains(it.Detail, "host.docker.internal") {
			t.Errorf("reachability error lacks a hint: %q", it.Detail)
		}
	}
	// A stopped terminal isn't probed.
	plan := testPlan()
	plan.TerminalRunning = false
	r = c.Check(context.Background(), plan)
	if got := strings.Join(statuses(r, "terminal"), ","); got != "missing" {
		t.Errorf("terminal (stopped) %v", got)
	}
}

func TestErrorsDoNotLeakSecrets(t *testing.T) {
	f := newFake()
	f.fail422 = true
	c := startFake(t, f)
	r := c.Connect(context.Background(), testPlan())
	if !r.HasErrors() {
		t.Fatal("expected an error")
	}
	assertNoSecrets(t, r, testAdminKey, testGWKey, testTermKey, "sk-user-openai-secret")
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var hits int
	var mu sync.Mutex
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		writeFakeJSON(w, 200, map[string]string{"version": "evil"})
	}))
	defer elsewhere.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	c, _ := NewOWUIClient(redirector.URL, testAdminKey)
	r := c.Check(context.Background(), testPlan())
	if r.Reachable || len(r.Items) != 1 || !strings.Contains(r.Items[0].Detail, "redirect") {
		t.Fatalf("report %+v", r)
	}
	if hits != 0 {
		t.Fatal("the redirect was followed")
	}
}

func TestNotOpenWebUI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>hello</html>"))
	}))
	defer srv.Close()
	c, _ := NewOWUIClient(srv.URL, testAdminKey)
	r := c.Check(context.Background(), testPlan())
	if r.Admin || len(r.Items) != 1 || r.Items[0].Status != "error" {
		t.Fatalf("report %+v", r)
	}
}

func TestNewOWUIClientValidation(t *testing.T) {
	bad := map[string]string{
		"ftp://x":                testAdminKey,
		"host.docker.internal":   testAdminKey,
		"http://user:pw@x:3000":  testAdminKey,
		"http://x:3000":          "",
		"http://x:3000/":         "a\nb",
		"javascript:alert(1)//x": testAdminKey,
	}
	for u, k := range bad {
		if _, err := NewOWUIClient(u, k); err == nil {
			t.Errorf("NewOWUIClient(%q, %q) accepted", u, k)
		}
	}
	c, err := NewOWUIClient(" http://host.docker.internal:3000/owui/ ", " "+testAdminKey+" ")
	if err != nil || c.BaseURL != "http://host.docker.internal:3000/owui" {
		t.Fatalf("got %v, %v", c, err)
	}
}
