package agent

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// HostGuard keeps web pages out of a model container's API. Any site the user
// visits can make the browser send requests to a port on this computer; with
// a domain it controls resolving to 127.0.0.1 (DNS rebinding) it can even read
// the answers. Through a model or an ensemble that would let a page chat as
// the user, use the harness's tools (write into the workspace, fetch pages)
// or make an ensemble speak in a cloned voice.
//
// So a request must name this machine in its Host (an IP address, localhost or
// a *.localhost name, the container engines' host aliases, the container's own
// hostname, or a name listed in GATE_ALLOWED_HOSTS), which defeats rebinding,
// and a request that carries an Origin (only browsers send one) must come from
// a page on that same host or from an origin listed in GATE_ALLOWED_ORIGINS
// ("*" allows any). Programs that aren't browsers send no Origin and pass, so
// the factory, Open WebUI's server and curl are unaffected.
type HostGuard struct {
	next    http.Handler
	hosts   map[string]bool
	origins map[string]bool
}

// NewHostGuard wraps next. getenv reads GATE_ALLOWED_HOSTS and
// GATE_ALLOWED_ORIGINS (comma-separated); nil means os.Getenv.
func NewHostGuard(next http.Handler, getenv func(string) string) *HostGuard {
	if getenv == nil {
		getenv = os.Getenv
	}
	g := &HostGuard{next: next, hosts: map[string]bool{}, origins: map[string]bool{}}
	for _, h := range []string{"host.docker.internal", "host.containers.internal", "gateway.docker.internal"} {
		g.hosts[h] = true
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		g.hosts[normHost(h)] = true
	}
	for _, h := range splitList(getenv("GATE_ALLOWED_HOSTS")) {
		g.hosts[normHost(h)] = true
	}
	for _, o := range splitList(getenv("GATE_ALLOWED_ORIGINS")) {
		g.origins[strings.TrimRight(o, "/")] = true
	}
	return g
}

func (g *HostGuard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !g.hostOK(r.Host) {
		http.Error(w, "this server answers on this computer's own addresses only; add the name to GATE_ALLOWED_HOSTS to allow "+r.Host, http.StatusForbidden)
		return
	}
	if o := r.Header.Get("Origin"); o != "" && !g.originOK(o, r.Host) {
		http.Error(w, "requests from other web pages are refused; add the origin to GATE_ALLOWED_ORIGINS to allow "+o, http.StatusForbidden)
		return
	}
	g.next.ServeHTTP(w, r)
}

func (g *HostGuard) hostOK(hostport string) bool {
	h := hostport
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		h = host
	}
	h = normHost(strings.Trim(h, "[]"))
	switch {
	case h == "":
		return true // HTTP/1.0 without Host: no browser sends this
	case net.ParseIP(h) != nil:
		return true // an address can't be rebound
	case h == "localhost" || strings.HasSuffix(h, ".localhost"):
		return true
	}
	return g.hosts[h]
}

func (g *HostGuard) originOK(origin, host string) bool {
	if g.origins["*"] || g.origins[strings.TrimRight(origin, "/")] {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, host)
}

func normHost(h string) string { return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".") }

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
