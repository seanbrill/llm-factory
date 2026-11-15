package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ff is the factory as a browser on this computer addresses it (the guard checks the Host).
const ff = "http://localhost:8799"

const testFFKey = "ffk_live_0123456789abcdefTOPSECRETxyz"

// newArchiveServer is the real server with no FileFreak settings from the
// developer's shell, and no helper (tools/filefreak isn't in its temp dir), so
// nothing reaches FileFreak.
func newArchiveServer(t *testing.T) (*Server, http.Handler, map[string]string) {
	t.Helper()
	for _, k := range []string{"FILEFREAK_ARCHIVE_URL", "FILEFREAK_HELPER", "FILEFREAK_NODE", "FILEFREAK_API_URL", "FILEFREAK_LINKS_ORIGIN", "FILEFREAK_KEY"} {
		t.Setenv(k, "")
	}
	s := newAuthServer(t, nil)
	if s.ff == nil || s.b.Archive == nil {
		t.Fatalf("the archive isn't set up: %v", s.ffErr)
	}
	t.Cleanup(s.ff.Close)
	hdr := tokenHeader(s)
	hdr["Content-Type"] = "application/json"
	return s, s.Handler(), hdr
}

func TestFileFreakKeyIsKeptPrivate(t *testing.T) {
	s, h, hdr := newArchiveServer(t)

	rec := send(h, "POST", ff+"/api/filefreak/key", `{"key": "`+testFFKey+`"}`, hdr)
	if rec.Code != http.StatusOK {
		t.Fatalf("set key: %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if strings.Contains(body, testFFKey) || strings.Contains(body, "TOPSECRET") {
		t.Errorf("the answer carries the key: %s", body)
	}
	out := jsonBody(t, rec)
	st := out["status"].(map[string]any)
	key := st["key"].(map[string]any)
	if key["set"] != true || key["hint"] != "ffk_live_…Txyz" {
		t.Errorf("key: %v", key)
	}
	// No helper in a temp dir: the key is kept, and the folder says why it isn't found yet.
	if out["folder_error"] == nil {
		t.Errorf("no folder error: %v", out)
	}

	file := filepath.Join(s.b.ConfigDir, "filefreak.json")
	data, err := os.ReadFile(file)
	if err != nil || !strings.Contains(string(data), testFFKey) {
		t.Fatalf("key not saved: %v", err)
	}
	if fi, _ := os.Stat(file); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("filefreak.json mode %v", fi.Mode().Perm())
	}

	if rec := send(h, "GET", ff+"/api/filefreak/status", "", hdr); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "TOPSECRET") {
		t.Errorf("status: %d %s", rec.Code, rec.Body)
	}

	if rec := send(h, "POST", ff+"/api/filefreak/key/remove", `{}`, hdr); rec.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body)
	}
	if data, _ := os.ReadFile(file); strings.Contains(string(data), testFFKey) {
		t.Error("the removed key is still saved")
	}
}

func TestFileFreakSettings(t *testing.T) {
	_, h, hdr := newArchiveServer(t)
	link := "https://filefreak.link/public/archivist/0a1b2c3d-0000-4000-8000-000000000001"

	rec := send(h, "GET", ff+"/api/filefreak/status", "", hdr)
	st := jsonBody(t, rec)["status"].(map[string]any)
	if st["read_from_archive"] != true || st["archive"].(map[string]any)["source"] != "none" || st["helper"].(map[string]any)["ok"] != false {
		t.Errorf("defaults: %v", st)
	}

	rec = send(h, "POST", ff+"/api/filefreak/settings", `{"read_from_archive": false, "archive_url": "`+link+`"}`, hdr)
	st = jsonBody(t, rec)["status"].(map[string]any)
	if rec.Code != http.StatusOK || st["read_from_archive"] != false || st["archive_setting"] != link || st["archive"].(map[string]any)["source"] != "setting" {
		t.Errorf("after saving: %d %v", rec.Code, st)
	}

	if rec := send(h, "POST", ff+"/api/filefreak/settings", `{"archive_url": "https://example.com/files"}`, hdr); rec.Code != http.StatusBadRequest {
		t.Errorf("a link that isn't a public folder: %d %s", rec.Code, rec.Body)
	}
	if rec := send(h, "POST", ff+"/api/filefreak/key", `{"key": "has spaces in it, not a key"}`, hdr); rec.Code != http.StatusBadRequest {
		t.Errorf("a bad key: %d %s", rec.Code, rec.Body)
	}
	if rec := send(h, "POST", ff+"/api/filefreak/queue/retry", `{"id": "nope"}`, hdr); rec.Code != http.StatusNotFound {
		t.Errorf("retry of nothing: %d %s", rec.Code, rec.Body)
	}
}

// The routes act on secrets, so they are same-origin JSON only, as the harness's are.
func TestFileFreakRoutesAreGuarded(t *testing.T) {
	s, h, _ := newArchiveServer(t)
	tok := tokenHeader(s)
	if rec := send(h, "POST", ff+"/api/filefreak/key", `{"key": "`+testFFKey+`"}`, tok); rec.Code != http.StatusForbidden {
		t.Errorf("a form post: %d", rec.Code)
	}
	hdr := map[string]string{"Content-Type": "application/json", "Origin": "https://evil.example", "Authorization": tok["Authorization"]}
	if rec := send(h, "POST", ff+"/api/filefreak/key", `{"key": "`+testFFKey+`"}`, hdr); rec.Code != http.StatusForbidden {
		t.Errorf("a cross-site post: %d", rec.Code)
	}
	if rec := send(h, "GET", ff+"/api/filefreak/key", "", tok); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET of the key route: %d", rec.Code)
	}
	if s.ff.Status(context.Background()).Key.Set {
		t.Error("a refused request set the key")
	}
}
