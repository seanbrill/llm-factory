package builder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeArchive is a WeightArchive that answers as told.
type fakeArchive struct {
	fill       []byte // when set, Fetch writes it to dest and reports done
	want       string
	gotSum     string
	gotSize    int64
	calledBack bool
}

func (f *fakeArchive) Fetch(_ context.Context, _, dest, _ string, log func(string)) (bool, string, func(string, int64)) {
	if f.fill != nil {
		log("from the archive")
		return os.WriteFile(dest, f.fill, 0o644) == nil, "", nil
	}
	return false, f.want, func(sum string, size int64) { f.calledBack, f.gotSum, f.gotSize = true, sum, size }
}

func weightServer(t *testing.T, body string) (*httptest.Server, *int) {
	t.Helper()
	gets := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gets++
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &gets
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestDownloadToUsesTheArchiveFirst(t *testing.T) {
	b := &Builder{Archive: &fakeArchive{fill: []byte("archived")}}
	srv, gets := weightServer(t, "official")
	dest := filepath.Join(t.TempDir(), "m.gguf")
	if err := b.downloadTo(context.Background(), srv.URL+"/m.gguf", dest, "m.gguf", func(string) {}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(dest); string(data) != "archived" || *gets != 0 {
		t.Errorf("dest %q, %d downloads", data, *gets)
	}
}

func TestDownloadToChecksTheSHA256(t *testing.T) {
	srv, _ := weightServer(t, "official")
	dir := t.TempDir()

	// Matches: saved, and the archive hears about it.
	fa := &fakeArchive{want: sum("official")}
	b := &Builder{Archive: fa}
	var lines []string
	dest := filepath.Join(dir, "good.gguf")
	if err := b.downloadTo(context.Background(), srv.URL+"/m.gguf", dest, "good.gguf", func(s string) { lines = append(lines, s) }); err != nil {
		t.Fatal(err)
	}
	if !fa.calledBack || fa.gotSum != sum("official") || fa.gotSize != int64(len("official")) {
		t.Errorf("callback: %+v", fa)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "(SHA-256 checked)") {
		t.Errorf("log: %v", lines)
	}

	// Doesn't match: deleted, an error, and nothing reported.
	fa = &fakeArchive{want: strings.Repeat("0", 64)}
	b = &Builder{Archive: fa}
	bad := filepath.Join(dir, "bad.gguf")
	err := b.downloadTo(context.Background(), srv.URL+"/m.gguf", bad, "bad.gguf", func(string) {})
	if err == nil || !strings.Contains(err.Error(), "doesn't match the SHA-256") {
		t.Fatalf("want a mismatch error, got %v", err)
	}
	for _, p := range []string{bad, bad + ".part"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s kept", p)
		}
	}
	if fa.calledBack {
		t.Error("reported a download that didn't match")
	}

	// No archive: as before, unchecked.
	b = &Builder{}
	if err := b.downloadTo(context.Background(), srv.URL+"/m.gguf", filepath.Join(dir, "plain.gguf"), "plain.gguf", func(string) {}); err != nil {
		t.Fatal(err)
	}
}
