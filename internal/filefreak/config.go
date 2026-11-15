package filefreak

// config/filefreak.json: the archive settings and the upload key. Written
// atomically with owner-only permissions (0600), as config/harness.json is,
// because it holds the key. The key never leaves this file except as the
// helper's FILEFREAK_KEY environment variable: it is not logged, and the UI
// only ever sees a short hint of it.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Config is what config/filefreak.json holds.
type Config struct {
	// ReadFirst: look in the archive before downloading from Hugging Face.
	// Nil means the default, on.
	ReadFirst *bool `json:"read_from_archive,omitempty"`
	// UploadKey is a FileFreak API key. Only an install with one uploads.
	UploadKey string `json:"upload_key,omitempty"`
	// ArchiveURL is the public folder link readers use, as entered on the
	// System page. FILEFREAK_ARCHIVE_URL and the key's own folder come first.
	ArchiveURL string `json:"archive_url,omitempty"`
	// Folder is the key's archive folder, once the factory has made or found it.
	Folder *KeyFolder `json:"key_folder,omitempty"`
}

// KeyFolder is the public folder the upload key's archive lives in.
type KeyFolder struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"`
	URL      string `json:"url"`
	// Public is false while the account's public page is switched off: the
	// folder is public, but nobody can see it until it is on.
	Public    bool      `json:"public"`
	CheckedAt time.Time `json:"checked_at"`
	// KeySum names the key that found it (a hash prefix, not the key), so a
	// replaced key looks its folder up again.
	KeySum string `json:"key_sum"`
}

// readFirst is ReadFirst with its default.
func (c Config) readFirst() bool { return c.ReadFirst == nil || *c.ReadFirst }

// keySum is a short hash of a key, to tell keys apart without keeping a second copy.
func keySum(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:6])
}

// KeyHint is how the UI shows a key: its kind and its last four characters.
func KeyHint(key string) string {
	if key == "" {
		return ""
	}
	prefix := ""
	if i := strings.Index(key, "_live_"); i > 0 && i < 8 {
		prefix = key[:i+len("_live_")]
	}
	if len(key) < len(prefix)+12 {
		return prefix + "…"
	}
	return prefix + "…" + key[len(key)-4:]
}

// CheckKey is the shape a FileFreak key has: no spaces, a sensible length.
func CheckKey(key string) error {
	if key == "" {
		return errors.New("paste the upload key")
	}
	if len(key) < 16 || len(key) > 400 {
		return errors.New("that doesn't look like a FileFreak key: it should be a long string starting with ffk_live_")
	}
	for _, r := range key {
		if r <= ' ' || r == 0x7f {
			return errors.New("that key has spaces or line breaks in it: paste it again without them")
		}
	}
	return nil
}

// Store loads and saves config/filefreak.json.
type Store struct {
	mu   sync.Mutex
	path string
	cfg  Config
}

// OpenStore reads path; a missing file is the defaults.
func OpenStore(path string) (*Store, error) {
	s := &Store{path: path}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &s.cfg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
		}
		// Tighten a file a hand edit left readable by others.
		_ = os.Chmod(path, 0o600)
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, err
	}
	return s, nil
}

// Get is a copy of the config.
func (s *Store) Get() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.cfg
	if c.Folder != nil {
		f := *c.Folder
		c.Folder = &f
	}
	return c
}

// Update applies fn to a copy and saves it; nothing changes when fn or the save fails.
func (s *Store) Update(fn func(*Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cfg
	if next.Folder != nil {
		f := *next.Folder
		next.Folder = &f
	}
	if err := fn(&next); err != nil {
		return err
	}
	if err := writePrivate(s.path, next); err != nil {
		return err
	}
	s.cfg = next
	return nil
}

// writePrivate writes v as JSON to path atomically, readable by its owner only.
func writePrivate(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // a no-op after the rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
