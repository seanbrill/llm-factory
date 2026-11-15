package main

import (
	_ "embed"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/seanbrill/local-llm/internal/agent"
	"github.com/seanbrill/local-llm/internal/native"
)

//go:embed chat.html
var chatPage []byte

// handler serves the chat page and /v1/package, and passes everything else
// to the gate (the OpenAI API in front of llama-server), behind the browser
// guard.
func (l *launcher) handler(front http.Handler, h *agent.Agent, port int) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if (r.URL.Path == "/" || r.URL.Path == "/index.html") && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src 'self' data: blob:; connect-src 'self'; frame-ancestors 'none'")
			_, _ = w.Write(chatPage)
			return
		}
		front.ServeHTTP(w, r)
	})
	mux.HandleFunc("/v1/package", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(l.packageInfo(h))
	})
	origins := map[string]bool{}
	for _, o := range l.o.allowOrigins {
		origins[o] = true
	}
	return &guard{next: mux, loopbackOnly: isLoopback(l.o.host), origins: origins}
}

// packageInfo is GET /v1/package: what the chat page shows about the package.
type packageInfo struct {
	Object      string       `json:"object"` // "package"
	Name        string       `json:"name"`
	ModelID     string       `json:"model_id"`
	Modality    string       `json:"modality"`
	Target      string       `json:"target"`
	TargetLabel string       `json:"target_label"`
	Accel       string       `json:"accel"`
	AccelLabel  string       `json:"accel_label"`
	CtxSize     int          `json:"ctx_size"`
	GPULayers   string       `json:"gpu_layers"`
	LlamaCpp    string       `json:"llama_cpp"`
	Launcher    string       `json:"launcher"`
	License     string       `json:"license,omitempty"`
	SourceURL   string       `json:"source_url,omitempty"`
	Prompt      bool         `json:"system_prompt"`
	Ready       bool         `json:"ready"`
	LoadedInMS  int64        `json:"loaded_in_ms,omitempty"`
	Harness     harnessBrief `json:"harness"`
}

type harnessBrief struct {
	Enabled   bool     `json:"enabled"`
	Toolsets  []string `json:"toolsets"`
	Workspace string   `json:"workspace,omitempty"`
	Web       bool     `json:"web"` // web search or page reading: what the page warns leaves the computer
}

func (l *launcher) packageInfo(h *agent.Agent) packageInfo {
	p := packageInfo{
		Object: "package", Name: l.m.Name, ModelID: l.m.ModelID, Modality: l.engine.Modality,
		Target: l.target.ID, TargetLabel: l.target.Label, Accel: l.engine.Accel, AccelLabel: accelLabel(l.engine.Accel),
		CtxSize: l.llama.CtxSize, GPULayers: l.llama.GPULayers,
		LlamaCpp: native.LlamaCppRelease + " (" + native.LlamaCppBuild + ")", Launcher: versionLine(),
		License: l.m.License, SourceURL: l.m.SourceURL, Prompt: l.prompt != "",
		Ready: l.ready.Load(), LoadedInMS: l.loadedIn.Load(),
		Harness: harnessBrief{Toolsets: []string{}},
	}
	if h != nil {
		info := h.Info()
		p.Harness.Enabled = true
		p.Harness.Toolsets = info.Toolsets
		p.Harness.Workspace = info.Workspace
		for _, t := range info.Toolsets {
			if t == agent.ToolsetWebSearch || t == agent.ToolsetFetchURL {
				p.Harness.Web = true
			}
		}
	}
	return p
}

// guard keeps other web pages out. A page on any site can make the browser
// send requests to 127.0.0.1; without this, one could chat with the model and
// make it use its tools (write files into the workspace, fetch pages). So a
// request that carries an Origin must come from the chat page's own origin
// (or one allowed with --allow-origin), and, while the launcher listens on
// loopback only, the Host must be a loopback name, which also defeats DNS
// rebinding. Programs that aren't browsers send no Origin and pass.
type guard struct {
	next         http.Handler
	loopbackOnly bool
	origins      map[string]bool
}

func (g *guard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if g.loopbackOnly && !isLoopbackHostPort(r.Host) {
		http.Error(w, "this model answers at 127.0.0.1 or localhost only", http.StatusForbidden)
		return
	}
	if o := r.Header.Get("Origin"); o != "" && !g.originOK(o, r.Host) {
		http.Error(w, "requests from other web pages are refused; start the package with --allow-origin "+o+" to allow this one", http.StatusForbidden)
		return
	}
	g.next.ServeHTTP(w, r)
}

func (g *guard) originOK(origin, host string) bool {
	if g.origins["*"] || g.origins[strings.TrimRight(origin, "/")] {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" {
		return false
	}
	return strings.EqualFold(u.Host, host)
}

// isLoopback reports whether a listen address is loopback only.
func isLoopback(host string) bool {
	h := strings.Trim(host, "[]")
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// isLoopbackHostPort reports whether a Host header names this computer.
func isLoopbackHostPort(hostport string) bool {
	h := hostport
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		h = host
	}
	return isLoopback(h)
}

// browseHost is the host name to open in a browser for a listen address.
func browseHost(host string) string {
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		return "127.0.0.1"
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return "[" + host + "]"
	}
	return host
}
