package harness

// The factory's MCP server registry: servers the user adds on the Harness
// page and picks per run. They live in config/harness.json (0600) with their
// headers; the UI only ever sees masked values (MCPView).

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/seanbrill/local-llm/internal/agent"
)

// MCPServer is one registered MCP server (Streamable HTTP).
type MCPServer struct {
	Name    string            `json:"name"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	// Tools is the allowlist: null = all the server's tools, [] = none yet.
	Tools []string `json:"tools"`
	// ToolsChosen: the user edited the allowlist (a test then keeps it).
	ToolsChosen bool `json:"tools_chosen,omitempty"`
	// Load is how the tools reach the model (agent.MCPServer.Load): "auto"
	// (""), "full" or "on_demand". Pinned tools are always sent in full.
	Load   string   `json:"load,omitempty"`
	Pinned []string `json:"pinned,omitempty"`
	// Catalog is the server's tools as of the last successful test.
	Catalog  []MCPTool `json:"catalog,omitempty"`
	TestedAt string    `json:"tested_at,omitempty"`
	Error    string    `json:"error,omitempty"`
}

// MCPTool is a tool of a registered server.
type MCPTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Tokens      int    `json:"tokens"`
}

// MCPView is a server as the UI sees it: no header values, no URL path.
type MCPView struct {
	Name           string            `json:"name"`
	URL            string            `json:"url"`
	Headers        map[string]string `json:"headers"`
	Tools          []string          `json:"tools"`
	ToolsChosen    bool              `json:"tools_chosen"`
	Catalog        []MCPTool         `json:"catalog"`
	CatalogTokens  int               `json:"catalog_tokens"`
	SelectedTokens int               `json:"selected_tokens"`
	Load           string            `json:"load"`
	Pinned         []string          `json:"pinned"`
	// OnDemand: the selected tools reach the model as a catalog (tool_info,
	// call_tool). RequestTokens: what they cost each request.
	OnDemand      bool   `json:"on_demand"`
	RequestTokens int    `json:"request_tokens"`
	TestedAt      string `json:"tested_at,omitempty"`
	Error         string `json:"error,omitempty"`
	Loopback      bool   `json:"loopback"`
}

// MaskURL shows scheme://host:port, with "/…" when the URL has a path or
// query (a secret, for knitted).
func MaskURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "(invalid URL)"
	}
	out := u.Scheme + "://" + u.Host
	if u.User != nil {
		out = u.Scheme + "://…@" + u.Host
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		out += "/…"
	}
	return out
}

// View masks a server for the UI.
func (s MCPServer) View() MCPView {
	v := MCPView{Name: s.Name, URL: MaskURL(s.URL), Headers: map[string]string{}, Tools: s.Tools, ToolsChosen: s.ToolsChosen,
		Catalog: s.Catalog, TestedAt: s.TestedAt, Error: s.Error, Loopback: agent.IsLoopbackURL(s.URL)}
	for k := range s.Headers {
		v.Headers[k] = agent.MaskedValue
	}
	if v.Catalog == nil {
		v.Catalog = []MCPTool{}
	}
	allow := map[string]bool{}
	for _, t := range s.Tools {
		allow[t] = true
	}
	pinned := map[string]bool{}
	for _, t := range s.Pinned {
		pinned[t] = true
	}
	for _, t := range s.Catalog {
		v.CatalogTokens += t.Tokens
		if s.Tools == nil || allow[t.Name] {
			v.SelectedTokens += t.Tokens
		}
	}
	v.Load, v.Pinned = s.Load, s.Pinned
	if v.Load == "" {
		v.Load = agent.MCPLoadAuto
	}
	if v.Pinned == nil {
		v.Pinned = []string{}
	}
	v.OnDemand = agent.LoadOnDemand(s.Load, v.SelectedTokens)
	v.RequestTokens = v.SelectedTokens
	if v.OnDemand {
		v.RequestTokens = 0
		for _, t := range s.Catalog {
			if s.Tools != nil && !allow[t.Name] {
				continue
			}
			if pinned[t.Name] {
				v.RequestTokens += t.Tokens
			} else {
				v.RequestTokens += agent.CatalogLineTokens(s.Name, t.Name, t.Description)
			}
		}
	}
	return v
}

// Agent is the server as a harness gets it (HARNESS_MCP, the admin API).
// via is the connect_via for a loopback URL ("" = none).
func (s MCPServer) Agent(via string) agent.MCPServer {
	a := agent.MCPServer{Name: s.Name, URL: s.URL, Headers: s.Headers, Tools: s.Tools, Load: s.Load, Pinned: s.Pinned}
	if via != "" && agent.IsLoopbackURL(s.URL) {
		a.ConnectVia = via
	}
	return a
}

// ApplyTest records a test: the catalog and, unless the user chose tools,
// the default selection (all up to agent.ManyMCPTools, else none).
func (s *MCPServer) ApplyTest(tools []agent.MCPToolStatus, err error, now string) {
	s.TestedAt = now
	if err != nil {
		s.Error = err.Error()
		return
	}
	s.Error = ""
	s.Catalog = make([]MCPTool, 0, len(tools))
	for _, t := range tools {
		s.Catalog = append(s.Catalog, MCPTool{Name: t.Name, Description: t.Description, Tokens: t.Tokens})
	}
	if !s.ToolsChosen {
		if len(tools) <= agent.ManyMCPTools {
			s.Tools = nil
		} else {
			s.Tools = []string{}
		}
	}
}

// ValidateMCPServer checks a registry entry (the same rules as the harness).
func ValidateMCPServer(s MCPServer) error {
	return s.Agent("").Validate()
}

// FindMCP returns the registered server of that name.
func (c Config) FindMCP(name string) (MCPServer, bool) {
	for _, s := range c.MCP {
		if s.Name == name {
			return s, true
		}
	}
	return MCPServer{}, false
}

// EngineHostAlias is how a container on engine reaches its host.
func EngineHostAlias(engine string) string {
	if engine == "podman" {
		return "host.containers.internal"
	}
	return "host.docker.internal"
}

// ViaFor is the connect_via a loopback URL needs from a container on engine:
// the engine's host alias with the URL's port.
func ViaFor(rawURL, engine string) string {
	if !agent.IsLoopbackURL(rawURL) {
		return ""
	}
	return EngineHostAlias(engine)
}

// ---------------------------------------------------------------------------
// Run tokens: the admin token of each running harness, by container name.
// Kept in a 0600 file under config/ (labels and env are readable by anyone
// who can list containers; this file is the factory's).
// ---------------------------------------------------------------------------

// TokenStore maps container names to their harness admin tokens.
type TokenStore struct {
	mu   sync.Mutex
	path string
}

// NewTokenStore keeps tokens at path (config/run-tokens.json).
func NewTokenStore(path string) *TokenStore { return &TokenStore{path: path} }

func (t *TokenStore) load() map[string]string {
	m := map[string]string{}
	if data, err := os.ReadFile(t.path); err == nil {
		_ = json.Unmarshal(data, &m)
	}
	return m
}

// Get returns a container's token ("" when unknown).
func (t *TokenStore) Get(container string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.load()[strings.TrimPrefix(container, "/")]
}

// Set stores (or, with "", forgets) a container's token.
func (t *TokenStore) Set(container, token string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	m := t.load()
	container = strings.TrimPrefix(container, "/")
	if token == "" {
		delete(m, container)
	} else {
		m[container] = token
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(t.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".run-tokens-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, t.path)
}

// NewAdminToken returns a fresh harness admin token.
func NewAdminToken() (string, error) { return NewKey("hat-") }

// hostPortOf is the URL's host:port (for messages).
func hostPortOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if u.Port() != "" {
		return net.JoinHostPort(u.Hostname(), u.Port())
	}
	return u.Hostname()
}

// LoopbackNote explains how a container reaches a loopback MCP URL.
func LoopbackNote(raw, engine string) string {
	if !agent.IsLoopbackURL(raw) {
		return ""
	}
	return fmt.Sprintf("%s is this computer; containers reach it as %s, keeping the original Host header.", hostPortOf(raw), EngineHostAlias(engine))
}
