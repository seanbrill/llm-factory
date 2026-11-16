package server

import (
	"strings"
	"testing"

	"github.com/seanbrill/local-llm/internal/builder"
)

func newPromptServer(t *testing.T) *Server {
	t.Helper()
	b, err := builder.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{b: b, mux: newRouteMux()}
	s.registerPrompts()
	return s
}

func TestSavedPrompts(t *testing.T) {
	s := newPromptServer(t)

	if code, out := tcall(t, s, "GET", "/api/prompts", nil); code != 200 || len(out["prompts"].([]any)) != 0 {
		t.Fatalf("empty list: %d %v", code, out)
	}
	if code, _ := tcall(t, s, "POST", "/api/prompts", map[string]any{"name": "x", "text": "  "}); code != 400 {
		t.Errorf("a prompt without text: %d, want 400", code)
	}
	if code, _ := tcall(t, s, "POST", "/api/prompts", map[string]any{"text": "hi", "temperature": 3}); code != 400 {
		t.Errorf("temperature 3: %d, want 400", code)
	}

	code, p := tcall(t, s, "POST", "/api/prompts", map[string]any{
		"text": "Write a haiku about rust.\nKeep it short.", "tags": []string{" Poetry ", "poetry", "short"}, "temperature": 0.2,
	})
	if code != 200 {
		t.Fatalf("POST: %d %v", code, p)
	}
	id, _ := p["id"].(string)
	if id == "" || p["name"] != "Write a haiku about rust." || p["created"] == "" {
		t.Errorf("created prompt: %v", p)
	}
	if tags := p["tags"].([]any); len(tags) != 2 || tags[0] != "poetry" {
		t.Errorf("tags = %v", tags)
	}

	code, p2 := tcall(t, s, "PUT", "/api/prompts/"+id, map[string]any{"name": "Haiku", "text": "Write a haiku.", "system": "Be terse."})
	if code != 200 || p2["name"] != "Haiku" || p2["id"] != id || p2["created"] != p["created"] {
		t.Errorf("PUT: %d %v", code, p2)
	}
	if code, _ := tcall(t, s, "PUT", "/api/prompts/nope", map[string]any{"text": "x"}); code != 404 {
		t.Errorf("PUT unknown: %d", code)
	}

	// Results.
	if code, _ := tcall(t, s, "POST", "/api/prompts/results", map[string]any{"prompt_id": "nope", "model": "m"}); code != 404 {
		t.Errorf("a result for an unknown prompt: %d, want 404", code)
	}
	long := strings.Repeat("é", maxResultOutput) // 2 bytes each
	code, r := tcall(t, s, "POST", "/api/prompts/results", map[string]any{
		"prompt_id": id, "model": "Qwen 9B", "ms": 1234.5, "ttft": 210.0, "tokens": 80, "tps": 26.1, "output": long, "source": "bench",
	})
	if code != 200 {
		t.Fatalf("POST result: %d %v", code, r)
	}
	if out := r["output"].(string); len(out) > maxResultOutput || !strings.HasPrefix(long, out) || r["output_bytes"].(float64) != float64(len(long)) {
		t.Errorf("the output should be cut at a rune boundary: %d bytes, output_bytes %v", len(out), r["output_bytes"])
	}
	tcall(t, s, "POST", "/api/prompts/results", map[string]any{"prompt_id": id, "model": "gpt-oss 20B", "ms": 900.0})
	if code, out := tcall(t, s, "GET", "/api/prompts/results?prompt="+id, nil); code != 200 || len(out["results"].([]any)) != 2 {
		t.Errorf("results: %d %v", code, out)
	}
	if code, _ := tcall(t, s, "DELETE", "/api/prompts/results/"+r["id"].(string), nil); code != 200 {
		t.Errorf("DELETE result: %d", code)
	}
	if _, out := tcall(t, s, "GET", "/api/prompts/results", nil); len(out["results"].([]any)) != 1 {
		t.Errorf("after deleting one result: %v", out)
	}

	// An import keeps free ids and replaces taken ones.
	code, imp := tcall(t, s, "POST", "/api/prompts", map[string]any{"prompts": []map[string]any{
		{"id": id, "name": "dup", "text": "a"}, {"id": "keepme", "name": "new", "text": "b"},
	}})
	if code != 200 {
		t.Fatalf("import: %d %v", code, imp)
	}
	got := imp["prompts"].([]any)
	if len(got) != 2 || got[0].(map[string]any)["id"] == id || got[1].(map[string]any)["id"] != "keepme" {
		t.Errorf("imported: %v", got)
	}

	// Deleting a prompt deletes its results.
	if code, _ := tcall(t, s, "DELETE", "/api/prompts/"+id, nil); code != 200 {
		t.Errorf("DELETE: %d", code)
	}
	if _, out := tcall(t, s, "GET", "/api/prompts", nil); len(out["prompts"].([]any)) != 2 {
		t.Errorf("after delete: %v", out)
	}
	if _, out := tcall(t, s, "GET", "/api/prompts/results", nil); len(out["results"].([]any)) != 0 {
		t.Errorf("the deleted prompt's results should go too: %v", out)
	}
}
