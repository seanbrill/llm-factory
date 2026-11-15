package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seanbrill/local-llm/internal/builder"
	"github.com/seanbrill/local-llm/internal/catalog"
)

// A factory in a container: it reads its repo at dir, the host engine mounts
// /Users/tester/llm-factory.
func workspaceServer(t *testing.T) (*Server, string) {
	t.Helper()
	t.Setenv("HOST_HOME", "/Users/tester")
	dir := t.TempDir()
	return &Server{b: &builder.Builder{BaseDir: dir, HostDir: "/Users/tester/llm-factory"}}, dir
}

func TestRunWorkspace(t *testing.T) {
	s, dir := workspaceServer(t)
	files := map[string]string{builder.LabelHarness: "1", builder.LabelHarnessTools: "web_search,files"}

	// No harness, or one without the files and shell tools: no mount.
	for _, labels := range []map[string]string{
		nil,
		{"local-llm.model": "qwen"},
		{builder.LabelHarness: "1", builder.LabelHarnessTools: "web_search,fetch_url,skills"},
	} {
		if ws, err := s.runWorkspace(labels, "/Users/tester/projects/site"); ws != "" || err != nil {
			t.Errorf("%v: %q %v", labels, ws, err)
		}
	}

	// The default: the factory's workspace/ folder, created for the run.
	ws, err := s.runWorkspace(files, "")
	if err != nil || ws != "/Users/tester/llm-factory/workspace" {
		t.Fatalf("default = %q %v", ws, err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "workspace")); err != nil || !fi.IsDir() {
		t.Errorf("workspace/ wasn't created: %v", err)
	}
	shell := map[string]string{builder.LabelHarness: "1", builder.LabelHarnessTools: "shell"}
	if ws, err := s.runWorkspace(shell, "  "); err != nil || ws != "/Users/tester/llm-factory/workspace" {
		t.Errorf("shell only = %q %v", ws, err)
	}

	// A project folder elsewhere is fine (and normalized); it isn't created.
	if ws, err := s.runWorkspace(files, "/Users/tester/projects/site/"); err != nil || ws != "/Users/tester/projects/site" {
		t.Errorf("project folder = %q %v", ws, err)
	}
	if ws, err := s.runWorkspace(files, "/Users/tester/llm-factory/workspace/notes"); err != nil || ws != "/Users/tester/llm-factory/workspace/notes" {
		t.Errorf("inside workspace/ = %q %v", ws, err)
	}

	// Never a system, hidden or home folder, nor the rest of the factory.
	for _, bad := range []string{
		"/etc",
		"/var/run",
		"/Users/tester",
		"/Users/tester/.ssh",
		"/Users/tester/Library/Keychains",
		"/Users/tester/llm-factory",
		"/Users/tester/llm-factory/config",
		"relative/folder",
		"/Users/tester/a:b",
	} {
		if ws, err := s.runWorkspace(files, bad); err == nil || ws != "" {
			t.Errorf("%s accepted: %q", bad, ws)
		} else if !strings.Contains(err.Error(), "at /workspace") {
			t.Errorf("%s: %v", bad, err)
		}
	}
}

// The factory installed under a system folder can't hand out its workspace/.
func TestRunWorkspaceDefaultChecked(t *testing.T) {
	t.Setenv("HOST_HOME", "/home/tester")
	s := &Server{b: &builder.Builder{BaseDir: t.TempDir(), HostDir: "/usr/local/llm-factory"}}
	labels := map[string]string{builder.LabelHarness: "1", builder.LabelHarnessTools: "files"}
	if ws, err := s.runWorkspace(labels, ""); err == nil || !strings.Contains(err.Error(), "/usr") {
		t.Errorf("default under /usr = %q %v", ws, err)
	}
}

// The Build plan's `harness` (ui/src/lib/baked.ts harnessBody): the defaults,
// and a cloned image's config sent back with the plan's fields on top.
func TestParseHarnessOptionTakesTheUIBody(t *testing.T) {
	plain := `{"tools":["web_search","fetch_url","files","skills"],"limits":{"max_steps":8,"max_calls_per_step":8,"tool_timeout":"60s","max_output_bytes":8192},"search":{"backend":"duckduckgo"}}`
	c, err := parseHarnessOption(json.RawMessage(plain))
	if err != nil || c.Limits.MaxSteps != 8 || c.Limits.ToolTimeout.D().Seconds() != 60 || builder.HarnessUsesWorkspace(c) != true {
		t.Errorf("plan body: %+v %v", c, err)
	}
	clone := `{"version":1,"tools":["web_search","read_file","shell"],"limits":{"max_steps":12,"max_calls_per_step":4,"tool_timeout":"120s","max_output_bytes":16384},"search":{"backend":"searxng","searxng_url":"http://s:1","max_results":7},"fetch":{"max_bytes":4096},"shell":{"env":["LANG"]},"instructions":"Be brief.","workspace":"/workspace","skills_dir":"/etc/llmgate/skills"}`
	c, err = parseHarnessOption(json.RawMessage(clone))
	if err != nil || c.Search.SearXNGURL != "http://s:1" || c.Instructions != "Be brief." || !c.EnabledTools()["run_command"] || c.EnabledTools()["write_file"] {
		t.Errorf("clone body: %+v %v", c, err)
	}
}

func TestBuildRefusesHarnessForOtherModels(t *testing.T) {
	cat := &catalog.Catalog{Models: []catalog.Model{
		{ID: "nomic", Name: "Nomic Embed", Modality: "embedding"},
		{ID: "flux", Name: "FLUX", Modality: "image"},
	}}
	s := &Server{cat: cat}
	for _, id := range []string{"nomic", "flux"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/build", strings.NewReader(`{"model_id":"`+id+`","image_name":"local-llm/x","harness":true}`))
		s.handleBuild(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "needs a llama.cpp chat model") {
			t.Errorf("%s: status %d: %s", id, rec.Code, rec.Body)
		}
	}
}

func TestCatalogSaysWhoCanTakeTools(t *testing.T) {
	cat := &catalog.Catalog{Models: []catalog.Model{
		{ID: "qwen", Modality: "text"},
		{ID: "llava", Modality: "vision"},
		{ID: "nomic", Modality: "embedding"},
		{ID: "piper", Modality: "tts"},
		{ID: "py", Modality: "text", Runtime: "python"},
	}}
	b, err := builder.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{b: b, cat: cat}
	rec := httptest.NewRecorder()
	s.handleCatalog(rec, httptest.NewRequest(http.MethodGet, "/api/catalog", nil))
	var out []struct {
		ID         string `json:"id"`
		CanHarness bool   `json:"can_harness"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"qwen": true, "llava": true, "nomic": false, "piper": false, "py": false}
	if len(out) != len(want) {
		t.Fatalf("catalog = %s", rec.Body)
	}
	for _, m := range out {
		if m.CanHarness != want[m.ID] {
			t.Errorf("%s: can_harness = %v", m.ID, m.CanHarness)
		}
	}
}
