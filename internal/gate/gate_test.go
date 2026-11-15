package gate

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/seanbrill/local-llm/internal/agent"
)

func TestUpstreamKey(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Path+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`)
	}))
	defer up.Close()
	h, err := agent.New(agent.Config{Tools: []string{agent.ToolsetFetchURL}}, agent.Options{Host: "test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, harness := range []*agent.Agent{nil, h} {
		g, err := New(Options{Upstream: up.URL, Harness: harness, UpstreamKey: "s3cret"})
		if err != nil {
			t.Fatal(err)
		}
		for _, req := range []*http.Request{
			httptest.NewRequest(http.MethodGet, "/v1/models", nil),
			httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"hello"}]}`)),
		} {
			req.Header.Set("Authorization", "Bearer client-key")
			rec := httptest.NewRecorder()
			g.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("harness=%v %s: %d %s", harness != nil, req.URL.Path, rec.Code, rec.Body.String())
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 4 {
		t.Fatalf("upstream saw %v", seen)
	}
	for _, s := range seen {
		if !strings.HasSuffix(s, " Bearer s3cret") {
			t.Errorf("upstream request without the key: %q", s)
		}
	}
	// Without a key the client's header passes through, as before.
	seen = nil
	mu.Unlock()
	g, _ := New(Options{Upstream: up.URL})
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer client-key")
	g.ServeHTTP(httptest.NewRecorder(), req)
	mu.Lock()
	if len(seen) != 1 || seen[0] != "/v1/models Bearer client-key" {
		t.Errorf("no key: %v", seen)
	}
}

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
	got := roles(t, InjectSystemPrompt(in, "BAKED", "missing"))
	want := []string{"system", "user"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v want %v", got, want)
	}

	// Client already sent a system message -> body is left untouched.
	in2 := []byte(`{"messages":[{"role":"system","content":"CLIENT"},{"role":"user","content":"hi"}]}`)
	out2 := InjectSystemPrompt(in2, "BAKED", "missing")
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
	out := InjectSystemPrompt(in, "BAKED", "always")
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
	out := InjectSystemPrompt(in, "BAKED", "missing")
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
	if out := InjectSystemPrompt(in, "BAKED", "always"); string(out) != string(in) {
		t.Fatalf("malformed body should pass through unchanged")
	}
}

func TestNewRejectsBadUpstream(t *testing.T) {
	for _, u := range []string{"", "127.0.0.1:8081", "://x"} {
		if _, err := New(Options{Upstream: u}); err == nil {
			t.Errorf("New(%q) accepted a bad upstream", u)
		}
	}
	if _, err := New(Options{Upstream: "http://127.0.0.1:8081/"}); err != nil {
		t.Errorf("good upstream refused: %v", err)
	}
}

func TestLlamaArgs(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.gguf")
	proj := filepath.Join(dir, "mmproj.gguf")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(proj, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		l    Llama
		want string
	}{
		{"container defaults", Llama{Model: "/models/model.gguf", Port: 8081, CtxSize: 4096, Threads: 8, Parallel: 1, MMProj: empty, Modality: "text"},
			"--model /models/model.gguf --host 127.0.0.1 --port 8081 --ctx-size 4096 --threads 8 --threads-batch 8 --parallel 1 --cont-batching --jinja --metrics"},
		{"native metal", Llama{Model: "m.gguf", Port: 9, CtxSize: 8192, GPULayers: "all", NoWebUI: true},
			"--model m.gguf --host 127.0.0.1 --port 9 --ctx-size 8192 --parallel 1 --cont-batching --jinja --metrics --no-webui --n-gpu-layers all"},
		{"vision on vulkan, embedding flag, extras", Llama{Model: "m", Port: 1, CtxSize: 1, Threads: 2, Parallel: 2, GPULayers: "99", MMProj: proj, MMProjNoOffload: true, Modality: "embedding", Extra: []string{"--flash-attn", "on"}},
			"--model m --host 127.0.0.1 --port 1 --ctx-size 1 --threads 2 --threads-batch 2 --parallel 2 --cont-batching --jinja --metrics --n-gpu-layers 99 --embedding --mmproj " + proj + " --no-mmproj-offload --flash-attn on"},
		{"zero gpu layers is the CPU", Llama{Model: "m", Port: 1, CtxSize: 1, GPULayers: "0"},
			"--model m --host 127.0.0.1 --port 1 --ctx-size 1 --parallel 1 --cont-batching --jinja --metrics"},
	}
	for _, c := range cases {
		if got := strings.Join(c.l.Args(), " "); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
}
