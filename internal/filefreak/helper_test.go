package filefreak

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestMain lets the test binary stand in for node: with FAKE_NODE set it acts
// out the helper's side of the protocol instead of running the tests.
func TestMain(m *testing.M) {
	if mode := os.Getenv("FAKE_NODE"); mode != "" {
		os.Exit(fakeNode(mode, os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeNode(mode string, args []string) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println(os.Getenv("FAKE_NODE_VERSION"))
		return 0
	}
	emit := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Println(string(b))
	}
	switch mode {
	case "ok":
		emit(map[string]any{"event": "progress", "done": 1, "total": 2})
		fmt.Println("not json: ignored")
		emit(map[string]any{"event": "begun", "upload_id": "up-1"})
		emit(map[string]any{"event": "result", "data": map[string]any{"args": args[1:], "key": os.Getenv("FILEFREAK_KEY"), "api": os.Getenv("FILEFREAK_API_URL")}})
		return 0
	case "refuse":
		emit(map[string]any{"event": "error", "code": "network_error", "message": "stopped with key " + os.Getenv("FILEFREAK_KEY"), "status": 0, "upload_id": "up-2"})
		return 1
	case "crash":
		fmt.Fprintln(os.Stderr, "TypeError: boom with "+os.Getenv("FILEFREAK_KEY"))
		return 3
	case "hang":
		time.Sleep(time.Minute)
		return 0
	}
	return 9
}

// fakeNodeHelper runs this test binary as node, in mode.
func fakeNodeHelper(t *testing.T, mode string) *NodeHelper {
	t.Helper()
	t.Setenv("FAKE_NODE", mode)
	dir := t.TempDir()
	script := filepath.Join(dir, "ff.mjs")
	if err := os.WriteFile(script, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return &NodeHelper{Node: os.Args[0], Script: script, Env: []string{"FILEFREAK_API_URL=http://127.0.0.1:1"}}
}

func TestNodeHelperRun(t *testing.T) {
	t.Setenv("FILEFREAK_KEY", "a-key-from-the-factory-environment")
	h := fakeNodeHelper(t, "ok")
	var events []Event
	raw, err := h.Run(context.Background(), []string{"upload", "--name=-starts-with-a-dash"}, upKey, func(e Event) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Args []string `json:"args"`
		Key  string   `json:"key"`
		API  string   `json:"api"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Args, " ") != "upload --name=-starts-with-a-dash" || got.Key != upKey || got.API != "http://127.0.0.1:1" {
		t.Errorf("helper saw %+v", got)
	}
	if len(events) != 2 || events[0].Event != "progress" || events[1].UploadID != "up-1" {
		t.Errorf("events %+v", events)
	}

	// A read gets no key at all, not even the factory's own FILEFREAK_KEY.
	raw, _ = h.Run(context.Background(), []string{"lookup"}, "", nil)
	_ = json.Unmarshal(raw, &got)
	if got.Key != "" {
		t.Errorf("a read was given the key %q", got.Key)
	}
}

func TestNodeHelperErrors(t *testing.T) {
	_, err := fakeNodeHelper(t, "refuse").Run(context.Background(), []string{"upload"}, upKey, nil)
	var he *HelperError
	if !errors.As(err, &he) || he.Code != "network_error" || he.UploadID != "up-2" || strings.Contains(he.Message, upKey) {
		t.Errorf("refusal: %#v", err)
	}

	_, err = fakeNodeHelper(t, "crash").Run(context.Background(), []string{"exists"}, upKey, nil)
	if err == nil || !strings.Contains(err.Error(), "TypeError: boom") || strings.Contains(err.Error(), upKey) {
		t.Errorf("crash: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = fakeNodeHelper(t, "hang").Run(ctx, []string{"download"}, "", nil)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 15*time.Second {
		t.Errorf("hang: %v after %v", err, time.Since(start))
	}
}

func TestNodeHelperCheck(t *testing.T) {
	h := fakeNodeHelper(t, "ok")
	t.Setenv("FAKE_NODE_VERSION", "v18.19.0")
	if _, err := h.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "too old") {
		t.Errorf("node 18: %v", err)
	}

	h = fakeNodeHelper(t, "ok")
	t.Setenv("FAKE_NODE_VERSION", "v22.11.0")
	if _, err := h.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "npm ci") {
		t.Errorf("no SDK: %v", err)
	}

	h = fakeNodeHelper(t, "ok")
	sdk := filepath.Join(filepath.Dir(h.Script), "node_modules", "@filefreak.io", "sdk")
	if err := os.MkdirAll(sdk, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sdk, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if v, err := h.Check(context.Background()); err != nil || v != "v22.11.0" {
		t.Errorf("good: %q %v", v, err)
	}

	missing := &NodeHelper{Node: "no-such-node-binary-here", Script: h.Script}
	if _, err := missing.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "isn't installed") {
		t.Errorf("no node: %v", err)
	}
}

// ---------------------------------------------------------------------------
// The real helper (tools/filefreak/ff.mjs, the real SDK) against a fake
// FileFreak site. Skipped where Node 20+ or the helper's node_modules isn't.
// ---------------------------------------------------------------------------

// fakePublicSite serves FileFreak's public answers for one folder tree, as
// the website does at /api/public, and storage that answers ranges.
func fakePublicSite(t *testing.T, tree map[string][]byte, listedSHA map[string]string) (link string, idOf func(string) string) {
	t.Helper()
	type node struct {
		id, name, parent string
		folder           bool
		data             []byte
	}
	nodes := map[string]*node{}
	kids := map[string][]string{}
	n := 0
	newID := func() string {
		n++
		return fmt.Sprintf("0a1b2c3d-0000-4000-8000-%012d", n)
	}
	root := newID()
	nodes[root] = &node{id: root, name: FolderName, folder: true}
	byPath := map[string]string{".": root}
	var ensure func(p string) string
	ensure = func(p string) string {
		if id, ok := byPath[p]; ok {
			return id
		}
		parent := ensure(path.Dir(p))
		id := newID()
		nodes[id] = &node{id: id, name: path.Base(p), parent: parent, folder: true}
		kids[parent] = append(kids[parent], id)
		byPath[p] = id
		return id
	}
	for p, data := range tree {
		parent := ensure(path.Dir(p))
		id := newID()
		nodes[id] = &node{id: id, name: path.Base(p), parent: parent, data: data}
		kids[parent] = append(kids[parent], id)
		byPath[p] = id
	}
	item := func(nd *node) map[string]any {
		it := map[string]any{"id": nd.id, "name": nd.name, "kind": "folder", "sizeBytes": 0, "protected": false, "clientSha256": nil, "createdAt": "2026-11-01T00:00:00Z"}
		if !nd.folder {
			sum := sumOf(nd.data)
			for p, s := range listedSHA {
				if byPath[p] == nd.id {
					sum = s
				}
			}
			it["kind"], it["sizeBytes"], it["clientSha256"] = "file", len(nd.data), sum
		}
		return it
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		js := func(v any) { w.Header().Set("Content-Type", "application/json"); _ = json.NewEncoder(w).Encode(v) }
		switch {
		case len(parts) == 4 && parts[0] == "api" && parts[1] == "public" && parts[2] == "files" && nodes[parts[3]] != nil:
			js(map[string]any{"item": item(nodes[parts[3]]), "crumbs": []any{}, "owner": map[string]any{"username": "archivist"}})
		case len(parts) == 5 && parts[4] == "items" && nodes[parts[3]] != nil:
			var items []any
			for _, id := range kids[parts[3]] {
				items = append(items, item(nodes[id]))
			}
			js(map[string]any{"items": items, "nextOffset": nil, "total": len(items)})
		case len(parts) == 5 && parts[4] == "url" && nodes[parts[3]] != nil:
			js(map[string]any{"url": "http://" + r.Host + "/storage/" + parts[3], "expiresAt": time.Now().Add(time.Hour).Format(time.RFC3339)})
		case len(parts) == 2 && parts[0] == "storage" && nodes[parts[1]] != nil:
			http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(nodes[parts[1]].data))
		default:
			w.WriteHeader(http.StatusNotFound)
			js(map[string]any{"error": "not_found", "message": "That file is not public, or does not exist."})
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/public/archivist/" + root, func(p string) string { return byPath[p] }
}

func realHelper(t *testing.T) *NodeHelper {
	t.Helper()
	if testing.Short() {
		t.Skip("runs Node")
	}
	tools, err := filepath.Abs(filepath.Join("..", "..", "tools", "filefreak"))
	if err != nil {
		t.Fatal(err)
	}
	h := &NodeHelper{Node: "node", Script: filepath.Join(tools, "ff.mjs")}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node isn't installed")
	}
	if _, err := h.Check(context.Background()); err != nil {
		t.Skipf("the helper can't run here: %v", err)
	}
	return h
}

func TestTheRealHelperReadsTheArchive(t *testing.T) {
	h := realHelper(t)
	dir := "huggingface/org/model-GGUF/" + testCommit
	link, idOf := fakePublicSite(t, map[string][]byte{
		dir + "/model.gguf":         weights,
		dir + "/sub/Q4/part-1.gguf": []byte("part one"),
		dir + "/changed.gguf":       []byte("bytes in storage"),
		dir + "/model.gguf" + ".provenance.json": []byte(`{"repo": "org/model-GGUF", "ref": "main", "revision": "` + testCommit +
			`", "file": "model.gguf", "sha256": "` + sumOf(weights) + `", "size": ` + fmt.Sprint(len(weights)) + `, "archived_at": "2026-11-02T00:00:00Z"}`),
	}, map[string]string{dir + "/changed.gguf": sumOf([]byte("bytes that were uploaded"))})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	f, err := lookupFile(ctx, h, link, dir+"/model.gguf")
	if err != nil || f == nil || f.SHA256 != sumOf(weights) || f.Size != int64(len(weights)) || f.ID != idOf(dir+"/model.gguf") {
		t.Fatalf("lookup: %+v %v", f, err)
	}
	if f, err := lookupFile(ctx, h, link, dir+"/sub/Q4/part-1.gguf"); err != nil || f == nil {
		t.Errorf("lookup in a sub-folder: %+v %v", f, err)
	}
	if f, err := lookupFile(ctx, h, link, dir+"/missing.gguf"); err != nil || f != nil {
		t.Errorf("lookup of a missing file: %+v %v", f, err)
	}

	out := filepath.Join(t.TempDir(), "model.gguf")
	var progress []Event
	got, err := downloadFile(ctx, h, link, f.ID, out, sumOf(weights), func(e Event) { progress = append(progress, e) })
	if err != nil || got.SHA256 != sumOf(weights) {
		t.Fatalf("download: %+v %v", got, err)
	}
	if b, _ := os.ReadFile(out); !bytes.Equal(b, weights) {
		t.Error("downloaded bytes differ")
	}
	if len(progress) == 0 {
		t.Error("no progress")
	}

	// Not the SHA-256 Hugging Face publishes: nothing kept.
	wrong := filepath.Join(t.TempDir(), "wrong.gguf")
	var he *HelperError
	if _, err := downloadFile(ctx, h, link, f.ID, wrong, strings.Repeat("a", 64), nil); !errors.As(err, &he) || he.Code != "sha256_mismatch" {
		t.Errorf("mismatch: %v", err)
	}
	// Storage doesn't hold what was uploaded: nothing kept.
	if _, err := downloadFile(ctx, h, link, idOf(dir+"/changed.gguf"), wrong, sumOf([]byte("bytes that were uploaded")), nil); !errors.As(err, &he) || he.Code != "integrity_failed" {
		t.Errorf("changed in storage: %v", err)
	}
	for _, p := range []string{wrong, wrong + ".part"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s left behind", p)
		}
	}

	cands, err := listCandidates(ctx, h, link, "huggingface/org/model-GGUF", "model.gguf")
	if err != nil || len(cands) != 1 || cands[0].Revision != testCommit || cands[0].Provenance == nil || cands[0].Provenance.SHA256 != sumOf(weights) {
		t.Errorf("candidates: %+v %v", cands, err)
	}
}

// The whole read path: Fetch with the real helper, a fake Hugging Face and a fake FileFreak.
func TestFetchWithTheRealHelper(t *testing.T) {
	h := realHelper(t)
	hf := newFakeHF(t)
	link, _ := fakePublicSite(t, map[string][]byte{"huggingface/org/model-GGUF/" + testCommit + "/model.gguf": weights}, nil)
	a := testArchive(t, hf, h, Config{ArchiveURL: "https://filefreak.link/public/placeholder/0a1b2c3d-0000-4000-8000-000000000001"})
	t.Setenv(ArchiveURLEnv, link) // the env override names the local fake
	dest := filepath.Join(t.TempDir(), "model.gguf")
	var log logLines
	done, _, _ := a.Fetch(context.Background(), hf.url("model.gguf"), dest, "model.gguf", log.add)
	if !done {
		t.Fatalf("not from the archive:\n%s", log.text())
	}
	if b, _ := os.ReadFile(dest); !bytes.Equal(b, weights) {
		t.Error("wrong bytes")
	}
	if runtime.GOOS != "windows" {
		if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
			t.Error(".part left behind")
		}
	}
}
