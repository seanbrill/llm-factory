package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/seanbrill/local-llm/internal/builder"
)

// fakeMCPServer answers initialize, tools/list (n tools) and notifications.
func fakeMCPServer(t *testing.T, n int, wantAuth string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != wantAuth {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &m)
		var result any
		switch m.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "s1")
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}}
		case "tools/list":
			var tools []any
			for i := 0; i < n; i++ {
				tools = append(tools, map[string]any{"name": fmt.Sprintf("t%d", i), "description": "Tool.", "inputSchema": map[string]any{"type": "object"}})
			}
			result = map[string]any{"tools": tools}
		default:
			w.WriteHeader(http.StatusAccepted)
			return
		}
		out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
		w.Header().Set("Content-Type", "application/json")
		w.Write(out)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (th *testHarness) call(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, th.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestMCPRegistry(t *testing.T) {
	th := newTestHarness(t)
	mcp := fakeMCPServer(t, 20, "Bearer key-s3cr3t")
	url := mcp.URL + "/p4th-s3cr3t/mcp"
	code, body := th.call(t, "POST", "/api/harness/mcp", fmt.Sprintf(`{"name":"knit","url":%q,"headers":{"Authorization":"Bearer key-s3cr3t"}}`, url))
	if code != 200 || !strings.Contains(body, `"ok":true`) || !strings.Contains(body, `"tools":[]`) {
		t.Fatalf("add: %d %s", code, body)
	}
	for _, b := range []string{body} {
		if strings.Contains(b, "s3cr3t") {
			t.Errorf("a secret reached the UI: %s", b)
		}
	}
	if code, _ := th.call(t, "POST", "/api/harness/mcp", fmt.Sprintf(`{"name":"knit","url":%q}`, url)); code != 409 {
		t.Errorf("duplicate: %d", code)
	}
	// Pick tools; a masked header value keeps the stored one.
	code, body = th.call(t, "PUT", "/api/harness/mcp/knit", `{"tools":["t1","t2"],"headers":{"Authorization":"••••••"}}`)
	if code != 200 || !strings.Contains(body, `"tools":["t1","t2"]`) || !strings.Contains(body, `"tools_chosen":true`) {
		t.Fatalf("put: %d %s", code, body)
	}
	code, body = th.call(t, "POST", "/api/harness/mcp/knit/test", `{}`)
	if code != 200 || !strings.Contains(body, `"ok":true`) || !strings.Contains(body, `"tools":["t1","t2"]`) {
		t.Errorf("test after put (the header must still work, the pick must stay): %d %s", code, body)
	}
	code, body = th.call(t, "GET", "/api/harness/mcp", "")
	if code != 200 || strings.Contains(body, "s3cr3t") || !strings.Contains(body, `"url":"`+mcp.URL+`/…"`) || !strings.Contains(body, `"many_tools":15`) {
		t.Errorf("list: %d %s", code, body)
	}
	if fi, err := os.Stat(th.h.store.Path()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("config file: %v %v", fi, err)
	}

	// A run passes the pick with connect_via for the loopback URL.
	s := th.h.s
	s.hapi = th.h
	rh, notes, err := s.runHarness(map[string]string{builder.LabelHarness: "1", builder.LabelHarnessConfig: `{"tools":[]}`}, []string{"knit"}, "podman")
	if err != nil || rh == nil || rh.AdminToken == "" || len(rh.MCP) != 1 || rh.MCP[0].ConnectVia != "host.containers.internal" ||
		strings.Join(rh.MCP[0].Tools, ",") != "t1,t2" || len(notes) != 1 {
		t.Errorf("runHarness = %+v %v %v", rh, notes, err)
	}
	if _, _, err := s.runHarness(map[string]string{}, []string{"knit"}, "docker"); err == nil {
		t.Error("an image without a harness took MCP servers")
	}

	// Tools on demand: saved, shown with their per-request cost, passed to
	// an image that reads them and left out for one that doesn't.
	code, body = th.call(t, "PUT", "/api/harness/mcp/knit", `{"load":"on_demand","pinned":["t1"]}`)
	if code != 200 || !strings.Contains(body, `"load":"on_demand"`) || !strings.Contains(body, `"pinned":["t1"]`) || !strings.Contains(body, `"on_demand":true`) {
		t.Fatalf("put load: %d %s", code, body)
	}
	if code, _ := th.call(t, "PUT", "/api/harness/mcp/knit", `{"load":"sometimes"}`); code != 400 {
		t.Errorf("a bad load mode: %d", code)
	}
	labels := map[string]string{builder.LabelHarness: "1", builder.LabelHarnessConfig: `{"tools":[]}`}
	rh, _, _ = s.runHarness(labels, []string{"knit"}, "podman")
	if rh.MCP[0].Load != "" || rh.MCP[0].Pinned != nil {
		t.Errorf("an older image must not get load/pinned (it refuses unknown fields): %+v", rh.MCP[0])
	}
	labels[builder.LabelHarnessFeatures] = builder.HarnessOnDemand
	rh, _, _ = s.runHarness(labels, []string{"knit"}, "podman")
	if rh.MCP[0].Load != "on_demand" || strings.Join(rh.MCP[0].Pinned, ",") != "t1" {
		t.Errorf("a new image gets load/pinned: %+v", rh.MCP[0])
	}

	if code, _ := th.call(t, "DELETE", "/api/harness/mcp/knit", `{}`); code != 200 || len(th.h.store.Get().MCP) != 0 {
		t.Errorf("delete: %d", code)
	}
}
