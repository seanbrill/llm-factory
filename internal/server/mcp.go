package server

// MCP servers: the factory's registry (config/harness.json), the Test
// button, what a run passes to a model's harness, and the calls that change
// the MCP servers of a running model (its harness's admin API). Header
// values and URL paths never leave the factory: the UI gets masked views.
//
//	GET    /api/harness/mcp                {servers, many_tools, targets}
//	POST   /api/harness/mcp                {name, url, headers} -> {server, test}
//	PUT    /api/harness/mcp/{name}         {url?, headers?, tools?} -> {server}
//	DELETE /api/harness/mcp/{name}
//	POST   /api/harness/mcp/{name}/test    -> {ok, tools, total_tokens, error, server}
//
//	GET    /api/containers/mcp?id=&engine=           the model's MCP servers
//	POST   /api/containers/mcp/add  {id, engine, server: "<name>" | {name, url, headers}, tools?}
//	PATCH  /api/containers/mcp      {id, engine, name, tools? | enabled?}
//	DELETE /api/containers/mcp      {id, engine, name}
//	POST   /api/containers/mcp/test {id, engine, name}

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/seanbrill/local-llm/internal/agent"
	"github.com/seanbrill/local-llm/internal/builder"
	"github.com/seanbrill/local-llm/internal/harness"
)

func (h *harnessAPI) mountMCP(mux interface {
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}) {
	mux.HandleFunc("/api/harness/mcp", guardedAny(h.handleMCP, http.MethodGet, http.MethodPost))
	mux.HandleFunc("/api/harness/mcp/", guardedAny(h.handleMCPItem, http.MethodPut, http.MethodDelete, http.MethodPost))
	mux.HandleFunc("/api/containers/mcp", guardedAny(h.handleContainerMCP, http.MethodGet, http.MethodPatch, http.MethodDelete))
	mux.HandleFunc("/api/containers/mcp/add", guardedAny(h.handleContainerMCPAdd, http.MethodPost))
	mux.HandleFunc("/api/containers/mcp/test", guardedAny(h.handleContainerMCPTest, http.MethodPost))
}

// guardedAny is guarded for several methods.
func guardedAny(next http.HandlerFunc, methods ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ok := false
		for _, m := range methods {
			if r.Method == m || (m == http.MethodGet && r.Method == http.MethodHead) {
				ok = true
			}
		}
		if !ok {
			w.Header().Set("Allow", strings.Join(methods, ", "))
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": strings.Join(methods, " or ") + " only"})
			return
		}
		if err := checkLocalRequest(r); err != nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
			return
		}
		next(w, r)
	}
}

func jsonErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// mcpVia is how the factory itself reaches a loopback MCP URL: as is when it
// runs on the host, through the host alias from its container.
func (h *harnessAPI) mcpVia() string {
	if isLoopbackName(h.s.modelHost) {
		return ""
	}
	return h.s.modelHost
}

// mcpTarget is a running model whose harness can take MCP servers.
type mcpTarget struct {
	ID     string `json:"id"`
	Engine string `json:"engine"`
	Name   string `json:"name"`
	Port   int    `json:"port"`
	Ref    string `json:"ref"`
	Kind   string `json:"kind"`  // model | ensemble
	Admin  bool   `json:"admin"` // the factory holds its admin token
}

func (h *harnessAPI) mcpTargets(ctx context.Context) []mcpTarget {
	out := []mcpTarget{}
	rows, err := h.containers(ctx)
	if err != nil {
		return out
	}
	for _, row := range rows {
		if t, ok := h.targetOf(row); ok {
			out = append(out, t)
		}
	}
	return out
}

func (h *harnessAPI) targetOf(row map[string]any) (mcpTarget, bool) {
	if !harness.RowRunning(row) {
		return mcpTarget{}, false
	}
	kind := "model"
	if harness.RowLabel(row, "local-llm.kind") == "ensemble" {
		kind = "ensemble"
	} else if harness.RowLabel(row, builder.LabelHarness) != "1" {
		return mcpTarget{}, false
	}
	name, _ := row["Names"].(string)
	name = strings.TrimPrefix(strings.Split(name, ",")[0], "/")
	id, _ := row["ID"].(string)
	eng, _ := row["Engine"].(string)
	ref, _ := row["Image"].(string)
	t := mcpTarget{ID: id, Engine: normalizeEngine(eng), Name: name, Port: harness.RowPort(row), Ref: ref, Kind: kind}
	t.Admin = h.s.runTokens().Get(name) != ""
	return t, t.Port > 0
}

// ---- registry ---------------------------------------------------------------------

func (h *harnessAPI) handleMCP(w http.ResponseWriter, r *http.Request) {
	if !h.storeOK(w) {
		return
	}
	if r.Method != http.MethodPost {
		cfg := h.store.Get()
		views := make([]harness.MCPView, 0, len(cfg.MCP))
		for _, s := range cfg.MCP {
			views = append(views, s.View())
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		writeJSON(w, http.StatusOK, map[string]any{"servers": views, "many_tools": agent.ManyMCPTools, "targets": h.mcpTargets(ctx)})
		return
	}
	var req struct {
		Name    string            `json:"name"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s := harness.MCPServer{Name: strings.TrimSpace(req.Name), URL: strings.TrimSpace(req.URL), Headers: cleanHeaders(req.Headers)}
	if err := harness.ValidateMCPServer(s); err != nil {
		jsonErr(w, http.StatusBadRequest, redactMCP(err.Error(), s))
		return
	}
	if _, exists := h.store.Get().FindMCP(s.Name); exists {
		jsonErr(w, http.StatusConflict, "there is an MCP server named "+s.Name+" already")
		return
	}
	test := h.runTest(r.Context(), &s)
	cfg, err := h.store.Update(func(c *harness.Config) error {
		if _, exists := c.FindMCP(s.Name); exists {
			return errors.New("there is an MCP server named " + s.Name + " already")
		}
		c.MCP = append(c.MCP, s)
		return nil
	})
	if err != nil {
		jsonErr(w, http.StatusConflict, err.Error())
		return
	}
	saved, _ := cfg.FindMCP(s.Name)
	test["server"] = saved.View()
	log.Printf("harness: MCP server %s added (%s)", s.Name, agent.RedactURL(s.URL))
	writeJSON(w, http.StatusOK, map[string]any{"server": saved.View(), "test": test})
}

func cleanHeaders(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		if k = strings.TrimSpace(k); k != "" {
			out[k] = strings.TrimSpace(v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func redactMCP(text string, s harness.MCPServer) string {
	u, _ := url.Parse(s.URL)
	secrets := []string{s.URL}
	if u != nil && len(u.Path) > 1 {
		secrets = append(secrets, u.Path, u.EscapedPath())
	}
	if u != nil && u.RawQuery != "" {
		secrets = append(secrets, u.RawQuery)
	}
	for _, v := range s.Headers {
		secrets = append(secrets, v)
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, sec := range secrets {
		if len(sec) >= 4 {
			text = strings.ReplaceAll(text, sec, "…")
		}
	}
	return text
}

// runTest probes the server and records the result on s.
func (h *harnessAPI) runTest(ctx context.Context, s *harness.MCPServer) map[string]any {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	tools, err := agent.ProbeMCP(ctx, s.Agent(h.mcpVia()))
	s.ApplyTest(tools, err, time.Now().UTC().Format(time.RFC3339))
	out := map[string]any{"ok": err == nil, "tools": s.Catalog, "total_tokens": 0}
	if err != nil {
		out["error"] = redactMCP(err.Error(), *s)
		out["tools"] = []harness.MCPTool{}
		log.Printf("harness: MCP server %s (%s) test failed: %s", s.Name, agent.RedactURL(s.URL), redactMCP(err.Error(), *s))
		return out
	}
	total := 0
	for _, t := range s.Catalog {
		total += t.Tokens
	}
	out["total_tokens"] = total
	return out
}

func (h *harnessAPI) handleMCPItem(w http.ResponseWriter, r *http.Request) {
	if !h.storeOK(w) {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/harness/mcp/")
	name, action, _ := strings.Cut(rest, "/")
	if n, err := url.PathUnescape(name); err == nil {
		name = n
	}
	cur, ok := h.store.Get().FindMCP(name)
	if !ok {
		jsonErr(w, http.StatusNotFound, "there is no MCP server named "+name)
		return
	}
	switch {
	case action == "test" && r.Method == http.MethodPost:
		test := h.runTest(r.Context(), &cur)
		cfg, err := h.store.Update(func(c *harness.Config) error {
			for i := range c.MCP {
				if c.MCP[i].Name == name {
					c.MCP[i].Catalog, c.MCP[i].TestedAt, c.MCP[i].Error = cur.Catalog, cur.TestedAt, cur.Error
					c.MCP[i].Tools = cur.Tools
				}
			}
			return nil
		})
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if s, ok := cfg.FindMCP(name); ok {
			test["server"] = s.View()
		}
		writeJSON(w, http.StatusOK, test)
	case action == "" && r.Method == http.MethodDelete:
		if _, err := h.store.Update(func(c *harness.Config) error {
			kept := c.MCP[:0]
			for _, s := range c.MCP {
				if s.Name != name {
					kept = append(kept, s)
				}
			}
			c.MCP = kept
			return nil
		}); err != nil {
			jsonErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		log.Printf("harness: MCP server %s removed", name)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	case action == "" && r.Method == http.MethodPut:
		var raw map[string]json.RawMessage
		if err := decodeBody(r, &raw); err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
		next := cur
		if v, ok := raw["url"]; ok {
			var u string
			if json.Unmarshal(v, &u) != nil {
				jsonErr(w, http.StatusBadRequest, "url must be text")
				return
			}
			if u = strings.TrimSpace(u); u != cur.URL {
				next.URL, next.Catalog, next.TestedAt, next.Error = u, nil, "", ""
			}
		}
		if v, ok := raw["headers"]; ok {
			var hs map[string]string
			if json.Unmarshal(v, &hs) != nil {
				jsonErr(w, http.StatusBadRequest, "headers must be an object of text values")
				return
			}
			hs = cleanHeaders(hs)
			for k, val := range hs {
				if val == agent.MaskedValue { // unchanged: keep the stored value
					if old, ok := cur.Headers[k]; ok {
						hs[k] = old
					} else {
						delete(hs, k)
					}
				}
			}
			next.Headers = hs
		}
		if v, ok := raw["tools"]; ok {
			if string(v) == "null" {
				next.Tools = nil
			} else {
				var ts []string
				if json.Unmarshal(v, &ts) != nil {
					jsonErr(w, http.StatusBadRequest, "tools must be a list of tool names or null (all)")
					return
				}
				if ts == nil {
					ts = []string{}
				}
				next.Tools = ts
			}
			next.ToolsChosen = true
		}
		if err := harness.ValidateMCPServer(next); err != nil {
			jsonErr(w, http.StatusBadRequest, redactMCP(err.Error(), next))
			return
		}
		cfg, err := h.store.Update(func(c *harness.Config) error {
			for i := range c.MCP {
				if c.MCP[i].Name == name {
					c.MCP[i] = next
				}
			}
			return nil
		})
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		s, _ := cfg.FindMCP(name)
		writeJSON(w, http.StatusOK, map[string]any{"server": s.View()})
	default:
		jsonErr(w, http.StatusMethodNotAllowed, "PUT or DELETE /api/harness/mcp/{name}, POST /api/harness/mcp/{name}/test")
	}
}

// ---- runs ---------------------------------------------------------------------------

var (
	tokenStoresMu sync.Mutex
	tokenStores   = map[string]*harness.TokenStore{}
)

// runTokens holds the admin token of each running harness the factory
// started (config/run-tokens.json, 0600).
func (s *Server) runTokens() *harness.TokenStore {
	p := filepath.Join(s.b.ConfigDir, "run-tokens.json")
	tokenStoresMu.Lock()
	defer tokenStoresMu.Unlock()
	if t, ok := tokenStores[p]; ok {
		return t
	}
	t := harness.NewTokenStore(p)
	tokenStores[p] = t
	return t
}

// runHarness is what a run gives an image's harness: an admin token (so the
// factory can add MCP servers while it runs) and the MCP servers picked for
// the run. Only images with a baked harness and ensembles have a harness.
// The returned notes are for the run's response.
func (s *Server) runHarness(labels map[string]string, names []string, engine string) (*builder.RunHarness, []string, error) {
	baked := builder.BakedHarness(labels) != nil || labels["local-llm.kind"] == "ensemble"
	if !baked {
		if len(names) > 0 {
			return nil, nil, errors.New("this image has no agent harness, so it can't use MCP servers; rebuild it with tools on")
		}
		return nil, nil, nil
	}
	tok, err := harness.NewAdminToken()
	if err != nil {
		return nil, nil, err
	}
	rh := &builder.RunHarness{AdminToken: tok}
	if len(names) == 0 {
		return rh, nil, nil
	}
	var cfg harness.Config
	if s.hapi != nil && s.hapi.store != nil {
		cfg = s.hapi.store.Get()
	}
	var notes []string
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			continue
		}
		seen[n] = true
		m, ok := cfg.FindMCP(n)
		if !ok {
			return nil, nil, fmt.Errorf("there is no MCP server named %q on the Harness page", n)
		}
		if m.Tools != nil && len(m.Tools) == 0 {
			notes = append(notes, fmt.Sprintf("MCP server %s has no tools picked, so it was left out; pick its tools on the Harness page", n))
			continue
		}
		rh.MCP = append(rh.MCP, m.Agent(builder.HostAlias(engine)))
		if note := harness.LoopbackNote(m.URL, engine); note != "" {
			notes = append(notes, note)
		}
	}
	return rh, notes, nil
}

// rememberRun stores the admin token of a container just started.
func (s *Server) rememberRun(port int, rh *builder.RunHarness) {
	if rh == nil || rh.AdminToken == "" {
		return
	}
	if port == 0 {
		port = 8080
	}
	if err := s.runTokens().Set(fmt.Sprintf("localllm-%d", port), rh.AdminToken); err != nil {
		log.Printf("harness: can't save the run's admin token: %v", err)
	}
}

func mcpNames(rh *builder.RunHarness) []string {
	if rh == nil {
		return nil
	}
	out := []string{}
	for _, m := range rh.MCP {
		out = append(out, m.Name)
	}
	return out
}

// ---- a running model's MCP servers ----------------------------------------------------

// gateCall calls a running model's harness admin API.
func (h *harnessAPI) gateCall(ctx context.Context, id, engine, method, suffix string, body any) (int, []byte, error) {
	rows, err := h.containers(ctx)
	if err != nil {
		return 0, nil, errors.New(engineMessage(err))
	}
	var target mcpTarget
	found := false
	for _, row := range rows {
		rid, _ := row["ID"].(string)
		name, _ := row["Names"].(string)
		eng, _ := row["Engine"].(string)
		if id == "" || (!strings.HasPrefix(rid, id) && !strings.HasPrefix(id, rid) && strings.TrimPrefix(name, "/") != id) {
			continue
		}
		if engine != "" && normalizeEngine(eng) != normalizeEngine(engine) {
			continue
		}
		t, ok := h.targetOf(row)
		if !ok {
			return 0, nil, errors.New("that container isn't a running model with an agent harness")
		}
		target, found = t, true
		break
	}
	if !found {
		return 0, nil, errors.New("no running container " + id)
	}
	tok := h.s.runTokens().Get(target.Name)
	if tok == "" {
		return 0, nil, errors.New("the factory doesn't hold this model's admin token (it was started before MCP support, or by hand); run it again from the Library, or use /mcp commands in its chat")
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.s.modelURL(target.Port, agent.AdminPath+suffix), rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 45 * time.Second, Transport: &http.Transport{Proxy: nil}}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("the model on port %d isn't answering", target.Port)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, b, nil
}

func relay(w http.ResponseWriter, status int, b []byte, err error) {
	if err != nil {
		jsonErr(w, http.StatusBadGateway, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

type containerMCPReq struct {
	ID      string          `json:"id"`
	Engine  string          `json:"engine"`
	Name    string          `json:"name"`
	Server  json.RawMessage `json:"server"`
	Tools   json.RawMessage `json:"tools"`
	Enabled *bool           `json:"enabled"`
}

func (h *harnessAPI) handleContainerMCP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		q := r.URL.Query()
		st, b, err := h.gateCall(ctx, q.Get("id"), q.Get("engine"), http.MethodGet, "", nil)
		relay(w, st, b, err)
		return
	}
	var req containerMCPReq
	if err := decodeBody(r, &req); err != nil || req.Name == "" {
		jsonErr(w, http.StatusBadRequest, "send {id, engine, name, ...}")
		return
	}
	suffix := "/" + url.PathEscape(req.Name)
	if r.Method == http.MethodDelete {
		st, b, err := h.gateCall(ctx, req.ID, req.Engine, http.MethodDelete, suffix, nil)
		relay(w, st, b, err)
		return
	}
	patch := map[string]any{}
	if len(req.Tools) > 0 {
		patch["tools"] = req.Tools
	}
	if req.Enabled != nil {
		patch["enabled"] = *req.Enabled
	}
	st, b, err := h.gateCall(ctx, req.ID, req.Engine, http.MethodPatch, suffix, patch)
	relay(w, st, b, err)
}

func (h *harnessAPI) handleContainerMCPTest(w http.ResponseWriter, r *http.Request) {
	var req containerMCPReq
	if err := decodeBody(r, &req); err != nil || req.Name == "" {
		jsonErr(w, http.StatusBadRequest, "send {id, engine, name}")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	st, b, err := h.gateCall(ctx, req.ID, req.Engine, http.MethodPost, "/"+url.PathEscape(req.Name)+"/test", map[string]any{})
	relay(w, st, b, err)
}

// handleContainerMCPAdd connects a registered server (by name) or a new one
// ({name, url, headers}) to a running model.
func (h *harnessAPI) handleContainerMCPAdd(w http.ResponseWriter, r *http.Request) {
	if !h.storeOK(w) {
		return
	}
	var req containerMCPReq
	if err := decodeBody(r, &req); err != nil || len(req.Server) == 0 {
		jsonErr(w, http.StatusBadRequest, "send {id, engine, server: \"<name>\" | {name, url, headers}}")
		return
	}
	var s harness.MCPServer
	var name string
	if json.Unmarshal(req.Server, &name) == nil {
		m, ok := h.store.Get().FindMCP(name)
		if !ok {
			jsonErr(w, http.StatusNotFound, "there is no MCP server named "+name)
			return
		}
		s = m
	} else {
		var in struct {
			Name    string            `json:"name"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		}
		if err := json.Unmarshal(req.Server, &in); err != nil {
			jsonErr(w, http.StatusBadRequest, "server must be a registered name or {name, url, headers}")
			return
		}
		s = harness.MCPServer{Name: strings.TrimSpace(in.Name), URL: strings.TrimSpace(in.URL), Headers: cleanHeaders(in.Headers)}
		if err := harness.ValidateMCPServer(s); err != nil {
			jsonErr(w, http.StatusBadRequest, redactMCP(err.Error(), s))
			return
		}
	}
	engine := normalizeEngine(req.Engine)
	a := s.Agent(builder.HostAlias(engine))
	body := map[string]any{"name": a.Name, "url": a.URL}
	if len(a.Headers) > 0 {
		body["headers"] = a.Headers
	}
	if a.ConnectVia != "" {
		body["connect_via"] = a.ConnectVia
	}
	switch {
	case len(req.Tools) > 0:
		body["tools"] = req.Tools
	case s.ToolsChosen || s.Catalog != nil:
		body["tools"] = s.Tools // the registry's pick (null = all)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	st, b, err := h.gateCall(ctx, req.ID, engine, http.MethodPost, "", body)
	if err == nil && st < 300 {
		log.Printf("harness: MCP server %s connected to a running model (%s)", s.Name, agent.RedactURL(s.URL))
	}
	relay(w, st, b, err)
}
