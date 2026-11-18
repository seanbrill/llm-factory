package filefreak

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/seanbrill/local-llm/internal/catalog"
)

func TestParseHFURL(t *testing.T) {
	ok := map[string]HFFile{
		"https://huggingface.co/unsloth/Qwen3.5-2B-GGUF/resolve/main/Qwen3.5-2B-Q4_K_M.gguf?download=true": {"unsloth/Qwen3.5-2B-GGUF", "main", "Qwen3.5-2B-Q4_K_M.gguf"},
		"https://huggingface.co/Comfy-Org/flux2-dev/resolve/main/split_files/vae/flux2-vae.safetensors":    {"Comfy-Org/flux2-dev", "main", "split_files/vae/flux2-vae.safetensors"},
		"https://huggingface.co/org/repo/resolve/refs%2Fpr%2F1/a%20b.gguf":                                 {"org/repo", "refs/pr/1", "a b.gguf"},
		"https://HuggingFace.co/org/repo/resolve/0123456789abcdef0123456789abcdef01234567/x.gguf":          {"org/repo", "0123456789abcdef0123456789abcdef01234567", "x.gguf"},
	}
	for raw, want := range ok {
		got, good := ParseHFURL(raw, DefaultHFBase)
		if !good || got != want {
			t.Errorf("ParseHFURL(%q) = %+v, %v; want %+v", raw, got, good, want)
		}
	}
	for _, raw := range []string{
		"https://example.com/org/repo/resolve/main/x.gguf",
		"http://huggingface.co/org/repo/resolve/main/x.gguf",
		"https://huggingface.co/datasets/org/repo/resolve/main/x.gguf",
		"https://huggingface.co/spaces/org/repo/resolve/main/x.gguf",
		"https://huggingface.co/org/repo/blob/main/x.gguf",
		"https://huggingface.co/org/repo/resolve/main",
		"https://huggingface.co/gpt2/resolve/main/x.bin",
		"https://huggingface.co/org/repo/resolve/main/../x.gguf",
		"https://huggingface.co/org/repo/resolve/main/a%2Fb.gguf",
		"https://user:pw@huggingface.co/org/repo/resolve/main/x.gguf",
		"",
	} {
		if got, good := ParseHFURL(raw, DefaultHFBase); good {
			t.Errorf("ParseHFURL(%q) = %+v, want not a model file", raw, got)
		}
	}
}

// Every weight URL in the catalog is a file of a Hugging Face model repository
// at main, as the archive's lookups expect.
func TestEveryCatalogURLParses(t *testing.T) {
	cat, err := catalog.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	check := func(id, raw string) {
		if raw == "" {
			return
		}
		n++
		f, ok := ParseHFURL(raw, DefaultHFBase)
		if !ok {
			t.Errorf("%s: %s isn't a Hugging Face model file URL", id, raw)
			return
		}
		if f.Ref != "main" || f.Path == "" || strings.Count(f.Repo, "/") != 1 {
			t.Errorf("%s: %s parsed as %+v", id, raw, f)
		}
	}
	for _, m := range cat.Models {
		check(m.ID, m.URL)
		check(m.ID, m.MMProjURL)
		for _, w := range m.ExtraFiles {
			check(m.ID, w.URL)
		}
	}
	if n < 50 {
		t.Errorf("only %d catalog URLs checked", n)
	}
}

const (
	testCommit = "0123456789abcdef0123456789abcdef01234567"
	testSHA    = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
)

func TestHeadReadsTheRedirect(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/old/repo/resolve/main/m.gguf":
			// A renamed repository: a relative redirect to its new name.
			w.Header().Set("Location", "/new/repo/resolve/main/m.gguf")
			w.WriteHeader(http.StatusTemporaryRedirect)
		case "/new/repo/resolve/main/m.gguf":
			w.Header().Set("X-Repo-Commit", testCommit)
			w.Header().Set("X-Linked-Etag", `"`+testSHA+`"`)
			w.Header().Set("X-Linked-Size", "1234567")
			w.Header().Set("Location", "https://cdn.example.invalid/blob")
			w.WriteHeader(http.StatusFound)
		case "/new/repo/resolve/main/README.md":
			w.Header().Set("X-Repo-Commit", testCommit)
			w.Header().Set("ETag", `"abc123"`)
			w.Header().Set("Content-Length", "42")
			w.WriteHeader(http.StatusOK)
		case "/new/repo/resolve/main/gone.gguf":
			w.Header().Set("X-Error-Code", "EntryNotFound")
			w.WriteHeader(http.StatusNotFound)
		case "/private/repo/resolve/main/m.gguf":
			w.Header().Set("X-Error-Code", "RepoNotFound")
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	hf := &HF{Base: srv.URL, Client: NewHF().Client}
	ctx := context.Background()

	got, err := hf.Head(ctx, srv.URL+"/old/repo/resolve/main/m.gguf?download=true")
	if err != nil || got != (Official{Commit: testCommit, SHA256: testSHA, Size: 1234567}) {
		t.Fatalf("LFS file: %+v %v", got, err)
	}
	for _, a := range asked {
		if !strings.HasPrefix(a, "HEAD ") || strings.Contains(a, "cdn") {
			t.Errorf("asked %s", a)
		}
	}
	if got, err := hf.Head(ctx, srv.URL+"/new/repo/resolve/main/README.md"); err != nil || got.SHA256 != "" || got.Commit != testCommit || got.Size != 42 {
		t.Errorf("non-LFS file: %+v %v", got, err)
	}
	for _, p := range []string{"/new/repo/resolve/main/gone.gguf", "/private/repo/resolve/main/m.gguf"} {
		if _, err := hf.Head(ctx, srv.URL+p); !errors.Is(err, ErrGone) {
			t.Errorf("%s: want ErrGone, got %v", p, err)
		}
	}
	if _, err := hf.Head(ctx, srv.URL+"/broken/repo/resolve/main/x"); err == nil || errors.Is(err, ErrGone) {
		t.Errorf("a 500 is not gone: %v", err)
	}
}

func TestInfoAndTheLicenseRule(t *testing.T) {
	bodies := map[string]string{
		"apache":  `{"gated": false, "cardData": {"license": "apache-2.0"}, "tags": ["gguf", "license:apache-2.0"], "siblings": [{"rfilename": "README.md"}, {"rfilename": "LICENSE"}, {"rfilename": "q4/model.gguf"}, {"rfilename": "model.gguf"}]}`,
		"gated":   `{"gated": "auto", "cardData": {"license": "apache-2.0"}}`,
		"gemma":   `{"gated": false, "cardData": {"license": "gemma"}, "tags": ["license:gemma"]}`,
		"other":   `{"gated": false, "cardData": {"license": "other", "license_name": "custom", "license_link": "LICENSE"}}`,
		"none":    `{"gated": false, "tags": ["gguf"]}`,
		"mixed":   `{"gated": false, "cardData": {"license": ["mit", "llama3.1"]}}`,
		"rail":    `{"gated": false, "cardData": {"license": "CreativeML-OpenRAIL-M"}}`,
		"private": `{"private": true, "gated": false, "cardData": {"license": "mit"}}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var org, name, commit string
		if _, err := fmt.Sscanf(strings.ReplaceAll(r.URL.Path, "/", " "), " api models %s %s revision %s", &org, &name, &commit); err != nil || commit != testCommit {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, ok := bodies[name]
		if !ok {
			w.Header().Set("X-Error-Code", "RepoNotFound")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	hf := &HF{Base: srv.URL, Client: srv.Client()}
	ctx := context.Background()

	info, err := hf.Info(ctx, "org/apache", testCommit)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(info.Licenses, []string{"apache-2.0"}) || info.Gated || len(info.Siblings) != 4 {
		t.Errorf("apache: %+v", info)
	}
	if got := NoticeFiles(info.Siblings); !reflect.DeepEqual(got, []string{"LICENSE", "README.md"}) {
		t.Errorf("notices: %v", got)
	}

	cases := map[string]string{ // repo -> "" (archived) or a word of the reason
		"apache":  "",
		"rail":    "",
		"gated":   "gated",
		"gemma":   "gemma",
		"other":   "other",
		"none":    "no license",
		"mixed":   "llama3.1",
		"private": "private",
	}
	for repo, why := range cases {
		info, err := hf.Info(ctx, "org/"+repo, testCommit)
		if err != nil {
			t.Fatalf("%s: %v", repo, err)
		}
		ok, license, reason := LicenseVerdict(info)
		if why == "" && (!ok || license == "") {
			t.Errorf("%s: want archived, got %v %q", repo, ok, reason)
		}
		if why != "" && (ok || !strings.Contains(reason, why)) {
			t.Errorf("%s: want refused for %q, got %v %q", repo, why, ok, reason)
		}
	}
	if _, err := hf.Info(ctx, "org/missing", testCommit); !errors.Is(err, ErrGone) {
		t.Errorf("missing repo: %v", err)
	}
}

func TestNoticeFiles(t *testing.T) {
	got := NoticeFiles([]string{"model.gguf", "LICENSE.txt", "licence", "NOTICE", "COPYING", "readme.md", "README.txt", "docs/LICENSE", "USE_POLICY.md", "Acceptable-Use-Policy.md", "usage.md"})
	want := []string{"Acceptable-Use-Policy.md", "COPYING", "LICENSE.txt", "NOTICE", "USE_POLICY.md", "licence", "readme.md"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestFileURL(t *testing.T) {
	hf := &HF{Base: DefaultHFBase}
	if got := hf.FileURL("org/repo", testCommit, "sub dir/a+b.gguf"); got != "https://huggingface.co/org/repo/resolve/"+testCommit+"/sub%20dir/a+b.gguf" {
		t.Errorf("got %s", got)
	}
}
