package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func lastContent(t *testing.T, req map[string]any) string {
	t.Helper()
	msgs := messagesOf(t, req)
	return fmt.Sprint(msgs[len(msgs)-1]["content"])
}

func TestToolsOnDemand(t *testing.T) {
	_, srv := newFakeMCP(t, false, 5)
	a := mcpAgent(t, Config{MCP: []MCPServer{{Name: "fake", URL: srv.URL + secretPath, Load: MCPLoadOnDemand, Pinned: []string{"echo"}}}}, nil)
	f := &fakeModel{t: t, turns: []fakeTurn{
		{calls: []toolCall{{ID: "a", Name: "tool_info", Args: `{"names":["fake__tool_3"]}`}}},
		{calls: []toolCall{{ID: "b", Name: "call_tool", Args: `{"name":"fake__tool_3","arguments":{"text":"x"}}`}}},
		{calls: []toolCall{{ID: "c", Name: "call_tool", Args: `{"name":"fail","arguments":"{}"}`}}},
		{calls: []toolCall{{ID: "d", Name: "fake__tool_4", Args: `{}`}}}, // a catalog tool called by name
		{content: "done"},
	}}
	rec := serve(a, f, `{"messages":[{"role":"user","content":"go"}]}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	names := toolNames(f.request(0))
	if strings.Join(names, ",") != "fake__echo,tool_info,call_tool" {
		t.Errorf("tools = %v; want the pinned tool, tool_info and call_tool", names)
	}
	sys := fmt.Sprint(messagesOf(t, f.request(0))[0]["content"])
	if !strings.Contains(sys, catalogHeading) || !strings.Contains(sys, "- fake__tool_3: Tool tool_3.") || strings.Contains(sys, "- fake__echo") {
		t.Errorf("catalog in the system note:\n%s", sys)
	}
	if info := lastContent(t, f.request(1)); !strings.Contains(info, "## fake__tool_3") || !strings.Contains(info, `"properties":{"text"`) {
		t.Errorf("tool_info = %s", info)
	}
	if out := lastContent(t, f.request(2)); !strings.Contains(out, "ok tool_3") {
		t.Errorf("call_tool = %s", out)
	}
	if out := lastContent(t, f.request(3)); !strings.Contains(out, "it broke") || !strings.Contains(out, "The arguments fake__fail takes:") {
		t.Errorf("a failed call_tool should carry the tool's arguments: %s", out)
	}
	if out := lastContent(t, f.request(4)); !strings.Contains(out, "ok tool_4") {
		t.Errorf("a catalog tool called by name = %s", out)
	}

	n, tok := a.ToolCost()
	if n != 5 || tok <= 0 {
		t.Errorf("ToolCost = %d, %d", n, tok)
	}
	st := a.Info().MCP[0]
	if !st.OnDemand || st.RequestTokens <= 0 || st.RequestTokens >= st.SchemaTokens {
		t.Errorf("status: on demand %v, request %d of %d tokens", st.OnDemand, st.RequestTokens, st.SchemaTokens)
	}
}

func TestToolsOnDemandAuto(t *testing.T) {
	if LoadOnDemand("", OnDemandOver) || !LoadOnDemand("", OnDemandOver+1) || LoadOnDemand(MCPLoadFull, 1<<20) || !LoadOnDemand(MCPLoadOnDemand, 1) {
		t.Error("LoadOnDemand")
	}
	_, srv := newFakeMCP(t, false, 5) // five small tools: well under the threshold
	a := mcpAgent(t, Config{MCP: []MCPServer{{Name: "fake", URL: srv.URL + secretPath}}}, nil)
	f := &fakeModel{t: t, turns: []fakeTurn{{content: "hi"}}}
	serve(a, f, `{"messages":[{"role":"user","content":"go"}]}`)
	if names := toolNames(f.request(0)); len(names) != 5 || contains(names, "call_tool") {
		t.Errorf("auto with few tools should send them in full: %v", names)
	}
	if err := (MCPServer{Name: "x", URL: "http://h", Load: "sometimes"}).Validate(); err == nil {
		t.Error("a bad load mode should be refused")
	}
}

func TestCallToolArgs(t *testing.T) {
	for _, tc := range []struct{ in, name, args string }{
		{`{"name":"a__b","arguments":{"x":1}}`, "a__b", `{"x":1}`},
		{`{"name":"a__b","arguments":"{\"x\":1}"}`, "a__b", `{"x":1}`},
		{`{"name":"a__b","x":1}`, "a__b", `{"x":1}`},
		{`{"name":"a__b","arguments":null}`, "a__b", `{}`},
	} {
		name, args, err := callToolArgs(json.RawMessage(tc.in))
		if err != nil || name != tc.name || compactJSON(args) != tc.args {
			t.Errorf("%s: %q %s %v", tc.in, name, args, err)
		}
	}
	for _, in := range []string{`{"arguments":{}}`, `{"name":"a","arguments":[1]}`, `{"name":"a","arguments":"nope"}`} {
		if _, _, err := callToolArgs(json.RawMessage(in)); err == nil {
			t.Errorf("%s should fail", in)
		}
	}
}

func TestRepeatedFailureNote(t *testing.T) {
	fail := &FuncTool{ToolName: "arrange_design", Params: json.RawMessage(`{"type":"object"}`),
		Fn: func(context.Context, json.RawMessage) (string, error) {
			return "", fmt.Errorf("undefined section; sections are s1, s2")
		}}
	a := newTestAgent(t, Config{}, fail)
	call := toolCall{Name: "arrange_design", Args: `{"section":"x"}`}
	f := &fakeModel{t: t, turns: []fakeTurn{
		{calls: []toolCall{call}}, {calls: []toolCall{call}}, {content: "gave up"},
	}}
	serve(a, f, `{"messages":[{"role":"user","content":"go"}]}`)
	if first := lastContent(t, f.request(1)); strings.Contains(first, "[harness]") {
		t.Errorf("the first failure needs no note: %s", first)
	}
	if second := lastContent(t, f.request(2)); !strings.Contains(second, "failed 2 times the same way") {
		t.Errorf("the second identical failure should say so: %s", second)
	}
}

func TestCutCallNote(t *testing.T) {
	set := &FuncTool{ToolName: "set_frame", Params: json.RawMessage(`{"type":"object"}`),
		Fn: func(context.Context, json.RawMessage) (string, error) { return "ok", nil }}
	a := newTestAgent(t, Config{}, set)
	f := &fakeModel{t: t, turns: []fakeTurn{
		{calls: []toolCall{{Name: "set_frame", Args: `{"html":"<div>`}}, finish: "length"},
		{content: "sorry"},
	}}
	serve(a, f, `{"messages":[{"role":"user","content":"go"}]}`)
	if out := lastContent(t, f.request(1)); !strings.Contains(out, "length limit (max_tokens)") {
		t.Errorf("a call cut off by the length limit should say so: %s", out)
	}
}

func TestCheckYourWork(t *testing.T) {
	tools := func() []Tool {
		mk := func(name string) Tool {
			return &FuncTool{ToolName: name, Params: json.RawMessage(`{"type":"object"}`),
				Fn: func(context.Context, json.RawMessage) (string, error) { return name + " ok", nil }}
		}
		return []Tool{mk("set_frame"), mk("get_frame")}
	}
	t.Run("asked once after an unchecked change", func(t *testing.T) {
		a := newTestAgent(t, Config{}, tools()...)
		f := &fakeModel{t: t, turns: []fakeTurn{
			{calls: []toolCall{{Name: "set_frame", Args: `{}`}}},
			{content: "All done!"},
			{calls: []toolCall{{Name: "get_frame", Args: `{}`}}},
			{content: "Checked, all done."},
		}}
		rec := serve(a, f, `{"messages":[{"role":"user","content":"go"}]}`)
		if n := len(f.reqs); n != 4 {
			t.Fatalf("%d model calls, want 4", n)
		}
		msgs := messagesOf(t, f.request(2))
		if prev := msgs[len(msgs)-2]; prev["role"] != "assistant" || prev["content"] != "All done!" {
			t.Errorf("the first answer should stay in the conversation: %v", prev)
		}
		if note := lastContent(t, f.request(2)); !strings.Contains(note, "you changed things with set_frame") {
			t.Errorf("check note = %s", note)
		}
		if !strings.Contains(rec.Body.String(), "Checked, all done.") {
			t.Errorf("reply = %s", rec.Body)
		}
	})
	t.Run("not asked after a read", func(t *testing.T) {
		a := newTestAgent(t, Config{}, tools()...)
		f := &fakeModel{t: t, turns: []fakeTurn{
			{calls: []toolCall{{Name: "set_frame", Args: `{}`}}},
			{calls: []toolCall{{Name: "get_frame", Args: `{}`}}},
			{content: "done"},
		}}
		serve(a, f, `{"messages":[{"role":"user","content":"go"}]}`)
		if n := len(f.reqs); n != 3 {
			t.Errorf("%d model calls, want 3", n)
		}
	})
	t.Run("off", func(t *testing.T) {
		a := newTestAgent(t, Config{ChecksOff: true}, tools()...)
		f := &fakeModel{t: t, turns: []fakeTurn{
			{calls: []toolCall{{Name: "set_frame", Args: `{}`}}},
			{content: "done"},
		}}
		serve(a, f, `{"messages":[{"role":"user","content":"go"}]}`)
		if n := len(f.reqs); n != 2 {
			t.Errorf("%d model calls, want 2", n)
		}
	})
}

func TestToolKind(t *testing.T) {
	for name, want := range map[string][2]bool{
		"knitted__set_design_frame": {true, false}, "knitted__get_design": {false, true}, "write_file": {true, false},
		"read_file": {false, true}, "run_command": {false, false}, "search_web": {false, true},
	} {
		if w, r := toolKind(name); w != want[0] || r != want[1] {
			t.Errorf("%s: write %v read %v", name, w, r)
		}
	}
}
