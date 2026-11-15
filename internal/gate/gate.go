// Package gate is the front door shared by llmgate (the entrypoint of every
// llama.cpp model image) and llmpack (the launcher of a native package): an
// OpenAI-compatible reverse proxy to llama-server that injects a baked system
// prompt into chat requests and, when a harness is baked, runs the tool loop
// of internal/agent around them. It also assembles llama-server's command
// line (llama.go).
//
// Standard library only, plus the stdlib-only internal/agent: the model image
// compiles llmgate from these sources without network access (the builder
// stages them, see builder.stageGate).
package gate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/seanbrill/local-llm/internal/agent"
)

// MaxChatBody caps a chat-completions body read for prompt injection or the
// harness (images in vision requests make bodies large).
const MaxChatBody = agent.MaxRequestBytes

// Options configure a Gate.
type Options struct {
	// Upstream is the model server's base URL, e.g. "http://127.0.0.1:8081".
	Upstream string
	// SystemPrompt is the baked initialization prompt ("" = none).
	SystemPrompt string
	// InjectMode is "missing" (the default: only when the client sent no
	// system message) or "always" (replace the client's).
	InjectMode string
	// Harness runs the tool loop for chat requests (nil = no harness).
	Harness *agent.Agent
	// Host names the program in GET /v1/harness when there is no harness.
	Host string
	// UpstreamKey, when set, is sent to the upstream as a bearer token, on
	// proxied requests and the harness's alike. llmpack starts llama-server
	// with an API key so that only its gate can use it: a web page that finds
	// llama-server's loopback port gets 401.
	UpstreamKey string
}

// Gate is the reverse proxy. Everything but the chat-completions route and
// GET /v1/harness is passed through verbatim (SSE streaming included), so the
// gate stays a drop-in OpenAI-compatible server.
type Gate struct {
	proxy        *httputil.ReverseProxy
	systemPrompt string
	injectMode   string
	host         string
	harness      *agent.Agent   // nil: no harness
	upstream     agent.Upstream // the model's chat completions, for the harness
}

// New builds a Gate in front of o.Upstream.
func New(o Options) (*Gate, error) {
	base := strings.TrimRight(o.Upstream, "/")
	target, err := url.Parse(base)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return nil, fmt.Errorf("gate: bad upstream URL %q", o.Upstream)
	}
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.FlushInterval = -1 // flush immediately so SSE streaming isn't buffered
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		// Most common during the model-load window: upstream not up yet.
		http.Error(w, "upstream not ready: "+err.Error(), http.StatusBadGateway)
	}
	// The harness talks to the model server directly: no proxy settings and
	// no overall timeout (a long answer streams for minutes; the request's
	// context ends it).
	var tr http.RoundTripper = &http.Transport{Proxy: nil, MaxIdleConnsPerHost: 8, IdleConnTimeout: 90 * time.Second}
	if key := o.UpstreamKey; key != "" {
		direct := rp.Director
		rp.Director = func(r *http.Request) {
			direct(r)
			r.Header.Set("Authorization", "Bearer "+key)
		}
		tr = bearer{key: key, next: tr}
	}
	client := &http.Client{Transport: tr}
	return &Gate{
		proxy: rp, systemPrompt: o.SystemPrompt, injectMode: o.InjectMode, host: o.Host,
		harness: o.Harness, upstream: agent.HTTPUpstream(client, base+"/v1/chat/completions"),
	}, nil
}

// bearer adds the upstream's API key to the harness's requests.
type bearer struct {
	key  string
	next http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.key)
	return b.next.RoundTrip(r)
}

// IsChatPath reports whether a path is llama-server's chat-completions route
// (it serves it with and without the /v1 prefix).
func IsChatPath(p string) bool { return p == "/v1/chat/completions" || p == "/chat/completions" }

func (g *Gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/harness" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		g.serveHarnessInfo(w)
		return
	}
	if r.Method == http.MethodPost && IsChatPath(r.URL.Path) && (g.systemPrompt != "" || g.harness != nil) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxChatBody))
		r.Body.Close()
		if err != nil {
			http.Error(w, "request body too large or unreadable: "+err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		if g.systemPrompt != "" {
			body = InjectSystemPrompt(body, g.systemPrompt, g.injectMode)
		}
		if g.harness != nil {
			g.harness.ServeChat(w, r, body, g.upstream)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		r.Header.Set("Content-Length", strconv.Itoa(len(body)))
	}
	g.proxy.ServeHTTP(w, r)
}

// serveHarnessInfo answers GET /v1/harness: what the baked harness offers, so
// the factory (and any client) can tell this model runs its own tool loop.
func (g *Gate) serveHarnessInfo(w http.ResponseWriter) {
	info := agent.DisabledInfo(g.host)
	if g.harness != nil {
		info = g.harness.Info()
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(info)
}

// InjectSystemPrompt prepends (or, in "always" mode, replaces) the system
// message in a chat-completions request body. On any parse error it returns
// the original body unchanged so a malformed request still reaches the server.
func InjectSystemPrompt(body []byte, prompt, mode string) []byte {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	raw, ok := m["messages"].([]any)
	if !ok {
		return body
	}

	hasSystem := false
	for _, it := range raw {
		if msg, ok := it.(map[string]any); ok {
			if role, _ := msg["role"].(string); role == "system" {
				hasSystem = true
				break
			}
		}
	}

	sys := map[string]any{"role": "system", "content": prompt}
	switch mode {
	case "always":
		kept := make([]any, 0, len(raw))
		for _, it := range raw {
			if msg, ok := it.(map[string]any); ok {
				if role, _ := msg["role"].(string); role == "system" {
					continue // drop client-supplied system messages
				}
			}
			kept = append(kept, it)
		}
		m["messages"] = append([]any{sys}, kept...)
	default: // "missing": only inject when the client didn't send one
		if hasSystem {
			return body
		}
		m["messages"] = append([]any{sys}, raw...)
	}

	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// LoadHarness reads a baked harness config (plus the HARNESS_* overrides in
// getenv) and builds the agent; see NewHarness. It returns nil when there is
// no harness.
func LoadHarness(path string, getenv func(string) string, host string, logf func(string, ...any)) (*agent.Agent, error) {
	cfg, ok, err := agent.Load(path, getenv)
	if err != nil || !ok {
		return nil, err
	}
	return NewHarness(cfg, host, logf)
}

// NewHarness builds the agent for a validated harness config and logs what it
// offers: each tool that can't run here and why, then the tools that can. It
// returns nil when none of the configured tools can run, so the host serves
// without the harness instead of failing.
func NewHarness(cfg agent.Config, host string, logf func(string, ...any)) (*agent.Agent, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	h, err := agent.New(cfg, agent.Options{
		Host: host,
		Logf: func(format string, args ...any) { logf("[harness] "+format, args...) },
	})
	if err != nil {
		return nil, err
	}
	info := h.Info()
	for _, u := range info.Unavailable {
		logf("harness: %s is unavailable: %s", u.Tool, u.Reason)
	}
	var names []string
	for _, t := range info.Tools {
		names = append(names, t.Name)
	}
	if len(names) == 0 {
		logf("harness: none of the tools %v can run here; serving without the harness", cfg.Tools)
		return nil, nil
	}
	c := h.Config()
	logf("harness on: %d tools (%s), max %d steps, %s per tool", len(names), strings.Join(names, ", "),
		c.Limits.MaxSteps, c.Limits.ToolTimeout.D())
	return h, nil
}
