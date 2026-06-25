package main

import (
	"encoding/json"
	"testing"
)

func roles(t *testing.T, body []byte) []string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msgs, _ := m["messages"].([]any)
	var out []string
	for _, it := range msgs {
		mm := it.(map[string]any)
		out = append(out, mm["role"].(string))
	}
	return out
}

func TestInject_MissingMode(t *testing.T) {
	// No system message present -> prompt is prepended.
	in := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	got := roles(t, injectSystemPrompt(in, "BAKED", "missing"))
	want := []string{"system", "user"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v want %v", got, want)
	}

	// Client already sent a system message -> body is left untouched.
	in2 := []byte(`{"messages":[{"role":"system","content":"CLIENT"},{"role":"user","content":"hi"}]}`)
	out2 := injectSystemPrompt(in2, "BAKED", "missing")
	var m map[string]any
	_ = json.Unmarshal(out2, &m)
	sys := m["messages"].([]any)[0].(map[string]any)["content"]
	if sys != "CLIENT" {
		t.Fatalf("missing mode should not override client system; got %v", sys)
	}
}

func TestInject_AlwaysMode(t *testing.T) {
	// Existing system messages are dropped; baked one is forced to the front.
	in := []byte(`{"messages":[{"role":"system","content":"CLIENT"},{"role":"user","content":"hi"}]}`)
	out := injectSystemPrompt(in, "BAKED", "always")
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	msgs := m["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if c := msgs[0].(map[string]any)["content"]; c != "BAKED" {
		t.Fatalf("always mode should force baked prompt; got %v", c)
	}
	if r := msgs[1].(map[string]any)["role"]; r != "user" {
		t.Fatalf("expected user message preserved; got %v", r)
	}
}

func TestInject_PreservesOtherFields(t *testing.T) {
	in := []byte(`{"model":"x","temperature":0.7,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	out := injectSystemPrompt(in, "BAKED", "missing")
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["stream"] != true || m["model"] != "x" {
		t.Fatalf("non-message fields must survive injection: %v", m)
	}
}

func TestInject_MalformedPassthrough(t *testing.T) {
	in := []byte(`not json`)
	if out := injectSystemPrompt(in, "BAKED", "always"); string(out) != string(in) {
		t.Fatalf("malformed body should pass through unchanged")
	}
}
