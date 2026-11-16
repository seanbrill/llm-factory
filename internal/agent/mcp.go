package agent

// MCP client: the "mcp" toolset gives the model the tools of MCP servers.
// Each server is reached over Streamable HTTP (a URL) or stdio (a command run
// in the container). Servers come from the config (harness.json "mcp" plus
// HARNESS_MCP) and can be added, changed and removed while the harness runs
// (mcphub.go: the admin API and the /mcp chat commands).
//
// Secrets: an MCP URL may carry a secret in its path or query (knitted's
// does), and headers often carry keys. Neither is ever logged, reported in
// /v1/harness or put in an error message: servers are named by their name and
// scheme://host:port only.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// MCPServer is one MCP server the harness connects to.
type MCPServer struct {
	// Name identifies the server ([a-z0-9_-], 1 to 32 characters). Its tools
	// reach the model as "<name>__<tool>".
	Name string `json:"name"`
	// URL is a Streamable HTTP endpoint. Either URL or Command is set.
	URL string `json:"url,omitempty"`
	// Command, Args and Env start a stdio server in the container (only when
	// the command exists there).
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	// Headers are sent with every HTTP request (for example a key).
	Headers map[string]string `json:"headers,omitempty"`
	// ConnectVia is the host[:port] dialed instead of the URL's host, keeping
	// the URL's Host header: how a container reaches a server listening on
	// its host's 127.0.0.1 (host.docker.internal:64182).
	ConnectVia string `json:"connect_via,omitempty"`
	// Tools is the allowlist of the server's tools the model gets (their MCP
	// names): null = all of them, [] = none.
	Tools []string `json:"tools"`
	// TimeoutS bounds connecting and each tool call, in seconds (0 = the
	// defaults: 10 s to connect, limits.tool_timeout per call).
	TimeoutS int `json:"timeout_s,omitempty"`
	// Enabled false keeps the server configured but unused (nil = on).
	Enabled *bool `json:"enabled,omitempty"`
	// Load is how its tools reach the model: "full" sends every allowed
	// tool's whole schema in each request; "on_demand" sends a one-line
	// catalog, and the model reads a tool's arguments with tool_info and runs
	// it with call_tool; "auto" ("" too) is on demand when the allowed tools'
	// schemas cost more than OnDemandOver tokens.
	Load string `json:"load,omitempty"`
	// Pinned tools (MCP names) are always sent in full, even on demand: the
	// few a model uses most.
	Pinned []string `json:"pinned,omitempty"`
}

// On reports whether the server is enabled.
func (s MCPServer) On() bool { return s.Enabled == nil || *s.Enabled }

// Transport is "http" or "stdio".
func (s MCPServer) Transport() string {
	if s.Command != "" {
		return "stdio"
	}
	return "http"
}

// Endpoint names the server without secrets: scheme://host:port for a URL,
// the program's name for a command.
func (s MCPServer) Endpoint() string {
	if s.Command != "" {
		return "stdio:" + filepath.Base(s.Command)
	}
	return RedactURL(s.URL)
}

// HeaderNames lists the header names (never their values), sorted.
func (s MCPServer) HeaderNames() []string {
	out := make([]string, 0, len(s.Headers))
	for k := range s.Headers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// RedactURL keeps only scheme://host:port of a URL ("" if it isn't one).
func RedactURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "(invalid URL)"
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	return u.Scheme + "://" + net.JoinHostPort(host, port)
}

// IsLoopbackURL reports whether a URL points at the local host (127.0.0.0/8,
// ::1 or localhost).
func IsLoopbackURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return isLoopbackHost(u.Hostname())
}

func isLoopbackHost(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

var (
	mcpNameRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	headerNameRe = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]{1,128}$")
	hostNameRe   = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
)

const maxMCPTimeout = 1800

// validHostPort accepts host or host:port (a name or an IP literal).
func validHostPort(s string) bool {
	host, port := s, ""
	if h, p, err := net.SplitHostPort(s); err == nil {
		host, port = h, p
	}
	if port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	host = strings.Trim(host, "[]")
	return net.ParseIP(host) != nil || hostNameRe.MatchString(host)
}

// Validate checks one server.
func (s MCPServer) Validate() error {
	var errs []string
	add := func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) }
	if !mcpNameRe.MatchString(s.Name) {
		add("name %q must be 1 to 32 of a-z, 0-9, _ and -, starting with a letter or digit", s.Name)
	}
	switch {
	case s.URL != "" && s.Command != "":
		add("give a url or a command, not both")
	case s.URL == "" && s.Command == "":
		add("give a url (Streamable HTTP) or a command (stdio)")
	case s.URL != "":
		u, err := url.Parse(s.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			add("url must be an http(s) URL")
		}
	}
	if s.ConnectVia != "" && !validHostPort(s.ConnectVia) {
		add("connect_via must be host or host:port")
	}
	if s.ConnectVia != "" && s.URL == "" {
		add("connect_via needs a url")
	}
	for k, v := range s.Headers {
		if !headerNameRe.MatchString(k) {
			add("header name %q is not valid", k)
		}
		if strings.ContainsAny(v, "\r\n\x00") {
			add("header %s has a line break in its value", k)
		}
	}
	for k := range s.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") {
			add("env names a bad variable %q", k)
		}
	}
	for _, t := range s.Tools {
		if strings.TrimSpace(t) == "" {
			add("tools has an empty name")
			break
		}
	}
	if !ValidLoad(s.Load) {
		add("load must be auto, full or on_demand")
	}
	if s.TimeoutS < 0 || s.TimeoutS > maxMCPTimeout {
		add("timeout_s must be 0 to %d", maxMCPTimeout)
	}
	if len(errs) == 0 {
		return nil
	}
	name := s.Name
	if !mcpNameRe.MatchString(name) {
		name = "?"
	}
	return fmt.Errorf("mcp server %s: %s", name, strings.Join(errs, "; "))
}

// ValidateMCP checks a server list: every server, and unique names.
func ValidateMCP(list []MCPServer) error {
	var errs []string
	seen := map[string]bool{}
	for _, s := range list {
		if err := s.Validate(); err != nil {
			errs = append(errs, err.Error())
		}
		if seen[s.Name] {
			errs = append(errs, fmt.Sprintf("mcp server %s is listed twice", s.Name))
		}
		seen[s.Name] = true
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// ParseMCPServers reads a JSON array of servers (or a single server object).
// Unknown fields are an error, so a typo is caught.
func ParseMCPServers(s string) ([]MCPServer, error) {
	s = strings.TrimSpace(s)
	var list []MCPServer
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	if strings.HasPrefix(s, "{") {
		var one MCPServer
		if err := dec.Decode(&one); err != nil {
			return nil, fmt.Errorf("not a server object: %v", err)
		}
		list = []MCPServer{one}
	} else if err := dec.Decode(&list); err != nil {
		return nil, fmt.Errorf("not a JSON array of servers: %v", err)
	}
	return list, ValidateMCP(list)
}

// MergeMCP returns base with over merged in by name: a server in over
// replaces base's server of that name, others are added.
func MergeMCP(base, over []MCPServer) []MCPServer {
	out := append([]MCPServer{}, base...)
	for _, s := range over {
		replaced := false
		for i := range out {
			if out[i].Name == s.Name {
				out[i], replaced = s, true
			}
		}
		if !replaced {
			out = append(out, s)
		}
	}
	return out
}

// EstimateSchemaTokens is the rough context cost of offering a tool: the bytes
// of its name, description and parameter schema over 4.
func EstimateSchemaTokens(name, desc string, params []byte) int {
	return (len(name) + len(desc) + len(params) + 3) / 4
}

// ToolTokens is EstimateSchemaTokens for a Tool.
func ToolTokens(t Tool) int { return EstimateSchemaTokens(t.Name(), t.Description(), t.Parameters()) }

// ---------------------------------------------------------------------------
// Redaction
// ---------------------------------------------------------------------------

// secretsOf lists the parts of a server's settings that must never appear in
// a log or a message: the URL's path, query and user info, the header values
// and the env values.
func (s MCPServer) secretsOf() []string {
	var out []string
	if u, err := url.Parse(s.URL); err == nil && s.URL != "" {
		out = append(out, s.URL)
		if u.User != nil {
			out = append(out, u.User.String())
		}
		if p := u.EscapedPath(); len(p) > 1 {
			out = append(out, p, u.Path)
		}
		if u.RawQuery != "" {
			out = append(out, u.RawQuery)
		}
	}
	for _, v := range s.Headers {
		out = append(out, v)
	}
	for _, v := range s.Env {
		out = append(out, v)
	}
	return out
}

// redactErr turns an error into text without the server's secrets: Go's
// url.Error repeats the whole URL, which is dropped, and anything else that
// still contains a secret is masked.
func (s MCPServer) redactErr(err error) string {
	if err == nil {
		return ""
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return s.redact(err.Error())
}

func (s MCPServer) redact(text string) string {
	secrets := s.secretsOf()
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, sec := range secrets {
		if len(sec) >= 4 {
			text = strings.ReplaceAll(text, sec, "…")
		}
	}
	return short(text, 300)
}

// ---------------------------------------------------------------------------
// JSON-RPC over a transport
// ---------------------------------------------------------------------------

// mcpProtocolVersion is the MCP revision the client asks for; it accepts what
// the server answers.
const mcpProtocolVersion = "2025-06-18"

const (
	mcpConnectTimeout = 10 * time.Second
	mcpMaxMessage     = 16 << 20
	mcpMaxTools       = 1000
	mcpMaxDescBytes   = 1024
)

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("error %d: %s", e.Code, e.Message) }

// rpcMsg is any JSON-RPC message: a request, a notification or a response.
type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func (m rpcMsg) isResponse() bool { return m.Method == "" && len(m.ID) > 0 }

// errSessionGone: the server forgot the session (HTTP 404 with a session id).
var errSessionGone = errors.New("the MCP session expired")

// transportError marks a failure of the connection itself (as opposed to an
// answer from the server): the client reconnects before the next use.
type transportError struct{ err error }

func (e *transportError) Error() string { return e.err.Error() }
func (e *transportError) Unwrap() error { return e.err }

type mcpTransport interface {
	request(ctx context.Context, method string, params any) (json.RawMessage, error)
	notify(ctx context.Context, method string, params any) error
	setVersion(v string)
	close()
}

func rpcBody(id int64, method string, params any) []byte {
	m := map[string]any{"jsonrpc": "2.0", "method": method}
	if id > 0 {
		m["id"] = id
	}
	if params != nil {
		m["params"] = params
	}
	b, _ := json.Marshal(m)
	return b
}

// reply answers a request the server sent us: ping gets {}, anything else
// "method not found" (this client offers no sampling, roots or elicitation).
func reply(m rpcMsg) []byte {
	out := map[string]any{"jsonrpc": "2.0", "id": m.ID}
	if m.Method == "ping" {
		out["result"] = map[string]any{}
	} else {
		out["error"] = map[string]any{"code": -32601, "message": "method not supported by this client"}
	}
	b, _ := json.Marshal(out)
	return b
}

// ---- Streamable HTTP ----------------------------------------------------------

type mcpHTTP struct {
	srv    MCPServer
	url    string
	client *http.Client
	nextID atomic.Int64

	mu      sync.Mutex
	session string
	version string
}

// dialAddr is where a connection to the URL's host:port goes: ConnectVia
// (its port defaulting to the URL's) or the URL's own address.
func dialAddr(rawURL, via string) (target, dial string) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", ""
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	target = net.JoinHostPort(u.Hostname(), port)
	if via == "" {
		return target, target
	}
	if _, _, err := net.SplitHostPort(via); err == nil {
		return target, via
	}
	return target, net.JoinHostPort(strings.Trim(via, "[]"), port)
}

// mcpHTTPClient is an HTTP client for one MCP server: no proxy, and
// connections to the URL's host:port dialed at via when it is set (the
// request keeps the URL's Host header and TLS name).
func mcpHTTPClient(rawURL, via string) *http.Client {
	target, dial := dialAddr(rawURL, via)
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		Proxy:               nil,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr == target && dial != "" {
				addr = dial
			}
			return d.DialContext(ctx, network, addr)
		},
	}
	return &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse // a redirect could carry the secret path elsewhere
	}}
}

func newMCPHTTP(srv MCPServer, via string) *mcpHTTP {
	return &mcpHTTP{srv: srv, url: srv.URL, client: mcpHTTPClient(srv.URL, via)}
}

func (t *mcpHTTP) setVersion(v string) {
	t.mu.Lock()
	t.version = v
	t.mu.Unlock()
}

func (t *mcpHTTP) post(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("bad url")
	}
	req.ContentLength = int64(len(body))
	for k, v := range t.srv.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	t.mu.Lock()
	if t.session != "" {
		req.Header.Set("Mcp-Session-Id", t.session)
	}
	if t.version != "" {
		req.Header.Set("MCP-Protocol-Version", t.version)
	}
	t.mu.Unlock()
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, &transportError{err}
	}
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.mu.Lock()
		t.session = sid
		t.mu.Unlock()
	}
	return resp, nil
}

// httpStatusErr explains a non-2xx answer (with the JSON-RPC error message
// when the body carries one, never the raw body).
func httpStatusErr(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var m rpcMsg
	if json.Unmarshal(b, &m) == nil && m.Error != nil {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, short(m.Error.Message, 200))
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("HTTP %d: the server refused the request (check the URL and its headers or key)", resp.StatusCode)
	case http.StatusNotFound:
		return fmt.Errorf("HTTP 404: there is no MCP endpoint at that URL")
	case http.StatusMethodNotAllowed:
		return fmt.Errorf("HTTP 405: the URL doesn't take MCP's POST requests (an old HTTP+SSE server?)")
	}
	return fmt.Errorf("HTTP %d", resp.StatusCode)
}

func (t *mcpHTTP) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := t.nextID.Add(1)
	t.mu.Lock()
	hadSession := t.session != ""
	t.mu.Unlock()
	resp, err := t.post(ctx, rpcBody(id, method, params))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound && hadSession {
		return nil, errSessionGone
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, httpStatusErr(resp)
	}
	want := strconv.FormatInt(id, 10)
	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "text/event-stream") {
		rd := newSSEReader(resp.Body)
		for {
			data, err := rd.next()
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				if err == io.EOF {
					return nil, &transportError{errors.New("the server's event stream ended without an answer")}
				}
				return nil, &transportError{err}
			}
			if m, ok := t.handle(ctx, []byte(data), want); ok {
				return m.result()
			}
		}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, mcpMaxMessage))
	if err != nil {
		return nil, &transportError{err}
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, fmt.Errorf("the server answered %s with an empty body", method)
	}
	if m, ok := t.handle(ctx, b, want); ok {
		return m.result()
	}
	return nil, fmt.Errorf("the server's answer to %s has no response", method)
}

// handle reads one message (or a batch) and returns the response with id
// want. Requests from the server are answered; notifications are ignored.
func (t *mcpHTTP) handle(ctx context.Context, data []byte, want string) (rpcMsg, bool) {
	var msgs []rpcMsg
	data = bytes.TrimSpace(data)
	if bytes.HasPrefix(data, []byte("[")) {
		_ = json.Unmarshal(data, &msgs)
	} else {
		var m rpcMsg
		if json.Unmarshal(data, &m) == nil {
			msgs = []rpcMsg{m}
		}
	}
	for _, m := range msgs {
		switch {
		case m.isResponse():
			if strings.Trim(string(m.ID), `"`) == want {
				return m, true
			}
		case m.Method != "" && len(m.ID) > 0:
			go func(m rpcMsg) {
				cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				defer cancel()
				if resp, err := t.post(cctx, reply(m)); err == nil {
					_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
					resp.Body.Close()
				}
			}(m)
		}
	}
	return rpcMsg{}, false
}

func (m rpcMsg) result() (json.RawMessage, error) {
	if m.Error != nil {
		return nil, m.Error
	}
	if len(m.Result) == 0 {
		return json.RawMessage("{}"), nil
	}
	return m.Result, nil
}

func (t *mcpHTTP) notify(ctx context.Context, method string, params any) error {
	resp, err := t.post(ctx, rpcBody(0, method, params))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s: HTTP %d", method, resp.StatusCode)
	}
	return nil
}

// close ends the session (best effort).
func (t *mcpHTTP) close() {
	t.mu.Lock()
	sid := t.session
	t.mu.Unlock()
	if sid != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if req, err := http.NewRequestWithContext(ctx, http.MethodDelete, t.url, nil); err == nil {
			for k, v := range t.srv.Headers {
				req.Header.Set(k, v)
			}
			req.Header.Set("Mcp-Session-Id", sid)
			if resp, err := t.client.Do(req); err == nil {
				resp.Body.Close()
			}
		}
	}
	t.client.CloseIdleConnections()
}

// ---- stdio ---------------------------------------------------------------------

type mcpStdio struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	wmu    sync.Mutex
	nextID atomic.Int64

	mu      sync.Mutex
	pending map[string]chan rpcMsg
	dead    error
	done    chan struct{}
}

// stdioEnv is the minimal environment a stdio server starts with, plus its
// own variables.
func stdioEnv(extra map[string]string) []string {
	var env []string
	for _, k := range []string{"PATH", "HOME", "LANG", "LC_ALL", "TMPDIR", "TZ", "SYSTEMROOT", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "TEMP", "TMP"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

func startMCPStdio(srv MCPServer) (*mcpStdio, error) {
	bin, err := exec.LookPath(srv.Command)
	if err != nil {
		return nil, fmt.Errorf("the command %q isn't available here", filepath.Base(srv.Command))
	}
	cmd := exec.Command(bin, srv.Args...)
	cmd.Env = stdioEnv(srv.Env)
	cmd.Stderr = io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %v", filepath.Base(srv.Command), errors.Unwrap(err))
	}
	t := &mcpStdio{cmd: cmd, stdin: stdin, pending: map[string]chan rpcMsg{}, done: make(chan struct{})}
	go t.read(stdout)
	return t, nil
}

func (t *mcpStdio) read(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), mcpMaxMessage)
	for sc.Scan() {
		var m rpcMsg
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch {
		case m.isResponse():
			id := strings.Trim(string(m.ID), `"`)
			t.mu.Lock()
			ch := t.pending[id]
			delete(t.pending, id)
			t.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		case m.Method != "" && len(m.ID) > 0:
			_ = t.write(reply(m))
		}
	}
	err := sc.Err()
	if err == nil {
		err = errors.New("the server's process ended")
	}
	t.mu.Lock()
	t.dead = err
	for id, ch := range t.pending {
		close(ch)
		delete(t.pending, id)
	}
	t.mu.Unlock()
	close(t.done)
	_ = t.cmd.Wait()
}

func (t *mcpStdio) write(b []byte) error {
	t.wmu.Lock()
	defer t.wmu.Unlock()
	if _, err := t.stdin.Write(append(b, '\n')); err != nil {
		return &transportError{err}
	}
	return nil
}

func (t *mcpStdio) setVersion(string) {}

func (t *mcpStdio) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := t.nextID.Add(1)
	key := strconv.FormatInt(id, 10)
	ch := make(chan rpcMsg, 1)
	t.mu.Lock()
	if t.dead != nil {
		err := t.dead
		t.mu.Unlock()
		return nil, &transportError{err}
	}
	t.pending[key] = ch
	t.mu.Unlock()
	if err := t.write(rpcBody(id, method, params)); err != nil {
		t.mu.Lock()
		delete(t.pending, key)
		t.mu.Unlock()
		return nil, err
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return nil, &transportError{errors.New("the server's process ended")}
		}
		return m.result()
	case <-ctx.Done():
		t.mu.Lock()
		delete(t.pending, key)
		t.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (t *mcpStdio) notify(ctx context.Context, method string, params any) error {
	return t.write(rpcBody(0, method, params))
}

func (t *mcpStdio) close() {
	_ = t.stdin.Close()
	select {
	case <-t.done:
	case <-time.After(2 * time.Second):
		_ = t.cmd.Process.Kill()
	}
}

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

// mcpToolDef is a tool as the server lists it.
type mcpToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type mcpClient struct {
	srv MCPServer
	t   mcpTransport
	// instructions is what the server's initialize result says about using
	// it (MCP "instructions"), added to the system note with its tools.
	instructions string
}

// connectMCP opens a session: initialize, notifications/initialized, then
// the tool list. via is the effective connect_via.
func connectMCP(ctx context.Context, srv MCPServer, via string) (*mcpClient, []mcpToolDef, error) {
	var t mcpTransport
	if srv.Command != "" {
		st, err := startMCPStdio(srv)
		if err != nil {
			return nil, nil, err
		}
		t = st
	} else {
		t = newMCPHTTP(srv, via)
	}
	c := &mcpClient{srv: srv, t: t}
	if err := c.initialize(ctx); err != nil {
		t.close()
		return nil, nil, err
	}
	tools, err := c.listTools(ctx)
	if err != nil {
		t.close()
		return nil, nil, err
	}
	return c, tools, nil
}

func (c *mcpClient) initialize(ctx context.Context) error {
	res, err := c.t.request(ctx, "initialize", map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "llm-factory-harness", "version": Version},
	})
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	var r struct {
		ProtocolVersion string `json:"protocolVersion"`
		Instructions    string `json:"instructions"`
	}
	_ = json.Unmarshal(res, &r)
	c.instructions = strings.TrimSpace(r.Instructions)
	v := r.ProtocolVersion
	if v == "" {
		v = mcpProtocolVersion
	}
	c.t.setVersion(v)
	if err := c.t.notify(ctx, "notifications/initialized", nil); err != nil {
		return fmt.Errorf("initialized: %w", err)
	}
	return nil
}

func (c *mcpClient) listTools(ctx context.Context) ([]mcpToolDef, error) {
	var out []mcpToolDef
	cursor := ""
	for page := 0; page < 100; page++ {
		var params any
		if cursor != "" {
			params = map[string]any{"cursor": cursor}
		}
		res, err := c.t.request(ctx, "tools/list", params)
		if err != nil {
			return nil, fmt.Errorf("tools/list: %w", err)
		}
		var r struct {
			Tools      []mcpToolDef `json:"tools"`
			NextCursor string       `json:"nextCursor"`
		}
		if err := json.Unmarshal(res, &r); err != nil {
			return nil, errors.New("tools/list: the answer isn't a tool list")
		}
		out = append(out, r.Tools...)
		if r.NextCursor == "" || len(out) >= mcpMaxTools {
			break
		}
		cursor = r.NextCursor
	}
	if len(out) > mcpMaxTools {
		out = out[:mcpMaxTools]
	}
	kept := out[:0]
	for _, t := range out {
		if strings.TrimSpace(t.Name) != "" {
			kept = append(kept, t)
		}
	}
	return kept, nil
}

// callTool runs one tool. A result marked isError comes back as an error
// carrying the tool's text.
func (c *mcpClient) callTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage("{}")
	}
	res, err := c.t.request(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return "", err
	}
	text, isErr := mcpResultText(res)
	if isErr {
		if text == "" {
			text = "the tool failed"
		}
		return "", errors.New(text)
	}
	return text, nil
}

func (c *mcpClient) close() { c.t.close() }

// mcpResultText renders a tools/call result as text: text parts joined,
// other content noted briefly.
func mcpResultText(res json.RawMessage) (string, bool) {
	var r struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			MimeType string `json:"mimeType"`
			Data     string `json:"data"`
			URI      string `json:"uri"`
			Name     string `json:"name"`
			Resource *struct {
				URI      string `json:"uri"`
				MimeType string `json:"mimeType"`
				Text     string `json:"text"`
				Blob     string `json:"blob"`
			} `json:"resource"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return string(res), false
	}
	var parts []string
	for _, c := range r.Content {
		switch c.Type {
		case "text":
			parts = append(parts, c.Text)
		case "image", "audio":
			parts = append(parts, fmt.Sprintf("[%s %s, about %s, not shown]", c.Type, orUnknown(c.MimeType), humanSize(int64(len(c.Data)*3/4))))
		case "resource":
			if c.Resource == nil {
				continue
			}
			if c.Resource.Text != "" {
				parts = append(parts, fmt.Sprintf("[resource %s]\n%s", c.Resource.URI, c.Resource.Text))
			} else {
				parts = append(parts, fmt.Sprintf("[resource %s (%s), binary, not shown]", c.Resource.URI, orUnknown(c.Resource.MimeType)))
			}
		case "resource_link":
			parts = append(parts, fmt.Sprintf("[link %s: %s]", c.Name, c.URI))
		default:
			parts = append(parts, fmt.Sprintf("[%s content, not shown]", c.Type))
		}
	}
	if len(parts) == 0 && len(r.StructuredContent) > 0 && string(r.StructuredContent) != "null" {
		parts = append(parts, string(r.StructuredContent))
	}
	return strings.Join(parts, "\n"), r.IsError
}

func orUnknown(s string) string {
	if s == "" {
		return "of unknown type"
	}
	return s
}

// ---------------------------------------------------------------------------
// Tool names
// ---------------------------------------------------------------------------

var badToolChars = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// mcpToolName is the name a server's tool is offered under:
// "<server>__<tool>", cut to 64 characters (with a short hash when cut).
func mcpToolName(server, tool string) string {
	clean := badToolChars.ReplaceAllString(tool, "_")
	name := server + "__" + clean
	if len(name) <= 64 && clean == tool {
		return name
	}
	sum := sha256.Sum256([]byte(tool))
	suffix := "_" + hex.EncodeToString(sum[:])[:6]
	if len(name)+len(suffix) <= 64 && clean != tool {
		return name + suffix
	}
	if len(name) > 64 {
		return name[:64-len(suffix)] + suffix
	}
	return name
}

// mcpTool is one MCP tool in the registry.
type mcpTool struct {
	conn   *mcpConn
	name   string // as registered
	remote string // the server's name for it
	desc   string
	params json.RawMessage
	// onDemand: the model gets it through the catalog, tool_info and
	// call_tool instead of its schema in every request.
	onDemand bool
}

func (t *mcpTool) Name() string                { return t.name }
func (t *mcpTool) Description() string         { return t.desc }
func (t *mcpTool) Parameters() json.RawMessage { return t.params }

// SystemNote is the server's own instructions (the same for each of its
// tools, so the system note has them once): how to use its tools well, which
// a client like Claude Code also shows its model.
func (t *mcpTool) SystemNote() string { return t.conn.note() }
func (t *mcpTool) Run(ctx context.Context, args json.RawMessage) (string, error) {
	return t.conn.call(ctx, t.remote, args)
}

// toolParams is a server's input schema, or an empty object schema.
func toolParams(schema json.RawMessage) json.RawMessage {
	s := bytes.TrimSpace(schema)
	if len(s) == 0 || s[0] != '{' || !json.Valid(s) {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return json.RawMessage(s)
}

func toolDesc(d string) string {
	d = strings.TrimSpace(d)
	if len(d) > mcpMaxDescBytes {
		d = truncateUTF8(d, mcpMaxDescBytes) + "..."
	}
	return d
}

func defTokens(server string, d mcpToolDef) int {
	return EstimateSchemaTokens(mcpToolName(server, d.Name), toolDesc(d.Description), toolParams(d.InputSchema))
}
