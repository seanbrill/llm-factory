package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// TerminalContainer is the managed Open Terminal container's name.
	TerminalContainer = "local-llm-terminal"
	// TerminalImageRepo is Open Terminal's published image (tag = variant).
	TerminalImageRepo = "ghcr.io/open-webui/open-terminal"
	// TerminalID is the connection id the connector registers in Open WebUI.
	TerminalID = "local-llm-terminal"
	// TerminalName is the connection's display name in Open WebUI.
	TerminalName = "LLM Factory workspace"
	// SpecLabel stores a hash of the run spec so the UI can tell when the
	// running container no longer matches the saved settings.
	SpecLabel = "local-llm.harness.spec"

	containerHome      = "/home/user"
	containerWorkspace = containerHome + "/workspace"
	containerSkills    = containerHome + "/.agents/skills"
	containerClaude    = containerHome + "/.claude/skills"
)

// TerminalImage is the full image reference for a variant.
func TerminalImage(variant string) string { return TerminalImageRepo + ":" + variant }

// Paths locates the repo from both sides of the container boundary. The factory
// usually runs in a container: it reads files under BaseDir (/app) but must give
// the host engine HOST paths for bind mounts (HostDir, from HOST_DIR).
type Paths struct {
	BaseDir  string // repo as seen by the factory process
	HostDir  string // repo as seen by the host engine
	HostHome string // host home folder ("" = unknown)
}

// DetectPaths builds Paths for a factory whose repo is baseDir/hostDir. The
// host home comes from HOST_HOME (set by the start scripts); a native factory
// (hostDir == baseDir) falls back to the process's own home folder.
func DetectPaths(baseDir, hostDir string) Paths {
	p := Paths{BaseDir: baseDir, HostDir: hostDir, HostHome: strings.TrimSpace(os.Getenv("HOST_HOME"))}
	if p.HostHome == "" && filepath.Clean(baseDir) == filepath.Clean(hostDir) {
		if h, err := os.UserHomeDir(); err == nil {
			p.HostHome = h
		}
	}
	return p
}

// SkillsDir is the repo's skills/ folder as the factory reads it.
func (p Paths) SkillsDir() string { return filepath.Join(p.BaseDir, "skills") }

// HostSkillsDir is skills/ as the host engine mounts it.
func (p Paths) HostSkillsDir() string { return hostJoin(p.HostDir, "skills") }

// DefaultWorkspace is <repo>/workspace on the host.
func (p Paths) DefaultWorkspace() string { return hostJoin(p.HostDir, "workspace") }

// Workspace is the configured workspace host folder (or the default).
func (p Paths) Workspace(t TerminalConfig) string {
	if w := strings.TrimSpace(t.Workspace); w != "" {
		return NormalizeHostPath(w)
	}
	return p.DefaultWorkspace()
}

// ClaudeSkillsDir is ~/.claude/skills on the host, or "" when the host home is unknown.
func (p Paths) ClaudeSkillsDir() string {
	if p.HostHome == "" {
		return ""
	}
	return hostJoin(p.HostHome, ".claude", "skills")
}

// LocalPath maps a host path inside the repo to the factory's own view of it,
// so the factory can create the folder before mounting it. ok is false for
// paths outside the repo (the factory can't see those).
func (p Paths) LocalPath(hostPath string) (string, bool) {
	rel, ok := relTo(NormalizeHostPath(hostPath), NormalizeHostPath(p.HostDir))
	if !ok {
		return "", false
	}
	return filepath.Join(p.BaseDir, filepath.FromSlash(rel)), true
}

// TerminalSpec is everything that determines the terminal container.
type TerminalSpec struct {
	Engine         string // docker | podman
	Variant        string
	BindIP         string // host interface the port is published on
	Port           int
	APIKey         string
	MemoryMB       int
	Workspace      string // host folder -> ~/workspace (read-write)
	Skills         string // host repo skills/ -> ~/.agents/skills (read-only)
	ClaudeSkills   string // host ~/.claude/skills -> ~/.claude/skills (read-only); "" = off
	DisableBrowser bool   // slim: turn off browser/desktop endpoints
}

// BuildTerminalSpec validates the config and resolves it into a spec.
func BuildTerminalSpec(cfg TerminalConfig, p Paths, engine string) (TerminalSpec, error) {
	if _, ok := VariantByID(cfg.Variant); !ok {
		return TerminalSpec{}, fmt.Errorf("unknown terminal image variant %q", cfg.Variant)
	}
	if err := ValidatePort(cfg.Port); err != nil {
		return TerminalSpec{}, err
	}
	if cfg.APIKey == "" {
		return TerminalSpec{}, errors.New("terminal API key is missing")
	}
	if err := ValidateWorkspace(cfg.Workspace, p.HostDir, p.HostHome); err != nil {
		return TerminalSpec{}, err
	}
	spec := TerminalSpec{
		Engine:         engine,
		Variant:        cfg.Variant,
		BindIP:         "127.0.0.1",
		Port:           cfg.Port,
		APIKey:         cfg.APIKey,
		MemoryMB:       cfg.Memory(),
		Workspace:      p.Workspace(cfg),
		Skills:         p.HostSkillsDir(),
		DisableBrowser: cfg.Variant == "slim",
	}
	if cfg.ShareClaudeSkills {
		spec.ClaudeSkills = p.ClaudeSkillsDir()
		if spec.ClaudeSkills == "" {
			return TerminalSpec{}, errors.New("can't share ~/.claude/skills: the factory doesn't know your home folder (restart it with the updated start script, which passes HOST_HOME)")
		}
	}
	for _, m := range []string{spec.Workspace, spec.Skills, spec.ClaudeSkills} {
		if err := checkMountSource(m); err != nil {
			return TerminalSpec{}, err
		}
	}
	return spec, nil
}

// checkMountSource rejects strings that would break a `-v src:dst[:ro]` spec.
func checkMountSource(src string) error {
	if src == "" {
		return nil
	}
	if strings.ContainsAny(src, "\x00\r\n") {
		return fmt.Errorf("mount path %q contains control characters", src)
	}
	_, rest := splitDrive(NormalizeHostPath(src))
	if strings.Contains(rest, ":") {
		return fmt.Errorf("mount path %q can't contain ':'", src)
	}
	return nil
}

// Image is the spec's image reference.
func (s TerminalSpec) Image() string { return TerminalImage(s.Variant) }

// Env is the container environment (the API key included).
func (s TerminalSpec) Env() []string {
	env := []string{
		"OPEN_TERMINAL_API_KEY=" + s.APIKey,
		// Every shell and command starts in the shared workspace, and the file
		// browser treats it as the root (a UI hint; it doesn't restrict tools).
		"OPEN_TERMINAL_START_DIR=~/workspace",
		"OPEN_TERMINAL_FILE_BROWSER_ROOT={{home}}/workspace",
	}
	if s.DisableBrowser {
		// Slim ships no Chromium/desktop: advertise them as off instead of
		// letting Open WebUI offer tools that can only fail.
		env = append(env, "OPEN_TERMINAL_ENABLE_BROWSER=false", "OPEN_TERMINAL_ENABLE_DESKTOP=false")
	}
	return env
}

// Hash fingerprints the spec (stored as a label so drift is detectable).
func (s TerminalSpec) Hash() string {
	parts := []string{s.Engine, s.Image(), s.BindIP, strconv.Itoa(s.Port), strconv.Itoa(s.MemoryMB),
		s.Workspace, s.Skills, s.ClaudeSkills}
	parts = append(parts, s.Env()...)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// RunArgs is the full `<engine> run` argument list. Every value is its own
// argv element (exec, no shell), so nothing user-supplied is ever interpolated.
func (s TerminalSpec) RunArgs() []string {
	args := []string{
		"run", "-d", "--name", TerminalContainer,
		"--restart", "unless-stopped",
		"--label", "local-llm.tool=harness",
		"--label", SpecLabel + "=" + s.Hash(),
		// Loopback only: Open WebUI reaches it through host.docker.internal
		// (Docker Desktop routes that to the host's loopback); the LAN can't.
		"-p", fmt.Sprintf("%s:%d:8000", s.BindIP, s.Port),
		"--memory", fmt.Sprintf("%dm", s.MemoryMB),
		"--pids-limit", "1024",
	}
	for _, e := range s.Env() {
		args = append(args, "-e", e)
	}
	args = append(args, "-v", s.Workspace+":"+containerWorkspace)
	args = append(args, "-v", s.Skills+":"+containerSkills+":ro")
	if s.ClaudeSkills != "" {
		args = append(args, "-v", s.ClaudeSkills+":"+containerClaude+":ro")
	}
	if s.Engine == "podman" {
		// The Podman machine enforces SELinux; without this the bind mounts are
		// unreadable inside the container (same reason model containers set it).
		args = append(args, "--security-opt", "label=disable")
	}
	return append(args, s.Image())
}
