package builder

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/seanbrill/local-llm/internal/agent"
	"github.com/seanbrill/local-llm/internal/catalog"
)

func TestCanHarness(t *testing.T) {
	for _, c := range []struct {
		m    catalog.Model
		want bool
	}{
		{catalog.Model{Name: "chat"}, true}, // modality defaults to text
		{catalog.Model{Modality: "code"}, true},
		{catalog.Model{Modality: "reasoning"}, true},
		{catalog.Model{Modality: "vision"}, true},
		{catalog.Model{Modality: "embedding"}, false},
		{catalog.Model{Modality: "image"}, false},
		{catalog.Model{Modality: "video"}, false},
		{catalog.Model{Modality: "audio-stt"}, false},
		{catalog.Model{Modality: "tts"}, false},
		{catalog.Model{Modality: "text", Runtime: "python"}, false},
		{catalog.Model{Modality: "text", Runtime: "audiocpp"}, false},
	} {
		if got := CanHarness(c.m); got != c.want {
			t.Errorf("CanHarness(%s/%s) = %v", c.m.Mod(), c.m.Rt(), got)
		}
		if err := HarnessModelError(c.m); (err == nil) != c.want {
			t.Errorf("HarnessModelError(%s/%s) = %v", c.m.Mod(), c.m.Rt(), err)
		}
	}
	if err := HarnessModelError(catalog.Model{Name: "Nomic", Modality: "embedding"}); err == nil || !strings.Contains(err.Error(), "Nomic is a embedding model") {
		t.Errorf("message = %v", err)
	}
}

// labelsFor returns the labels an image built with c carries.
func labelsFor(t *testing.T, c *agent.Config) map[string]string {
	t.Helper()
	out := map[string]string{"local-llm.model": "qwen"}
	args := harnessLabels(c)
	for i := 0; i+1 < len(args); i += 2 {
		k, v, _ := strings.Cut(args[i+1], "=")
		out[k] = v
	}
	return out
}

func TestBakedHarness(t *testing.T) {
	if c := BakedHarness(map[string]string{"local-llm.model": "qwen"}); c != nil {
		t.Errorf("no harness: %+v", c)
	}
	if c := BakedHarness(map[string]string{LabelHarness: "0", LabelHarnessTools: "files"}); c != nil {
		t.Errorf("harness=0: %+v", c)
	}

	in, err := NormalizeHarness(&agent.Config{
		Tools:  []string{"web_search", "read_file", "shell"},
		Limits: agent.Limits{MaxSteps: 3},
		Search: agent.SearchConfig{Backend: "searxng", SearXNGURL: "http://searx.lan:8888"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := BakedHarness(labelsFor(t, in))
	if c == nil || c.Limits.MaxSteps != 3 || c.Search.SearXNGURL != "http://searx.lan:8888" || !slices.Equal(c.Tools, in.Tools) {
		t.Fatalf("from the config label: %+v", c)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("read back config doesn't validate: %v", err)
	}
	// What the image config endpoint returns has to go back into a build as is.
	data, _ := json.Marshal(c)
	if back, err := agent.ParseConfig(data); err != nil {
		t.Errorf("config doesn't round-trip: %v (%s)", err, data)
	} else if _, err := NormalizeHarness(&back); err != nil {
		t.Errorf("round-tripped config isn't accepted: %v", err)
	}

	// No config label (or a broken one): the tools label stands in.
	for _, cfg := range []string{"", "{not json", `{"teleport":true}`} {
		l := map[string]string{LabelHarness: "1", LabelHarnessTools: "web_search, files,bogus", LabelHarnessConfig: cfg}
		c := BakedHarness(l)
		if c == nil || !slices.Equal(c.Tools, []string{"web_search", "files"}) || c.Workspace != ImageWorkspace || c.Limits.MaxSteps != agent.DefaultMaxSteps {
			t.Errorf("config %q: %+v", cfg, c)
		}
	}
	if c := BakedHarness(map[string]string{LabelHarness: "1"}); c == nil || len(c.Tools) != 0 {
		t.Errorf("no tools label: %+v", c)
	}
}

func TestHarnessInfo(t *testing.T) {
	if HarnessInfo(nil) != nil {
		t.Error("nil config has info")
	}
	def, _ := NormalizeHarness(&agent.Config{})
	h := HarnessInfo(def)
	if !slices.Equal(h.Tools, []string{"web_search", "fetch_url", "files", "skills"}) || h.Search != "duckduckgo" || !h.Workspace || h.MaxSteps != agent.DefaultMaxSteps {
		t.Errorf("defaults: %+v", h)
	}
	web, _ := NormalizeHarness(&agent.Config{Tools: []string{"fetch_url", "skills"}, Search: agent.SearchConfig{Backend: "searxng", SearXNGURL: "http://s:1"}})
	if h := HarnessInfo(web); h.Workspace || h.Search != "" || h.SearXNGURL != "" || !slices.Equal(h.Tools, []string{"fetch_url", "skills"}) {
		t.Errorf("no search, no files: %+v", h)
	}
	sx, _ := NormalizeHarness(&agent.Config{Tools: []string{"web_search", "shell"}, Search: agent.SearchConfig{Backend: "searxng", SearXNGURL: "http://s:1"}})
	if h := HarnessInfo(sx); !h.Workspace || h.Search != "searxng" || h.SearXNGURL != "http://s:1" {
		t.Errorf("searxng + shell: %+v", h)
	}
	none, _ := NormalizeHarness(&agent.Config{Tools: []string{}})
	if h := HarnessInfo(none); h.Tools == nil || len(h.Tools) != 0 || h.Workspace {
		t.Errorf("no tools: %+v", h)
	}
	if data, _ := json.Marshal(HarnessInfo(none)); !strings.Contains(string(data), `"tools":[]`) {
		t.Errorf("no tools encodes as %s", data)
	}
}

func TestRunArgsWorkspace(t *testing.T) {
	o := RunOptions{Ref: "local-llm/qwen-agent:latest", HostPort: 8082}
	if args := runArgs("docker", "localllm-8082", o, -1); slices.Contains(args, "-v") {
		t.Errorf("no workspace, but a mount: %q", args)
	}
	o.Workspace = "/Users/me/llm-factory/workspace"
	args := runArgs("docker", "localllm-8082", o, -1)
	i := slices.Index(args, "-v")
	if i < 0 || args[i+1] != "/Users/me/llm-factory/workspace:/workspace" {
		t.Errorf("workspace mount missing: %q", args)
	}
	if !slices.Contains(args, "local-llm.workspace=/Users/me/llm-factory/workspace") {
		t.Errorf("workspace label missing: %q", args)
	}
	if args[len(args)-1] != o.Ref {
		t.Errorf("the image must stay last: %q", args)
	}
}
