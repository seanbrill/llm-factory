package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// Tool is one function the model can call.
type Tool interface {
	// Name is the function name the model calls ([A-Za-z0-9_-], at most 64).
	Name() string
	// Description tells the model what the tool does and when to use it.
	Description() string
	// Parameters is the JSON Schema of the arguments object.
	Parameters() json.RawMessage
	// Run executes one call. args is the JSON arguments object the model sent.
	// The returned text goes back to the model; an error is reported to the
	// model as the result. ctx carries the per-tool timeout.
	Run(ctx context.Context, args json.RawMessage) (string, error)
}

// TrustedTool is implemented by tools whose output is operator-provided
// instructions (skills) rather than outside data. Other tools' output reaches
// the model wrapped as untrusted data.
type TrustedTool interface {
	TrustedOutput() bool
}

// Noter is implemented by tools that add a line to the harness note in the
// system prompt (for example the list of installed skills).
type Noter interface {
	SystemNote() string
}

// FuncTool adapts a function to Tool.
type FuncTool struct {
	ToolName string
	Desc     string
	Params   json.RawMessage
	Fn       func(ctx context.Context, args json.RawMessage) (string, error)
	Trusted  bool
}

func (t *FuncTool) Name() string                { return t.ToolName }
func (t *FuncTool) Description() string         { return t.Desc }
func (t *FuncTool) Parameters() json.RawMessage { return t.Params }
func (t *FuncTool) TrustedOutput() bool         { return t.Trusted }
func (t *FuncTool) Run(ctx context.Context, args json.RawMessage) (string, error) {
	return t.Fn(ctx, args)
}

var toolNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ToolInfo describes a registered tool.
type ToolInfo struct {
	Name        string `json:"name"`
	Toolset     string `json:"toolset"`
	Description string `json:"description"`
}

// Unavailable is a tool the config turns on that can't run on this host.
type Unavailable struct {
	Tool   string `json:"tool"`
	Reason string `json:"reason"`
}

// Registry holds the tools an Agent offers. It is safe for concurrent use, so
// a host can add tools (an ensemble member, an MCP server) while serving.
type Registry struct {
	mu      sync.RWMutex
	entries []regEntry
}

type regEntry struct {
	group string
	tool  Tool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{} }

// Register adds a tool under a toolset name (group). Names must be unique.
func (r *Registry) Register(group string, t Tool) error {
	if t == nil {
		return errors.New("register: nil tool")
	}
	name := t.Name()
	if !toolNameRe.MatchString(name) {
		return fmt.Errorf("register: bad tool name %q", name)
	}
	params := t.Parameters()
	if len(params) == 0 || !json.Valid(params) {
		return fmt.Errorf("register %s: parameters must be a JSON Schema object", name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.entries {
		if e.tool.Name() == name {
			return fmt.Errorf("register: tool %q already registered", name)
		}
	}
	r.entries = append(r.entries, regEntry{group: group, tool: t})
	return nil
}

// Remove drops a tool by name (no-op when absent).
func (r *Registry) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, e := range r.entries {
		if e.tool.Name() == name {
			r.entries = append(r.entries[:i], r.entries[i+1:]...)
			return
		}
	}
}

// Lookup returns a tool by name.
func (r *Registry) Lookup(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, e := range r.entries {
		if e.tool.Name() == name {
			return e.tool, true
		}
	}
	return nil, false
}

// Tools returns the registered tools in registration order.
func (r *Registry) Tools() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, len(r.entries))
	for i, e := range r.entries {
		out[i] = e.tool
	}
	return out
}

// Describe lists the registered tools.
func (r *Registry) Describe() []ToolInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ToolInfo, len(r.entries))
	for i, e := range r.entries {
		out[i] = ToolInfo{Name: e.tool.Name(), Toolset: e.group, Description: e.tool.Description()}
	}
	return out
}

// groupOf returns the toolset a registered tool was added under.
func (r *Registry) groupOf(name string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, e := range r.entries {
		if e.tool.Name() == name {
			return e.group
		}
	}
	return ""
}

// Schema renders a tool as an OpenAI "tools" entry.
func Schema(t Tool) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        t.Name(),
			"description": t.Description(),
			"parameters":  t.Parameters(),
		},
	})
	return b
}

// ---------------------------------------------------------------------------
// Argument helpers. Small models often send numbers and booleans as strings,
// so the built-in tools read them leniently.
// ---------------------------------------------------------------------------

// flexInt reads 5, 5.0 or "5".
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		return nil
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("%s is not a number", string(b))
	}
	*f = flexInt(n)
	return nil
}

// flexBool reads true or "true".
type flexBool bool

func (f *flexBool) UnmarshalJSON(b []byte) error {
	s := strings.ToLower(strings.Trim(strings.TrimSpace(string(b)), `"`))
	switch s {
	case "true", "1", "yes":
		*f = true
	case "false", "0", "no", "", "null":
		*f = false
	default:
		return fmt.Errorf("%s is not true or false", string(b))
	}
	return nil
}

// decodeArgs reads a tool's JSON arguments into v.
func decodeArgs(args json.RawMessage, v any) error {
	if len(strings.TrimSpace(string(args))) == 0 {
		args = json.RawMessage("{}")
	}
	if err := json.Unmarshal(args, v); err != nil {
		return fmt.Errorf("bad arguments: %v", err)
	}
	return nil
}

// truncateUTF8 cuts s to at most n bytes without splitting a character.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 0 {
		return ""
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
