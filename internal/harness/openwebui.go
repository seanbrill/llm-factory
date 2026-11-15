package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Open WebUI connector.
//
// It talks to Open WebUI's admin config API (verified against 0.13.3) with an
// admin API key the user pastes for the one call; the key is never stored or
// echoed. Every write is a read-modify-write that keeps the user's existing
// entries intact, and the report says exactly what changed. Endpoints:
//
//	GET  /api/version                          (public; identifies Open WebUI)
//	GET  /openai/config                        OpenAI-compatible connections
//	POST /openai/config/update                 (whole lists; configs keyed by index)
//	GET  /api/v1/configs/terminal_servers      Open Terminal connections
//	POST /api/v1/configs/terminal_servers      (replaces the whole list)
//	GET  /api/v1/retrieval/config              web search state (only 2 fields read)
//	POST /api/v1/retrieval/config/update       {"web": {...only the fields we set}}

// OWUIClient calls one Open WebUI instance as an admin.
type OWUIClient struct {
	BaseURL string
	key     string
	http    *http.Client
	secrets []string // redacted from anything we report
}

// NewOWUIClient validates baseURL (http/https, host, no credentials) and
// returns a client that never follows redirects (so the key can't be replayed
// to another host) and caps responses at 8 MB.
func NewOWUIClient(baseURL, key string) (*OWUIClient, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("Open WebUI URL must look like http://host:port (got %q)", baseURL)
	}
	if u.User != nil {
		return nil, errors.New("Open WebUI URL can't contain a user name or password")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, errors.New("an Open WebUI admin API key is required")
	}
	if strings.ContainsAny(key, "\r\n") {
		return nil, errors.New("the API key contains a line break")
	}
	base := u.Scheme + "://" + u.Host + strings.TrimRight(u.Path, "/")
	return &OWUIClient{
		BaseURL: base,
		key:     key,
		secrets: []string{key},
		http: &http.Client{
			Timeout: 15 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// Plan is what Connect provisions (and Check compares against).
type Plan struct {
	GatewayURL  string // OpenAI base URL as Open WebUI reaches it, e.g. http://host.docker.internal:8799/v1
	GatewayKey  string
	TerminalURL string // Open Terminal as Open WebUI reaches it, e.g. http://host.docker.internal:8798
	TerminalKey string
	OpenAI      bool // add/update the gateway connection
	Terminal    bool // add/update the terminal connection
	WebSearch   bool // enable web search (DuckDuckGo) when it's off

	// TerminalRunning lets the connector ask Open WebUI to reach the terminal
	// (pointless while it's stopped).
	TerminalRunning bool
}

// Item is one line of a connector report.
type Item struct {
	Area   string `json:"area"`   // openwebui | models | terminal | web_search
	Status string `json:"status"` // ok | added | updated | missing | outdated | error | skipped | info
	Detail string `json:"detail"`
}

// WebSearchState is Open WebUI's web search switch and engine.
type WebSearchState struct {
	Enabled bool   `json:"enabled"`
	Engine  string `json:"engine"`
}

// Report is the result of Check or Connect.
type Report struct {
	Reachable bool            `json:"reachable"`
	Admin     bool            `json:"admin"`
	Version   string          `json:"version,omitempty"`
	Changed   bool            `json:"changed"`
	Items     []Item          `json:"items"`
	WebSearch *WebSearchState `json:"web_search,omitempty"`
}

func (r *Report) add(area, status, detail string) {
	r.Items = append(r.Items, Item{Area: area, Status: status, Detail: detail})
}

// HasErrors reports whether any item failed.
func (r *Report) HasErrors() bool {
	for _, it := range r.Items {
		if it.Status == "error" {
			return true
		}
	}
	return false
}

// apiError is a non-2xx answer from Open WebUI.
type apiError struct {
	Status   int
	Detail   string
	Location string
}

func (e *apiError) Error() string {
	if e.Status >= 300 && e.Status < 400 {
		return fmt.Sprintf("Open WebUI answered with a redirect (HTTP %d) to %q; check the URL (http vs https, extra path)", e.Status, e.Location)
	}
	if e.Detail != "" {
		return fmt.Sprintf("HTTP %d: %s", e.Status, e.Detail)
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

// detailOf extracts FastAPI's {"detail": ...} (or an OpenAI-style
// {"error": {"message": ...}} that Open WebUI passes through from a verify
// call). Validation errors (a list) are reduced to location + message so the
// echoed request input (which can hold keys) is never surfaced.
func detailOf(data []byte) string {
	var body struct {
		Detail json.RawMessage `json:"detail"`
		Error  json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &body) == nil && len(body.Detail) == 0 && len(body.Error) > 0 {
		var e struct {
			Message string `json:"message"`
		}
		var s string
		switch {
		case json.Unmarshal(body.Error, &e) == nil && e.Message != "":
			return e.Message
		case json.Unmarshal(body.Error, &s) == nil:
			return s
		}
	}
	if json.Unmarshal(data, &body) != nil || len(body.Detail) == 0 {
		s := strings.TrimSpace(string(data))
		if len(s) > 200 || strings.HasPrefix(s, "<") {
			return "" // an HTML page or a large body: not a useful message
		}
		return s
	}
	var s string
	if json.Unmarshal(body.Detail, &s) == nil {
		return s
	}
	var list []struct {
		Loc []any  `json:"loc"`
		Msg string `json:"msg"`
	}
	if json.Unmarshal(body.Detail, &list) == nil {
		var parts []string
		for _, e := range list {
			loc := make([]string, 0, len(e.Loc))
			for _, l := range e.Loc {
				loc = append(loc, fmt.Sprint(l))
			}
			parts = append(parts, strings.Join(loc, ".")+": "+e.Msg)
		}
		return strings.Join(parts, "; ")
	}
	return ""
}

func (c *OWUIClient) redact(s string) string {
	for _, sec := range c.secrets {
		if len(sec) >= 8 {
			s = strings.ReplaceAll(s, sec, "[redacted]")
		}
	}
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	return s
}

func (c *OWUIClient) do(ctx context.Context, method, path string, auth bool, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "llm-factory-harness")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &apiError{Status: resp.StatusCode, Detail: detailOf(data), Location: resp.Header.Get("Location")}
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("unexpected (non-JSON) answer from %s%s; is this the Open WebUI URL?", c.BaseURL, path)
		}
	}
	return nil
}

// explain turns a request error into a user-facing sentence.
func (c *OWUIClient) explain(err error) string {
	var ae *apiError
	if errors.As(err, &ae) {
		switch {
		case ae.Status == http.StatusUnauthorized || ae.Status == http.StatusForbidden:
			msg := "Open WebUI rejected the API key"
			if ae.Detail != "" {
				msg += " (" + ae.Detail + ")"
			}
			if strings.Contains(ae.Detail, "API key is not enabled") {
				return c.redact(msg + ". Turn on API keys in Open WebUI (Admin Panel → Settings → General → Enable API Keys), then create one under Settings → Account.")
			}
			return c.redact(msg + ". Use an API key that belongs to an admin user (Settings → Account → API keys).")
		case ae.Status == http.StatusNotFound:
			return c.redact("Open WebUI doesn't have this endpoint (" + ae.Error() + "); this connector targets Open WebUI 0.13.x.")
		}
		return c.redact(ae.Error())
	}
	return c.redact(fmt.Sprintf("can't reach Open WebUI at %s: %v", c.BaseURL, err))
}

func normURL(u string) string { return strings.ToLower(strings.TrimRight(strings.TrimSpace(u), "/")) }

// ---- OpenAI connections ----------------------------------------------------

type openAIConfig struct {
	Enable  *bool                      `json:"ENABLE_OPENAI_API"`
	URLs    []string                   `json:"OPENAI_API_BASE_URLS"`
	Keys    []string                   `json:"OPENAI_API_KEYS"`
	Configs map[string]json.RawMessage `json:"OPENAI_API_CONFIGS"`
}

// normalize pads/truncates keys to the URL count and re-keys configs by index
// (carrying legacy URL-keyed entries over, since the update endpoint drops any
// key that isn't an index).
func (oc *openAIConfig) normalize() {
	keys := make([]string, len(oc.URLs))
	copy(keys, oc.Keys)
	oc.Keys = keys
	cfgs := make(map[string]json.RawMessage, len(oc.URLs))
	for i, u := range oc.URLs {
		k := strconv.Itoa(i)
		if v, ok := oc.Configs[k]; ok {
			cfgs[k] = v
		} else if v, ok := oc.Configs[u]; ok {
			cfgs[k] = v
		}
	}
	oc.Configs = cfgs
}

func (oc *openAIConfig) find(target string) int {
	t := normURL(target)
	for i, u := range oc.URLs {
		if normURL(u) == t {
			return i
		}
	}
	return -1
}

func connConfig(raw json.RawMessage) map[string]any {
	m := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &m)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m
}

// newGatewayConnConfig mirrors what Open WebUI's "Add connection" dialog saves.
// connection_type "local" files the models under Local in the model picker.
func newGatewayConnConfig() map[string]any {
	return map[string]any{
		"enable": true, "tags": []any{}, "prefix_id": "", "model_ids": []any{},
		"connection_type": "local", "auth_type": "bearer",
	}
}

func (c *OWUIClient) openAIState(oc *openAIConfig, p Plan, r *Report) {
	if oc.Enable == nil || !*oc.Enable {
		r.add("models", "outdated", "OpenAI-compatible connections are turned off in Open WebUI (Admin Panel → Settings → Connections).")
	}
	idx := oc.find(p.GatewayURL)
	switch {
	case idx < 0:
		r.add("models", "missing", "No connection to the factory's model gateway ("+p.GatewayURL+").")
	case idx >= len(oc.Keys) || oc.Keys[idx] != p.GatewayKey:
		r.add("models", "outdated", "A connection to "+p.GatewayURL+" exists but uses a different key (e.g. after Regenerate). Connect again to update it.")
	default:
		if en, ok := connConfig(oc.Configs[strconv.Itoa(idx)])["enable"].(bool); ok && !en {
			r.add("models", "outdated", "The connection to "+p.GatewayURL+" is switched off in Open WebUI.")
			return
		}
		r.add("models", "ok", "Connected: factory models appear in Open WebUI through "+p.GatewayURL+".")
	}
}

func (c *OWUIClient) connectOpenAI(ctx context.Context, oc *openAIConfig, p Plan, r *Report) bool {
	oc.normalize()
	var changes []string
	status := "updated"
	idx := oc.find(p.GatewayURL)
	if idx < 0 {
		oc.URLs = append(oc.URLs, p.GatewayURL)
		oc.Keys = append(oc.Keys, p.GatewayKey)
		raw, _ := json.Marshal(newGatewayConnConfig())
		oc.Configs[strconv.Itoa(len(oc.URLs)-1)] = raw
		status = "added"
		changes = append(changes, "added the connection "+p.GatewayURL+" (type Local)")
	} else {
		if oc.Keys[idx] != p.GatewayKey {
			oc.Keys[idx] = p.GatewayKey
			changes = append(changes, "updated the key of "+p.GatewayURL)
		}
		k := strconv.Itoa(idx)
		if raw, ok := oc.Configs[k]; ok {
			m := connConfig(raw)
			dirty := false
			if en, ok := m["enable"].(bool); ok && !en {
				m["enable"] = true
				dirty = true
				changes = append(changes, "switched the connection back on")
			}
			if at, ok := m["auth_type"].(string); ok && at != "" && at != "bearer" {
				m["auth_type"] = "bearer"
				dirty = true
				changes = append(changes, "set its auth to Bearer (was "+at+")")
			}
			if dirty {
				b, _ := json.Marshal(m)
				oc.Configs[k] = b
			}
		}
	}
	if oc.Enable == nil || !*oc.Enable {
		t := true
		oc.Enable = &t
		changes = append(changes, "turned on OpenAI-compatible connections (this also re-enables any other OpenAI connections you had switched off globally)")
	}
	if len(changes) == 0 {
		r.add("models", "ok", "Already connected: "+p.GatewayURL+" (key up to date).")
		return true
	}
	body := map[string]any{
		"ENABLE_OPENAI_API":    *oc.Enable,
		"OPENAI_API_BASE_URLS": oc.URLs,
		"OPENAI_API_KEYS":      oc.Keys,
		"OPENAI_API_CONFIGS":   oc.Configs,
	}
	if err := c.do(ctx, http.MethodPost, "/openai/config/update", true, body, nil); err != nil {
		r.add("models", "error", "Couldn't update the OpenAI connections: "+c.explain(err))
		return false
	}
	r.Changed = true
	r.add("models", status, upperFirst(strings.Join(changes, "; "))+". Your other connections were left as they were.")
	return true
}

// ---- Terminal servers ----------------------------------------------------------

type terminalServers struct {
	Conns []map[string]json.RawMessage `json:"TERMINAL_SERVER_CONNECTIONS"`
}

func rawString(m map[string]json.RawMessage, k string) string {
	var s string
	if v, ok := m[k]; ok {
		_ = json.Unmarshal(v, &s)
	}
	return s
}

func rawBool(m map[string]json.RawMessage, k string, def bool) bool {
	b := def
	if v, ok := m[k]; ok && string(v) != "null" {
		_ = json.Unmarshal(v, &b)
	}
	return b
}

func setRaw(m map[string]json.RawMessage, k string, v any) {
	b, _ := json.Marshal(v)
	m[k] = b
}

func findTerminal(conns []map[string]json.RawMessage, p Plan) int {
	for i, c := range conns {
		if rawString(c, "id") == TerminalID {
			return i
		}
	}
	t := normURL(p.TerminalURL)
	for i, c := range conns {
		if normURL(rawString(c, "url")) == t {
			return i
		}
	}
	return -1
}

func (c *OWUIClient) terminalState(ts *terminalServers, p Plan, r *Report) {
	i := findTerminal(ts.Conns, p)
	if i < 0 {
		r.add("terminal", "missing", "No Open Terminal connection for the factory's workspace terminal.")
		return
	}
	conn := ts.Conns[i]
	var issues []string
	if normURL(rawString(conn, "url")) != normURL(p.TerminalURL) {
		issues = append(issues, "points at "+rawString(conn, "url")+" instead of "+p.TerminalURL)
	}
	if rawString(conn, "key") != p.TerminalKey {
		issues = append(issues, "uses a different key")
	}
	if !rawBool(conn, "enabled", true) {
		issues = append(issues, "is switched off")
	}
	if len(issues) > 0 {
		r.add("terminal", "outdated", "The terminal connection "+strings.Join(issues, ", ")+".")
		return
	}
	r.add("terminal", "ok", "Connected: \""+rawString(conn, "name")+"\" at "+p.TerminalURL+".")
}

func (c *OWUIClient) connectTerminal(ctx context.Context, p Plan, r *Report) bool {
	var ts terminalServers
	if err := c.do(ctx, http.MethodGet, "/api/v1/configs/terminal_servers", true, nil, &ts); err != nil {
		r.add("terminal", "error", "Couldn't read the terminal connections: "+c.explain(err))
		return false
	}
	if ts.Conns == nil {
		ts.Conns = []map[string]json.RawMessage{}
	}
	var changes []string
	status := "updated"
	i := findTerminal(ts.Conns, p)
	if i < 0 {
		conn := map[string]json.RawMessage{}
		setRaw(conn, "id", TerminalID)
		setRaw(conn, "name", TerminalName)
		setRaw(conn, "enabled", true)
		setRaw(conn, "url", p.TerminalURL)
		setRaw(conn, "path", "/openapi.json")
		setRaw(conn, "key", p.TerminalKey)
		setRaw(conn, "auth_type", "bearer")
		setRaw(conn, "forward_cookies", false)
		// Same shape as Open WebUI's own dialog. No access grants = admin-only.
		setRaw(conn, "config", map[string]any{"access_grants": []any{}, "working_directory_context": true, "user_shell_tools": "auto"})
		setRaw(conn, "server_type", "terminal")
		ts.Conns = append(ts.Conns, conn)
		status = "added"
		changes = append(changes, "added \""+TerminalName+"\" at "+p.TerminalURL+" (visible to admins; share it under Admin Panel → Settings → Integrations)")
	} else {
		conn := ts.Conns[i]
		if rawString(conn, "id") == "" {
			setRaw(conn, "id", TerminalID)
		}
		if rawString(conn, "name") == "" {
			setRaw(conn, "name", TerminalName)
		}
		if normURL(rawString(conn, "url")) != normURL(p.TerminalURL) {
			setRaw(conn, "url", p.TerminalURL)
			changes = append(changes, "pointed it at "+p.TerminalURL)
		}
		if rawString(conn, "key") != p.TerminalKey {
			setRaw(conn, "key", p.TerminalKey)
			changes = append(changes, "updated its key")
		}
		if !rawBool(conn, "enabled", true) {
			setRaw(conn, "enabled", true)
			changes = append(changes, "switched it on")
		}
		if at := rawString(conn, "auth_type"); at != "" && at != "bearer" {
			setRaw(conn, "auth_type", "bearer")
			changes = append(changes, "set its auth to Bearer")
		}
		if path := rawString(conn, "path"); path != "/openapi.json" {
			setRaw(conn, "path", "/openapi.json")
			if path != "" {
				changes = append(changes, "set its OpenAPI path to /openapi.json")
			}
		}
		if len(changes) == 0 {
			r.add("terminal", "ok", "Already connected: \""+rawString(conn, "name")+"\" at "+p.TerminalURL+".")
			return true
		}
	}
	body := map[string]any{"TERMINAL_SERVER_CONNECTIONS": ts.Conns}
	if err := c.do(ctx, http.MethodPost, "/api/v1/configs/terminal_servers", true, body, nil); err != nil {
		r.add("terminal", "error", "Couldn't save the terminal connection: "+c.explain(err))
		return false
	}
	r.Changed = true
	r.add("terminal", status, upperFirst(strings.Join(changes, "; "))+". Other terminal connections were left as they were.")
	return true
}

// ---- Web search ------------------------------------------------------------------

type retrievalWeb struct {
	Web struct {
		Enable *bool   `json:"ENABLE_WEB_SEARCH"`
		Engine *string `json:"WEB_SEARCH_ENGINE"`
	} `json:"web"`
}

func (rw retrievalWeb) state() *WebSearchState {
	s := &WebSearchState{}
	if rw.Web.Enable != nil {
		s.Enabled = *rw.Web.Enable
	}
	if rw.Web.Engine != nil {
		s.Engine = *rw.Web.Engine
	}
	return s
}

func webSearchItem(s *WebSearchState) Item {
	switch {
	case s.Enabled && s.Engine != "":
		return Item{"web_search", "ok", "Web search is on (engine: " + s.Engine + "). Models get search_web and fetch_url when Web Search is toggled on in the chat."}
	case s.Enabled:
		return Item{"web_search", "outdated", "Web search is on but no search engine is chosen."}
	default:
		return Item{"web_search", "missing", "Web search is off in Open WebUI (Admin Panel → Settings → Web Search)."}
	}
}

func (c *OWUIClient) connectWebSearch(ctx context.Context, r *Report) {
	var rw retrievalWeb
	if err := c.do(ctx, http.MethodGet, "/api/v1/retrieval/config", true, nil, &rw); err != nil {
		r.add("web_search", "error", "Couldn't read the web search settings: "+c.explain(err))
		return
	}
	cur := rw.state()
	r.WebSearch = cur
	// Only send the fields we change: the handler writes just the submitted
	// fields, so the domain filter list, result count, API keys etc. stay put.
	update := map[string]any{}
	var changes []string
	if !cur.Enabled {
		update["ENABLE_WEB_SEARCH"] = true
		changes = append(changes, "turned web search on")
	}
	if cur.Engine == "" {
		update["WEB_SEARCH_ENGINE"] = "duckduckgo"
		changes = append(changes, "chose DuckDuckGo (no API key needed)")
	} else if !cur.Enabled {
		changes = append(changes, "kept your engine ("+cur.Engine+")")
	}
	if len(update) == 0 {
		it := webSearchItem(cur)
		r.Items = append(r.Items, it)
		return
	}
	var resp retrievalWeb
	if err := c.do(ctx, http.MethodPost, "/api/v1/retrieval/config/update", true, map[string]any{"web": update}, &resp); err != nil {
		msg := c.explain(err)
		var ae *apiError
		if errors.As(err, &ae) && ae.Status == http.StatusBadRequest {
			msg += " Pick a search provider by hand in Admin Panel → Settings → Web Search (slim Open WebUI images can't run DuckDuckGo)."
		}
		r.add("web_search", "error", "Couldn't enable web search: "+msg)
		return
	}
	r.Changed = true
	if resp.Web.Enable != nil {
		r.WebSearch = resp.state()
	} else {
		r.WebSearch = &WebSearchState{Enabled: true, Engine: cur.Engine}
		if cur.Engine == "" {
			r.WebSearch.Engine = "duckduckgo"
		}
	}
	r.add("web_search", "updated", upperFirst(strings.Join(changes, "; "))+". Models get search_web and fetch_url when Web Search is toggled on in the chat.")
}

// ---- Reachability (Open WebUI's own verify endpoints; they change nothing) ------

func reachHint(target string) string {
	return " Open WebUI must be able to open " + target + ". With Docker Desktop that works through host.docker.internal; on other setups change \"Open WebUI reaches this computer at\" (see docs/HARNESS.md › Troubleshooting)."
}

// verifyGateway has Open WebUI list the gateway's models itself, proving the
// URL and key work from Open WebUI's side of the network.
func (c *OWUIClient) verifyGateway(ctx context.Context, p Plan, r *Report) {
	var out struct {
		Data []json.RawMessage `json:"data"`
	}
	body := map[string]any{"url": p.GatewayURL, "key": p.GatewayKey, "config": map[string]any{"auth_type": "bearer"}}
	err := c.do(ctx, http.MethodPost, "/openai/verify", true, body, &out)
	if err == nil {
		r.add("models", "ok", fmt.Sprintf("Open WebUI reached the gateway: %d model(s) running right now.", len(out.Data)))
		return
	}
	var ae *apiError
	switch {
	case errors.As(err, &ae) && ae.Status == http.StatusUnauthorized:
		r.add("models", "error", "The gateway refused the key Open WebUI sent; reload this page and try again.")
	case errors.As(err, &ae) && ae.Status == http.StatusNotFound:
		r.add("models", "error", c.redact(p.GatewayURL+" answered 404: something other than the factory is listening there."+reachHint(p.GatewayURL)))
	case errors.As(err, &ae):
		r.add("models", "error", c.redact("Open WebUI couldn't reach the gateway at "+p.GatewayURL+" ("+ae.Error()+")."+reachHint(p.GatewayURL)))
	default:
		r.add("models", "error", "Couldn't ask Open WebUI to test the gateway: "+c.explain(err))
	}
}

// verifyTerminal has Open WebUI call the terminal with its key.
func (c *OWUIClient) verifyTerminal(ctx context.Context, p Plan, r *Report) {
	var out struct {
		Status bool   `json:"status"`
		Type   string `json:"type"`
	}
	body := map[string]any{"url": p.TerminalURL, "key": p.TerminalKey, "auth_type": "bearer", "path": "/openapi.json"}
	err := c.do(ctx, http.MethodPost, "/api/v1/configs/terminal_servers/verify", true, body, &out)
	if err == nil && out.Status {
		r.add("terminal", "ok", "Open WebUI reached the workspace terminal at "+p.TerminalURL+".")
		return
	}
	var ae *apiError
	switch {
	case err == nil:
		r.add("terminal", "error", "Open WebUI couldn't confirm the terminal at "+p.TerminalURL+".")
	case errors.As(err, &ae) && ae.Status == http.StatusForbidden && strings.Contains(ae.Detail, "Tool servers are disabled"):
		r.add("terminal", "error", "Open WebUI has tool servers turned off (ENABLE_TOOL_SERVERS=false), so it can't use the terminal. Remove that setting and restart Open WebUI.")
	case errors.As(err, &ae):
		r.add("terminal", "error", "Open WebUI couldn't connect to the terminal at "+p.TerminalURL+" (unreachable, or the key doesn't match: restart the terminal after regenerating its key)."+reachHint(p.TerminalURL))
	default:
		r.add("terminal", "error", "Couldn't ask Open WebUI to test the terminal: "+c.explain(err))
	}
}

// ---- Check / Connect -----------------------------------------------------------

// preflight identifies Open WebUI and proves the key is an admin's (by reading
// the OpenAI connection config, which needs admin rights).
func (c *OWUIClient) preflight(ctx context.Context, r *Report) (*openAIConfig, bool) {
	var v struct {
		Version string `json:"version"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/version", false, nil, &v); err != nil {
		r.add("openwebui", "error", c.explain(err))
		return nil, false
	}
	r.Reachable = true
	r.Version = v.Version
	if v.Version == "" {
		r.add("openwebui", "error", c.BaseURL+" answered, but it doesn't look like Open WebUI (no version).")
		return nil, false
	}
	var oc openAIConfig
	if err := c.do(ctx, http.MethodGet, "/openai/config", true, nil, &oc); err != nil {
		r.add("openwebui", "error", c.explain(err))
		return nil, false
	}
	r.Admin = true
	r.add("openwebui", "info", "Open WebUI "+v.Version+" at "+c.BaseURL+"; the key has admin rights.")
	return &oc, true
}

// Check reports the current state without changing anything.
func (c *OWUIClient) Check(ctx context.Context, p Plan) Report {
	c.secrets = append(c.secrets, p.GatewayKey, p.TerminalKey)
	r := Report{Items: []Item{}}
	oc, ok := c.preflight(ctx, &r)
	if !ok {
		return r
	}
	c.openAIState(oc, p, &r)
	c.verifyGateway(ctx, p, &r)
	var ts terminalServers
	if err := c.do(ctx, http.MethodGet, "/api/v1/configs/terminal_servers", true, nil, &ts); err != nil {
		r.add("terminal", "error", "Couldn't read the terminal connections: "+c.explain(err))
	} else {
		c.terminalState(&ts, p, &r)
	}
	if p.TerminalRunning {
		c.verifyTerminal(ctx, p, &r)
	}
	var rw retrievalWeb
	if err := c.do(ctx, http.MethodGet, "/api/v1/retrieval/config", true, nil, &rw); err != nil {
		r.add("web_search", "error", "Couldn't read the web search settings: "+c.explain(err))
	} else {
		r.WebSearch = rw.state()
		r.Items = append(r.Items, webSearchItem(r.WebSearch))
	}
	return r
}

// Connect idempotently provisions the selected parts of the plan.
func (c *OWUIClient) Connect(ctx context.Context, p Plan) Report {
	c.secrets = append(c.secrets, p.GatewayKey, p.TerminalKey)
	r := Report{Items: []Item{}}
	oc, ok := c.preflight(ctx, &r)
	if !ok {
		return r
	}
	if p.OpenAI {
		if c.connectOpenAI(ctx, oc, p, &r) {
			c.verifyGateway(ctx, p, &r)
		}
	} else {
		r.add("models", "skipped", "Left the OpenAI connections alone (not selected).")
	}
	if p.Terminal {
		if c.connectTerminal(ctx, p, &r) && p.TerminalRunning {
			c.verifyTerminal(ctx, p, &r)
		}
	} else {
		r.add("terminal", "skipped", "Left the terminal connections alone (not selected).")
	}
	if p.WebSearch {
		c.connectWebSearch(ctx, &r)
	} else {
		r.add("web_search", "skipped", "Left web search alone (not selected).")
	}
	return r
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
