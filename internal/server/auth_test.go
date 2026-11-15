package server

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/seanbrill/local-llm/internal/builder"
	"github.com/seanbrill/local-llm/internal/catalog"
)

// The contract the UI's lock screen is written against.
const (
	wantAuthRequiredMsg = "This factory needs its access link: open the link start.sh printed, or paste the token from config/factory-token."
	wantBadTokenMsg     = "That token isn't right."
	wantRateLimitedMsg  = "Too many tries — wait a minute."
)

// newAuthServer builds the real server (every route, behind the real token
// check) in a temp project dir. env sets the auth variables; the rest are
// cleared so the developer's shell can't leak in.
func newAuthServer(t *testing.T, env map[string]string) *Server {
	t.Helper()
	for _, k := range []string{"FACTORY_AUTH", "FACTORY_TOKEN", "HOST_DIR", "HOST_HOME", "FACTORY_PORT", "FACTORY_ALLOWED_HOSTS"} {
		t.Setenv(k, env[k])
	}
	b, err := builder.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(b, &catalog.Catalog{}, "127.0.0.1", "")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// send runs one request through h.
func send(h http.Handler, method, target, body string, hdrs ...map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for _, hdr := range hdrs {
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// tokenHeader is how a test (or any program) calls a protected route through
// Handler(): the server's token as a Bearer header.
func tokenHeader(s *Server) map[string]string {
	return map[string]string{"Authorization": "Bearer " + s.Auth().Token()}
}

func cookieHeader(tok string) map[string]string {
	return map[string]string{"Cookie": authCookieName + "=" + tok}
}

var jsonHeader = map[string]string{"Content-Type": "application/json"}

func jsonBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body %q: %v", rec.Body, err)
	}
	return out
}

func wantAuthRequired(t *testing.T, rec *httptest.ResponseRecorder, what string) {
	t.Helper()
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusUnauthorized || body["code"] != "auth_required" || body["error"] != wantAuthRequiredMsg ||
		rec.Header().Get("WWW-Authenticate") != `Bearer realm="llm-factory"` || rec.Header().Get("Set-Cookie") != "" {
		t.Errorf("%s: want 401 auth_required, got %d %q %v", what, rec.Code, rec.Body, rec.Header())
	}
	noCORS(t, rec, what)
}

func noCORS(t *testing.T, rec *httptest.ResponseRecorder, what string) {
	t.Helper()
	for k := range rec.Header() {
		if strings.HasPrefix(http.CanonicalHeaderKey(k), "Access-Control-") {
			t.Errorf("%s: sends CORS header %s", what, k)
		}
	}
}

// sessionCookieOf returns the llmf_token cookie a response sets (nil if none).
func sessionCookieOf(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == authCookieName {
			return c
		}
	}
	return nil
}

// wantSessionCookie checks the cookie a sign-in sets, attribute by attribute.
func wantSessionCookie(t *testing.T, rec *httptest.ResponseRecorder, tok string, secure bool, what string) {
	t.Helper()
	raw := rec.Header().Get("Set-Cookie")
	c := sessionCookieOf(rec)
	if c == nil || c.Value != tok || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" ||
		c.MaxAge != 34560000 || c.Secure != secure || !strings.Contains(raw, "Max-Age=34560000") {
		t.Errorf("%s: cookie %q", what, raw)
	}
}

// samplePath is a request path that a mux pattern matches.
func samplePath(pattern string) string {
	if _, p, ok := strings.Cut(pattern, " "); ok { // "GET /x"
		pattern = p
	}
	if i := strings.Index(pattern, "/"); i > 0 { // "host/x"
		pattern = pattern[i:]
	}
	segs := strings.Split(pattern, "/")
	for i, seg := range segs {
		if strings.HasPrefix(seg, "{") {
			segs[i] = "x"
		}
	}
	p := strings.Join(segs, "/")
	if p != "/" && strings.HasSuffix(p, "/") {
		p += "x"
	}
	return p
}

// Every route on the real mux is public (publicRoutes), the model gateway (its
// own key), or needs the token. A route added later lands in the last group by
// itself, so it is checked here without anyone listing it.
func TestEveryRouteNeedsTheTokenUnlessPublic(t *testing.T) {
	s := newAuthServer(t, nil)
	h := s.Handler()
	tok := s.Auth().Token()

	seen := map[string]bool{}
	for _, p := range s.mux.patterns {
		seen[p] = true
	}
	for _, p := range []string{"/", "/auth", "/healthz", "/readyz", "/v1/", "/api/run", "/api/ensemble/build", "/api/image/delete",
		"/api/media/", "/api/harness/terminal/start", "/api/harness/status", "/api/gpu/probe", "/api/storage/cleanup",
		"/api/builds/retry", "/api/auth/status", "/api/auth/login", "/api/auth/logout"} {
		if !seen[p] {
			t.Errorf("route %s isn't recorded on the mux (patterns: %v)", p, s.mux.patterns)
		}
	}

	wrong := []map[string]string{
		nil,
		cookieHeader(strings.Repeat("0", 64)),
		{"Authorization": "Bearer " + tok[1:]},
		{authHeaderName: tok + "0"},
	}
	protected := 0
	for _, pattern := range s.mux.patterns {
		p := samplePath(pattern)
		switch {
		case pattern == gatewayRoute:
			// The gateway answers with its own key check, and the factory
			// token is not that key.
			for _, hdr := range []map[string]string{nil, cookieHeader(tok), {"Authorization": "Bearer " + tok}, {authHeaderName: tok}} {
				rec := send(h, "GET", "/v1/models", "", hdr)
				if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "invalid_api_key") || strings.Contains(rec.Body.String(), "auth_required") {
					t.Errorf("gateway with %v: %d %s", hdr, rec.Code, rec.Body)
				}
			}
		case publicRoutes[pattern]:
			if needsToken(p, pattern) {
				t.Errorf("public route %s (%s) needs the token", pattern, p)
			}
		default:
			protected++
			if !needsToken(p, pattern) {
				t.Errorf("route %s (%s) answers without the token; list it in publicRoutes only if it neither acts nor leaks data", pattern, p)
			}
			for _, method := range []string{"GET", "HEAD", "POST", "PUT", "DELETE", "OPTIONS"} {
				for _, hdr := range wrong {
					wantAuthRequired(t, send(h, method, p, `{}`, jsonHeader, hdr), method+" "+p)
				}
			}
		}
	}
	if protected < 40 {
		t.Errorf("only %d protected routes", protected)
	}
}

func TestNewRoutesNeedTheTokenByDefault(t *testing.T) {
	s := newAuthServer(t, nil)
	ok := func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]bool{"ok": true}) }
	s.mux.HandleFunc("/api/brand-new", ok)
	s.mux.HandleFunc("/debug/brand-new", ok) // not under /api: still protected unless publicRoutes says so
	h := s.Handler()
	for _, p := range []string{"/api/brand-new", "/debug/brand-new"} {
		wantAuthRequired(t, send(h, "GET", p, ""), p)
		if rec := send(h, "GET", p, "", tokenHeader(s)); rec.Code != http.StatusOK {
			t.Errorf("%s with the token: %d %s", p, rec.Code, rec.Body)
		}
	}
}

func TestPathTricksStillNeedTheToken(t *testing.T) {
	s := newAuthServer(t, nil)
	h := s.Handler()
	for _, p := range []string{"/api", "/api/", "/api//run", "/api/./run", "/x/../api/run", "/v1/../api/run", "/%61pi/run",
		"/api%2Frun", "/api/auth/status/../../run", "/api/auth/status/", "/api/auth/login/", "/api/media", "/api/media/../run",
		"/api/nope", "/auth/../api/version"} {
		wantAuthRequired(t, send(h, "GET", p, ""), p)
	}
}

func TestTheTokenOpensTheAPI(t *testing.T) {
	s := newAuthServer(t, nil)
	h := s.Handler()
	tok := s.Auth().Token()
	for _, hdr := range []map[string]string{
		cookieHeader(tok),
		{"Cookie": "theme=dark; " + authCookieName + "=" + tok},
		{"Cookie": authCookieName + "=stale; " + authCookieName + "=" + tok}, // an old cookie beside the right one
		{"Authorization": "Bearer " + tok},
		{"Authorization": "bearer   " + tok},
		{authHeaderName: tok},
	} {
		rec := send(h, "GET", "/api/version", "", hdr)
		if rec.Code != http.StatusOK || jsonBody(t, rec)["version"] == nil {
			t.Errorf("%v: %d %s", hdr, rec.Code, rec.Body)
		}
		noCORS(t, rec, "authorized /api/version")
	}
	for _, hdr := range []map[string]string{
		cookieHeader(tok + "0"),
		cookieHeader(tok[:len(tok)-1]),
		cookieHeader(""),
		{"Cookie": "other=" + tok},
		{"Authorization": "Bearer " + strings.ToUpper(tok)},
		{"Authorization": "Basic " + tok},
		{"Authorization": tok},
		{"Authorization": "Bearer"},
		{authHeaderName: ""},
		{authHeaderName: " "},
		{"X-Other-Token": tok},
	} {
		wantAuthRequired(t, send(h, "GET", "/api/version", "", hdr), "wrong credential")
	}
	// A cross-origin preflight never carries credentials, and gets no CORS grant.
	rec := send(h, "OPTIONS", "/api/run", "", map[string]string{"Origin": "http://evil.example", "Access-Control-Request-Method": "POST"})
	wantAuthRequired(t, rec, "preflight")
}

// Every localhost port is the same site, so the browser sends the cookie with
// requests a page on another port (Open WebUI, the terminal) makes. Those may
// read, but a cookie-only call that changes something must be same-origin.
func TestCookieCallsThatActMustBeSameOrigin(t *testing.T) {
	s := newAuthServer(t, nil)
	h := s.Handler()
	tok := s.Auth().Token()
	for site, want := range map[string]int{"": 200, "same-origin": 200, "none": 200, "same-site": 403, "cross-site": 403} {
		rec := send(h, "POST", "/api/auth/logout", "", cookieHeader(tok), map[string]string{"Sec-Fetch-Site": site})
		if rec.Code != want {
			t.Errorf("cookie POST, Sec-Fetch-Site %q: %d %s", site, rec.Code, rec.Body)
		}
		if want == 403 && (jsonBody(t, rec)["code"] != "cross_site" || sessionCookieOf(rec) != nil) {
			t.Errorf("cookie POST, Sec-Fetch-Site %q: %s %v", site, rec.Body, rec.Header())
		}
	}
	if rec := send(h, "GET", "/api/version", "", cookieHeader(tok), map[string]string{"Sec-Fetch-Site": "same-site"}); rec.Code != 200 {
		t.Errorf("same-site cookie GET: %d", rec.Code)
	}
	// A page can't add headers to another origin's request, so a header token
	// is trusted wherever it comes from.
	if rec := send(h, "POST", "/api/auth/logout", "", tokenHeader(s), map[string]string{"Sec-Fetch-Site": "cross-site"}); rec.Code != 200 {
		t.Errorf("cross-site header POST: %d", rec.Code)
	}
}

func TestAccessLinkSetsTheCookie(t *testing.T) {
	s := newAuthServer(t, nil)
	h := s.Handler()
	tok := s.Auth().Token()
	for _, target := range []string{"/?token=" + tok, "/auth?token=" + tok, "/?token=" + tok + "%0A", "/?x=1&token=" + tok} {
		rec := send(h, "GET", target, "")
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
			t.Fatalf("%s: %d Location=%q", target, rec.Code, rec.Header().Get("Location"))
		}
		if rec.Header().Get("Referrer-Policy") != "no-referrer" || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: headers %v", target, rec.Header())
		}
		wantSessionCookie(t, rec, tok, false, target)
		if strings.Contains(rec.Body.String(), tok) {
			t.Errorf("%s: the redirect body repeats the token", target)
		}
		// The cookie it set opens the API.
		if rec := send(h, "GET", "/api/version", "", cookieHeader(sessionCookieOf(rec).Value)); rec.Code != 200 {
			t.Errorf("%s: cookie from the link: %d", target, rec.Code)
		}
	}
	if rec := send(h, "HEAD", "/?token="+tok, ""); rec.Code != http.StatusFound || sessionCookieOf(rec) == nil {
		t.Errorf("HEAD link: %d", rec.Code)
	}
	// Over HTTPS the cookie is Secure.
	rec := send(h, "GET", "https://localhost/?token="+tok, "")
	wantSessionCookie(t, rec, tok, true, "https link")
}

// A chosen token may hold URL-special characters: the link main.go builds
// (query-escaped) must still sign in, and the cookie the browser sends back
// must still match.
func TestChosenTokenSurvivesTheLinkAndTheCookie(t *testing.T) {
	tok := "base64/std+token=with?odd&chars#and%more"
	s := newAuthServer(t, map[string]string{"FACTORY_TOKEN": tok})
	h := s.Handler()
	rec := send(h, "GET", "/?token="+url.QueryEscape(tok), "")
	if rec.Code != http.StatusFound {
		t.Fatalf("link: %d", rec.Code)
	}
	wantSessionCookie(t, rec, tok, false, "chosen token")
	pair, _, _ := strings.Cut(rec.Header().Get("Set-Cookie"), ";") // what the browser sends back
	if rec := send(h, "GET", "/api/version", "", map[string]string{"Cookie": pair}); rec.Code != http.StatusOK {
		t.Errorf("cookie %q: %d", pair, rec.Code)
	}
	if rec := send(h, "GET", "/api/version", "", map[string]string{authHeaderName: tok}); rec.Code != http.StatusOK {
		t.Errorf("header: %d", rec.Code)
	}
}

func TestAccessLinkWithoutAValidTokenServesTheUI(t *testing.T) {
	s := newAuthServer(t, nil)
	h := s.Handler()
	tok := s.Auth().Token()
	for _, target := range []string{"/", "/auth", "/?token=", "/?token=wrong", "/auth?token=wrong", "/?token=" + tok[1:], "/?token=" + tok + "0"} {
		rec := send(h, "GET", target, "")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `<div id="app">`) || rec.Header().Get("Set-Cookie") != "" {
			t.Errorf("%s: %d %q", target, rec.Code, rec.Header())
		}
		if strings.Contains(target, "token=") && rec.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s: no Referrer-Policy", target)
		}
	}
	// Only GET/HEAD trade a token for the cookie.
	if rec := send(h, "POST", "/?token="+tok, ""); sessionCookieOf(rec) != nil {
		t.Errorf("POST link set the cookie")
	}
	// The rest of the static UI needs no token either.
	if rec := send(h, "GET", "/assets/app.js", ""); rec.Code != http.StatusOK {
		t.Errorf("/assets/app.js: %d", rec.Code)
	}
	if rec := send(h, "GET", "/healthz", ""); rec.Code != http.StatusOK {
		t.Errorf("/healthz: %d", rec.Code)
	}
}

func TestLoginAndItsRateLimit(t *testing.T) {
	s := newAuthServer(t, nil)
	h := s.Handler()
	a := s.Auth()
	tok := a.Token()
	clock := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return clock }
	login := func(tok string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"token": tok})
		return send(h, "POST", authLoginRoute, string(body), jsonHeader)
	}

	rec := login(tok)
	if rec.Code != 200 || rec.Body.String() != "{\"authed\":true}\n" {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	wantSessionCookie(t, rec, tok, false, "login")
	noCORS(t, rec, "login")
	if rec := login("  " + tok + "\n"); rec.Code != 200 { // pasted with a newline
		t.Errorf("padded token: %d", rec.Code)
	}

	// Not JSON, or not a POST: refused without counting as a wrong token.
	for i := 0; i < 2*maxLoginFails; i++ {
		if rec := send(h, "POST", authLoginRoute, `{"token":"x"}`, map[string]string{"Content-Type": "text/plain"}); rec.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("text/plain login: %d", rec.Code)
		}
		if rec := send(h, "POST", authLoginRoute, `{"token":"x"}`); rec.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("login without a content type: %d", rec.Code)
		}
	}
	if rec := send(h, "GET", authLoginRoute, ""); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET login: %d", rec.Code)
	}
	if rec := send(h, "POST", authLoginRoute, `{"token":`, jsonHeader); rec.Code != http.StatusBadRequest {
		t.Errorf("broken JSON: %d", rec.Code)
	}

	for i := 0; i < maxLoginFails; i++ {
		rec := login("nope-" + tok)
		body := jsonBody(t, rec)
		if rec.Code != 401 || body["error"] != wantBadTokenMsg || body["code"] != "bad_token" || sessionCookieOf(rec) != nil {
			t.Fatalf("wrong token %d: %d %s", i, rec.Code, rec.Body)
		}
		clock = clock.Add(time.Second)
	}
	// Five wrong tokens within a minute: refused, even the right one, until
	// the first of them is a minute old.
	for _, try := range []string{"nope", tok} {
		rec := login(try)
		body := jsonBody(t, rec)
		if rec.Code != 429 || body["error"] != wantRateLimitedMsg || body["code"] != "rate_limited" || rec.Header().Get("Retry-After") != "55" || sessionCookieOf(rec) != nil {
			t.Fatalf("rate-limited %q: %d %s %v", try, rec.Code, rec.Body, rec.Header())
		}
	}
	clock = clock.Add(54 * time.Second) // the first wrong token is 59 s old
	if rec := login(tok); rec.Code != 429 || rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("at 59 s: %d %v", rec.Code, rec.Header())
	}
	clock = clock.Add(time.Second) // 60 s: the first has left the window
	if rec := login(tok); rec.Code != 200 {
		t.Fatalf("after the window: %d %s", rec.Code, rec.Body)
	}
	// Four wrong tokens are still in the window: one more trips the limit.
	if rec := login("nope"); rec.Code != 401 {
		t.Fatalf("one more wrong token: %d", rec.Code)
	}
	if rec := login(tok); rec.Code != 429 {
		t.Fatalf("limit again: %d", rec.Code)
	}
	clock = clock.Add(2 * time.Minute)
	if rec := login(tok); rec.Code != 200 {
		t.Fatalf("after it all ages out: %d", rec.Code)
	}
}

func TestAuthStatus(t *testing.T) {
	s := newAuthServer(t, nil)
	h := s.Handler()
	tok := s.Auth().Token()
	for _, c := range []struct {
		hdr  map[string]string
		want string
	}{
		{nil, `{"authed":false,"required":true}`},
		{cookieHeader("wrong"), `{"authed":false,"required":true}`},
		{cookieHeader(tok), `{"authed":true,"required":true}`},
		{tokenHeader(s), `{"authed":true,"required":true}`},
	} {
		rec := send(h, "GET", authStatusRoute, "", c.hdr)
		if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != c.want || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("status with %v: %d %s", c.hdr, rec.Code, rec.Body)
		}
		noCORS(t, rec, "status")
	}
	if rec := send(h, "POST", authStatusRoute, "{}", jsonHeader); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status: %d", rec.Code)
	}
}

func TestLogout(t *testing.T) {
	s := newAuthServer(t, nil)
	h := s.Handler()
	tok := s.Auth().Token()
	rec := send(h, "POST", authLogoutRoute, "", cookieHeader(tok))
	c := sessionCookieOf(rec)
	if rec.Code != 200 || c == nil || c.Value != "" || c.MaxAge >= 0 || c.Path != "/" || !c.HttpOnly ||
		!strings.Contains(rec.Header().Get("Set-Cookie"), "Max-Age=0") {
		t.Fatalf("logout: %d %q", rec.Code, rec.Header().Get("Set-Cookie"))
	}
	wantAuthRequired(t, send(h, "POST", authLogoutRoute, ""), "logout without the token")
	if rec := send(h, "GET", authLogoutRoute, "", cookieHeader(tok)); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET logout: %d", rec.Code)
	}
}

func TestAuthOff(t *testing.T) {
	s := newAuthServer(t, map[string]string{"FACTORY_AUTH": "off", "FACTORY_TOKEN": "ignored-when-auth-is-off-0123456789"})
	h := s.Handler()
	if s.Auth().Required() || s.Auth().Token() != "" {
		t.Fatalf("auth is on: %+v", s.Auth())
	}
	if _, err := os.Stat(filepath.Join(s.b.ConfigDir, tokenFileName)); !os.IsNotExist(err) {
		t.Errorf("token file written with auth off: %v", err)
	}
	if rec := send(h, "GET", "/api/version", ""); rec.Code != 200 {
		t.Errorf("/api/version: %d", rec.Code)
	}
	if rec := send(h, "POST", "/api/auth/logout", "", map[string]string{"Sec-Fetch-Site": "cross-site"}); rec.Code != 200 {
		t.Errorf("logout: %d", rec.Code)
	}
	if rec := send(h, "GET", authStatusRoute, ""); strings.TrimSpace(rec.Body.String()) != `{"authed":true,"required":false}` {
		t.Errorf("status: %s", rec.Body)
	}
	if rec := send(h, "POST", authLoginRoute, `{"token":"anything"}`, jsonHeader); rec.Code != 200 || sessionCookieOf(rec) != nil {
		t.Errorf("login: %d %v", rec.Code, rec.Header())
	}
	if rec := send(h, "GET", "/?token=anything", ""); rec.Code != 200 || sessionCookieOf(rec) != nil {
		t.Errorf("link: %d %v", rec.Code, rec.Header())
	}
	// The gateway still wants its own key.
	if rec := send(h, "GET", "/v1/models", ""); rec.Code != 401 {
		t.Errorf("gateway: %d", rec.Code)
	}
}

func TestTokenFileIsCreatedPrivateAndReused(t *testing.T) {
	t.Setenv("FACTORY_AUTH", "")
	t.Setenv("FACTORY_TOKEN", "")
	dir := filepath.Join(t.TempDir(), "config")
	file := filepath.Join(dir, tokenFileName)
	a, err := LoadAuth(dir)
	if err != nil {
		t.Fatal(err)
	}
	tok := a.Token()
	if raw, err := hex.DecodeString(tok); err != nil || len(raw) != 32 {
		t.Fatalf("token %q: want 32 random bytes as hex", tok)
	}
	if !a.Required() || !strings.Contains(a.Source(), "new") {
		t.Errorf("first start: %+v", a)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != tok+"\n" {
		t.Fatalf("file holds %q (%v)", data, err)
	}
	wantPrivate(t, file)
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("left temp files behind: %v", entries)
	}

	// Reused on the next start, and a loosened file is tightened again.
	if runtime.GOOS != "windows" {
		if err := os.Chmod(file, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, err := LoadAuth(dir)
	if err != nil || b.Token() != tok || strings.Contains(b.Source(), "new") {
		t.Fatalf("second start: %v %+v", err, b)
	}
	wantPrivate(t, file)

	// The server uses <ConfigDir>/factory-token.
	s := newAuthServer(t, nil)
	if data, _ := os.ReadFile(filepath.Join(s.b.ConfigDir, tokenFileName)); strings.TrimSpace(string(data)) != s.Auth().Token() {
		t.Errorf("server token %q, file %q", s.Auth().Token(), data)
	}

	// FACTORY_TOKEN wins and leaves the file alone.
	t.Setenv("FACTORY_TOKEN", "  my-own-token-is-long-enough-0123 ")
	c, err := LoadAuth(dir)
	if err != nil || c.Token() != "my-own-token-is-long-enough-0123" {
		t.Fatalf("FACTORY_TOKEN: %v %+v", err, c)
	}
	if data, _ := os.ReadFile(file); string(data) != tok+"\n" {
		t.Errorf("FACTORY_TOKEN changed the file: %q", data)
	}
	t.Setenv("FACTORY_TOKEN", "")

	// An empty file gets a new token.
	if err := os.WriteFile(file, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := LoadAuth(dir)
	if err != nil || d.Token() == tok || len(d.Token()) != 64 {
		t.Fatalf("empty file: %v %+v", err, d)
	}
	if data, _ := os.ReadFile(file); strings.TrimSpace(string(data)) != d.Token() {
		t.Errorf("empty file not refilled: %q", data)
	}

	// A token someone typed that is too weak is an error, not quietly replaced.
	if err := os.WriteFile(file, []byte("hunter2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAuth(dir); err == nil || !strings.Contains(err.Error(), "config/factory-token") || !strings.Contains(err.Error(), "at least 24") {
		t.Errorf("short token in the file: %v", err)
	}
	if data, _ := os.ReadFile(file); string(data) != "hunter2\n" {
		t.Errorf("short token file was changed: %q", data)
	}
}

func TestTokenFileThatCantBeRead(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a Unix user that file permissions apply to")
	}
	t.Setenv("FACTORY_AUTH", "")
	t.Setenv("FACTORY_TOKEN", "")
	dir := t.TempDir()
	file := filepath.Join(dir, tokenFileName)
	if err := os.WriteFile(file, []byte(strings.Repeat("a", 64)+"\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	a, err := LoadAuth(dir)
	if err != nil || !a.Required() || len(a.Token()) != 64 || a.Token() == strings.Repeat("a", 64) || !strings.Contains(a.Source(), "one-off") {
		t.Fatalf("unreadable file: %v %+v", err, a)
	}
	if fi, _ := os.Stat(file); fi.Mode().Perm() != 0 {
		t.Errorf("unreadable file was touched: %v", fi.Mode())
	}
}

func TestAuthEnvironmentErrors(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct{ auth, token, wantErr string }{
		{"", "short", "at least 24"},
		{"", strings.Repeat("x", 23), "at least 24"},
		{"", "has a space but is long enough!!", "isn't allowed"},
		{"", "has;a;semicolon;and;is;long;enough", "isn't allowed"},
		{"", "has\"a\"quote\"and\"is\"long\"enough", "isn't allowed"},
		{"", "has,a,comma,and,is,long,enough!!", "isn't allowed"},
		{"", "non-ascii-é-token-that-is-long-enough", "isn't allowed"},
		{"maybe", "", `FACTORY_AUTH="maybe"`},
		{"false", "", "FACTORY_AUTH"},
		{"", strings.Repeat("x", 24), ""},
		{"on", "", ""},
		{"OFF", "", ""},
		{" off ", "short", ""}, // off wins: the token isn't used
	} {
		t.Setenv("FACTORY_AUTH", c.auth)
		t.Setenv("FACTORY_TOKEN", c.token)
		_, err := LoadAuth(dir)
		if c.wantErr == "" && err != nil || c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("FACTORY_AUTH=%q FACTORY_TOKEN=%q: err %v, want %q", c.auth, c.token, err, c.wantErr)
		}
	}
	// A bad FACTORY_TOKEN stops the server from starting.
	t.Setenv("FACTORY_AUTH", "")
	t.Setenv("FACTORY_TOKEN", "short")
	b, err := builder.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(b, &catalog.Catalog{}, "", ""); err == nil {
		t.Error("New started with a short FACTORY_TOKEN")
	}
}

func TestNilAuthRefusesProtectedRoutes(t *testing.T) {
	var a *Auth
	mux := newRouteMux()
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	wantAuthRequired(t, send(a.Wrap(mux), "GET", "/api/version", "", map[string]string{"Authorization": "Bearer x"}), "nil auth")
}

func wantPrivate(t *testing.T, file string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	fi, err := os.Stat(file)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("%s: mode %v (%v), want 0600", file, fi.Mode(), err)
	}
}
