package main

import (
	"context"
	"encoding/base64"
	"net/url"
	"strings"
	"testing"

	"github.com/seanbrill/local-llm/internal/ensemble"
)

func TestParseMembers(t *testing.T) {
	got, err := parseMembers(" chat=http://host.containers.internal:41001 , see_image=http://127.0.0.1:41002/ ")
	if err != nil || len(got) != 2 || got["chat"].String() != "http://host.containers.internal:41001" || got["see_image"].String() != "http://127.0.0.1:41002" {
		t.Fatalf("parseMembers = %v %v", got, err)
	}
	for _, bad := range []string{"", "chat", "chat=", "chat=ftp://h:1", "chat=http://h:1/v1", "chat=http://u:p@h:1",
		"chat=http://h:1,chat=http://h:2", "chat=localhost:1"} {
		if _, err := parseMembers(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

// With MEMBERS the Conductor starts no containers: it reaches the members the
// app started, and a pasted image still goes to the vision member.
func TestNoSocketMode(t *testing.T) {
	te := newTestEns(t)
	addrs, err := parseMembers("chat=" + te.eng.images["local-llm/chat"].URL + ",see_image=" + te.eng.images["local-llm/vision"].URL)
	if err != nil {
		t.Fatal(err)
	}
	missing, err := te.g.mg.useAddresses(addrs)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(missing, ",") != "transcribe,speak,generate_image,generate_video" {
		t.Errorf("missing = %v", missing)
	}
	te.g.mg.pollEvery = te.g.mg.pollEvery / 10

	te.brain.turns = []turn{{calls: [][2]string{{"look_at_image", `{"question":"What does it say?","image":"1"}`}}}, {text: "It says hi."}}
	img := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("PNG"))
	code, out := te.chat(map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "text", "text": "what's this?"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": img}},
	}}}})
	if code != 200 || message(t, out)["content"] != "It says hi." {
		t.Fatalf("%d %v", code, out)
	}
	if len(te.eng.runs) != 0 {
		t.Errorf("the Conductor started containers: %v", te.eng.runs)
	}
	if n := len(te.vision.bodies(t)); n != 1 {
		t.Errorf("the vision member was asked %d times", n)
	}
	if s := memberState(t, te.g.mg, ensemble.ToolChat); s.State != ensemble.StateReady {
		t.Errorf("chat state = %s", s.State)
	}

	// A member MEMBERS doesn't name can't be used, and says why.
	if _, err := te.g.mg.Acquire(context.Background(), ensemble.ToolGenerateImage, nil); err == nil || !strings.Contains(err.Error(), "MEMBERS gives no address") {
		t.Errorf("an unnamed member: %v", err)
	}
	if _, err := te.g.mg.useAddresses(map[string]*url.URL{"nope": addrs["chat"]}); err == nil {
		t.Error("an unknown tool in MEMBERS should be refused")
	}
}
