package filefreak

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	archiveURL = "https://filefreak.link/public/archivist/0a1b2c3d-0000-4000-8000-000000000001"
	upKey      = "ffk_live_0123456789abcdefSECRET"
)

var weights = []byte("pretend these are four gigabytes of weights")

func sumOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// fakeHF is Hugging Face for the archive: HEAD on resolve URLs, model info,
// and license files.
type fakeHF struct {
	srv     *httptest.Server
	mu      sync.Mutex
	heads   int
	gone    bool   // the file is gone (404)
	sha     string // the SHA-256 it publishes
	info    string // model info JSON
	license string
}

func newFakeHF(t *testing.T) *fakeHF {
	f := &fakeHF{sha: sumOf(weights), info: `{"gated": false, "cardData": {"license": "apache-2.0"}, "siblings": [{"rfilename": "LICENSE"}, {"rfilename": "README.md"}, {"rfilename": "model.gguf"}]}`, license: "Apache License 2.0"}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodHead && strings.HasPrefix(r.URL.Path, "/org/model-GGUF/resolve/main/"):
			f.heads++
			if f.gone {
				w.Header().Set("X-Error-Code", "EntryNotFound")
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("X-Repo-Commit", testCommit)
			w.Header().Set("X-Linked-Etag", `"`+f.sha+`"`)
			w.Header().Set("X-Linked-Size", fmt.Sprint(len(weights)))
			w.Header().Set("Location", "https://cdn.example.invalid/x")
			w.WriteHeader(http.StatusFound)
		case r.URL.Path == "/api/models/org/model-GGUF/revision/"+testCommit:
			_, _ = w.Write([]byte(f.info))
		case r.URL.Path == "/org/model-GGUF/resolve/"+testCommit+"/LICENSE":
			_, _ = w.Write([]byte(f.license))
		case r.URL.Path == "/org/model-GGUF/resolve/"+testCommit+"/README.md":
			_, _ = w.Write([]byte("---\nlicense: apache-2.0\n---\n# model"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHF) url(file string) string {
	return f.srv.URL + "/org/model-GGUF/resolve/main/" + file + "?download=true"
}

func (f *fakeHF) set(fn func(*fakeHF)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// fakeRunner is the Node helper: each command answered by handle.
type fakeRunner struct {
	mu       sync.Mutex
	checkErr error
	calls    []string
	keys     []string
	handle   func(cmd string, args map[string]string, key string, onEvent func(Event)) (any, error)
}

func (f *fakeRunner) Check(context.Context) (string, error) { return "v22.11.0", f.checkErr }

func (f *fakeRunner) Run(ctx context.Context, args []string, key string, onEvent func(Event)) (json.RawMessage, error) {
	opts := map[string]string{}
	for _, a := range args[1:] {
		k, v, _ := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		opts[k] = v
	}
	f.mu.Lock()
	f.calls = append(f.calls, strings.Join(args, " "))
	f.keys = append(f.keys, key)
	f.mu.Unlock()
	if f.handle == nil {
		return nil, errors.New("no helper")
	}
	v, err := f.handle(args[0], opts, key, onEvent)
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

func (f *fakeRunner) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// testArchive is an Archive on a temp config dir with the fakes.
func testArchive(t *testing.T, hf *fakeHF, r runner, cfg Config) *Archive {
	t.Helper()
	t.Setenv(ArchiveURLEnv, "")
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "filefreak.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(c *Config) error { *c = cfg; return nil }); err != nil {
		t.Fatal(err)
	}
	q, err := openQueue(filepath.Join(dir, "filefreak-queue.json"))
	if err != nil {
		t.Fatal(err)
	}
	a := newArchive(store, q, &HF{Base: hf.srv.URL, Client: NewHF().Client}, r, Options{StagingDir: filepath.Join(dir, "staging"), Version: "test"})
	t.Cleanup(a.Close)
	return a
}

type logLines struct {
	mu    sync.Mutex
	lines []string
}

func (l *logLines) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, s)
}

func (l *logLines) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// archived answers lookups and downloads as an archive holding weights at
// huggingface/org/model-GGUF/<commit>/model.gguf, listed with sha.
func archived(sha string, downloadErr error) func(string, map[string]string, string, func(Event)) (any, error) {
	return func(cmd string, args map[string]string, _ string, onEvent func(Event)) (any, error) {
		switch cmd {
		case "lookup":
			if args["path"] != "huggingface/org/model-GGUF/"+testCommit+"/model.gguf" {
				return map[string]any{"found": false}, nil
			}
			return map[string]any{"found": true, "file": ArchivedFile{ID: "file-1", Name: "model.gguf", Size: int64(len(weights)), SHA256: sha}}, nil
		case "download":
			if downloadErr != nil {
				_ = os.WriteFile(args["out"]+".part", []byte("half"), 0o644)
				return nil, downloadErr
			}
			if args["sha256"] != sha {
				return nil, &HelperError{Code: "sha256_mismatch", Message: "not the one asked for"}
			}
			onEvent(Event{Event: "progress", Done: int64(len(weights)), Total: int64(len(weights))})
			if err := os.WriteFile(args["out"], weights, 0o644); err != nil {
				return nil, err
			}
			return downloaded{Size: int64(len(weights)), SHA256: sha}, nil
		}
		return nil, fmt.Errorf("unexpected %s", cmd)
	}
}

func TestFetchDoesNothingWithoutAnArchiveOrAKey(t *testing.T) {
	hf := newFakeHF(t)
	r := &fakeRunner{}
	a := testArchive(t, hf, r, Config{})
	done, want, after := a.Fetch(context.Background(), hf.url("model.gguf"), filepath.Join(t.TempDir(), "m"), "model.gguf", func(string) {})
	if done || want != "" || after != nil || hf.heads != 0 || len(r.called()) != 0 {
		t.Fatalf("done=%v want=%q after=%v heads=%d calls=%v", done, want, after != nil, hf.heads, r.called())
	}
	// Not a Hugging Face URL: left alone even with an archive set.
	a = testArchive(t, hf, r, Config{ArchiveURL: archiveURL, UploadKey: upKey})
	if done, want, after := a.Fetch(context.Background(), "https://example.com/x.gguf", "x", "x", func(string) {}); done || want != "" || after != nil {
		t.Fatal("touched a URL that isn't Hugging Face's")
	}
}

func TestFetchFromTheArchive(t *testing.T) {
	hf := newFakeHF(t)
	r := &fakeRunner{handle: archived(sumOf(weights), nil)}
	a := testArchive(t, hf, r, Config{ArchiveURL: archiveURL})
	dest := filepath.Join(t.TempDir(), "model.gguf")
	var log logLines
	done, _, _ := a.Fetch(context.Background(), hf.url("model.gguf"), dest, "model.gguf", log.add)
	if !done {
		t.Fatalf("not from the archive:\n%s", log.text())
	}
	if b, _ := os.ReadFile(dest); string(b) != string(weights) {
		t.Fatalf("dest holds %q", b)
	}
	for _, want := range []string{"revision 0123456", "Downloading model.gguf from the FileFreak archive", "100% (", "Saved model.gguf to models/ from the FileFreak archive"} {
		if !strings.Contains(log.text(), want) {
			t.Errorf("log lacks %q:\n%s", want, log.text())
		}
	}
	calls := r.called()
	if len(calls) != 2 || !strings.Contains(calls[1], "--sha256="+sumOf(weights)) || !strings.Contains(calls[0], "--archive="+archiveURL) {
		t.Errorf("calls: %v", calls)
	}
	for _, k := range r.keys {
		if k != "" {
			t.Error("a read was given the key")
		}
	}
}

func TestFetchIgnoresACopyThatIsntHuggingFacesFile(t *testing.T) {
	hf := newFakeHF(t)
	r := &fakeRunner{handle: archived(strings.Repeat("a", 64), nil)}
	a := testArchive(t, hf, r, Config{ArchiveURL: archiveURL})
	var log logLines
	done, want, after := a.Fetch(context.Background(), hf.url("model.gguf"), filepath.Join(t.TempDir(), "m"), "model.gguf", log.add)
	if done || want != sumOf(weights) || after != nil {
		t.Fatalf("done=%v want=%q after=%v\n%s", done, want, after != nil, log.text())
	}
	if !strings.Contains(log.text(), "isn't the file Hugging Face publishes") {
		t.Errorf("log:\n%s", log.text())
	}
	for _, c := range r.called() {
		if strings.HasPrefix(c, "download") {
			t.Error("downloaded a copy with the wrong SHA-256")
		}
	}
}

func TestFetchFallsBackWhenTheArchiveDownloadFails(t *testing.T) {
	hf := newFakeHF(t)
	r := &fakeRunner{handle: archived(sumOf(weights), &HelperError{Code: "integrity_failed", Message: "did not match"})}
	a := testArchive(t, hf, r, Config{ArchiveURL: archiveURL, UploadKey: upKey})
	dest := filepath.Join(t.TempDir(), "model.gguf")
	var log logLines
	done, want, after := a.Fetch(context.Background(), hf.url("model.gguf"), dest, "model.gguf", log.add)
	if done || want != sumOf(weights) {
		t.Fatalf("done=%v want=%q", done, want)
	}
	// The archive has it, so it isn't queued again.
	if after != nil {
		t.Error("queued a file the archive already has")
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Error("left the archive's .part behind")
	}
	if !strings.Contains(log.text(), "Downloading from Hugging Face instead") {
		t.Errorf("log:\n%s", log.text())
	}
}

func TestFetchWithoutNodeGoesToHuggingFace(t *testing.T) {
	hf := newFakeHF(t)
	r := &fakeRunner{checkErr: errors.New("Node.js 20 or later isn't installed")}
	a := testArchive(t, hf, r, Config{ArchiveURL: archiveURL})
	var log logLines
	done, want, _ := a.Fetch(context.Background(), hf.url("model.gguf"), "m", "model.gguf", log.add)
	if done || want != sumOf(weights) || len(r.called()) != 0 {
		t.Fatalf("done=%v want=%q calls=%v", done, want, r.called())
	}
	if !strings.Contains(log.text(), "not checked (Node.js 20 or later isn't installed)") {
		t.Errorf("log:\n%s", log.text())
	}
}

func TestReadingCanBeTurnedOff(t *testing.T) {
	hf := newFakeHF(t)
	r := &fakeRunner{handle: archived(sumOf(weights), nil)}
	off := false
	a := testArchive(t, hf, r, Config{ArchiveURL: archiveURL, ReadFirst: &off})
	if done, want, after := a.Fetch(context.Background(), hf.url("model.gguf"), "m", "model.gguf", func(string) {}); done || want != "" || after != nil || hf.heads != 0 {
		t.Fatal("read the archive with reading off")
	}
}

// Hugging Face can't be asked (down, or the file is gone): an archived copy is
// used only when its provenance record and FileFreak's checksum agree.
func TestFetchWithoutHuggingFaceUsesTheProvenanceRecord(t *testing.T) {
	good := func(sha string) Candidate {
		return Candidate{Revision: testCommit, File: ArchivedFile{ID: "file-1", Size: int64(len(weights)), SHA256: sha},
			Provenance: &Provenance{Repo: "org/model-GGUF", Ref: "main", Revision: testCommit, File: "model.gguf", SHA256: sha, Size: int64(len(weights)), ArchivedAt: "2026-11-02T10:00:00Z"}}
	}
	sha := sumOf(weights)
	bad := []Candidate{
		{Revision: testCommit, File: ArchivedFile{ID: "x", Size: int64(len(weights)), SHA256: sha}}, // no record
		func() Candidate { c := good(sha); c.Provenance.SHA256 = strings.Repeat("b", 64); return c }(),
		func() Candidate { c := good(sha); c.Provenance.File = "other.gguf"; return c }(),
		func() Candidate { c := good(sha); c.Provenance.Ref = "dev"; return c }(),
		func() Candidate { c := good(sha); c.Provenance.Size++; return c }(),
		func() Candidate { c := good(sha); c.Provenance.Repo = "someone/else"; return c }(),
		func() Candidate { c := good(sha); c.Revision = "fedcba9876543210fedcba9876543210fedcba98"; return c }(),
	}
	for _, mode := range []string{"down", "gone"} {
		t.Run(mode, func(t *testing.T) {
			hf := newFakeHF(t)
			target := hf.url("model.gguf")
			if mode == "down" {
				hf.srv.Close()
			} else {
				hf.set(func(f *fakeHF) { f.gone = true })
			}
			for i, cands := range [][]Candidate{bad, append(append([]Candidate{}, bad...), good(sha))} {
				r := &fakeRunner{handle: func(cmd string, args map[string]string, key string, onEvent func(Event)) (any, error) {
					if cmd == "candidates" {
						if args["repo-dir"] != "huggingface/org/model-GGUF" || args["file"] != "model.gguf" {
							return nil, fmt.Errorf("asked %v", args)
						}
						return map[string]any{"candidates": cands}, nil
					}
					return archived(sha, nil)(cmd, args, key, onEvent)
				}}
				a := testArchive(t, hf, r, Config{ArchiveURL: archiveURL, UploadKey: upKey})
				dest := filepath.Join(t.TempDir(), "model.gguf")
				var log logLines
				done, want, after := a.Fetch(context.Background(), target, dest, "model.gguf", log.add)
				if want != "" || after != nil {
					t.Errorf("unconfirmed by Hugging Face, yet want=%q queued=%v", want, after != nil)
				}
				if i == 0 {
					if done {
						t.Errorf("used a copy whose record doesn't agree:\n%s", log.text())
					}
					if downloads := strings.Count(strings.Join(r.called(), "\n"), "download "); downloads != 0 {
						t.Errorf("%d downloads of copies whose record doesn't agree", downloads)
					}
					continue
				}
				if !done {
					t.Fatalf("didn't use the copy whose record agrees:\n%s", log.text())
				}
				if !strings.Contains(log.text(), "without Hugging Face's confirmation") || !strings.Contains(log.text(), "archived 2026-11-02") {
					t.Errorf("log doesn't say so:\n%s", log.text())
				}
			}
		})
	}
}

func TestDownloadedFilesAreQueuedOnlyWithAKey(t *testing.T) {
	hf := newFakeHF(t)
	notThere := func(cmd string, args map[string]string, key string, onEvent func(Event)) (any, error) {
		return map[string]any{"found": false}, nil
	}
	// No key: nothing is queued.
	a := testArchive(t, hf, &fakeRunner{handle: notThere}, Config{ArchiveURL: archiveURL})
	if _, want, after := a.Fetch(context.Background(), hf.url("model.gguf"), "m", "model.gguf", func(string) {}); after != nil || want == "" {
		t.Fatal("a keyless install queued an upload")
	}

	// With a key: queued once. (The worker can't run: the fake has no uploads.)
	r := &fakeRunner{handle: notThere, checkErr: nil}
	a = testArchive(t, hf, r, Config{ArchiveURL: archiveURL, UploadKey: upKey})
	a.Close() // no worker: this test reads the queue as Fetch leaves it
	dest := filepath.Join(t.TempDir(), "model.gguf")
	var log logLines
	_, want, after := a.Fetch(context.Background(), hf.url("model.gguf"), dest, "model.gguf", log.add)
	if want != sumOf(weights) || after == nil {
		t.Fatalf("want=%q after=%v", want, after != nil)
	}
	after(strings.Repeat("c", 64), 3) // a download that didn't match isn't queued
	if len(a.queue.list()) != 0 {
		t.Fatal("queued a file that isn't Hugging Face's")
	}
	after(want, int64(len(weights)))
	_, _, again := a.Fetch(context.Background(), hf.url("model.gguf"), dest, "model.gguf", log.add)
	again(want, int64(len(weights)))
	jobs := a.queue.list()
	if len(jobs) != 1 {
		t.Fatalf("jobs: %+v", jobs)
	}
	j := jobs[0]
	if j.Repo != "org/model-GGUF" || j.Revision != testCommit || j.File != "model.gguf" || j.Ref != "main" || j.SHA256 != want || j.State != StateQueued ||
		j.LocalPath != dest || j.SourceURL != hf.srv.URL+"/org/model-GGUF/resolve/"+testCommit+"/model.gguf" {
		t.Errorf("job: %+v", j)
	}
	for _, s := range []string{"FileFreak archive: queued model.gguf", "already in the upload queue"} {
		if !strings.Contains(log.text(), s) {
			t.Errorf("log lacks %q:\n%s", s, log.text())
		}
	}
	// The queue is on disk, readable by its owner only.
	fi, err := os.Stat(a.queue.path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("queue file mode %v", fi.Mode().Perm())
	}
}

func TestTheKeyStaysInItsFile(t *testing.T) {
	hf := newFakeHF(t)
	r := &fakeRunner{handle: func(cmd string, args map[string]string, key string, _ func(Event)) (any, error) {
		if cmd != "ensure-folder" || key != upKey {
			return nil, fmt.Errorf("unexpected %s", cmd)
		}
		return KeyFolder{ID: "folder-1", Name: FolderName, Username: "archivist", URL: archiveURL, Public: true}, nil
	}}
	a := testArchive(t, hf, r, Config{})
	if _, err := a.SetKey(context.Background(), "has a space"); err == nil {
		t.Error("took a key with a space")
	}
	folderErr, err := a.SetKey(context.Background(), " "+upKey+"\n")
	if err != nil || folderErr != nil {
		t.Fatal(err, folderErr)
	}
	st := a.Status(context.Background())
	if !st.Key.Set || st.Key.Hint != "ffk_live_…CRET" || st.Folder == nil || st.Folder.URL != archiveURL || st.Archive.Source != "key" || st.Archive.URL != archiveURL {
		t.Errorf("status: %+v", st)
	}
	b, _ := json.Marshal(st)
	if strings.Contains(string(b), upKey) || strings.Contains(string(b), keySum(upKey)) {
		t.Errorf("the status carries the key: %s", b)
	}
	data, err := os.ReadFile(a.store.path)
	if err != nil || !strings.Contains(string(data), upKey) {
		t.Fatalf("key not saved: %v", err)
	}
	if fi, _ := os.Stat(a.store.path); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("config file mode %v", fi.Mode().Perm())
	}

	// An env override wins over the key's folder; the setting comes after both.
	t.Setenv(ArchiveURLEnv, "https://filefreak.link/public/other/0a1b2c3d-0000-4000-8000-000000000009")
	if st := a.Status(context.Background()); st.Archive.Source != "env" {
		t.Errorf("env: %+v", st.Archive)
	}
	t.Setenv(ArchiveURLEnv, "")

	if err := a.RemoveKey(); err != nil {
		t.Fatal(err)
	}
	if err := a.SetArchiveURL("https://example.com/not-a-folder"); err == nil {
		t.Error("took a link that isn't a public folder")
	}
	if err := a.SetArchiveURL(archiveURL); err != nil {
		t.Fatal(err)
	}
	st = a.Status(context.Background())
	if st.Key.Set || st.Folder != nil || st.Archive.Source != "setting" {
		t.Errorf("after removing the key: %+v", st)
	}
	if data, _ := os.ReadFile(a.store.path); strings.Contains(string(data), upKey) {
		t.Error("the removed key is still in the file")
	}
}

// ---------------------------------------------------------------------------
// The worker
// ---------------------------------------------------------------------------

// fakeFileFreak answers the keyed commands as a FileFreak drive kept in memory.
type fakeFileFreak struct {
	mu        sync.Mutex
	files     map[string]string // path under the key's top -> SHA-256
	uploads   []string          // dir/name of each upload
	staged    map[string]string // name -> content, read as the upload was asked for
	failNext  bool              // the next weight upload stops with an upload id
	resumedAs []string          // --resume of each weight upload
}

func newFakeFileFreak() *fakeFileFreak {
	return &fakeFileFreak{files: map[string]string{}, staged: map[string]string{}}
}

func (f *fakeFileFreak) handle(cmd string, args map[string]string, key string, onEvent func(Event)) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if key != upKey {
		return nil, fmt.Errorf("%s without the key", cmd)
	}
	switch cmd {
	case "ensure-folder":
		return KeyFolder{ID: "folder-1", Name: args["name"], Username: "archivist", URL: archiveURL, Public: true}, nil
	case "exists":
		if sha, ok := f.files[args["path"]]; ok {
			return map[string]any{"found": true, "file": ArchivedFile{ID: "x", SHA256: sha, URL: "https://filefreak.link/api/public/files/x/raw"}}, nil
		}
		return map[string]any{"found": false}, nil
	case "upload":
		if args["parent"] != "folder-1" {
			return nil, fmt.Errorf("uploaded under %s", args["parent"])
		}
		data, err := os.ReadFile(args["file"])
		if err != nil {
			return nil, err
		}
		where := args["dir"] + "/" + args["name"]
		f.uploads = append(f.uploads, where)
		if strings.HasSuffix(args["name"], ".gguf") {
			f.resumedAs = append(f.resumedAs, args["resume"])
			onEvent(Event{Event: "begun", UploadID: "up-1"})
			if f.failNext {
				f.failNext = false
				return nil, &HelperError{Code: "network_error", Message: "The upload stopped. The API could not be reached.", UploadID: "up-1"}
			}
			if args["sha256"] != sumOf(data) {
				return nil, &HelperError{Code: "sha256_mismatch", Message: "hashes to something else"}
			}
			onEvent(Event{Event: "progress", Done: int64(len(data)), Total: int64(len(data))})
		} else {
			f.staged[args["name"]] = string(data)
		}
		f.files[FolderName+"/"+where] = sumOf(data)
		return map[string]any{"file": ArchivedFile{ID: "up", Name: args["name"], SHA256: sumOf(data), URL: "https://filefreak.link/api/public/files/up/raw"}}, nil
	}
	return nil, fmt.Errorf("unexpected %s", cmd)
}

// queued is an archive with a key and one job for weights on disk.
func queued(t *testing.T, hf *fakeHF, ff *fakeFileFreak) (*Archive, Job) {
	t.Helper()
	a := testArchive(t, hf, &fakeRunner{handle: ff.handle}, Config{UploadKey: upKey})
	a.retryIn = func(int) time.Duration { return time.Millisecond }
	local := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(local, weights, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.queue.add(Job{Repo: "org/model-GGUF", Ref: "main", Revision: testCommit, File: "model.gguf", Label: "model.gguf",
		LocalPath: local, SHA256: sumOf(weights), Size: int64(len(weights)), SourceURL: hf.url("model.gguf")}, time.Now()); err != nil {
		t.Fatal(err)
	}
	return a, a.queue.list()[0]
}

// settle waits until no job is queued, checking or uploading.
func settle(t *testing.T, a *Archive) Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		jobs := a.queue.list()
		if len(jobs) == 1 && jobs[0].finished() {
			return jobs[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the worker didn't finish: %+v", a.queue.list())
	return Job{}
}

func TestTheWorkerArchivesALicensedFile(t *testing.T) {
	hf := newFakeHF(t)
	ff := newFakeFileFreak()
	ff.failNext = true // the first try stops part way, and is carried on
	a, _ := queued(t, hf, ff)
	a.kick()
	j := settle(t, a)
	if j.State != StateDone || j.License != "apache-2.0" || j.URL == "" || j.UploadID != "" || j.Attempts != 2 {
		t.Fatalf("job: %+v", j)
	}
	dir := "huggingface/org/model-GGUF/" + testCommit
	want := []string{dir + "/LICENSE", dir + "/README.md", dir + "/model.gguf", dir + "/model.gguf", dir + "/model.gguf.provenance.json"}
	if strings.Join(ff.uploads, "\n") != strings.Join(want, "\n") {
		t.Errorf("uploads:\n%s\nwant:\n%s", strings.Join(ff.uploads, "\n"), strings.Join(want, "\n"))
	}
	if len(ff.resumedAs) != 2 || ff.resumedAs[0] != "" || ff.resumedAs[1] != "up-1" {
		t.Errorf("resumed as %q", ff.resumedAs)
	}
	if ff.staged["LICENSE"] != "Apache License 2.0" {
		t.Errorf("LICENSE: %q", ff.staged["LICENSE"])
	}
	var rec Provenance
	if err := json.Unmarshal([]byte(ff.staged["model.gguf.provenance.json"]), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Repo != "org/model-GGUF" || rec.Ref != "main" || rec.Revision != testCommit || rec.File != "model.gguf" || rec.SHA256 != sumOf(weights) ||
		rec.Size != int64(len(weights)) || rec.License != "apache-2.0" || rec.SourceURL != hf.url("model.gguf") || rec.ArchivedAt == "" ||
		strings.Join(rec.Notices, ",") != "LICENSE,README.md" {
		t.Errorf("provenance: %+v", rec)
	}
	if entries, _ := os.ReadDir(a.staging); len(entries) != 0 {
		t.Errorf("staging left behind: %v", entries)
	}
}

func TestTheWorkerSkipsWhatTheLicenseDoesntAllow(t *testing.T) {
	for name, info := range map[string]string{
		"gemma":  `{"gated": false, "cardData": {"license": "gemma"}}`,
		"gated":  `{"gated": "manual", "cardData": {"license": "apache-2.0"}}`,
		"llama":  `{"gated": false, "cardData": {"license": "llama3.2"}, "tags": ["license:llama3.2"]}`,
		"silent": `{"gated": false}`,
	} {
		t.Run(name, func(t *testing.T) {
			hf := newFakeHF(t)
			hf.set(func(f *fakeHF) { f.info = info })
			ff := newFakeFileFreak()
			a, _ := queued(t, hf, ff)
			a.kick()
			j := settle(t, a)
			if j.State != StateSkipped || !strings.HasPrefix(j.Reason, "Not archived: ") || len(ff.uploads) != 0 {
				t.Errorf("job: %+v, uploads %v", j, ff.uploads)
			}
		})
	}
}

func TestTheWorkerDoesntUploadWhatTheArchiveHas(t *testing.T) {
	hf := newFakeHF(t)
	ff := newFakeFileFreak()
	ff.files[FolderName+"/huggingface/org/model-GGUF/"+testCommit+"/model.gguf"] = sumOf(weights)
	a, _ := queued(t, hf, ff)
	a.kick()
	j := settle(t, a)
	if j.State != StateDone || j.Reason != "Already in the archive." {
		t.Errorf("job: %+v", j)
	}
	// Only the missing provenance record goes up.
	if len(ff.uploads) != 1 || !strings.HasSuffix(ff.uploads[0], ".provenance.json") {
		t.Errorf("uploads: %v", ff.uploads)
	}
}

func TestTheWorkerNeedsTheFileOnDisk(t *testing.T) {
	hf := newFakeHF(t)
	ff := newFakeFileFreak()
	a, j := queued(t, hf, ff)
	os.Remove(j.LocalPath)
	a.kick()
	if j := settle(t, a); j.State != StateFailed || !strings.Contains(j.Reason, "gone or has changed") {
		t.Errorf("job: %+v", j)
	}
	// Retry puts it back in line.
	if err := a.Retry(j.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(j.LocalPath, weights, 0o644); err != nil {
		t.Fatal(err)
	}
	if j := settle(t, a); j.State != StateDone {
		t.Errorf("after a retry: %+v", j)
	}
	if err := a.Remove(j.ID); err != nil || len(a.queue.list()) != 0 {
		t.Errorf("remove: %v %v", err, a.queue.list())
	}
	if err := a.Remove(j.ID); !errors.Is(err, ErrNoJob) {
		t.Errorf("remove again: %v", err)
	}
}

func TestTheWorkerWaitsForAKey(t *testing.T) {
	hf := newFakeHF(t)
	ff := newFakeFileFreak()
	a, _ := queued(t, hf, ff)
	if err := a.RemoveKey(); err != nil {
		t.Fatal(err)
	}
	a.kick()
	time.Sleep(30 * time.Millisecond)
	if j := a.queue.list()[0]; j.State != StateQueued || len(ff.uploads) != 0 {
		t.Fatalf("worked without a key: %+v", j)
	}
	if _, err := a.SetKey(context.Background(), upKey); err != nil {
		t.Fatal(err)
	}
	if j := settle(t, a); j.State != StateDone {
		t.Errorf("job: %+v", j)
	}
}

// The System page's queue: no local paths, and a next try only when one waits.
func TestStatusShowsTheQueue(t *testing.T) {
	hf := newFakeHF(t)
	ff := newFakeFileFreak()
	a, _ := queued(t, hf, ff)
	if err := a.RemoveKey(); err != nil {
		t.Fatal(err)
	}
	view := func() string {
		data, err := json.Marshal(a.Status(context.Background()))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	got := view()
	if !strings.Contains(got, `"state":"queued"`) || strings.Contains(got, "next_try") || strings.Contains(got, "local_path") {
		t.Errorf("status: %s", got)
	}
	id := a.queue.list()[0].ID
	a.queue.update(id, false, func(j *Job) { j.NextTry, j.Reason = time.Now().Add(time.Hour), "FileFreak is busy" })
	if got := view(); !strings.Contains(got, `"next_try":"`) {
		t.Errorf("no next try: %s", got)
	}
}

// A restart in the middle of an upload carries it on with its upload id.
func TestTheQueueSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "filefreak-queue.json")
	q, err := openQueue(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.add(Job{Repo: "org/r", Revision: testCommit, File: "a.gguf"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	id := q.list()[0].ID
	q.update(id, true, func(j *Job) { j.State, j.UploadID, j.Sent = StateUploading, "up-9", 100 })
	again, err := openQueue(path)
	if err != nil {
		t.Fatal(err)
	}
	j := again.list()[0]
	if j.State != StateQueued || j.UploadID != "up-9" {
		t.Errorf("after a restart: %+v", j)
	}
	if next, _ := again.next(time.Now()); next == nil || next.ID != id {
		t.Errorf("not next: %+v", next)
	}
}

func TestTheQueueKeepsSoManyFinished(t *testing.T) {
	q, _ := openQueue(filepath.Join(t.TempDir(), "q.json"))
	start := time.Now()
	for i := 0; i < keepFinished+10; i++ {
		_, _ = q.add(Job{Repo: "org/r", Revision: testCommit, File: fmt.Sprintf("%d.gguf", i)}, start)
		id := q.list()[len(q.list())-1].ID
		q.update(id, true, func(j *Job) { j.State, j.UpdatedAt = StateDone, start.Add(time.Duration(i)*time.Second) })
	}
	if n := len(q.list()); n != keepFinished {
		t.Errorf("%d kept", n)
	}
	if q.list()[0].File != "10.gguf" {
		t.Errorf("oldest kept: %s", q.list()[0].File)
	}
}

func TestBackoff(t *testing.T) {
	want := []time.Duration{time.Minute, 4 * time.Minute, 16 * time.Minute, 64 * time.Minute, 4 * time.Hour, 4 * time.Hour}
	for i, w := range want {
		if got := backoff(i + 1); got != w {
			t.Errorf("backoff(%d) = %v, want %v", i+1, got, w)
		}
	}
}

func TestKeyHint(t *testing.T) {
	for key, want := range map[string]string{
		"":                              "",
		"ffk_live_abcdefghijklmnop1234": "ffk_live_…1234",
		"ffp_live_short":                "ffp_live_…",
		"plainkeywithoutaprefix9876":    "…9876",
	} {
		if got := KeyHint(key); got != want {
			t.Errorf("KeyHint(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestCheckArchiveURL(t *testing.T) {
	for _, ok := range []string{archiveURL, "http://127.0.0.1:8080/public/archivist/0A1B2C3D-0000-4000-8000-000000000001"} {
		if err := CheckArchiveURL(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "http://filefreak.link/public/archivist/0a1b2c3d-0000-4000-8000-000000000001", "https://filefreak.link/share/abc",
		"https://filefreak.link/public/archivist/0a1b2c3d-0000-4000-8000-000000000001?x=1", "https://filefreak.link/public/a/0a1b2c3d-0000-4000-8000-000000000001"} {
		if err := CheckArchiveURL(bad); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}
}
