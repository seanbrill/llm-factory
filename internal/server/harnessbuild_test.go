package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/seanbrill/local-llm/internal/builder"
)

func TestParseHarnessOption(t *testing.T) {
	for _, none := range []string{"", "null", "false"} {
		if c, err := parseHarnessOption(json.RawMessage(none)); c != nil || err != nil {
			t.Errorf("%q: %v %v", none, c, err)
		}
	}
	c, err := parseHarnessOption(json.RawMessage("true"))
	if err != nil || c == nil || c.Workspace != builder.ImageWorkspace || len(c.Tools) == 0 {
		t.Fatalf("true: %+v %v", c, err)
	}
	if c.EnabledTools()["run_command"] {
		t.Error("the default harness has the shell on")
	}
	c, err = parseHarnessOption(json.RawMessage(`{"tools":["web_search","shell"],"limits":{"max_steps":3}}`))
	if err != nil || c.Limits.MaxSteps != 3 || !c.EnabledTools()["run_command"] {
		t.Errorf("object: %+v %v", c, err)
	}
	for _, bad := range []string{`{"tools":["teleport"]}`, `{"brave_api_key":"k"}`, `"yes"`, `{"limits":{"max_steps":0.5}}`} {
		if _, err := parseHarnessOption(json.RawMessage(bad)); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestBuildRejectsBadHarness(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/build", strings.NewReader(`{"model_id":"x","image_name":"y","harness":{"tools":["nope"]}}`))
	s.handleBuild(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unknown tools") {
		t.Errorf("status %d: %s", rec.Code, rec.Body)
	}
}
