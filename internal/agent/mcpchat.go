package agent

// The /mcp chat commands: manage MCP servers from any chat client (Open
// WebUI included) while the model runs. Only the latest user message counts:
// never an earlier turn, a system message or a tool output, so a fetched page
// can't issue commands. The harness answers a command itself, without the
// model, and leaves earlier commands and their replies out of what the model
// sees (clients resend the whole chat).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// MCPReplyMarker starts every reply to a /mcp command; with the command
// text it identifies the pair in a resent chat history.
const MCPReplyMarker = "[harness /mcp]"

// MaskedValue replaces a secret in replies and lists.
const MaskedValue = "••••••"

// isMCPCommand reports whether text is a /mcp command.
func isMCPCommand(text string) bool {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "/mcp") {
		return false
	}
	rest := t[len("/mcp"):]
	return rest == "" || rest[0] == ' ' || rest[0] == '\t' || rest[0] == '\n'
}

// latestMCPCommand returns the command in the latest message when it is a
// user message that starts with /mcp.
func latestMCPCommand(msgs []json.RawMessage) (string, bool) {
	if len(msgs) == 0 {
		return "", false
	}
	var m struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(msgs[len(msgs)-1], &m) != nil || m.Role != "user" {
		return "", false
	}
	text := strings.TrimSpace(contentText(m.Content))
	if !isMCPCommand(text) {
		return "", false
	}
	return text, true
}

// stripMCPTurns drops earlier /mcp commands and the harness's replies to
// them: a user message that is a command, followed by an assistant message
// that starts with MCPReplyMarker. The latest message is never dropped.
func stripMCPTurns(msgs []json.RawMessage) []json.RawMessage {
	type msg struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	var out []json.RawMessage
	for i := 0; i < len(msgs); i++ {
		if i+1 < len(msgs)-1 { // the pair ends before the latest message
			var u, a msg
			if json.Unmarshal(msgs[i], &u) == nil && u.Role == "user" && isMCPCommand(contentText(u.Content)) &&
				json.Unmarshal(msgs[i+1], &a) == nil && a.Role == "assistant" &&
				strings.HasPrefix(strings.TrimSpace(contentText(a.Content)), MCPReplyMarker) {
				i++
				continue
			}
		}
		out = append(out, msgs[i])
	}
	if len(out) == 0 {
		return msgs
	}
	return out
}

// answerCommand answers a /mcp command as the assistant's reply.
func (rn *run) answerCommand() {
	reply := rn.a.mcpCommand(rn.ctx, rn.req.command)
	rn.out.addContent(MCPReplyMarker + "\n\n" + strings.TrimSpace(reply) + "\n")
	rn.finish(&turn{finish: "stop"}, nil)
}

const mcpHelp = "MCP commands (answered by the harness, not the model):\n\n" +
	"- `/mcp` or `/mcp list`: the servers, their status and tools\n" +
	"- `/mcp add <name> <url> [header:Name=Value ...]`: connect a server (Streamable HTTP)\n" +
	"- `/mcp remove <name>`\n" +
	"- `/mcp tools <name>`: its tools and what each costs in context\n" +
	"- `/mcp tools <name> +tool -tool`, `all` or `none`: choose the tools the model gets\n" +
	"- `/mcp test <name>`: reconnect and list its tools again\n" +
	"- `/mcp enable <name>`, `/mcp disable <name>`\n" +
	"- `/mcp help`"

// mcpCommand runs one /mcp command and returns the reply (markdown).
func (a *Agent) mcpCommand(ctx context.Context, cmd string) string {
	f := strings.Fields(cmd)
	verb := "list"
	if len(f) > 1 {
		verb = strings.ToLower(f[1])
	}
	args := []string{}
	if len(f) > 2 {
		args = f[2:]
	}
	a.logf("mcp chat command: %s", logName(verb))
	need := func(n int, usage string) string {
		if len(args) < n {
			return "Usage: `" + usage + "`"
		}
		return ""
	}
	switch verb {
	case "list", "ls", "status":
		return a.mcpListText()
	case "help", "?":
		return mcpHelp
	case "add":
		if u := need(2, "/mcp add <name> <url> [header:Name=Value ...]"); u != "" {
			return u
		}
		return a.mcpAddText(ctx, args)
	case "remove", "rm", "delete":
		if u := need(1, "/mcp remove <name>"); u != "" {
			return u
		}
		if err := a.mcp.remove(args[0]); err != nil {
			return fmt.Sprintf("%s: %v.", args[0], err)
		}
		return fmt.Sprintf("Removed **%s**; its tools are gone from the next request.%s", args[0], a.notSavedNote())
	case "tools":
		if u := need(1, "/mcp tools <name> [+tool -tool | all | none]"); u != "" {
			return u
		}
		if len(args) == 1 {
			return a.mcpToolsText(args[0])
		}
		return a.mcpEditTools(args[0], args[1:])
	case "test":
		if u := need(1, "/mcp test <name>"); u != "" {
			return u
		}
		st, err := a.mcp.test(ctx, args[0])
		if err != nil {
			return fmt.Sprintf("%s: %v.", args[0], err)
		}
		return statusLine(st) + "\n\n" + toolsLine(st)
	case "enable", "disable":
		if u := need(1, "/mcp "+verb+" <name>"); u != "" {
			return u
		}
		on := verb == "enable"
		st, err := a.mcp.update(args[0], func(s *MCPServer) { s.Enabled = &on })
		if err != nil {
			return fmt.Sprintf("%s: %v.", args[0], err)
		}
		return statusLine(st) + a.notSavedNote()
	}
	return fmt.Sprintf("There is no /mcp command %q.\n\n%s", short(verb, 40), mcpHelp)
}

func (a *Agent) notSavedNote() string {
	if ok, _ := a.mcp.saved(); ok {
		return ""
	}
	return "\n\n(Not saved: this harness has no state folder, so the change lasts until it stops.)"
}

func statusLine(st MCPStatus) string {
	line := fmt.Sprintf("**%s** (%s", st.Name, st.Endpoint)
	if st.ConnectVia != "" {
		line += ", reached through " + st.ConnectVia
	}
	line += "): "
	switch st.Status {
	case MCPConnected:
		line += fmt.Sprintf("connected, %d of %d tools chosen, ≈%d tokens of context per request", st.ToolCount, st.ToolsOffered, st.SchemaTokens)
	case MCPUnavailable:
		line += "unavailable: " + st.Error
	case MCPDisabled:
		line += "disabled"
	default:
		line += "not connected yet (connects on the next request)"
	}
	return line
}

func toolsLine(st MCPStatus) string {
	if st.Status != MCPConnected {
		return ""
	}
	if st.ToolsOffered == 0 {
		return "It offers no tools."
	}
	if st.ToolCount == 0 {
		return fmt.Sprintf("No tools are chosen, so the model gets none from it. See them with `/mcp tools %s`, choose with `/mcp tools %s +<tool>` or `all`.", st.Name, st.Name)
	}
	return ""
}

func (a *Agent) mcpListText() string {
	list := a.mcp.list(false)
	if len(list) == 0 {
		return "No MCP servers yet. Add one with `/mcp add <name> <url>` (or on the factory's Harness page).\n\n" + mcpHelp
	}
	var b strings.Builder
	b.WriteString("MCP servers:\n\n")
	for _, st := range list {
		b.WriteString("- " + statusLine(st) + "\n")
	}
	n, tok := a.ToolCost()
	fmt.Fprintf(&b, "\nAll tools together: %d, ≈%d tokens of every request%s.", n, tok, a.contextShare(tok))
	return b.String() + a.notSavedNote()
}

func (a *Agent) mcpToolsText(name string) string {
	c := a.mcp.find(name)
	if c == nil {
		return fmt.Sprintf("%s: %v.", name, errMCPUnknown)
	}
	st := c.status(true)
	if len(st.Tools) == 0 {
		return statusLine(st) + "\n\nIts tools show once it connects: `/mcp test " + name + "`."
	}
	var b strings.Builder
	b.WriteString(statusLine(st) + "\n\n")
	for _, t := range st.Tools {
		mark := " "
		if t.Allowed {
			mark = "x"
		}
		fmt.Fprintf(&b, "- [%s] `%s` ≈%d tok: %s\n", mark, t.Name, t.Tokens, short(t.Description, 100))
	}
	fmt.Fprintf(&b, "\nChoose with `/mcp tools %s +tool -tool`, `all` or `none`.", name)
	return b.String()
}

// mcpEditTools applies +tool -tool, all or none to a server's allowlist.
func (a *Agent) mcpEditTools(name string, edits []string) string {
	c := a.mcp.find(name)
	if c == nil {
		return fmt.Sprintf("%s: %v.", name, errMCPUnknown)
	}
	st := c.status(true)
	known := map[string]string{} // either name -> the server's name
	var all []string
	for _, t := range st.Tools {
		known[t.Name], known[t.RegisteredAs] = t.Name, t.Name
		all = append(all, t.Name)
	}
	cur := map[string]bool{}
	if st.ToolsAllowed == nil {
		for _, n := range all {
			cur[n] = true
		}
	} else {
		for _, n := range st.ToolsAllowed {
			cur[n] = true
		}
	}
	var unknown []string
	setAll := false
	for _, e := range edits {
		switch strings.ToLower(e) {
		case "all":
			setAll = true
			continue
		case "none":
			cur, setAll = map[string]bool{}, false
			continue
		}
		add := !strings.HasPrefix(e, "-")
		n := strings.TrimLeft(e, "+-")
		real, ok := known[n]
		if !ok && len(all) > 0 {
			unknown = append(unknown, n)
			continue
		}
		if !ok {
			real = n // not connected: take the name as given
		}
		if add {
			cur[real] = true
		} else {
			delete(cur, real)
			setAll = false
		}
	}
	var list []string
	if !setAll {
		list = []string{}
		for n := range cur {
			list = append(list, n)
		}
		sort.Strings(list)
	}
	st, err := a.mcp.update(name, func(s *MCPServer) { s.Tools = list })
	if err != nil {
		return fmt.Sprintf("%s: %v.", name, err)
	}
	out := statusLine(st)
	if len(unknown) > 0 {
		out += fmt.Sprintf("\n\nNot tools of %s: %s (see `/mcp tools %s`).", name, joinNames(unknown, 10), name)
	}
	return out + a.notSavedNote()
}

// mcpAddText runs /mcp add <name> <url> [header:Name=Value ...].
func (a *Agent) mcpAddText(ctx context.Context, args []string) string {
	name, raw := args[0], args[1]
	s := MCPServer{Name: name, URL: raw}
	var headerNames []string
	for _, h := range args[2:] {
		if !strings.HasPrefix(strings.ToLower(h), "header:") {
			return fmt.Sprintf("I don't understand %q: give headers as header:Name=Value.", short(maskArg(h), 60))
		}
		kv := h[len("header:"):]
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return "Give headers as header:Name=Value."
		}
		if s.Headers == nil {
			s.Headers = map[string]string{}
		}
		s.Headers[k] = v
		headerNames = append(headerNames, k)
	}
	if u, err := url.Parse(raw); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "The URL must be an http(s) URL of a Streamable HTTP MCP endpoint."
	}
	st, err := a.mcp.add(ctx, s, false)
	if err != nil {
		return "Not added: " + s.redact(err.Error())
	}
	var b strings.Builder
	b.WriteString("Added. " + statusLine(st) + "\n\n")
	if st.ConnectVia != "" && IsLoopbackURL(raw) {
		fmt.Fprintf(&b, "Inside this container 127.0.0.1 is the container itself, so the harness dials %s and keeps the URL's Host header.\n\n", st.ConnectVia)
	}
	if st.Status == MCPConnected {
		if st.ToolsOffered > ManyMCPTools {
			fmt.Fprintf(&b, "It has %d tools, more than %d, so none are chosen yet: every chosen tool's description rides along in every request. "+
				"Choose some with `/mcp tools %s +<tool> +<tool>` (or `all`).\n\n", st.ToolsOffered, ManyMCPTools, name)
		} else {
			fmt.Fprintf(&b, "All its %d tools are on. Narrow them with `/mcp tools %s -<tool>`.\n\n", st.ToolsOffered, name)
		}
		full := a.mcp.find(name)
		if full != nil {
			var names []string
			for _, t := range full.status(true).Tools {
				names = append(names, fmt.Sprintf("`%s` ≈%d", t.Name, t.Tokens))
			}
			b.WriteString("Tools: " + joinNames(names, 40) + "\n\n")
		}
	} else if st.Status == MCPUnavailable {
		fmt.Fprintf(&b, "It is kept and tried again on later requests; `/mcp test %s` tries now.\n\n", name)
	}
	if len(headerNames) > 0 {
		masked := make([]string, len(headerNames))
		for i, k := range headerNames {
			masked[i] = "header:" + k + "=" + MaskedValue
		}
		fmt.Fprintf(&b, "Warning: the header value you typed (%s) is now in this chat's history and goes wherever the chat goes. "+
			"Add servers with secrets on the factory's Harness page instead, and delete this message if you can.\n\n", strings.Join(masked, " "))
	}
	return strings.TrimSpace(b.String()) + a.notSavedNote()
}

// maskArg hides a header value in an argument echoed back.
func maskArg(s string) string {
	if k, _, ok := strings.Cut(s, "="); ok {
		return k + "=" + MaskedValue
	}
	return s
}
