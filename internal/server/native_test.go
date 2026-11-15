package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seanbrill/local-llm/internal/builder"
	"github.com/seanbrill/local-llm/internal/catalog"
)

type fakePackager struct {
	mu      sync.Mutex
	got     []builder.NativeOptions
	release chan struct{} // nil = finish at once
	err     error
}

func (f *fakePackager) build(ctx context.Context, o builder.NativeOptions, log builder.LogFunc) (*builder.NativePackage, error) {
	f.mu.Lock()
	f.got = append(f.got, o)
	rel := f.release
	f.mu.Unlock()
	log("Making a native package of " + o.Model.Name)
	if rel != nil {
		select {
		case <-rel:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	return &builder.NativePackage{Name: o.Name}, nil
}

func (f *fakePackager) last() builder.NativeOptions {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.got[len(f.got)-1]
}

func newNativeTest(t *testing.T) (*nativeAPI, *fakePackager, http.Handler) {
	t.Helper()
	b, err := builder.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cat := &catalog.Catalog{Models: []catalog.Model{
		{ID: "tiny-chat", Name: "Tiny Chat", File: "tiny.gguf", SizeGB: 0.4},
		{ID: "emb", Name: "Embedder", File: "emb.gguf", Modality: "embedding"},
	}}
	s := &Server{b: b, cat: cat, mux: newRouteMux()}
	fp := &fakePackager{}
	n := &nativeAPI{s: s, q: newBuildQueue(), build: fp.build, jobs: map[string]nativeJobInfo{},
		labels: func(_ context.Context, engine, ref string) (map[string]string, error) {
			if ref != "factory/tiny:latest" {
				return nil, errors.New("no such image")
			}
			return map[string]string{
				"local-llm.model":          "tiny-chat",
				"local-llm.system_prompt":  "You are Tiny.",
				"local-llm.inject_mode":    "always",
				"local-llm.ctx":            "16384",
				builder.LabelHarnessConfig: `{"tools":["files","shell"]}`,
			}, nil
		}}
	mux := http.NewServeMux()
	for p, h := range map[string]http.HandlerFunc{
		"targets": n.handleTargets, "source": n.handleSource, "package": n.handlePackage,
		"jobs": n.handleJobs, "state": n.handleState, "cancel": n.handleCancel,
		"packages": n.handlePackages, "download": n.handleDownload, "delete": n.handleDelete,
	} {
		mux.HandleFunc("/api/native/"+p, h)
	}
	return n, fp, mux
}

func call(t *testing.T, h http.Handler, method, target, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, strings.NewReader(body)))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func waitNative(t *testing.T, h http.Handler, id, want string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, st := call(t, h, http.MethodGet, "/api/native/state?id="+id, "")
		if st["status"] == want {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s: %v, want %s", id, st["status"], want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestNativeRoutesAreRegistered(t *testing.T) {
	b, _ := builder.New(t.TempDir())
	s := &Server{b: b, cat: &catalog.Catalog{}, mux: newRouteMux()}
	s.registerNative()
	if !strings.Contains(strings.Join(s.mux.patterns, " "), "/api/native/package") {
		t.Errorf("patterns = %v", s.mux.patterns)
	}
}

func TestNativeTargetsAndSource(t *testing.T) {
	_, _, h := newNativeTest(t)
	code, out := call(t, h, http.MethodGet, "/api/native/targets", "")
	targets, _ := out["targets"].([]any)
	if code != 200 || len(targets) == 0 || targets[0].(map[string]any)["id"] != "macos-arm64" {
		t.Fatalf("targets %d %v", code, out)
	}

	code, src := call(t, h, http.MethodGet, "/api/native/source?model_id=tiny-chat", "")
	if code != 200 || src["capable"] != true || src["ctx_size"] != float64(builder.DefaultNativeCtx) || src["harness"] != nil || src["inject_mode"] != "missing" {
		t.Errorf("catalog source %d %v", code, src)
	}
	code, src = call(t, h, http.MethodGet, "/api/native/source?ref=factory/tiny:latest&engine=docker", "")
	harness, _ := src["harness"].(map[string]any)
	if code != 200 || src["system_prompt"] != "You are Tiny." || src["ctx_size"] != float64(16384) || src["inject_mode"] != "always" ||
		src["from_image"] != "factory/tiny:latest" || src["shell_dropped"] != true || harness == nil {
		t.Fatalf("image source %d %v", code, src)
	}
	if tools, _ := harness["tools"].([]any); len(tools) != 1 || tools[0] != "files" {
		t.Errorf("harness tools = %v", harness["tools"])
	}
	if code, src = call(t, h, http.MethodGet, "/api/native/source?model_id=emb", ""); code != 200 || src["capable"] != false || src["why"] == "" {
		t.Errorf("embedding %d %v", code, src)
	}
	if code, _ = call(t, h, http.MethodGet, "/api/native/source?ref=nope:1", ""); code != http.StatusNotFound {
		t.Errorf("missing image: %d", code)
	}
	if code, _ = call(t, h, http.MethodGet, "/api/native/source", ""); code != http.StatusBadRequest {
		t.Errorf("no ref: %d", code)
	}
}

func TestNativePackageJob(t *testing.T) {
	_, fp, h := newNativeTest(t)
	// From an image, its settings carried over; the prompt overridden.
	code, out := call(t, h, http.MethodPost, "/api/native/package", `{"ref":"factory/tiny:latest","engine":"docker","target":"macos-arm64","system_prompt":"Be brief."}`)
	if code != 200 || out["package"] != "tiny-chat-macos-arm64" {
		t.Fatalf("package %d %v", code, out)
	}
	st := waitNative(t, h, out["id"].(string), jobDone)
	if st["package"] != "tiny-chat-macos-arm64" || st["target"] != "macos-arm64" || st["kind"] != "native" {
		t.Errorf("state = %v", st)
	}
	o := fp.last()
	if o.SystemPrompt != "Be brief." || o.InjectMode != "always" || o.CtxSize != 16384 || o.FromImage != "factory/tiny:latest" ||
		o.Harness == nil || strings.Join(o.Harness.Tools, ",") != "files" || o.Model.ID != "tiny-chat" {
		t.Errorf("options = %+v", o)
	}
	// From the catalog, harness turned off, a name of its own.
	code, out = call(t, h, http.MethodPost, "/api/native/package", `{"model_id":"tiny-chat","target":"linux-x64","name":"my-tiny","harness":false,"ctx_size":4096}`)
	if code != 200 || out["package"] != "my-tiny" {
		t.Fatalf("package %d %v", code, out)
	}
	waitNative(t, h, out["id"].(string), jobDone)
	if o := fp.last(); o.Harness != nil || o.CtxSize != 4096 || o.Target != "linux-x64" || o.SystemPrompt != "" {
		t.Errorf("options = %+v", o)
	}
	_, jobs := call(t, h, http.MethodGet, "/api/native/jobs", "")
	if list, _ := jobs["jobs"].([]any); len(list) != 2 {
		t.Errorf("jobs = %v", jobs)
	}

	for body, want := range map[string]int{
		`{"model_id":"tiny-chat","target":"plan9"}`:                                    http.StatusBadRequest,
		`{"model_id":"emb","target":"macos-arm64"}`:                                    http.StatusBadRequest,
		`{"model_id":"tiny-chat","target":"macos-arm64","name":"../x"}`:                http.StatusBadRequest,
		`{"model_id":"tiny-chat","target":"macos-arm64","harness":{"tools":["nope"]}}`: http.StatusBadRequest,
		`{"target":"macos-arm64"}`:                                                     http.StatusBadRequest,
		`{"ref":"nope:1","target":"macos-arm64"}`:                                      http.StatusNotFound,
	} {
		if code, out := call(t, h, http.MethodPost, "/api/native/package", body); code != want {
			t.Errorf("%s: %d %v, want %d", body, code, out, want)
		}
	}
}

func TestNativePackageFailsAndCancels(t *testing.T) {
	_, fp, h := newNativeTest(t)
	fp.err = errors.New("connection refused while downloading llama.cpp")
	_, out := call(t, h, http.MethodPost, "/api/native/package", `{"model_id":"tiny-chat","target":"macos-arm64"}`)
	st := waitNative(t, h, out["id"].(string), jobError)
	// The message as it is, not the container engine's hint.
	if st["error"] != "connection refused while downloading llama.cpp" {
		t.Errorf("error = %v", st["error"])
	}

	fp.err = nil
	fp.release = make(chan struct{})
	_, out = call(t, h, http.MethodPost, "/api/native/package", `{"model_id":"tiny-chat","target":"macos-arm64"}`)
	id := out["id"].(string)
	waitNative(t, h, id, jobRunning)
	if code, _ := call(t, h, http.MethodPost, "/api/native/package", `{"model_id":"tiny-chat","target":"macos-arm64"}`); code != http.StatusConflict {
		t.Errorf("the same package twice: %d", code)
	}
	if code, _ := call(t, h, http.MethodPost, "/api/native/delete", `{"name":"tiny-chat-macos-arm64"}`); code != http.StatusConflict {
		t.Errorf("delete while making it: %d", code)
	}
	if code, _ := call(t, h, http.MethodPost, "/api/native/cancel", `{"id":"`+id+`"}`); code != 200 {
		t.Errorf("cancel: %d", code)
	}
	waitNative(t, h, id, jobCanceled)
	if code, _ := call(t, h, http.MethodPost, "/api/native/cancel", `{"id":"nope"}`); code != http.StatusNotFound {
		t.Errorf("cancel unknown: %d", code)
	}
}

func TestNativePackagesDownloadDelete(t *testing.T) {
	n, _, h := newNativeTest(t)
	dir := n.s.b.PackagesDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	zip := filepath.Join(dir, "tiny-chat-macos-arm64.zip")
	if err := os.WriteFile(zip, []byte("PK zip bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := strings.Repeat("ab", 32)
	if err := os.WriteFile(zip+".sha256", []byte(sum+"  tiny-chat-macos-arm64.zip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, out := call(t, h, http.MethodGet, "/api/native/packages", "")
	if list, _ := out["packages"].([]any); len(list) != 1 || list[0].(map[string]any)["name"] != "tiny-chat-macos-arm64" {
		t.Errorf("packages = %v", out)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/native/download?name=tiny-chat-macos-arm64", nil))
	if rec.Code != 200 || rec.Body.String() != "PK zip bytes" || rec.Header().Get("X-Checksum-Sha256") != sum ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), `filename="tiny-chat-macos-arm64.zip"`) {
		t.Errorf("download %d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
	req := httptest.NewRequest(http.MethodGet, "/api/native/download?name=tiny-chat-macos-arm64", nil)
	req.Header.Set("Range", "bytes=3-5")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "zip" {
		t.Errorf("range %d %q", rec.Code, rec.Body.String())
	}
	for _, name := range []string{"../config/factory-token", "nope", ""} {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/native/download?name="+name, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("download %q: %d", name, rec.Code)
		}
	}

	if code, _ := call(t, h, http.MethodPost, "/api/native/delete", `{"name":"tiny-chat-macos-arm64"}`); code != 200 {
		t.Errorf("delete: %d", code)
	}
	if _, err := os.Stat(zip); err == nil {
		t.Error("the zip is still there")
	}
	if code, _ := call(t, h, http.MethodPost, "/api/native/delete", `{"name":"tiny-chat-macos-arm64"}`); code != http.StatusNotFound {
		t.Errorf("delete twice: %d", code)
	}
}
