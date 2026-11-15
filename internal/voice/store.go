package voice

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ConsentStatement is what the person ticks before recording or uploading a
// voice. The store refuses a clip without it and keeps when it was given.
const ConsentStatement = "This is my voice, or I have permission to use it."

// MaxClipBytes caps an upload: 30 s of mono 32-bit audio at 48 kHz is ~5.8 MB.
const MaxClipBytes = 8 << 20

// Clip is a stored voice reference: metadata only, the audio sits beside it.
type Clip struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Transcript string  `json:"transcript"`
	Seconds    float64 `json:"seconds"`
	SampleRate int     `json:"sample_rate"`
	Bytes      int     `json:"bytes"`
	SHA256     string  `json:"sha256"`
	CreatedAt  int64   `json:"created_at"`
	ConsentAt  int64   `json:"consent_at"`
	Consent    string  `json:"consent"` // the statement ticked
}

// ErrNoClip is returned for an unknown clip id.
var ErrNoClip = errors.New("no such voice clip")

var clipIDRe = regexp.MustCompile(`^v-[0-9a-f]{12}$`)

// ValidClipID reports whether id has the store's shape (it never names a path).
func ValidClipID(id string) bool { return clipIDRe.MatchString(id) }

// Store keeps voice clips under <config>/voices: <id>.wav and <id>.json, both
// 0600 in a 0700 folder. Clips never leave this machine: nothing here is baked
// into an image, exported, or uploaded, and the folder is git-ignored.
type Store struct {
	mu  sync.Mutex
	dir string
}

// NewStore keeps clips in configDir/voices.
func NewStore(configDir string) *Store { return &Store{dir: filepath.Join(configDir, "voices")} }

// Dir is the folder the clips live in.
func (s *Store) Dir() string { return s.dir }

func (s *Store) ensureDir() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(s.dir, 0o700)
}

// writePrivate writes a file 0600 through a temp file, so a crash can't leave
// half a clip or a world-readable one.
func (s *Store) writePrivate(name string, data []byte) error {
	tmp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(s.dir, name))
}

func newClipID() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "v-" + hex.EncodeToString(b[:]), nil
}

// Add stores a clip. wav must pass CheckClip and consent must be given.
func (s *Store) Add(name string, wav []byte, transcript string, consent bool) (Clip, error) {
	if !consent {
		return Clip{}, fmt.Errorf("tick “%s” to save a voice clip", ConsentStatement)
	}
	if len(wav) > MaxClipBytes {
		return Clip{}, fmt.Errorf("the clip is too large (%d MB); keep it to 30 s of mono audio", len(wav)>>20)
	}
	// Stored as 16-bit mono at no more than 48 kHz: the shape every speech
	// runtime reads, and small enough for audio.cpp's 5 MiB reference limit.
	wav, w, err := NormalizeClip(wav)
	if err != nil {
		return Clip{}, err
	}
	id, err := newClipID()
	if err != nil {
		return Clip{}, err
	}
	sum := sha256.Sum256(wav)
	now := time.Now().Unix()
	c := Clip{
		ID: id, Name: cleanName(name, id), Transcript: strings.TrimSpace(transcript),
		Seconds: float64(int(w.Seconds()*10+0.5)) / 10, SampleRate: w.SampleRate, Bytes: len(wav),
		SHA256: hex.EncodeToString(sum[:]), CreatedAt: now, ConsentAt: now, Consent: ConsentStatement,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDir(); err != nil {
		return Clip{}, err
	}
	if err := s.writePrivate(id+".wav", wav); err != nil {
		return Clip{}, err
	}
	if err := s.writeMetaLocked(c); err != nil {
		os.Remove(filepath.Join(s.dir, id+".wav"))
		return Clip{}, err
	}
	return c, nil
}

func cleanName(name, id string) string {
	name = strings.Join(strings.Fields(name), " ")
	if len(name) > 80 {
		name = name[:80]
	}
	if name == "" {
		name = "Voice " + strings.TrimPrefix(id, "v-")
	}
	return name
}

func (s *Store) writeMetaLocked(c Clip) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return s.writePrivate(c.ID+".json", b)
}

func (s *Store) getLocked(id string) (Clip, error) {
	if !ValidClipID(id) {
		return Clip{}, ErrNoClip
	}
	b, err := os.ReadFile(filepath.Join(s.dir, id+".json"))
	if os.IsNotExist(err) {
		return Clip{}, ErrNoClip
	}
	if err != nil {
		return Clip{}, err
	}
	var c Clip
	if err := json.Unmarshal(b, &c); err != nil {
		return Clip{}, fmt.Errorf("voice clip %s: %w", id, err)
	}
	c.ID = id
	return c, nil
}

// Get returns a clip's metadata.
func (s *Store) Get(id string) (Clip, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getLocked(id)
}

// Audio returns a clip's WAV bytes.
func (s *Store) Audio(id string) ([]byte, error) {
	if !ValidClipID(id) {
		return nil, ErrNoClip
	}
	b, err := os.ReadFile(filepath.Join(s.dir, id+".wav"))
	if os.IsNotExist(err) {
		return nil, ErrNoClip
	}
	return b, err
}

// List returns every clip, newest first. A missing folder is an empty list.
func (s *Store) List() ([]Clip, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ents, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return []Clip{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Clip{}
	for _, e := range ents {
		id := strings.TrimSuffix(e.Name(), ".json")
		if e.IsDir() || id == e.Name() || !ValidClipID(id) {
			continue
		}
		if c, err := s.getLocked(id); err == nil {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out, nil
}

// Update changes a clip's name and/or transcript (nil leaves a field as is).
func (s *Store) Update(id string, name, transcript *string) (Clip, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.getLocked(id)
	if err != nil {
		return Clip{}, err
	}
	if name != nil {
		c.Name = cleanName(*name, id)
	}
	if transcript != nil {
		c.Transcript = strings.TrimSpace(*transcript)
	}
	if err := s.writeMetaLocked(c); err != nil {
		return Clip{}, err
	}
	return c, nil
}

// Delete removes a clip's audio and metadata.
func (s *Store) Delete(id string) error {
	if !ValidClipID(id) {
		return ErrNoClip
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	errWav := os.Remove(filepath.Join(s.dir, id+".wav"))
	errMeta := os.Remove(filepath.Join(s.dir, id+".json"))
	if os.IsNotExist(errWav) && os.IsNotExist(errMeta) {
		return ErrNoClip
	}
	for _, e := range []error{errWav, errMeta} {
		if e != nil && !os.IsNotExist(e) {
			return e
		}
	}
	return nil
}
