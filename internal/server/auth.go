package server

// The factory access token.
//
// The factory API builds, runs and deletes containers, and some containers it
// starts get the engine socket, so it answers only callers that hold a secret.
// Publishing the port on loopback isn't enough: the harness's workspace
// terminal runs whatever commands a model chooses, and from inside that
// container host.docker.internal:<port> reaches the factory. Network rules
// can't reliably close that path (gateway IPs, the default bridge, Podman), so
// the API asks for a token the terminal never sees.
//
// The token is config/factory-token (random, 0600, created on first start) or
// FACTORY_TOKEN; FACTORY_AUTH=off turns the check off. A browser gets the token
// once, from the access link the start scripts print (/?token=…) or by pasting
// it on the UI's lock screen, and then keeps it as an HttpOnly, SameSite=Strict
// cookie. Programs send it as "Authorization: Bearer …" or X-Factory-Token.
// The model gateway (/v1) keeps its own key. See docs/HARNESS.md › Security
// model.

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	tokenFileName   = "factory-token" // in config/
	authCookieName  = "llmf_token"
	authHeaderName  = "X-Factory-Token"
	authCookieAge   = 34560000 // 400 days, the longest a browser keeps a cookie
	minTokenLength  = 24
	maxLoginFails   = 5           // wrong tokens at the login within loginFailWindow
	loginFailWindow = time.Minute // before it answers 429 (globally, not per client)

	authStatusRoute = "/api/auth/status"
	authLoginRoute  = "/api/auth/login"
	authLogoutRoute = "/api/auth/logout"
	gatewayRoute    = "/v1/"

	authRequiredMsg = "This factory needs its access link: open the link start.sh printed, or paste the token from config/factory-token."
	badTokenMsg     = "That token isn't right."
	rateLimitedMsg  = "Too many tries — wait a minute."
)

// publicRoutes are the mux patterns that answer without the token: the UI's
// static files (and the access link), the health probes, and the two calls the
// lock screen makes before it has the token. The model gateway (gatewayRoute)
// checks its own key. Every other route needs the token, so a route added
// later is protected unless it is listed here.
var publicRoutes = map[string]bool{
	"/":             true, // static UI files (index.html, /assets/…), and /?token=…
	"/auth":         true, // the access link's other spelling
	"/healthz":      true, // liveness probe (the image's HEALTHCHECK)
	"/readyz":       true, // readiness probe
	authStatusRoute: true,
	authLoginRoute:  true,
}

// Auth checks the factory access token.
type Auth struct {
	required bool
	token    string
	sum      [sha256.Size]byte // of token: requests are compared against it in constant time
	source   string            // where the token came from, for the startup log

	mu    sync.Mutex
	fails []time.Time // recent wrong tokens at the login, for its rate limit
	now   func() time.Time
}

// LoadAuth sets up the token from the environment and configDir:
//
//	FACTORY_AUTH=off  no token: the API answers anyone who can reach the port
//	FACTORY_TOKEN=…   this token (24+ characters); the token file is left alone
//	otherwise         <configDir>/factory-token, created on first start
func LoadAuth(configDir string) (*Auth, error) {
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv("FACTORY_AUTH"))); v {
	case "off":
		return &Auth{source: "FACTORY_AUTH=off", now: time.Now}, nil
	case "", "on":
	default:
		return nil, fmt.Errorf("FACTORY_AUTH=%q isn't understood: set FACTORY_AUTH=off to turn the access token off, or leave it unset", v)
	}
	if tok := strings.TrimSpace(os.Getenv("FACTORY_TOKEN")); tok != "" {
		if err := checkToken(tok); err != nil {
			return nil, fmt.Errorf("FACTORY_TOKEN %v", err)
		}
		return newAuth(tok, "from FACTORY_TOKEN"), nil
	}

	file := filepath.Join(configDir, tokenFileName)
	shown := filepath.ToSlash(filepath.Join(filepath.Base(configDir), tokenFileName)) // config/factory-token
	data, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		// Unreadable (another user's file, say): leave it alone and run with a
		// token of our own, which only this run's log shows.
		log.Printf("auth: can't read %s (%v); this run uses a one-off token", shown, err)
		return oneOffAuth(shown + " can't be read")
	}
	if tok := strings.TrimSpace(string(data)); tok != "" {
		if err := checkToken(tok); err != nil {
			return nil, fmt.Errorf("the token in %s %v; fix it, or delete the file so the factory makes a new one", shown, err)
		}
		// Tighten a hand-made or loosened file (a symlink is left as it is).
		if fi, err := os.Lstat(file); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm() != 0o600 {
			_ = os.Chmod(file, 0o600)
		}
		return newAuth(tok, "saved in "+shown), nil
	}
	// Missing (or empty): make one.
	tok, err := newToken()
	if err != nil {
		return nil, err
	}
	if err := writeTokenFile(file, tok); err != nil {
		log.Printf("auth: can't save %s (%v); this run uses a one-off token", shown, err)
		return newAuth(tok, "one-off ("+shown+" can't be saved)"), nil
	}
	return newAuth(tok, "new, saved in "+shown), nil
}

func newAuth(tok, source string) *Auth {
	return &Auth{required: true, token: tok, sum: sha256.Sum256([]byte(tok)), source: source, now: time.Now}
}

func oneOffAuth(why string) (*Auth, error) {
	tok, err := newToken()
	if err != nil {
		return nil, err
	}
	return newAuth(tok, "one-off ("+why+")"), nil
}

// newToken returns 32 random bytes from crypto/rand as 64 hex characters (safe
// in a URL, a cookie and a shell, and one double-click selects it all).
func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate the access token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// checkToken vets a token someone chose: long enough to resist guessing, and
// only characters that survive a cookie and a URL query unchanged.
func checkToken(tok string) error {
	if len(tok) < minTokenLength {
		return fmt.Errorf("is too short (%d characters; use at least %d)", len(tok), minTokenLength)
	}
	for i := 0; i < len(tok); i++ {
		if c := tok[i]; c <= ' ' || c >= 0x7f || c == '"' || c == ',' || c == ';' || c == '\\' {
			return errors.New("has a character that isn't allowed (use printable ASCII without spaces, quotes, commas, semicolons or backslashes)")
		}
	}
	return nil
}

// writeTokenFile writes the token atomically, so a reader (the start script
// waiting for it) never sees half a token, with owner-only permissions.
func writeTokenFile(file, tok string) error {
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".factory-token-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after the rename
	if err = f.Chmod(0o600); err == nil {
		if _, err = f.WriteString(tok + "\n"); err == nil {
			err = f.Sync()
		}
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	giveToDirOwner(tmp, dir)
	return os.Rename(tmp, file)
}

// Required reports whether the API needs the token (false with FACTORY_AUTH=off).
func (a *Auth) Required() bool { return a.required }

// Token is the access token ("" when auth is off).
func (a *Auth) Token() string { return a.token }

// Source says where the token came from, for the startup log.
func (a *Auth) Source() string { return a.source }

// match reports, in constant time, whether got is the token.
func (a *Auth) match(got string) bool {
	if !a.required || got == "" {
		return false
	}
	sum := sha256.Sum256([]byte(got))
	return subtle.ConstantTimeCompare(sum[:], a.sum[:]) == 1
}

// credential is how a request carries the token.
type credential int

const (
	noCredential credential = iota
	viaCookie
	viaHeader // Authorization: Bearer, or X-Factory-Token
)

// presented reports how r carries the token, if it does. The headers come
// first: unlike the cookie, a page from another site can't add them.
func (a *Auth) presented(r *http.Request) credential {
	if a == nil || !a.required {
		return noCredential
	}
	if a.match(r.Header.Get(authHeaderName)) {
		return viaHeader
	}
	if tok, ok := bearerToken(r); ok && a.match(tok) {
		return viaHeader
	}
	for _, c := range r.Cookies() { // every llmf_token: a stale one may sit beside the right one
		if c.Name == authCookieName && a.match(c.Value) {
			return viaCookie
		}
	}
	return noCredential
}

// Authorized reports whether r may use the protected routes: auth is off, or r
// carries the token.
func (a *Auth) Authorized(r *http.Request) bool {
	if a != nil && !a.required {
		return true
	}
	return a.presented(r) != noCredential
}

func bearerToken(r *http.Request) (string, bool) {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	const prefix = "bearer "
	if len(auth) <= len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(auth[len(prefix):]), true
}

// router is what the token check needs from the mux: the route a request
// would reach, and the mux itself to serve it.
type router interface {
	http.Handler
	Handler(r *http.Request) (h http.Handler, pattern string)
}

// Wrap puts the token check in front of mux. Which requests need the token
// follows the routing table, so a route added later is protected unless
// publicRoutes lists it. A nil *Auth refuses every protected request.
func (a *Auth) Wrap(mux router) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := mux.Handler(r); needsToken(r.URL.Path, pattern) && !a.allow(w, r) {
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// needsToken reports whether a request for urlPath, which the mux routes to
// pattern, must carry the token. Anything under /api/ does, except the lock
// screen's two calls, whatever the mux would do with it.
func needsToken(urlPath, pattern string) bool {
	if isAPIPath(urlPath) {
		return pattern != authStatusRoute && pattern != authLoginRoute
	}
	return pattern != gatewayRoute && !publicRoutes[pattern]
}

// isAPIPath reports whether a path is under /api, as sent or once cleaned
// ("/x/../api/run" is redirected to /api/run, but needs the token either way).
func isAPIPath(p string) bool {
	for _, q := range []string{p, path.Clean("/" + p)} {
		if q == "/api" || strings.HasPrefix(q, "/api/") {
			return true
		}
	}
	return false
}

// allow checks a request for a protected route and answers it when refused.
func (a *Auth) allow(w http.ResponseWriter, r *http.Request) bool {
	if a != nil && !a.required {
		return true
	}
	switch a.presented(r) {
	case viaHeader:
		return true
	case viaCookie:
		// The browser sends the cookie with any same-site request, and every
		// port on this host (Open WebUI, the workspace terminal) is the same
		// site, so a cookie-only call that changes something must come from
		// the UI's own origin.
		if r.Method == http.MethodGet || r.Method == http.MethodHead || !crossSite(r) {
			return true
		}
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-site requests are refused", "code": "cross_site"})
		return false
	}
	w.Header().Set("WWW-Authenticate", `Bearer realm="llm-factory"`)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusUnauthorized, map[string]string{"error": authRequiredMsg, "code": "auth_required"})
	return false
}

// crossSite reports a browser request made by another origin (browsers too
// old for Sec-Fetch-Site pass).
func crossSite(r *http.Request) bool {
	site := r.Header.Get("Sec-Fetch-Site")
	return site != "" && site != "same-origin" && site != "none"
}

func sessionCookie(r *http.Request, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: authCookieName, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: r.TLS != nil, // a plain-HTTP localhost can't keep a Secure cookie
	}
}

// serveUI serves the static UI. The access link (/?token=… or /auth?token=…)
// trades a valid token for the cookie and redirects to "/", taking the token
// out of the address bar. Anything else, a wrong token included, gets the UI,
// which shows its lock screen until the browser is signed in.
func (a *Auth) serveUI(web http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		link := r.URL.Path == "/" || r.URL.Path == "/auth"
		if link && (r.Method == http.MethodGet || r.Method == http.MethodHead) && r.URL.Query().Has("token") {
			// Keep the token out of Referer headers and caches either way.
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().Set("Cache-Control", "no-store")
			if a.match(strings.TrimSpace(r.URL.Query().Get("token"))) {
				http.SetCookie(w, sessionCookie(r, a.token, authCookieAge))
				http.Redirect(w, r, "/", http.StatusFound)
				return
			}
		}
		if r.URL.Path == "/auth" { // the UI itself lives at "/"
			r = r.Clone(r.Context())
			r.URL.Path, r.URL.RawPath = "/", ""
		}
		web.ServeHTTP(w, r)
	})
}

type authState struct {
	Authed   bool `json:"authed"`
	Required bool `json:"required"`
}

// handleStatus answers GET /api/auth/status (public): whether this browser is
// signed in, and whether it needs to be.
func (a *Auth) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, authState{Authed: a.Authorized(r), Required: a.required})
}

// handleLogin answers POST /api/auth/login {"token": "…"} (public): the lock
// screen's paste-the-token sign-in. Only JSON is accepted, which a page from
// another site can't send without a CORS preflight the factory never grants.
func (a *Auth) handleLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "send JSON (Content-Type: application/json)"})
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if !a.required {
		writeJSON(w, http.StatusOK, map[string]bool{"authed": true})
		return
	}
	ok, wait := a.tryLogin(strings.TrimSpace(req.Token))
	switch {
	case ok:
		http.SetCookie(w, sessionCookie(r, a.token, authCookieAge))
		writeJSON(w, http.StatusOK, map[string]bool{"authed": true})
	case wait > 0:
		w.Header().Set("Retry-After", strconv.Itoa(int((wait+time.Second-1)/time.Second)))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": rateLimitedMsg, "code": "rate_limited"})
	default:
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": badTokenMsg, "code": "bad_token"})
	}
}

// tryLogin checks a pasted token under the login's rate limit: once
// maxLoginFails wrong tokens fall within loginFailWindow it refuses, without
// looking, until the oldest of them is a window old. It returns ok, or how
// long to wait (0 when the token was simply wrong).
func (a *Auth) tryLogin(tok string) (ok bool, wait time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	recent := a.fails[:0]
	for _, t := range a.fails {
		if now.Sub(t) < loginFailWindow {
			recent = append(recent, t)
		}
	}
	a.fails = recent
	if len(a.fails) >= maxLoginFails {
		return false, loginFailWindow - now.Sub(a.fails[0])
	}
	if a.match(tok) {
		return true, 0
	}
	a.fails = append(a.fails, now)
	if len(a.fails) == maxLoginFails {
		log.Printf("auth: %d wrong tokens at %s within %s; refusing sign-ins there for up to %s", maxLoginFails, authLoginRoute, loginFailWindow, loginFailWindow)
	}
	return false, 0
}

// handleLogout answers POST /api/auth/logout by clearing the cookie.
func (a *Auth) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	http.SetCookie(w, sessionCookie(r, "", -1))
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]bool{"authed": !a.required})
}
