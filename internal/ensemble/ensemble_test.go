package ensemble

import (
	"errors"
	"go/build"
	"strings"
	"testing"

	"github.com/seanbrill/local-llm/internal/agent"
)

func sample(name string) Ensemble {
	return Ensemble{Name: name, Members: []Member{
		{Tool: ToolChat, Modality: "text", Image: "local-llm/qwen", VRAMGB: 6},
		{Tool: ToolSpeak, Modality: "tts", Image: "local-llm/pocket", VRAMGB: 1},
	}}
}

func TestSaveRefusesClash(t *testing.T) {
	s := NewStore(t.TempDir())
	a, err := s.Save(sample("My Helper"))
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "my-helper" || a.Routing != RoutingAgent || a.PackageMode != "orchestrated" || a.VRAMBudgetGB != 12 {
		t.Errorf("saved = %+v", a)
	}
	// A new ensemble whose name slugs to the same id must not overwrite it.
	other := sample("my  helper!")
	other.Members = other.Members[:1]
	_, err = s.Save(other)
	var ee *ExistsError
	if !errors.As(err, &ee) || !errors.Is(err, ErrExists) || !strings.Contains(err.Error(), "My Helper") {
		t.Fatalf("clash = %v", err)
	}
	got, ok, _ := s.Get("my-helper")
	if !ok || len(got.Members) != 2 {
		t.Errorf("the first ensemble was changed: %+v", got)
	}
	// Editing it (with its id) updates in place.
	a.Name = "My Helper v2"
	a.Persona = "ada"
	if _, err := s.Save(a); err != nil {
		t.Fatal(err)
	}
	list, _ := s.List()
	if len(list) != 1 || list[0].Name != "My Helper v2" || list[0].ID != "my-helper" || list[0].Persona != "ada" {
		t.Errorf("after edit = %+v", list)
	}
	if err := s.Delete("my-helper"); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.List(); len(list) != 0 {
		t.Errorf("after delete = %+v", list)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		want string
		mod  func(*Ensemble)
	}{
		{"name the ensemble", func(e *Ensemble) { e.Name = " " }},
		{"at least one letter or digit", func(e *Ensemble) { e.Name = "!!!" }},
		{"pick at least one member", func(e *Ensemble) { e.Members = nil }},
		{"has no image", func(e *Ensemble) { e.Members[0].Image = "" }},
		{"isn't a member tool", func(e *Ensemble) { e.Members[0].Tool = "juggle" }},
		{"can't be the speak member", func(e *Ensemble) { e.Members[1].Modality = "image" }},
		{"two members are set for chat", func(e *Ensemble) { e.Members[1] = e.Members[0] }},
		{"characters an image name", func(e *Ensemble) { e.ID = "Bad ID" }},
		{"shell tool can't run", func(e *Ensemble) { e.Harness = &agent.Config{Tools: []string{"shell"}} }},
		{"shell tool can't run", func(e *Ensemble) { e.Harness = &agent.Config{Tools: []string{"run_command"}} }},
		{"unknown tools", func(e *Ensemble) { e.Harness = &agent.Config{Tools: []string{"teleport"}} }},
	}
	for _, c := range cases {
		e := sample("ok")
		c.mod(&e)
		e.Normalize()
		if err := e.Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v", c.want, err)
		}
	}
	e := sample("ok")
	e.Members[0].Modality = "vision" // a vision model can be the chat member
	if err := e.Validate(); err != nil {
		t.Errorf("vision chat: %v", err)
	}
}

func TestNormalizeRouting(t *testing.T) {
	for in, want := range map[string]string{"": RoutingAgent, "agent": RoutingAgent, "heuristic": RoutingHeuristic, "tool-calling": RoutingConductor, "magic": RoutingAgent} {
		e := sample("x")
		e.Routing, e.Conductor = in, "local-llm/small"
		e.Normalize()
		if e.Routing != want {
			t.Errorf("%q -> %q", in, e.Routing)
		}
		if (e.Conductor != "") != (want == RoutingConductor) {
			t.Errorf("%q kept conductor %q", in, e.Conductor)
		}
	}
}

func TestHarnessConfig(t *testing.T) {
	if c, err := HarnessConfig(nil); c != nil || err != nil {
		t.Errorf("nil = %v %v", c, err)
	}
	c, err := HarnessConfig(&agent.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.Tools, ",") != "web_search,fetch_url" || c.Workspace != HarnessWorkspace || c.SkillsDir != HarnessSkillsDir {
		t.Errorf("defaults = %+v", c)
	}
	in := &agent.Config{Tools: []string{"files", "skills"}, Workspace: "/home/me"}
	in.Search.APIKey = "secret"
	c, err = HarnessConfig(in)
	if err != nil || c.Workspace != HarnessWorkspace || c.Search.APIKey != "" || in.Workspace != "/home/me" {
		t.Errorf("normalized = %+v %v", c, err)
	}
}

func TestToolFor(t *testing.T) {
	for mod, tool := range map[string]string{"": ToolChat, "code": ToolChat, "vision": ToolSeeImage, "audio-stt": ToolTranscribe,
		"tts": ToolSpeak, "image": ToolGenerateImage, "video": ToolGenerateVideo, "embedding": ""} {
		if got := ToolFor(mod); got != tool {
			t.Errorf("ToolFor(%q) = %q", mod, got)
		}
	}
	if !ToolAccepts(ToolChat, "vision") || ToolAccepts(ToolSpeak, "text") || !ToolAccepts(ToolConductor, "text") {
		t.Error("ToolAccepts")
	}
}

func TestConfigHash(t *testing.T) {
	a := Config{Persona: &ConfigPersona{Name: "Ada", Prompt: "Hi"}}
	b := a
	b.Hash = "whatever"
	if a.ComputeHash() != b.ComputeHash() || len(a.ComputeHash()) != 24 {
		t.Error("the hash depends on Hash")
	}
	c := Config{Persona: &ConfigPersona{Name: "Ada", Prompt: "Hello"}}
	if c.ComputeHash() == a.ComputeHash() {
		t.Error("different personas, same hash")
	}
}

// The Conductor's image build compiles these packages offline: they must
// import the standard library only (and each other).
func TestStdlibOnly(t *testing.T) {
	allowed := map[string]bool{
		"github.com/seanbrill/local-llm/internal/ensemble": true,
		"github.com/seanbrill/local-llm/internal/voice":    true,
		"github.com/seanbrill/local-llm/internal/agent":    true,
	}
	for _, dir := range []string{".", "../voice", "../agent", "../../cmd/ensemblegate"} {
		pkg, err := build.ImportDir(dir, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range pkg.Imports {
			if strings.Contains(strings.SplitN(imp, "/", 2)[0], ".") && !allowed[imp] {
				t.Errorf("%s imports %s (not stdlib)", dir, imp)
			}
		}
	}
}
