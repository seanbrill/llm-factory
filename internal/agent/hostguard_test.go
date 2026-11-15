package agent

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHostGuard(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	env := map[string]string{"GATE_ALLOWED_HOSTS": "mymac.lan", "GATE_ALLOWED_ORIGINS": "http://localhost:3000"}
	g := NewHostGuard(ok, func(k string) string { return env[k] })

	cases := []struct {
		name, host, origin string
		want               int
	}{
		{"loopback ip", "127.0.0.1:8123", "", 200},
		{"ipv6 loopback", "[::1]:8123", "", 200},
		{"lan ip", "192.168.1.20:8123", "", 200},
		{"localhost", "localhost:8123", "", 200},
		{"caddy name", "marvin.localhost", "", 200},
		{"docker alias", "host.docker.internal:8123", "", 200},
		{"podman alias", "host.containers.internal:8123", "", 200},
		{"allowed by env", "MyMac.lan:8123", "", 200},
		{"rebinding domain", "evil.example.com:8123", "", 403},
		{"rebinding with origin", "evil.example.com:8123", "http://evil.example.com:8123", 403},
		{"cross-site page", "127.0.0.1:8123", "https://evil.example.com", 403},
		{"null origin", "127.0.0.1:8123", "null", 403},
		{"same-origin page", "marvin.localhost", "http://marvin.localhost", 200},
		{"allowed origin", "127.0.0.1:8123", "http://localhost:3000", 200},
		{"origin port differs", "127.0.0.1:8123", "http://127.0.0.1:9000", 403},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			r.Host = c.host
			if c.origin != "" {
				r.Header.Set("Origin", c.origin)
			}
			w := httptest.NewRecorder()
			g.ServeHTTP(w, r)
			if w.Code != c.want {
				t.Fatalf("host %q origin %q: got %d, want %d (%s)", c.host, c.origin, w.Code, c.want, w.Body.String())
			}
		})
	}
}

func TestHostGuardAnyOrigin(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	g := NewHostGuard(ok, func(k string) string {
		if k == "GATE_ALLOWED_ORIGINS" {
			return "*"
		}
		return ""
	})
	r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	r.Host = "127.0.0.1:8123"
	r.Header.Set("Origin", "https://anything.example")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("GATE_ALLOWED_ORIGINS=* should allow any origin, got %d", w.Code)
	}
	// "*" opens origins, never hosts: rebinding stays blocked.
	r.Host = "evil.example.com"
	w = httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("a foreign Host must stay blocked, got %d", w.Code)
	}
}
