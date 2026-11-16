package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestEmptyAnswerRetriedOnce(t *testing.T) {
	for _, stream := range []bool{false, true} {
		logs := &logCapture{}
		a := newTestAgent(t, Config{}, echoTool(false))
		a.logf = logs.logf
		f := &fakeModel{t: t, turns: []fakeTurn{{reasoning: "I'll start by listing"}, {content: "Here it is."}}}
		body := `{"messages":[{"role":"user","content":"list things"}]}`
		if stream {
			body = `{"stream":true,"messages":[{"role":"user","content":"list things"}]}`
		}
		rec := serve(a, f, body)
		if f.calls() != 2 {
			t.Fatalf("model called %d times, want a retry", f.calls())
		}
		msgs := messagesOf(t, f.request(1))
		if last := msgs[len(msgs)-1]; last["role"] != "user" || !strings.Contains(last["content"].(string), nudgeNote) {
			t.Errorf("retry has no nudge: %v", last)
		}
		if (!stream && !strings.Contains(rec.Body.String(), "Here it is.")) || !strings.Contains(rec.Body.String(), `"type":"retry"`) {
			t.Errorf("reply = %s", rec.Body)
		}
		if !strings.Contains(logs.text(), "no answer and no tool call") || !strings.Contains(logs.text(), "step 1: finish stop, tool calls none, content 0 B, reasoning 21 B") {
			t.Errorf("log = %s", logs.text())
		}
	}
}

func TestEmptyAnswerTwiceTellsTheClient(t *testing.T) {
	a := newTestAgent(t, Config{}, echoTool(false))
	f := &fakeModel{t: t, turns: []fakeTurn{{reasoning: "hmm"}, {reasoning: "hmm again"}}}
	rec := serve(a, f, `{"stream":true,"messages":[{"role":"user","content":"x"}]}`)
	var content strings.Builder
	var types []string
	for _, c := range readSSE(t, rec.Body.String()) {
		if c.Harness != nil {
			types = append(types, c.Harness.Type)
		}
		for _, ch := range c.Choices {
			content.WriteString(ch.Delta.Content)
		}
	}
	if !strings.Contains(content.String(), "ended its turn without an answer") {
		t.Errorf("content = %q", content.String())
	}
	if strings.Join(types, ",") != "retry,empty,done" {
		t.Errorf("events = %v", types)
	}
	if f.calls() != 2 {
		t.Errorf("calls = %d", f.calls())
	}
}

func TestSkippedCallsLogged(t *testing.T) {
	logs := &logCapture{}
	a := newTestAgent(t, Config{}, echoTool(false))
	a.logf = logs.logf
	f := &fakeModel{t: t, turns: []fakeTurn{
		{calls: []toolCall{{Name: "no such tool!", Args: `{}`}, {Name: "echo", Args: `{"text": bad`}}},
		{content: "ok"},
	}}
	serve(a, f, `{"messages":[{"role":"user","content":"secret user text"}]}`)
	l := logs.text()
	if !strings.Contains(l, "tool call no?such?tool? not run: unknown tool") || !strings.Contains(l, "tool call echo not run: invalid JSON arguments") {
		t.Errorf("log = %s", l)
	}
	if strings.Contains(l, "secret user text") {
		t.Error("user content in the log")
	}
}

func bigTool(n int) Tool {
	return &FuncTool{ToolName: "big", Params: json.RawMessage(`{"type":"object"}`),
		Fn: func(context.Context, json.RawMessage) (string, error) { return strings.Repeat("word ", n/5), nil }}
}

func TestContextGuardCompactsOldToolOutputs(t *testing.T) {
	logs := &logCapture{}
	a := newTestAgent(t, Config{Limits: Limits{MaxOutputBytes: 1 << 20}}, bigTool(12000))
	a.logf = logs.logf
	a.SetContextSize(8192)
	call := fakeTurn{calls: []toolCall{{Name: "big", Args: `{}`}}}
	f := &fakeModel{t: t, realUsage: true, turns: []fakeTurn{call, call, call, call, {content: "done"}}}
	rec := serve(a, f, `{"messages":[{"role":"user","content":"read a lot"}]}`)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	// Each output is capped at 35% of the context.
	cap := 8602
	last := messagesOf(t, f.request(4))
	var tools []string
	for _, m := range last {
		if m["role"] == "tool" {
			tools = append(tools, m["content"].(string))
		}
	}
	if len(tools) != 4 {
		t.Fatalf("tool messages = %d", len(tools))
	}
	if !strings.Contains(tools[3], "[output cut at") || len(tools[3]) > cap+200 {
		t.Errorf("the newest output isn't capped (%d bytes)", len(tools[3]))
	}
	if strings.Contains(tools[3], compactNote) {
		t.Error("the latest result was shortened")
	}
	if !strings.Contains(tools[0], compactNote) || !strings.HasSuffix(tools[0], "</tool_output>") || len(tools[0]) > 700 {
		t.Errorf("the oldest output wasn't shortened: %d bytes %q", len(tools[0]), tools[0][:80])
	}
	if !strings.Contains(rec.Body.String(), `"type":"compact"`) || !strings.Contains(logs.text(), "shortened") {
		t.Errorf("no compact event/log: %s", logs.text())
	}
}

func TestContextErrorIsClear(t *testing.T) {
	a := newTestAgent(t, Config{}, echoTool(false))
	a.SetContextSize(8192)
	f := &fakeModel{t: t, turns: []fakeTurn{{status: 400,
		body: `{"error":{"code":400,"message":"the request exceeds the available context size, try increasing it","type":"exceed_context_size_error","n_prompt_tokens":9000,"n_ctx":8192}}`}}}
	rec := serve(a, f, `{"messages":[{"role":"user","content":"x"}]}`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "too long for this model's context window (8192 tokens)") ||
		!strings.Contains(rec.Body.String(), "exceed_context_size_error") {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	// With older tool outputs, the step is retried once after shortening them.
	b := newTestAgent(t, Config{}, bigTool(4000))
	call := fakeTurn{calls: []toolCall{{Name: "big", Args: `{}`}}}
	tooLong := fakeTurn{status: 400, body: `{"error":{"message":"the request exceeds the available context size"}}`}
	f2 := &fakeModel{t: t, turns: []fakeTurn{call, call, tooLong, {content: "fits now"}}}
	rec = serve(b, f2, `{"messages":[{"role":"user","content":"x"}]}`)
	if !strings.Contains(rec.Body.String(), "fits now") {
		t.Errorf("no retry: %s", rec.Body)
	}
	msgs := messagesOf(t, f2.request(3))
	if c := msgs[len(msgs)-3]["content"].(string); !strings.Contains(c, compactNote) {
		t.Errorf("older output not shortened on retry: %d bytes", len(c))
	}
}

func TestPromptBytesCountsImagesCheaply(t *testing.T) {
	img := `data:image/png;base64,` + strings.Repeat("A", 300000)
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"` + img + `"}}]}]}`)
	if n := promptBytes(body); n > 10000 {
		t.Errorf("an image counts as %d bytes", n)
	}
}
