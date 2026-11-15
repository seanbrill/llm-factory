package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/seanbrill/local-llm/internal/ensemble"
)

func reqOf(texts ...string) *ChatRequest {
	var msgs []map[string]any
	for _, t := range texts {
		msgs = append(msgs, map[string]any{"role": "user", "content": t})
	}
	return &ChatRequest{Messages: msgs}
}

func hasAll(string) bool { return true }

func TestHeuristicPhrasing(t *testing.T) {
	h := Heuristic{has: hasAll}
	cases := []struct{ text, tool, prompt string }{
		{"draw a fox in the snow", ensemble.ToolGenerateImage, "fox in the snow"},
		{"Can you make me a picture of a castle?", ensemble.ToolGenerateImage, "a castle"},
		{"please generate a short video of waves", ensemble.ToolGenerateVideo, "waves"},
		{"animate a bouncing ball", ensemble.ToolGenerateVideo, "bouncing ball"},
		{`say "good morning"`, ensemble.ToolSpeak, "good morning"},
		{"read this aloud: hello", ensemble.ToolSpeak, ""},
		{"how do I make an image smaller in CSS?", ensemble.ToolChat, ""},
		{"what do they say about Paris?", ensemble.ToolChat, ""},
		{"write a function that draws a circle", ensemble.ToolChat, ""},
	}
	for _, c := range cases {
		d := h.Route(context.Background(), reqOf(c.text))
		if d.Tool != c.tool || d.Prompt != c.prompt {
			t.Errorf("%q -> %s %q, want %s %q", c.text, d.Tool, d.Prompt, c.tool, c.prompt)
		}
	}
}

func TestHeuristicImagesAndMissingTools(t *testing.T) {
	img := &ChatRequest{Messages: []map[string]any{{"role": "user", "content": []any{
		map[string]any{"type": "text", "text": "what's this"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AA"}},
	}}}}
	if d := (Heuristic{has: hasAll}).Route(context.Background(), img); d.Tool != ensemble.ToolSeeImage {
		t.Errorf("image -> %s", d.Tool)
	}
	followUp := &ChatRequest{Messages: append(img.Messages, map[string]any{"role": "user", "content": "and the colors?"})}
	if d := (Heuristic{has: hasAll}).Route(context.Background(), followUp); d.Tool != ensemble.ToolSeeImage {
		t.Errorf("follow-up -> %s", d.Tool)
	}
	chatOnly := Heuristic{has: func(t string) bool { return t == ensemble.ToolChat }}
	if d := chatOnly.Route(context.Background(), reqOf("draw a cat")); d.Tool != ensemble.ToolChat {
		t.Errorf("no image member -> %s", d.Tool)
	}
}

func TestConductorRouter(t *testing.T) {
	tools := []string{ensemble.ToolChat, ensemble.ToolGenerateImage, ensemble.ToolGenerateVideo}
	ask := func(reply string, err error) func(context.Context, string) (string, error) {
		return func(_ context.Context, prompt string) (string, error) {
			if !strings.Contains(prompt, "generate_image: draw") {
				t.Errorf("prompt = %q", prompt)
			}
			return reply, err
		}
	}
	c := Conductor{fallback: Heuristic{has: hasAll}, tools: tools, ask: ask("<think>hmm</think> generate_image", nil)}
	if d := c.Route(context.Background(), reqOf("I'd love a sketch of my dog")); d.Tool != ensemble.ToolGenerateImage {
		t.Errorf("picked %s", d.Tool)
	}
	c.ask = ask("", errors.New("down"))
	if d := c.Route(context.Background(), reqOf("draw a cat")); d.Tool != ensemble.ToolGenerateImage || !strings.Contains(d.Why, "asks for an image") {
		t.Errorf("fallback = %+v", d)
	}
	c.ask = ask("no idea", nil)
	if d := c.Route(context.Background(), reqOf("hello")); d.Tool != ensemble.ToolChat {
		t.Errorf("undecided = %+v", d)
	}
	if got := pickTool("generate_video please", tools); got != ensemble.ToolGenerateVideo {
		t.Errorf("pickTool = %q", got)
	}
}

func TestSpeakable(t *testing.T) {
	in := "<think>plan</think># Title\n\n**Bold** and `code` with [a link](http://x.y) and ![img](data:image/png;base64,AAAA).\n\n```go\nfmt.Println()\n```\n- item one\n| a | b |"
	got := speakable(in)
	want := "Title\nBold and code with a link and .\n(The code is in the chat.)\nitem one"
	if got != want {
		t.Errorf("speakable = %q\nwant        %q", got, want)
	}
	long := strings.Repeat("Sentence number one is here. ", 100)
	if s := speakable(long); len(s) > maxSpoken || !strings.HasSuffix(s, ".") {
		t.Errorf("long: %d bytes, ends %q", len(s), s[len(s)-5:])
	}
	if s := speakable(strings.Repeat("é", 2000)); len(s) > maxSpoken || !strings.HasSuffix(s, "é") {
		t.Errorf("a cut mid-character: %q", s[len(s)-4:])
	}
}

func TestHistoryText(t *testing.T) {
	got := historyText("Here.\n\n![generated image](data:image/png;base64,AAAA) and [clip](/api/media/abc.webm) <think>x</think>")
	if got != "Here.\n\n[generated image] and [generated media]" {
		t.Errorf("historyText = %q", got)
	}
}

func TestPrepareMessages(t *testing.T) {
	msgs := []map[string]any{
		{"role": "system", "content": "Client rules."},
		{"role": "assistant", "content": "![generated image](data:image/png;base64,AAAA)"},
		{"role": "user", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:x"}}, map[string]any{"type": "text", "text": "hi"}}},
	}
	out := prepareMessages(msgs, "Persona.", false)
	if out[0]["content"] != "Persona.\n\nClient rules." {
		t.Errorf("system = %v", out[0]["content"])
	}
	if out[1]["content"] != "[generated image]" {
		t.Errorf("assistant = %v", out[1]["content"])
	}
	if s, _ := out[2]["content"].(string); !strings.Contains(s, "can't see images") || !strings.Contains(s, "hi") {
		t.Errorf("user = %v", out[2]["content"])
	}
	if _, ok := msgs[1]["content"].(string); !ok || msgs[1]["content"] == "[generated image]" {
		t.Error("prepareMessages changed the caller's messages")
	}
	out = prepareMessages([]map[string]any{{"role": "user", "content": "hi"}}, "Persona.", true)
	if len(out) != 2 || out[0]["role"] != "system" {
		t.Errorf("no system message: %v", out)
	}
}

func TestImageNotes(t *testing.T) {
	msgs := []map[string]any{
		{"role": "user", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:a"}}}},
		{"role": "assistant", "content": "ok"},
		{"role": "user", "content": []any{map[string]any{"type": "text", "text": "and this"}, map[string]any{"type": "image_url", "image_url": "data:b"}}},
	}
	out, urls := imageNotes(msgs, true)
	if strings.Join(urls, ",") != "data:a,data:b" {
		t.Errorf("urls = %v", urls)
	}
	if out[2]["content"] != "and this\n[image 2 attached: call look_at_image to see it]" {
		t.Errorf("note = %q", out[2]["content"])
	}
	out, _ = imageNotes(msgs, false)
	if !strings.Contains(out[0]["content"].(string), "no model here can see images") {
		t.Errorf("note without a vision member = %q", out[0]["content"])
	}
}

func TestToolsRefusal(t *testing.T) {
	cases := []struct {
		status           int
		msg              string
		retry, permanent bool
	}{
		{500, "tools param requires --jinja flag", true, true},
		{400, "this model does not support tools", true, true},
		{500, "Failed to parse tool call", true, false},
		{400, "the request exceeds the available context size", false, false},
		{503, "loading model", false, false},
	}
	for _, c := range cases {
		r, p := toolsRefusal(c.status, []byte(`{"error":{"message":"`+c.msg+`"}}`))
		if r != c.retry || p != c.permanent {
			t.Errorf("%d %q -> %v %v", c.status, c.msg, r, p)
		}
	}
}
