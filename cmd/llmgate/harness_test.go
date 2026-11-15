package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/seanbrill/local-llm/internal/agent"
)

// fakeLlama is a stand-in llama-server: it records chat requests and answers
// them with scripted JSON responses; /health answers "ok".
type fakeLlama struct {
	mu      sync.Mutex
	bodies  []map[string]any
	answers []string
}

func (f *fakeLlama) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/health" {
		io.WriteString(w, `{"status":"ok"}`)
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.bodies = append(f.bodies, body)
	n := len(f.bodies)
	var answer string
	if n <= len(f.answers) {
		answer = f.answers[n-1]
	}
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if answer == "" {
		w.WriteHeader(500)
		io.WriteString(w, `{"error":{"message":"unexpected request"}}`)
		return
	}
	io.WriteString(w, answer)
}

func (f *fakeLlama) body(i int) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bodies[i]
}

func (f *fakeLlama) addAnswer(a string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers = append(f.answers, a)
}

func startGate(t *testing.T, f *fakeLlama, c config, h *agent.Agent) *httptest.Server {
	t.Helper()
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	c.upstreamPort = up.Listener.Addr().(*net.TCPAddr).Port
	srv := httptest.NewServer(newGate(c, h))
	t.Cleanup(srv.Close)
	return srv
}

func writeHarness(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "harness.json")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func noEnv(string) string { return "" }

func TestLoadHarness(t *testing.T) {
	if h, err := loadHarness("", noEnv); h != nil || err != nil {
		t.Errorf("no file: %v %v", h, err)
	}
	if h, err := loadHarness(writeHarness(t, ""), noEnv); h != nil || err != nil {
		t.Errorf("empty placeholder: %v %v", h, err)
	}
	ws := t.TempDir()
	good := writeHarness(t, `{"tools":["files"],"workspace":`+strconv.Quote(ws)+`}`)
	if h, err := loadHarness(good, noEnv); h == nil || err != nil {
		t.Fatalf("good config: %v %v", h, err)
	}
	off := func(k string) string {
		if k == "HARNESS" {
			return "off"
		}
		return ""
	}
	if h, _ := loadHarness(good, off); h != nil {
		t.Error("HARNESS=off ignored")
	}
	bad := func(k string) string {
		if k == "HARNESS_MAX_STEPS" {
			return "lots"
		}
		return ""
	}
	if _, err := loadHarness(good, bad); err == nil {
		t.Error("a bad override was accepted")
	}
	// Nothing can run (brave without a key): serve without the harness.
	if h, err := loadHarness(writeHarness(t, `{"tools":["web_search"],"search":{"backend":"brave"}}`), noEnv); h != nil || err != nil {
		t.Errorf("no runnable tools: %v %v", h, err)
	}
}

func TestHarnessInfoWithoutHarness(t *testing.T) {
	srv := startGate(t, &fakeLlama{}, config{}, nil)
	resp, err := http.Get(srv.URL + "/v1/harness")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var info agent.Info
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	if info.Enabled || info.Host != "llmgate" || info.Object != "harness" {
		t.Errorf("info = %+v", info)
	}
}

func TestGateRunsHarnessLoop(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := loadHarness(writeHarness(t, `{"tools":["files"],"workspace":`+strconv.Quote(ws)+`}`), noEnv)
	if err != nil || h == nil {
		t.Fatalf("harness: %v", err)
	}
	f := &fakeLlama{answers: []string{
		`{"choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"list_files","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
		`{"choices":[{"index":0,"message":{"role":"assistant","content":"There is hello.txt."},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
	}}
	srv := startGate(t, f, config{systemPrompt: "BAKED", injectMode: "missing"}, h)

	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"what files?"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "There is hello.txt.") || !strings.Contains(string(b), `"harness"`) {
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	first := f.body(0)
	msgs := first["messages"].([]any)
	sys := msgs[0].(map[string]any)["content"].(string)
	if !strings.HasPrefix(sys, "BAKED\n\n") || !strings.Contains(sys, "tool_output") {
		t.Errorf("system prompt = %q; want the baked prompt, then the harness note", sys)
	}
	if !strings.Contains(fmt.Sprint(first["tools"]), "list_files") {
		t.Errorf("tools = %v", first["tools"])
	}
	second := f.body(1)["messages"].([]any)
	last := second[len(second)-1].(map[string]any)
	if last["role"] != "tool" || !strings.Contains(last["content"].(string), "hello.txt") {
		t.Errorf("tool result = %v", last)
	}

	// The info endpoint describes the harness.
	r2, err := http.Get(srv.URL + "/v1/harness")
	if err != nil {
		t.Fatal(err)
	}
	var info agent.Info
	_ = json.NewDecoder(r2.Body).Decode(&info)
	r2.Body.Close()
	if !info.Enabled || info.Workspace != ws || len(info.Tools) == 0 || info.Tools[0].Name != "list_files" {
		t.Errorf("info = %+v", info)
	}

	// Everything else is still a pass-through.
	r3, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	hb, _ := io.ReadAll(r3.Body)
	r3.Body.Close()
	if string(hb) != `{"status":"ok"}` {
		t.Errorf("health = %s", hb)
	}

	// "harness": false gets the plain model, with the baked prompt still applied
	// (here on the route without /v1, which llama-server also serves).
	f.addAnswer(`{"choices":[{"index":0,"message":{"role":"assistant","content":"raw"},"finish_reason":"stop"}]}`)
	r4, err := http.Post(srv.URL+"/chat/completions", "application/json",
		strings.NewReader(`{"harness":false,"messages":[{"role":"user","content":"x"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	r4.Body.Close()
	third := f.body(2)
	if _, ok := third["tools"]; ok {
		t.Errorf("harness:false still sent tools: %v", third)
	}
	if _, ok := third["harness"]; ok {
		t.Error("the harness field reached llama-server")
	}
	if c := third["messages"].([]any)[0].(map[string]any)["content"]; c != "BAKED" {
		t.Errorf("baked prompt not injected on the raw path: %v", c)
	}
}

func TestGateWithoutHarnessStillInjects(t *testing.T) {
	f := &fakeLlama{answers: []string{`{"choices":[]}`}}
	srv := startGate(t, f, config{systemPrompt: "BAKED", injectMode: "missing"}, nil)
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"messages":[{"role":"user","content":"x"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	body := f.body(0)
	if _, ok := body["tools"]; ok {
		t.Error("tools added without a harness")
	}
	if c := body["messages"].([]any)[0].(map[string]any)["content"]; c != "BAKED" {
		t.Errorf("system = %v", c)
	}
}
