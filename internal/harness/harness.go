// Package harness wires the factory's local models into Open WebUI as an agent
// harness: a key-protected OpenAI-compatible gateway in front of every running
// model container, a sandboxed workspace terminal (Open Terminal) the model can
// run commands and edit files in, a folder of starter skills, and a connector
// that provisions all of it in Open WebUI with one click.
//
// This package is pure logic (config, keys, validation, docker-arg assembly,
// skill parsing, model discovery, the Open WebUI client). The HTTP handlers and
// the gateway proxy live in internal/server/harness.go; engine calls go through
// internal/builder so every container command shares the same engine helpers.
package harness

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

const (
	// DefaultTerminalPort is the host port (loopback) the terminal publishes on.
	DefaultTerminalPort = 8798
	// DefaultVariant is the Open Terminal image tag used unless the user picks another.
	DefaultVariant = "slim"
	// DefaultReachHost is how a containerized Open WebUI reaches this machine.
	DefaultReachHost = "host.docker.internal"

	gatewayKeyPrefix  = "sk-llmf-"
	terminalKeyPrefix = "otk-"
	configVersion     = 1
)

// Variant describes one Open Terminal image tag the factory offers.
type Variant struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Size     string `json:"size"`
	Desc     string `json:"desc"`
	MemoryMB int    `json:"memory_mb"` // default container memory limit
}

// Variants are the Open Terminal image tags offered in the UI. Slim is the
// default: a small Debian sandbox (git, curl, jq). Latest is the full toolkit.
var Variants = []Variant{
	{ID: "slim", Label: "Slim", Size: "~430 MB", Desc: "Minimal Debian sandbox with git, curl and jq. Browser and desktop are turned off.", MemoryMB: 2048},
	{ID: "latest", Label: "Full", Size: "several GB", Desc: "Full toolkit: Python, Node.js, build tools, a browser, a desktop and document tools.", MemoryMB: 4096},
}

// VariantByID returns the variant with that id.
func VariantByID(id string) (Variant, bool) {
	for _, v := range Variants {
		if v.ID == id {
			return v, true
		}
	}
	return Variant{}, false
}

// Config is the harness state persisted in config/harness.json (0600). It holds
// secrets: the gateway key Open WebUI uses to call /v1, and the terminal's API
// key. The Open WebUI admin key is never stored.
type Config struct {
	Version    int            `json:"version"`
	GatewayKey string         `json:"gateway_key"`
	Terminal   TerminalConfig `json:"terminal"`
	OpenWebUI  OpenWebUIPrefs `json:"openwebui"`
}

// TerminalConfig configures the managed Open Terminal container.
type TerminalConfig struct {
	Variant           string `json:"variant"`             // Open Terminal tag: slim | latest
	Port              int    `json:"port"`                // host port, published on 127.0.0.1
	Workspace         string `json:"workspace"`           // host folder; "" = <repo>/workspace
	ShareClaudeSkills bool   `json:"share_claude_skills"` // mount ~/.claude/skills read-only
	APIKey            string `json:"api_key"`             // OPEN_TERMINAL_API_KEY
	MemoryMB          int    `json:"memory_mb,omitempty"` // 0 = the variant's default
}

// OpenWebUIPrefs remembers the (non-secret) connector inputs between visits.
type OpenWebUIPrefs struct {
	URL       string `json:"url,omitempty"`        // Open WebUI as reached from the factory
	ReachHost string `json:"reach_host,omitempty"` // this machine as reached from Open WebUI
}

// Memory returns the terminal memory limit in MB (the variant default unless set).
func (t TerminalConfig) Memory() int {
	if t.MemoryMB > 0 {
		return t.MemoryMB
	}
	if v, ok := VariantByID(t.Variant); ok {
		return v.MemoryMB
	}
	return 2048
}

// ReachHost returns where Open WebUI reaches this machine (default host.docker.internal).
func (c Config) ReachHost() string {
	if c.OpenWebUI.ReachHost != "" {
		return c.OpenWebUI.ReachHost
	}
	return DefaultReachHost
}

// NewKey returns prefix + 48 hex chars (192 bits) from crypto/rand.
func NewKey(prefix string) (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate key: %w", err)
	}
	return prefix + hex.EncodeToString(b), nil
}

// NewGatewayKey returns a fresh gateway bearer key.
func NewGatewayKey() (string, error) { return NewKey(gatewayKeyPrefix) }

// NewTerminalKey returns a fresh Open Terminal API key.
func NewTerminalKey() (string, error) { return NewKey(terminalKeyPrefix) }

// fillDefaults sets every missing field (including generating missing keys) and
// reports whether anything changed.
func (c *Config) fillDefaults() (bool, error) {
	changed := false
	if c.Version != configVersion {
		c.Version = configVersion
		changed = true
	}
	if c.GatewayKey == "" {
		k, err := NewKey(gatewayKeyPrefix)
		if err != nil {
			return changed, err
		}
		c.GatewayKey, changed = k, true
	}
	if _, ok := VariantByID(c.Terminal.Variant); !ok {
		c.Terminal.Variant, changed = DefaultVariant, true
	}
	if c.Terminal.Port == 0 {
		c.Terminal.Port, changed = DefaultTerminalPort, true
	}
	if c.Terminal.APIKey == "" {
		k, err := NewKey(terminalKeyPrefix)
		if err != nil {
			return changed, err
		}
		c.Terminal.APIKey, changed = k, true
	}
	return changed, nil
}

// Store loads and saves config/harness.json. All access goes through it so
// reads and writes are serialized.
type Store struct {
	mu   sync.Mutex
	path string
	cfg  Config
}

// OpenStore loads path, creating it with defaults and fresh keys when missing.
// A config that can't be persisted still works for this process (an error is
// returned alongside the usable store so the caller can log it).
func OpenStore(path string) (*Store, error) {
	s := &Store{path: path}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if jerr := json.Unmarshal(data, &s.cfg); jerr != nil {
			return nil, fmt.Errorf("parse %s: %w", filepath.Base(path), jerr)
		}
		// Tighten a file an older build (or a hand edit) left group/world-readable.
		_ = os.Chmod(path, 0o600)
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, err
	}
	changed, err := s.cfg.fillDefaults()
	if err != nil {
		return nil, err
	}
	if changed {
		if werr := s.saveLocked(); werr != nil {
			return s, fmt.Errorf("save %s: %w", filepath.Base(path), werr)
		}
	}
	return s, nil
}

// Path is the config file location.
func (s *Store) Path() string { return s.path }

// Get returns a copy of the current config.
func (s *Store) Get() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

// Update applies fn to a copy of the config and persists it. Nothing changes
// when fn returns an error.
func (s *Store) Update(fn func(*Config) error) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cfg
	if err := fn(&next); err != nil {
		return s.cfg, err
	}
	if _, err := next.fillDefaults(); err != nil {
		return s.cfg, err
	}
	prev := s.cfg
	s.cfg = next
	if err := s.saveLocked(); err != nil {
		s.cfg = prev
		return prev, err
	}
	return next, nil
}

// saveLocked writes the config atomically with owner-only permissions.
func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".harness-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
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
	return os.Rename(tmpName, s.path)
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

// ValidatePort checks a terminal host port: unprivileged and in range.
func ValidatePort(p int) error {
	if p < 1024 || p > 65535 {
		return fmt.Errorf("port must be between 1024 and 65535 (got %d)", p)
	}
	return nil
}

var reachHostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)

// ValidateReachHost checks the host name Open WebUI uses to reach this machine.
func ValidateReachHost(h string) error {
	if ip := net.ParseIP(strings.Trim(h, "[]")); ip != nil {
		return nil
	}
	if !reachHostRe.MatchString(h) {
		return fmt.Errorf("%q is not a valid host name", h)
	}
	return nil
}

var driveRe = regexp.MustCompile(`^[A-Za-z]:/`)

// splitDrive separates a Windows drive prefix ("C:") from the rest of a
// forward-slash path.
func splitDrive(p string) (drive, rest string) {
	if driveRe.MatchString(p) {
		return p[:2], p[2:]
	}
	return "", p
}

// NormalizeHostPath converts backslashes to slashes and cleans the path,
// keeping a Windows drive prefix intact ("C:\x\..\y" -> "C:/y").
func NormalizeHostPath(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), `\`, "/")
	drive, rest := splitDrive(p)
	if rest == "" {
		return p
	}
	return drive + path.Clean(rest)
}

// hostJoin joins a host base path and elements with forward slashes (host paths
// may be Windows paths, so filepath.Join's OS separator is wrong here).
func hostJoin(base string, elems ...string) string {
	b := strings.TrimRight(strings.ReplaceAll(base, `\`, "/"), "/")
	for _, e := range elems {
		b += "/" + strings.Trim(e, "/")
	}
	return b
}

// samePath compares normalized host paths (case-insensitively for Windows drives).
func samePath(a, b string) bool {
	if driveRe.MatchString(a) || driveRe.MatchString(b) {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// within reports whether p equals root or sits inside it.
func within(p, root string) bool {
	_, ok := relTo(p, root)
	return ok
}

// relTo returns p relative to root ("" when equal) and whether p is root or
// inside it. Windows drive paths compare case-insensitively.
func relTo(p, root string) (string, bool) {
	if root == "" {
		return "", false
	}
	if samePath(p, root) {
		return "", true
	}
	prefix := strings.TrimRight(root, "/") + "/"
	if driveRe.MatchString(p) || driveRe.MatchString(root) {
		if len(p) >= len(prefix) && strings.EqualFold(p[:len(prefix)], prefix) {
			return p[len(prefix):], true
		}
		return "", false
	}
	if strings.HasPrefix(p, prefix) {
		return p[len(prefix):], true
	}
	return "", false
}

// systemDirs are host locations a workspace may never be, sit inside, or
// contain (so "/var" is refused because it holds /var/run/docker.sock).
var systemDirs = []string{
	"/etc", "/bin", "/sbin", "/usr", "/lib", "/lib64", "/boot", "/dev", "/proc", "/sys",
	"/run", "/root", "/var/run", "/var/lib", "/System", "/Library", "/private/etc", "/private/var",
	"/Applications", "/mnt/wsl",
	"C:/Windows", "C:/Program Files", "C:/Program Files (x86)", "C:/ProgramData",
}

// parentDirs hold many users' or volumes' data: a workspace may sit inside them
// but never be one of them (or above them).
var parentDirs = []string{"/Users", "/home", "/Volumes", "/mnt", "/media", "C:/Users"}

// homeDirs are folders under your home that hold app data and credentials.
var homeDirs = []string{"Library", "AppData"}

// ValidateWorkspace checks a user-supplied workspace host folder before it is
// bind-mounted read-write into the model's sandbox. Empty means the default
// (<repo>/workspace). hostDir is the repo's host path; hostHome may be "".
//
// Beyond the syntax rules (absolute, no NUL/newlines, no ':' outside a Windows
// drive letter — it would split the `-v src:dst` spec), it refuses folders that
// would hand the model more than a project folder: the filesystem root, your
// home folder or anything above it, hidden folders (~/.ssh, ~/.aws, …), system
// folders, the repo itself (its config/ holds the harness keys) or anything
// inside config/.
func ValidateWorkspace(p, hostDir, hostHome string) error {
	raw := strings.TrimSpace(p)
	if raw == "" {
		return nil
	}
	if strings.ContainsAny(raw, "\x00\r\n") {
		return errors.New("workspace path contains control characters")
	}
	n := NormalizeHostPath(raw)
	drive, rest := splitDrive(n)
	if drive == "" && !strings.HasPrefix(n, "/") {
		return fmt.Errorf("workspace must be an absolute path on this computer (got %q)", raw)
	}
	if strings.Contains(rest, ":") {
		return errors.New("workspace path can't contain ':' (except a Windows drive letter)")
	}
	if rest == "/" {
		return errors.New("workspace can't be the root of a drive")
	}
	for _, seg := range strings.Split(strings.Trim(rest, "/"), "/") {
		if strings.HasPrefix(seg, ".") {
			return fmt.Errorf("workspace can't be inside a hidden folder (%q)", seg)
		}
	}
	for _, d := range systemDirs {
		if within(n, d) || within(d, n) {
			return fmt.Errorf("workspace can't be, contain or sit inside the system folder %s", d)
		}
	}
	for _, d := range parentDirs {
		if within(d, n) {
			return fmt.Errorf("workspace can't be %s itself; pick a project folder inside it", n)
		}
		// A direct child is a whole home folder (/Users/<you>) or a whole disk
		// (/Volumes/<disk>, /mnt/<disk>; on a Mac often the boot disk again).
		if rel, ok := relTo(n, d); ok && rel != "" && !strings.Contains(rel, "/") {
			return fmt.Errorf("workspace can't be %s (a whole home folder or disk); pick a folder inside it", n)
		}
	}
	if hostHome != "" {
		home := NormalizeHostPath(hostHome)
		if within(home, n) {
			return errors.New("workspace can't be your home folder (or a folder above it); pick a project folder inside it")
		}
		for _, d := range homeDirs {
			if within(n, hostJoin(home, d)) {
				return fmt.Errorf("workspace can't be inside ~/%s (app data and credentials)", d)
			}
		}
	}
	if repo := NormalizeHostPath(hostDir); hostDir != "" {
		if within(repo, n) {
			return errors.New("workspace can't contain the factory folder (its config/ holds the harness keys); use its workspace/ subfolder or another folder")
		}
		// Elsewhere in the factory a writable mount would let the model edit
		// its keys, the skills, the source or the scripts you run on the host.
		if within(n, repo) && !within(n, hostJoin(repo, "workspace")) {
			return errors.New("inside the factory folder only workspace/ can be shared; use it or a folder outside the factory")
		}
	}
	return nil
}
