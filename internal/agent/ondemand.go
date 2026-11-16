package agent

// Tools on demand. Every tool's schema rides along in every request, and an
// MCP server can have dozens: knitted's 72 tools cost ≈15.5K tokens, a third of
// a small model's context, before the conversation starts. A server whose
// tools are on demand (MCPServer.Load) is offered instead as a catalog in the
// system note, one line per tool, plus two tools of the harness's own:
//
//	tool_info(names)            a tool's full description and arguments
//	call_tool(name, arguments)  runs a tool from the catalog
//
// The tools array stays the same for the whole conversation, so the server's
// prompt cache keeps working (adding schemas mid-conversation would change
// the start of the prompt and make the server read it all again), and the
// schemas the model asks for arrive at the end of the conversation, where
// they cost nothing to cache. A call_tool that fails returns the tool's
// arguments with the error, so a model that skipped tool_info corrects
// itself on the next step.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// MCP server load modes (MCPServer.Load).
const (
	MCPLoadAuto     = "auto"
	MCPLoadFull     = "full"
	MCPLoadOnDemand = "on_demand"
)

// OnDemandOver: an "auto" server whose allowed tools' schemas cost more than
// this many tokens is offered on demand. Ten average tools stay in full.
const OnDemandOver = 3000

const (
	toolInfoName   = "tool_info"
	callToolName   = "call_tool"
	catalogDescMax = 110  // bytes of a tool's description in its catalog line
	toolInfoMax    = 8000 // bytes of one tool's description and arguments in tool_info
	schemaOnError  = 3000 // bytes of arguments added to a failed call_tool
)

// ValidLoad reports whether s is a load mode ("" is auto).
func ValidLoad(s string) bool {
	switch s {
	case "", MCPLoadAuto, MCPLoadFull, MCPLoadOnDemand:
		return true
	}
	return false
}

// LoadOnDemand reports whether a server's tools go on demand, from its load
// mode and the token cost of its allowed tools' schemas.
func LoadOnDemand(load string, cost int) bool {
	switch load {
	case MCPLoadFull:
		return false
	case MCPLoadOnDemand:
		return true
	}
	return cost > OnDemandOver
}

// catalogDesc is the start of a description: its first sentence, at most
// catalogDescMax bytes.
func catalogDesc(d string) string {
	d = strings.Join(strings.Fields(d), " ")
	if i := strings.Index(d, ". "); i > 0 && i < catalogDescMax {
		return d[:i+1]
	}
	if len(d) > catalogDescMax {
		return strings.TrimRight(truncateUTF8(d, catalogDescMax), " ,;:") + "…"
	}
	return d
}

func catalogLine(name, desc string) string {
	if c := catalogDesc(desc); c != "" {
		return "- " + name + ": " + c
	}
	return "- " + name
}

// CatalogLineTokens estimates the catalog line of an on-demand tool, by the
// server's name and the tool's MCP name and description.
func CatalogLineTokens(server, tool, desc string) int {
	return (len(catalogLine(mcpToolName(server, tool), desc)) + 1 + bytesPerToken - 1) / bytesPerToken
}

// OnDemandBaseTokens is what offering any tools on demand costs on top of
// their catalog lines: tool_info's and call_tool's schemas and the catalog's
// heading.
func OnDemandBaseTokens() int {
	n := (len(catalogHeading) + bytesPerToken - 1) / bytesPerToken
	for _, t := range onDemandTools(nil) {
		n += ToolTokens(t)
	}
	return n
}

const catalogHeading = "More tools, on demand. They aren't in your tool list: call_tool runs them. Before using one for the " +
	"first time, read its arguments with tool_info (several at once if you like). The tools:"

// catalogNote is the system note listing the on-demand tools.
func catalogNote(tools []Tool) string {
	lines := []string{catalogHeading}
	for _, t := range tools {
		lines = append(lines, catalogLine(t.Name(), t.Description()))
	}
	return strings.Join(lines, "\n")
}

// onDemandTools are tool_info and call_tool for one request's on-demand tools.
func onDemandTools(tools []Tool) []Tool {
	byName := make(map[string]Tool, len(tools))
	for _, t := range tools {
		byName[t.Name()] = t
	}
	find := func(name string) (Tool, error) {
		name = strings.TrimSpace(name)
		if t, ok := byName[name]; ok {
			return t, nil
		}
		// A model may drop the server prefix: "create_project" for
		// "knitted__create_project". Take it when it names one tool.
		var hits []string
		for n := range byName {
			if strings.HasSuffix(n, "__"+name) {
				hits = append(hits, n)
			}
		}
		if len(hits) == 1 {
			return byName[hits[0]], nil
		}
		return nil, fmt.Errorf("there is no tool %q in the catalog.%s", name, closeNames(name, byName))
	}
	info := &FuncTool{
		ToolName: toolInfoName,
		Desc: "Read the full description and the arguments (a JSON Schema) of tools from the on-demand catalog in your " +
			"instructions, before calling them with call_tool.",
		Params: json.RawMessage(`{"type":"object","properties":{"names":{"type":"array","items":{"type":"string"},` +
			`"description":"Catalog tool names, like [\"server__tool\"]"}},"required":["names"]}`),
		Fn: func(_ context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Names []string `json:"names"`
				Name  string   `json:"name"`
			}
			if err := decodeArgs(args, &a); err != nil {
				return "", err
			}
			if a.Name != "" {
				a.Names = append(a.Names, a.Name)
			}
			if len(a.Names) == 0 {
				return "", errors.New(`give names, a list of catalog tool names`)
			}
			var parts []string
			for _, n := range a.Names {
				t, err := find(n)
				if err != nil {
					parts = append(parts, "## "+n+"\n"+err.Error())
					continue
				}
				parts = append(parts, truncateUTF8(fmt.Sprintf("## %s\n%s\nArguments: %s", t.Name(), t.Description(), compactJSON(t.Parameters())), toolInfoMax))
			}
			return strings.Join(parts, "\n\n") + "\n\nRun one with call_tool: {\"name\": \"<tool>\", \"arguments\": {...}}.", nil
		},
	}
	call := &FuncTool{
		ToolName: callToolName,
		Desc:     "Run a tool from the on-demand catalog in your instructions. Read its arguments with tool_info first.",
		Params: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"The catalog tool's name"},` +
			`"arguments":{"type":"object","description":"Its arguments, as tool_info describes them"}},"required":["name","arguments"]}`),
		Fn: func(ctx context.Context, args json.RawMessage) (string, error) {
			name, inner, err := callToolArgs(args)
			if err != nil {
				return "", err
			}
			t, err := find(name)
			if err != nil {
				return "", err
			}
			text, err := t.Run(ctx, inner)
			if err != nil && ctx.Err() == nil {
				return text, fmt.Errorf("%s: %v\n\nThe arguments %s takes: %s", t.Name(), err, t.Name(),
					truncateUTF8(compactJSON(t.Parameters()), schemaOnError))
			}
			return text, err
		},
	}
	return []Tool{info, call}
}

// callToolArgs reads call_tool's arguments. Small models send the inner
// arguments as a JSON string, or flat beside name; both are taken.
func callToolArgs(args json.RawMessage) (string, json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := decodeArgs(args, &m); err != nil {
		return "", nil, err
	}
	var name string
	if raw, ok := m["name"]; !ok || json.Unmarshal(raw, &name) != nil || strings.TrimSpace(name) == "" {
		return "", nil, errors.New(`give name (a catalog tool) and arguments (an object)`)
	}
	inner, ok := m["arguments"]
	if !ok {
		delete(m, "name")
		b, _ := json.Marshal(m)
		return name, b, nil
	}
	var s string
	if json.Unmarshal(inner, &s) == nil {
		s = strings.TrimSpace(s)
		if s == "" {
			return name, json.RawMessage("{}"), nil
		}
		if !json.Valid([]byte(s)) {
			return "", nil, errors.New("arguments must be a JSON object")
		}
		inner = json.RawMessage(s)
	}
	if t := strings.TrimSpace(string(inner)); t == "null" {
		inner = json.RawMessage("{}")
	} else if !strings.HasPrefix(t, "{") {
		return "", nil, errors.New("arguments must be a JSON object")
	}
	return name, inner, nil
}

// closeNames suggests catalog names that share a word with name.
func closeNames(name string, byName map[string]Tool) string {
	words := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool { return r == '_' || r == '-' || r == ' ' })
	var hits []string
	for n := range byName {
		ln := strings.ToLower(n)
		for _, w := range words {
			if len(w) > 2 && strings.Contains(ln, w) {
				hits = append(hits, n)
				break
			}
		}
	}
	sort.Strings(hits)
	if len(hits) > 8 {
		hits = hits[:8]
	}
	if len(hits) == 0 {
		return " The catalog in your instructions lists the tools."
	}
	return " Did you mean: " + strings.Join(hits, ", ") + "?"
}

func compactJSON(b json.RawMessage) string {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return string(b)
	}
	out, _ := json.Marshal(v)
	return string(out)
}

// isOnDemand reports whether a registered tool is offered on demand.
func isOnDemand(t Tool) bool {
	m, ok := t.(*mcpTool)
	return ok && m.onDemand
}
