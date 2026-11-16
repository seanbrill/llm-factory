package agent

// The live MCP server list. It starts from the config (harness.json "mcp"
// plus HARNESS_MCP) and changes while the harness runs (the admin API and the
// /mcp chat commands). Run-time changes are saved to <StateDir>/mcp.json
// (0600) and take effect on the next request; nothing restarts.
//
// Servers connect lazily: a request that may use tools connects the servers
// not connected yet (each with a short timeout). A server that fails is
// reported unavailable, logged once, and tried again on a later request after
// a backoff; the harness keeps serving with its other tools.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// StateDirName is the folder in the workspace that holds the harness's
// run-time state when HARNESS_STATE_DIR isn't set. The files tools refuse it.
const StateDirName = ".llm-harness"

// ManyMCPTools: a server added at run time with more tools than this starts
// with none selected (the user picks), since every offered tool's schema
// rides along in every request.
const ManyMCPTools = 15

const (
	mcpFirstBackoff = 30 * time.Second
	mcpMaxBackoff   = 5 * time.Minute
)

// MCP server states in MCPStatus.Status.
const (
	MCPPending     = "pending"     // not connected yet (connects on the next request)
	MCPConnected   = "connected"   // its allowed tools are offered
	MCPUnavailable = "unavailable" // the last attempt failed; tried again after RetryAt
	MCPDisabled    = "disabled"    // turned off
)

// MCPToolStatus is one tool of a server.
type MCPToolStatus struct {
	Name         string `json:"name"`          // the server's name for it
	RegisteredAs string `json:"registered_as"` // the name the model calls
	Description  string `json:"description"`
	Tokens       int    `json:"tokens"` // estimated schema cost
	Allowed      bool   `json:"allowed"`
	Pinned       bool   `json:"pinned,omitempty"`
}

// MCPStatus describes a server without its secrets.
type MCPStatus struct {
	Name         string     `json:"name"`
	Transport    string     `json:"transport"` // http | stdio
	Endpoint     string     `json:"endpoint"`  // scheme://host:port, or stdio:<program>
	ConnectVia   string     `json:"connect_via,omitempty"`
	Headers      []string   `json:"headers,omitempty"` // names only
	Source       string     `json:"source"`            // config | runtime
	Enabled      bool       `json:"enabled"`
	Status       string     `json:"status"`
	Error        string     `json:"error,omitempty"`
	RetryAt      *time.Time `json:"retry_at,omitempty"`
	ToolsOffered int        `json:"tools_offered"` // tools the server has (as of the last connect)
	ToolsAllowed []string   `json:"tools_allowed"` // the allowlist: null = all
	ToolCount    int        `json:"tool_count"`    // tools the model gets
	SchemaTokens int        `json:"schema_tokens"` // their estimated cost in full
	// Load, Pinned: as configured. OnDemand: its tools reach the model
	// through the catalog (ondemand.go). RequestTokens: what they cost each
	// request (catalog lines and pinned schemas when on demand; tool_info's
	// and call_tool's own schemas, once for all servers, aren't included).
	Load          string          `json:"load,omitempty"`
	Pinned        []string        `json:"pinned,omitempty"`
	OnDemand      bool            `json:"on_demand"`
	RequestTokens int             `json:"request_tokens"`
	Tools         []MCPToolStatus `json:"tools,omitempty"`
}

// mcpStateFile is <StateDir>/mcp.json.
type mcpStateFile struct {
	Version int `json:"version"`
	// Servers were added or changed at run time; they override the config's
	// servers of the same name.
	Servers []MCPServer `json:"servers"`
	// Removed names config servers removed at run time.
	Removed []string `json:"removed,omitempty"`
}

type mcpHub struct {
	reg       *Registry
	logf      func(string, ...any)
	alias     string
	statePath string // "" = not saved
	now       func() time.Time
	// onConnect runs after a server connects (the agent logs its tools' cost).
	onConnect func()

	mu      sync.Mutex
	conns   []*mcpConn
	base    []MCPServer // config + env
	state   mcpStateFile
	saveErr string
}

type mcpConn struct {
	hub    *mcpHub
	source string

	cmu sync.Mutex // serializes connecting

	mu         sync.Mutex
	srv        MCPServer
	client     *mcpClient
	catalog    []mcpToolDef
	names      []string // registered tool names
	state      string
	err        string
	retryAt    time.Time
	backoff    time.Duration
	loggedErr  string
	removed    bool
	pickOnNext bool // apply the default selection at the next successful connect
}

func newMCPHub(cfg Config, reg *Registry, logf func(string, ...any)) *mcpHub {
	h := &mcpHub{reg: reg, logf: logf, alias: cfg.HostAlias, now: time.Now, base: cfg.MCP,
		state: mcpStateFile{Version: 1}}
	dir := cfg.StateDir
	if dir == "" && cfg.Workspace != "" {
		if fi, err := os.Stat(cfg.Workspace); err == nil && fi.IsDir() {
			dir = filepath.Join(cfg.Workspace, StateDirName)
		}
	}
	if dir != "" {
		h.statePath = filepath.Join(dir, "mcp.json")
		if data, err := os.ReadFile(h.statePath); err == nil {
			var st mcpStateFile
			if err := json.Unmarshal(data, &st); err != nil {
				logf("mcp: ignoring %s: %v", h.statePath, err)
			} else {
				var ok []MCPServer
				for _, s := range st.Servers {
					if err := s.Validate(); err != nil {
						logf("mcp: ignoring a saved server: %v", err)
						continue
					}
					ok = append(ok, s)
				}
				st.Servers = ok
				st.Version = 1
				h.state = st
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			logf("mcp: can't read %s: %v", h.statePath, err)
		}
	}
	for _, s := range h.effective() {
		h.conns = append(h.conns, h.newConn(s.srv, s.source))
	}
	return h
}

type sourced struct {
	srv    MCPServer
	source string
}

// effective merges the config's servers with the run-time state.
func (h *mcpHub) effective() []sourced {
	removed := map[string]bool{}
	for _, n := range h.state.Removed {
		removed[n] = true
	}
	var out []sourced
	for _, s := range h.base {
		if !removed[s.Name] {
			out = append(out, sourced{s, "config"})
		}
	}
	for _, s := range h.state.Servers {
		replaced := false
		for i := range out {
			if out[i].srv.Name == s.Name {
				out[i] = sourced{s, "runtime"}
				replaced = true
			}
		}
		if !replaced {
			out = append(out, sourced{s, "runtime"})
		}
	}
	return out
}

func (h *mcpHub) newConn(s MCPServer, source string) *mcpConn {
	c := &mcpConn{hub: h, srv: s, source: source, state: MCPPending}
	if !s.On() {
		c.state = MCPDisabled
	}
	return c
}

// Saved reports whether run-time changes are saved, and the save problem.
func (h *mcpHub) saved() (bool, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.statePath != "" && h.saveErr == "", h.saveErr
}

func (h *mcpHub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.conns)
}

func (h *mcpHub) find(name string) *mcpConn {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.conns {
		if c.name() == name {
			return c
		}
	}
	return nil
}

func (h *mcpHub) snapshot() []*mcpConn {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*mcpConn{}, h.conns...)
}

// ensure connects every enabled server that isn't connected and isn't
// waiting out a backoff, concurrently.
func (h *mcpHub) ensure(ctx context.Context) {
	var wg sync.WaitGroup
	for _, c := range h.snapshot() {
		if !c.needsConnect() {
			continue
		}
		wg.Add(1)
		go func(c *mcpConn) {
			defer wg.Done()
			c.connect(ctx, false)
		}(c)
	}
	wg.Wait()
}

// save writes the run-time state (0600). Callers hold h.mu.
func (h *mcpHub) saveLocked() {
	if h.statePath == "" {
		return
	}
	err := func() error {
		data, err := json.MarshalIndent(h.state, "", "  ")
		if err != nil {
			return err
		}
		dir := filepath.Dir(h.statePath)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		tmp, err := os.CreateTemp(dir, ".mcp-*.json")
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
		return os.Rename(name, h.statePath)
	}()
	if err != nil {
		h.saveErr = "can't save run-time changes: " + err.Error()
		h.logf("mcp: %s", h.saveErr)
		return
	}
	h.saveErr = ""
}

// remember records a server's current settings as a run-time change.
func (h *mcpHub) rememberLocked(s MCPServer) {
	for i := range h.state.Servers {
		if h.state.Servers[i].Name == s.Name {
			h.state.Servers[i] = s
			h.saveLocked()
			return
		}
	}
	h.state.Servers = append(h.state.Servers, s)
	h.saveLocked()
}

// errMCPExists, errMCPUnknown are the admin API's 409 and 404.
var (
	errMCPExists  = errors.New("an MCP server of that name exists already; remove it first")
	errMCPUnknown = errors.New("there is no MCP server of that name")
)

// add adds a server at run time and connects it right away. With no tools
// given, the selection follows the default rule (all tools when there are
// at most ManyMCPTools, otherwise none) once the tool list is known.
func (h *mcpHub) add(ctx context.Context, s MCPServer, toolsGiven bool) (MCPStatus, error) {
	if s.ConnectVia == "" && s.URL != "" && h.alias != "" && IsLoopbackURL(s.URL) {
		// Inside a container, 127.0.0.1 is the container itself.
		s.ConnectVia = h.alias
	}
	if err := s.Validate(); err != nil {
		return MCPStatus{}, err
	}
	if !toolsGiven {
		s.Tools = []string{} // none until the tool list is known
	}
	h.mu.Lock()
	for _, c := range h.conns {
		if c.name() == s.Name {
			h.mu.Unlock()
			return MCPStatus{}, errMCPExists
		}
	}
	c := h.newConn(s, "runtime")
	c.pickOnNext = !toolsGiven
	h.conns = append(h.conns, c)
	h.rememberLocked(s)
	h.mu.Unlock()
	h.logf("mcp: added %s (%s) at run time", s.Name, s.Endpoint())
	if s.On() {
		c.connect(ctx, true)
	}
	return c.status(true), nil
}

// remove drops a server: its tools leave the registry at once.
func (h *mcpHub) remove(name string) error {
	h.mu.Lock()
	var c *mcpConn
	for i, x := range h.conns {
		if x.name() == name {
			c = x
			h.conns = append(h.conns[:i], h.conns[i+1:]...)
			break
		}
	}
	if c == nil {
		h.mu.Unlock()
		return errMCPUnknown
	}
	kept := h.state.Servers[:0]
	for _, s := range h.state.Servers {
		if s.Name != name {
			kept = append(kept, s)
		}
	}
	h.state.Servers = kept
	for _, s := range h.base {
		if s.Name == name {
			h.state.Removed = append(h.state.Removed, name)
		}
	}
	h.saveLocked()
	h.mu.Unlock()
	c.shutdown()
	h.logf("mcp: removed %s", name)
	return nil
}

// update changes a server's settings (the allowlist, enabled) and re-offers
// its tools.
func (h *mcpHub) update(name string, fn func(*MCPServer)) (MCPStatus, error) {
	c := h.find(name)
	if c == nil {
		return MCPStatus{}, errMCPUnknown
	}
	c.mu.Lock()
	s := c.srv
	fn(&s)
	c.srv = s
	c.pickOnNext = false
	wasOn := c.state != MCPDisabled
	if !s.On() {
		c.state = MCPDisabled
	} else if !wasOn {
		c.state = MCPPending
		c.retryAt = time.Time{}
	}
	c.mu.Unlock()
	h.mu.Lock()
	h.rememberLocked(s)
	h.mu.Unlock()
	if !s.On() {
		c.disconnect()
	} else {
		c.reregister()
	}
	return c.status(true), nil
}

// test reconnects a server now (closing its current session).
func (h *mcpHub) test(ctx context.Context, name string) (MCPStatus, error) {
	c := h.find(name)
	if c == nil {
		return MCPStatus{}, errMCPUnknown
	}
	c.disconnect()
	c.mu.Lock()
	if c.state != MCPDisabled {
		c.state = MCPPending
	}
	c.mu.Unlock()
	c.connect(ctx, true)
	return c.status(true), nil
}

func (h *mcpHub) list(detail bool) []MCPStatus {
	out := []MCPStatus{}
	for _, c := range h.snapshot() {
		out = append(out, c.status(detail))
	}
	return out
}

// ---- one server ------------------------------------------------------------------

func (c *mcpConn) name() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.srv.Name
}

func (c *mcpConn) needsConnect() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.removed || c.client != nil || !c.srv.On() {
		return false
	}
	return c.retryAt.IsZero() || !c.hub.now().Before(c.retryAt)
}

// via is the effective connect_via: the configured one, else the host alias
// for a loopback URL.
func (c *mcpConn) via(s MCPServer) string {
	if s.ConnectVia != "" {
		return s.ConnectVia
	}
	if s.URL != "" && c.hub.alias != "" && IsLoopbackURL(s.URL) {
		return c.hub.alias
	}
	return ""
}

// connect opens a session and offers the allowed tools. force ignores the
// backoff.
func (c *mcpConn) connect(ctx context.Context, force bool) {
	c.cmu.Lock()
	defer c.cmu.Unlock()
	c.mu.Lock()
	if c.removed || c.client != nil || !c.srv.On() || (!force && !c.retryAt.IsZero() && c.hub.now().Before(c.retryAt)) {
		c.mu.Unlock()
		return
	}
	s := c.srv
	c.mu.Unlock()

	timeout := mcpConnectTimeout
	if s.TimeoutS > 0 {
		timeout = time.Duration(s.TimeoutS) * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	client, tools, err := connectMCP(cctx, s, c.via(s))
	cancel()

	c.mu.Lock()
	if c.removed {
		c.mu.Unlock()
		if client != nil {
			client.close()
		}
		return
	}
	if err != nil {
		msg := s.redactErr(err)
		if errors.Is(err, context.DeadlineExceeded) {
			msg = fmt.Sprintf("no answer within %s", timeout)
		}
		if c.backoff == 0 {
			c.backoff = mcpFirstBackoff
		} else if c.backoff *= 2; c.backoff > mcpMaxBackoff {
			c.backoff = mcpMaxBackoff
		}
		c.state, c.err, c.retryAt = MCPUnavailable, msg, c.hub.now().Add(c.backoff)
		logIt := msg != c.loggedErr
		c.loggedErr = msg
		backoff := c.backoff
		c.mu.Unlock()
		c.unregister()
		if logIt {
			c.hub.logf("mcp: %s (%s) is unavailable: %s; trying again in %s", s.Name, s.Endpoint(), msg, backoff)
		}
		return
	}
	c.client, c.catalog = client, tools
	c.state, c.err, c.retryAt, c.backoff, c.loggedErr = MCPConnected, "", time.Time{}, 0, ""
	pick := c.pickOnNext
	c.pickOnNext = false
	if pick {
		if len(tools) <= ManyMCPTools {
			c.srv.Tools = nil
		} else {
			c.srv.Tools = []string{}
		}
		s = c.srv
	}
	c.mu.Unlock()
	if pick {
		c.hub.mu.Lock()
		c.hub.rememberLocked(s)
		c.hub.mu.Unlock()
	}
	c.reregister()
	st := c.status(false)
	c.hub.logf("mcp: %s (%s) connected: %d tools, %d offered (≈%d tokens)", s.Name, s.Endpoint(), len(tools), st.ToolCount, st.SchemaTokens)
	if c.hub.onConnect != nil {
		c.hub.onConnect()
	}
}

func allowSet(s MCPServer) map[string]bool {
	if s.Tools == nil {
		return nil
	}
	m := map[string]bool{}
	for _, t := range s.Tools {
		m[t] = true
	}
	return m
}

// reregister offers exactly the allowed tools of a connected server.
func (c *mcpConn) reregister() {
	c.unregister()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil || c.removed || !c.srv.On() {
		return
	}
	allow := allowSet(c.srv)
	pinned := map[string]bool{}
	for _, p := range c.srv.Pinned {
		pinned[p] = true
	}
	cost := 0
	for _, d := range c.catalog {
		if allow == nil || allow[d.Name] || allow[mcpToolName(c.srv.Name, d.Name)] {
			cost += defTokens(c.srv.Name, d)
		}
	}
	onDemand := LoadOnDemand(c.srv.Load, cost)
	for _, d := range c.catalog {
		reg := mcpToolName(c.srv.Name, d.Name)
		if allow != nil && !allow[d.Name] && !allow[reg] {
			continue
		}
		name := reg
		for i := 2; ; i++ {
			if _, taken := c.hub.reg.Lookup(name); !taken {
				break
			}
			sfx := fmt.Sprintf("_%d", i)
			name = truncateUTF8(reg, 64-len(sfx)) + sfx
		}
		t := &mcpTool{conn: c, name: name, remote: d.Name, desc: toolDesc(d.Description), params: toolParams(d.InputSchema),
			onDemand: onDemand && !pinned[d.Name] && !pinned[reg]}
		if err := c.hub.reg.Register(ToolsetMCP, t); err != nil {
			c.hub.logf("mcp: %s: tool %s left out: %v", c.srv.Name, short(d.Name, 64), err)
			continue
		}
		c.names = append(c.names, name)
	}
}

// mcpNoteMax caps a server's instructions in the system note (bytes).
const mcpNoteMax = 4000

// note is the server's instructions for the system note, "" when it gave none.
func (c *mcpConn) note() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil || c.client.instructions == "" {
		return ""
	}
	name := c.srv.Name
	return fmt.Sprintf("The MCP server %q explains how to use its tools (named %s__<tool> here):\n%s",
		name, name, truncateUTF8(c.client.instructions, mcpNoteMax))
}

func (c *mcpConn) unregister() {
	c.mu.Lock()
	names := c.names
	c.names = nil
	c.mu.Unlock()
	for _, n := range names {
		c.hub.reg.Remove(n)
	}
}

// disconnect closes the session and withdraws the tools.
func (c *mcpConn) disconnect() {
	c.mu.Lock()
	client := c.client
	c.client = nil
	c.mu.Unlock()
	c.unregister()
	if client != nil {
		client.close()
	}
}

func (c *mcpConn) shutdown() {
	c.mu.Lock()
	c.removed = true
	c.mu.Unlock()
	c.disconnect()
}

// broken marks a session that failed in use: the next request reconnects.
func (c *mcpConn) broken(client *mcpClient, msg string) {
	c.mu.Lock()
	if c.client != client {
		c.mu.Unlock()
		return
	}
	c.client = nil
	c.state, c.err, c.retryAt = MCPUnavailable, msg, time.Time{}
	c.mu.Unlock()
	client.close()
}

// call runs a tool, reconnecting once when the server forgot the session.
func (c *mcpConn) call(ctx context.Context, tool string, args json.RawMessage) (string, error) {
	c.mu.Lock()
	client, s := c.client, c.srv
	c.mu.Unlock()
	if client == nil {
		return "", fmt.Errorf("the MCP server %s isn't connected right now; try again shortly", s.Name)
	}
	if s.TimeoutS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(s.TimeoutS)*time.Second)
		defer cancel()
	}
	text, err := client.callTool(ctx, tool, args)
	if errors.Is(err, errSessionGone) {
		c.broken(client, "the session expired")
		c.connect(ctx, true)
		c.mu.Lock()
		client = c.client
		c.mu.Unlock()
		if client == nil {
			return "", fmt.Errorf("the MCP server %s ended the session and can't be reached again", s.Name)
		}
		text, err = client.callTool(ctx, tool, args)
	}
	if err != nil {
		var te *transportError
		if errors.As(err, &te) {
			msg := s.redactErr(err)
			c.broken(client, msg)
			c.hub.logf("mcp: %s (%s) failed in use: %s; reconnecting on the next request", s.Name, s.Endpoint(), msg)
			return "", fmt.Errorf("the MCP server %s can't be reached: %s", s.Name, msg)
		}
		var re *rpcError
		if errors.As(err, &re) {
			return "", fmt.Errorf("the MCP server refused the call: %s", s.redact(re.Message))
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return "", err
		}
		return "", errors.New(s.redact(err.Error()))
	}
	return text, nil
}

func (c *mcpConn) status(detail bool) MCPStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.srv
	st := MCPStatus{
		Name: s.Name, Transport: s.Transport(), Endpoint: s.Endpoint(), ConnectVia: c.via(s),
		Headers: s.HeaderNames(), Source: c.source, Enabled: s.On(), Status: c.state, Error: c.err,
		ToolsOffered: len(c.catalog), ToolsAllowed: s.Tools,
	}
	if len(st.Headers) == 0 {
		st.Headers = nil
	}
	if c.state == MCPUnavailable && !c.retryAt.IsZero() {
		t := c.retryAt
		st.RetryAt = &t
	}
	allow := allowSet(s)
	pinned := map[string]bool{}
	for _, p := range s.Pinned {
		pinned[p] = true
	}
	for _, d := range c.catalog {
		reg := mcpToolName(s.Name, d.Name)
		ok := allow == nil || allow[d.Name] || allow[reg]
		tok := defTokens(s.Name, d)
		if ok {
			st.ToolCount++
			st.SchemaTokens += tok
		}
		if detail {
			st.Tools = append(st.Tools, MCPToolStatus{Name: d.Name, RegisteredAs: reg, Description: short(d.Description, 300), Tokens: tok,
				Allowed: ok, Pinned: pinned[d.Name] || pinned[reg]})
		}
	}
	st.Load, st.Pinned = s.Load, s.Pinned
	st.OnDemand = LoadOnDemand(s.Load, st.SchemaTokens)
	st.RequestTokens = st.SchemaTokens
	if st.OnDemand {
		st.RequestTokens = 0
		for _, d := range c.catalog {
			reg := mcpToolName(s.Name, d.Name)
			if allow != nil && !allow[d.Name] && !allow[reg] {
				continue
			}
			if pinned[d.Name] || pinned[reg] {
				st.RequestTokens += defTokens(s.Name, d)
			} else {
				st.RequestTokens += CatalogLineTokens(s.Name, d.Name, d.Description)
			}
		}
	}
	return st
}

// ProbeMCP connects to a server once, lists its tools and disconnects: the
// factory's Test button. via is the connect_via to use ("" = the server's).
func ProbeMCP(ctx context.Context, s MCPServer) ([]MCPToolStatus, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	timeout := mcpConnectTimeout
	if s.TimeoutS > 0 {
		timeout = time.Duration(s.TimeoutS) * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client, tools, err := connectMCP(cctx, s, s.ConnectVia)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("no answer within %s", timeout)
		}
		return nil, errors.New(s.redactErr(err))
	}
	client.close()
	out := make([]MCPToolStatus, 0, len(tools))
	for _, d := range tools {
		out = append(out, MCPToolStatus{Name: d.Name, RegisteredAs: mcpToolName(s.Name, d.Name),
			Description: short(d.Description, 300), Tokens: defTokens(s.Name, d), Allowed: true})
	}
	return out, nil
}

// joinNames lists names for a message, at most n of them.
func joinNames(names []string, n int) string {
	if len(names) <= n {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:n], ", ") + fmt.Sprintf(" and %d more", len(names)-n)
}
