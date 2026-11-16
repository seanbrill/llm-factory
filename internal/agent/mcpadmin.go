package agent

// The admin API: manage the MCP servers of a running harness over HTTP.
// Hosts mount it at /v1/harness/mcp (llmgate, llmpack, ensemblegate). It is
// off unless the harness has an admin token (HARNESS_ADMIN_TOKEN), and every
// call must carry it as a bearer token. Answers never include header values
// or URL paths.
//
//	GET    /v1/harness/mcp              servers, status and tools
//	POST   /v1/harness/mcp              {name, url | command+args+env, headers, tools, connect_via, timeout_s}
//	PATCH  /v1/harness/mcp/{name}       {tools: [...] | null} and/or {enabled: bool}
//	DELETE /v1/harness/mcp/{name}
//	POST   /v1/harness/mcp/{name}/test  reconnect now

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// AdminPath is where hosts mount AdminHandler.
const AdminPath = "/v1/harness/mcp"

// IsAdminPath reports whether p belongs to the admin API.
func IsAdminPath(p string) bool { return p == AdminPath || strings.HasPrefix(p, AdminPath+"/") }

// MCPList is the admin API's GET answer.
type MCPList struct {
	Servers    []MCPStatus `json:"servers"`
	State      string      `json:"state"` // "saved" | "not saved"
	StateError string      `json:"state_error,omitempty"`
	ManyTools  int         `json:"many_tools"`
	ToolCount  int         `json:"tool_count"`    // every tool the model gets
	Tokens     int         `json:"schema_tokens"` // and their estimated cost
	Context    int         `json:"context_size,omitempty"`
}

// MCPList describes the servers (with their tools).
func (a *Agent) MCPList() MCPList {
	saved, why := a.mcp.saved()
	l := MCPList{Servers: a.mcp.list(true), State: "not saved", StateError: why, ManyTools: ManyMCPTools, Context: a.ContextSize()}
	if saved {
		l.State = "saved"
	}
	l.ToolCount, l.Tokens = a.ToolCost()
	return l
}

func adminJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func adminErr(w http.ResponseWriter, status int, msg string) {
	adminJSON(w, status, map[string]any{"error": msg})
}

// AdminHandler serves the admin API (see the file comment).
func (a *Agent) AdminHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.cfg.AdminToken == "" {
			adminErr(w, http.StatusNotFound, "the harness admin API is off (the harness has no HARNESS_ADMIN_TOKEN)")
			return
		}
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		const prefix = "bearer "
		got := ""
		if len(auth) > len(prefix) && strings.EqualFold(auth[:len(prefix)], prefix) {
			got = strings.TrimSpace(auth[len(prefix):])
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(a.cfg.AdminToken)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="harness"`)
			adminErr(w, http.StatusUnauthorized, "missing or wrong admin token")
			return
		}
		rest := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, AdminPath), "/")
		var name, action string
		if rest != "" {
			name, action, _ = strings.Cut(rest, "/")
			if n, err := url.PathUnescape(name); err == nil {
				name = n
			}
		}
		switch {
		case name == "" && r.Method == http.MethodGet:
			adminJSON(w, http.StatusOK, a.MCPList())
		case name == "" && r.Method == http.MethodPost:
			a.adminAdd(w, r)
		case name != "" && action == "" && r.Method == http.MethodGet:
			if c := a.mcp.find(name); c != nil {
				adminJSON(w, http.StatusOK, c.status(true))
			} else {
				adminErr(w, http.StatusNotFound, errMCPUnknown.Error())
			}
		case name != "" && action == "" && r.Method == http.MethodPatch:
			a.adminPatch(w, r, name)
		case name != "" && action == "" && r.Method == http.MethodDelete:
			if err := a.mcp.remove(name); err != nil {
				adminErr(w, http.StatusNotFound, err.Error())
				return
			}
			adminJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		case name != "" && action == "test" && r.Method == http.MethodPost:
			st, err := a.mcp.test(r.Context(), name)
			if err != nil {
				adminErr(w, http.StatusNotFound, err.Error())
				return
			}
			adminJSON(w, http.StatusOK, map[string]any{"server": st, "ok": st.Status == MCPConnected})
		default:
			adminErr(w, http.StatusMethodNotAllowed, "use GET or POST "+AdminPath+", PATCH or DELETE "+AdminPath+"/{name}, POST "+AdminPath+"/{name}/test")
		}
	})
}

func readAdminBody(r *http.Request) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, 256<<10))
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return []byte("{}"), nil
	}
	return b, nil
}

func (a *Agent) adminAdd(w http.ResponseWriter, r *http.Request) {
	b, err := readAdminBody(r)
	if err != nil {
		adminErr(w, http.StatusBadRequest, "can't read the body")
		return
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(b, &keys); err != nil {
		adminErr(w, http.StatusBadRequest, "send a JSON object {name, url, headers, tools}")
		return
	}
	var s MCPServer
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		adminErr(w, http.StatusBadRequest, "bad server: "+err.Error())
		return
	}
	_, toolsGiven := keys["tools"]
	if string(keys["tools"]) == "null" {
		s.Tools = nil
	}
	st, err := a.mcp.add(r.Context(), s, toolsGiven)
	switch {
	case errors.Is(err, errMCPExists):
		adminErr(w, http.StatusConflict, err.Error())
	case err != nil:
		adminErr(w, http.StatusBadRequest, s.redact(err.Error()))
	default:
		a.logf("admin: added MCP server %s (%s)", st.Name, st.Endpoint)
		if c := a.mcp.find(st.Name); c != nil {
			st = c.status(true)
		}
		adminJSON(w, http.StatusCreated, map[string]any{"server": st, "ok": st.Status == MCPConnected})
	}
}

func (a *Agent) adminPatch(w http.ResponseWriter, r *http.Request, name string) {
	b, err := readAdminBody(r)
	if err != nil {
		adminErr(w, http.StatusBadRequest, "can't read the body")
		return
	}
	var p map[string]json.RawMessage
	if err := json.Unmarshal(b, &p); err != nil {
		adminErr(w, http.StatusBadRequest, "send a JSON object {tools} or {enabled}")
		return
	}
	var tools []string
	rawTools, setTools := p["tools"]
	if setTools && string(rawTools) != "null" {
		if err := json.Unmarshal(rawTools, &tools); err != nil {
			adminErr(w, http.StatusBadRequest, "tools must be a list of tool names or null (all)")
			return
		}
		if tools == nil {
			tools = []string{}
		}
	}
	var enabled *bool
	if raw, ok := p["enabled"]; ok {
		var on bool
		if err := json.Unmarshal(raw, &on); err != nil {
			adminErr(w, http.StatusBadRequest, "enabled must be true or false")
			return
		}
		enabled = &on
	}
	for k := range p {
		if k != "tools" && k != "enabled" {
			adminErr(w, http.StatusBadRequest, "only tools and enabled can change; remove and add the server to change the rest")
			return
		}
	}
	st, err := a.mcp.update(name, func(s *MCPServer) {
		if setTools {
			s.Tools = tools
		}
		if enabled != nil {
			s.Enabled = enabled
		}
	})
	if err != nil {
		adminErr(w, http.StatusNotFound, err.Error())
		return
	}
	if c := a.mcp.find(name); c != nil {
		st = c.status(true)
	}
	adminJSON(w, http.StatusOK, map[string]any{"server": st})
}
