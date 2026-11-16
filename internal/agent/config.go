package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Version is the version of the harness protocol: the harness.json format, the
// "harness" field on responses and the info document. It changes only when one
// of those changes incompatibly.
const Version = "1"

// ConfigVersion is the harness.json format version this package reads.
const ConfigVersion = 1

// Toolset names: the entries a harness.json "tools" list can hold. A list entry
// may also name a single tool function (for example "read_file").
const (
	ToolsetWebSearch = "web_search"
	ToolsetFetchURL  = "fetch_url"
	ToolsetFiles     = "files"
	ToolsetShell     = "shell"
	ToolsetSkills    = "skills"
	// ToolsetMCP holds the tools of MCP servers (see Config.MCP).
	ToolsetMCP = "mcp"
)

// Tool function names, as the model sees them. They match the names Open WebUI
// and Open Terminal use, so skills written for either work unchanged.
const (
	ToolSearchWeb          = "search_web"
	ToolFetchURL           = "fetch_url"
	ToolListFiles          = "list_files"
	ToolReadFile           = "read_file"
	ToolWriteFile          = "write_file"
	ToolReplaceFileContent = "replace_file_content"
	ToolGlobSearch         = "glob_search"
	ToolGrepSearch         = "grep_search"
	ToolRunCommand         = "run_command"
	ToolListSkills         = "list_skills"
	ToolViewSkill          = "view_skill"
)

// toolsetOrder is the order toolsets are offered and reported in.
var toolsetOrder = []string{ToolsetWebSearch, ToolsetFetchURL, ToolsetFiles, ToolsetSkills, ToolsetShell}

// toolsetTools maps each toolset to its tool functions.
var toolsetTools = map[string][]string{
	ToolsetWebSearch: {ToolSearchWeb},
	ToolsetFetchURL:  {ToolFetchURL},
	ToolsetFiles:     {ToolListFiles, ToolReadFile, ToolWriteFile, ToolReplaceFileContent, ToolGlobSearch, ToolGrepSearch},
	ToolsetSkills:    {ToolListSkills, ToolViewSkill},
	ToolsetShell:     {ToolRunCommand},
}

// DefaultTools is the "tools" list when harness.json leaves it out. The shell
// is never on by default.
var DefaultTools = []string{ToolsetWebSearch, ToolsetFetchURL, ToolsetFiles, ToolsetSkills}

// Toolsets lists every built-in toolset name.
func Toolsets() []string { return append([]string(nil), toolsetOrder...) }

// ToolsetOf returns the built-in toolset a tool function belongs to ("" if none).
func ToolsetOf(tool string) string {
	for set, names := range toolsetTools {
		for _, n := range names {
			if n == tool {
				return set
			}
		}
	}
	return ""
}

// Search backends.
const (
	SearchDuckDuckGo = "duckduckgo"
	SearchSearXNG    = "searxng"
	SearchBrave      = "brave"
)

// Defaults and bounds for the limits.
const (
	DefaultMaxSteps        = 8
	DefaultMaxCallsPerStep = 8
	DefaultToolTimeout     = 60 * time.Second
	DefaultMaxOutputBytes  = 8192
	DefaultMaxResults      = 5
	DefaultFetchMaxBytes   = 2 << 20

	maxMaxSteps        = 50
	maxMaxCallsPerStep = 32
	maxToolTimeout     = 30 * time.Minute
	minOutputBytes     = 512
	maxOutputBytes     = 1 << 20
	maxMaxResults      = 20
	maxFetchBytes      = 32 << 20
	maxInstructions    = 8 << 10
)

// Config is harness.json: which tools are on, their limits and where they work.
// The zero value of every field means "use the default"; WithDefaults fills them
// in. A config holds no secrets: API keys only come from the environment.
type Config struct {
	// Version is the format version (ConfigVersion); 0 is read as 1.
	Version int `json:"version,omitempty"`
	// Tools lists the toolsets (web_search, fetch_url, files, skills, shell) or
	// single tool functions to offer. Missing means DefaultTools; [] means none.
	Tools []string `json:"tools"`
	// Limits bound each request's tool loop.
	Limits Limits `json:"limits"`
	// Search configures search_web.
	Search SearchConfig `json:"search"`
	// Fetch configures fetch_url.
	Fetch FetchConfig `json:"fetch"`
	// Shell configures run_command (only used when the shell toolset is on).
	Shell ShellConfig `json:"shell"`
	// Workspace is the folder the files and shell tools are confined to. It is
	// required when either is on, and must be an absolute path.
	Workspace string `json:"workspace,omitempty"`
	// SkillsDir is the folder holding <name>/SKILL.md skill folders (absolute).
	SkillsDir string `json:"skills_dir,omitempty"`
	// Instructions is extra text added to the harness note in the system prompt.
	Instructions string `json:"instructions,omitempty"`
	// MCP lists MCP servers whose tools the model gets (the "mcp" toolset).
	// HARNESS_MCP (a JSON array of the same objects) is merged in by name, and
	// servers added at run time (the admin API, /mcp chat commands) join them.
	MCP []MCPServer `json:"mcp,omitempty"`

	// Run-time settings, only ever from the environment (see WithEnv).

	// StateDir is where the harness saves its run-time changes (mcp.json):
	// HARNESS_STATE_DIR, else <workspace>/.llm-harness when the workspace
	// folder exists. "" = changes are kept in memory only.
	StateDir string `json:"-"`
	// HostAlias is the name a container reaches its host by
	// (HARNESS_HOST_ALIAS, e.g. host.docker.internal). A loopback MCP URL is
	// dialed through it, keeping the URL's Host header.
	HostAlias string `json:"-"`
	// AdminToken turns on the admin API (/v1/harness/mcp) for bearers of it
	// (HARNESS_ADMIN_TOKEN). "" = the admin API is off.
	AdminToken string `json:"-"`
	// MCPChatOff turns the /mcp chat commands off (HARNESS_MCP_CHAT=off).
	MCPChatOff bool `json:"-"`
	// ChecksOff turns off the loop's notes about repeated failures and
	// unchecked changes (HARNESS_CHECKS=off; checks.go).
	ChecksOff bool `json:"-"`
}

// Limits bound the tool loop.
type Limits struct {
	// MaxSteps is the most model turns that may call tools in one request. After
	// that the model is asked to answer without tools.
	MaxSteps int `json:"max_steps,omitempty"`
	// MaxCallsPerStep caps the tool calls run from one model turn.
	MaxCallsPerStep int `json:"max_calls_per_step,omitempty"`
	// ToolTimeout bounds each tool call ("30s", "2m", or a number of seconds).
	ToolTimeout Duration `json:"tool_timeout,omitempty"`
	// MaxOutputBytes caps the text one tool call returns to the model.
	MaxOutputBytes int `json:"max_output_bytes,omitempty"`
}

// SearchConfig configures search_web.
type SearchConfig struct {
	// Backend is duckduckgo (default, no key), searxng or brave.
	Backend string `json:"backend,omitempty"`
	// SearXNGURL is the SearXNG base URL (JSON output must be enabled there).
	SearXNGURL string `json:"searxng_url,omitempty"`
	// MaxResults is how many results a search returns at most.
	MaxResults int `json:"max_results,omitempty"`
	// APIKey is the Brave Search API key. It is never read from or written to
	// harness.json: hosts set it from HARNESS_BRAVE_API_KEY (see WithEnv).
	APIKey string `json:"-"`
}

// FetchConfig configures fetch_url.
type FetchConfig struct {
	// MaxBytes caps how much of a page is downloaded.
	MaxBytes int `json:"max_bytes,omitempty"`
}

// ShellConfig configures run_command.
type ShellConfig struct {
	// Program is the shell and its flags; the command is appended as the last
	// argument. Default: ["/bin/sh", "-c"], or PowerShell on Windows.
	Program []string `json:"program,omitempty"`
	// Env names extra environment variables passed through to commands. By
	// default commands get only a minimal environment (PATH, HOME, LANG, ...).
	Env []string `json:"env,omitempty"`
	// AllowRoot lets commands run when the host process is root. Off by default:
	// the shell refuses to start as root.
	AllowRoot bool `json:"allow_root,omitempty"`
}

// Duration is a time.Duration that reads from JSON as "30s"-style text or a
// number of seconds, and writes as text.
type Duration time.Duration

// D returns the time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		v, err := parseDuration(s)
		if err != nil {
			return err
		}
		*d = Duration(v)
		return nil
	}
	var secs float64
	if err := json.Unmarshal(b, &secs); err != nil {
		return fmt.Errorf("duration must be text like \"30s\" or a number of seconds")
	}
	*d = Duration(secs * float64(time.Second))
	return nil
}

// parseDuration reads "30s"-style text or a bare number of seconds.
func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		return time.Duration(n * float64(time.Second)), nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("bad duration %q (use text like \"30s\" or a number of seconds)", s)
	}
	return v, nil
}

// ParseConfig reads harness.json. Unknown fields are an error, so a typo (or
// an attempt to put an API key in the file) is caught instead of ignored. The
// result is not validated; call WithDefaults and Validate.
func ParseConfig(data []byte) (Config, error) {
	var c Config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("harness config: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return Config{}, errors.New("harness config: unexpected data after the JSON object")
	}
	return c, nil
}

// Load reads the harness config a host runs with: the file at path, then the
// environment overrides (see WithEnv), then defaults, validated. ok is false
// when there is no harness: path is "", the file is missing or empty (the
// placeholder images without a harness carry), or HARNESS is off.
func Load(path string, getenv func(string) string) (cfg Config, ok bool, err error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	if off(getenv("HARNESS")) || path == "" {
		return Config{}, false, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, fmt.Errorf("harness config: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return Config{}, false, nil
	}
	cfg, err = ParseConfig(data)
	if err != nil {
		return Config{}, false, err
	}
	if cfg, err = cfg.WithEnv(getenv); err != nil {
		return Config{}, false, err
	}
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return Config{}, false, err
	}
	return cfg, true, nil
}

func off(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "off", "false", "no", "disabled":
		return true
	}
	return false
}

// WithEnv applies the environment overrides a container is run with
// (`docker run -e ...`):
//
//	HARNESS_TOOLS             tools list: "web_search,files" replaces it;
//	                          "+shell" or "-fetch_url" entries add or remove
//	HARNESS_SEARCH_BACKEND    duckduckgo | searxng | brave
//	HARNESS_SEARXNG_URL       SearXNG base URL (selects searxng unless a backend is set)
//	HARNESS_BRAVE_API_KEY     Brave Search key (selects brave unless a backend is set)
//	HARNESS_WORKSPACE         workspace folder
//	HARNESS_SKILLS_DIR        skills folder
//	HARNESS_MAX_STEPS         limits.max_steps
//	HARNESS_TOOL_TIMEOUT      limits.tool_timeout ("90s" or seconds)
//	HARNESS_MAX_OUTPUT_BYTES  limits.max_output_bytes
//	HARNESS_MCP               MCP servers (a JSON array), merged by name into "mcp"
//	HARNESS_MCP_CHAT          "off" turns the /mcp chat commands off
//	HARNESS_CHECKS            "off" turns the repeated-failure and check-your-work notes off
//	HARNESS_MAX_CALLS_PER_STEP limits.max_calls_per_step
//	HARNESS_STATE_DIR         where run-time changes are saved
//	HARNESS_HOST_ALIAS        the host's name inside the container
//	HARNESS_ADMIN_TOKEN       bearer token of the admin API (off without one)
func (c Config) WithEnv(getenv func(string) string) (Config, error) {
	get := func(k string) string { return strings.TrimSpace(getenv(k)) }
	if v := get("HARNESS_MCP"); v != "" {
		servers, err := ParseMCPServers(v)
		if err != nil {
			return c, fmt.Errorf("HARNESS_MCP: %v", err)
		}
		c.MCP = MergeMCP(c.MCP, servers)
	}
	c.MCPChatOff = off(get("HARNESS_MCP_CHAT"))
	c.ChecksOff = off(get("HARNESS_CHECKS"))
	if v := get("HARNESS_STATE_DIR"); v != "" {
		c.StateDir = v
	}
	if v := get("HARNESS_HOST_ALIAS"); v != "" {
		c.HostAlias = v
	}
	if v := get("HARNESS_ADMIN_TOKEN"); v != "" {
		c.AdminToken = v
	}
	if v := get("HARNESS_TOOLS"); v != "" {
		c.Tools = editTools(c.Tools, v)
	}
	backendSet := false
	if v := get("HARNESS_SEARCH_BACKEND"); v != "" {
		c.Search.Backend = strings.ToLower(v)
		backendSet = true
	}
	if v := get("HARNESS_SEARXNG_URL"); v != "" {
		c.Search.SearXNGURL = v
		if !backendSet {
			c.Search.Backend = SearchSearXNG
		}
	}
	if v := get("HARNESS_BRAVE_API_KEY"); v != "" {
		c.Search.APIKey = v
		if !backendSet && get("HARNESS_SEARXNG_URL") == "" {
			c.Search.Backend = SearchBrave
		}
	}
	if v := get("HARNESS_WORKSPACE"); v != "" {
		c.Workspace = v
	}
	if v := get("HARNESS_SKILLS_DIR"); v != "" {
		c.SkillsDir = v
	}
	for _, e := range []struct {
		key string
		dst *int
	}{
		{"HARNESS_MAX_STEPS", &c.Limits.MaxSteps},
		{"HARNESS_MAX_CALLS_PER_STEP", &c.Limits.MaxCallsPerStep},
		{"HARNESS_MAX_OUTPUT_BYTES", &c.Limits.MaxOutputBytes},
	} {
		if v := get(e.key); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return c, fmt.Errorf("%s: %q is not a whole number", e.key, v)
			}
			*e.dst = n
		}
	}
	if v := get("HARNESS_TOOL_TIMEOUT"); v != "" {
		d, err := parseDuration(v)
		if err != nil {
			return c, fmt.Errorf("HARNESS_TOOL_TIMEOUT: %v", err)
		}
		c.Limits.ToolTimeout = Duration(d)
	}
	return c, nil
}

// editTools applies a HARNESS_TOOLS value: a plain list replaces the tools,
// "+name" adds and "-name" removes (starting from the configured list).
func editTools(cur []string, spec string) []string {
	var plain []string
	var edits []string
	for _, f := range strings.Split(spec, ",") {
		f = strings.TrimSpace(f)
		switch {
		case f == "":
		case strings.HasPrefix(f, "+") || strings.HasPrefix(f, "-"):
			edits = append(edits, f)
		case strings.EqualFold(f, "none"):
			plain = []string{}
		default:
			plain = append(plain, f)
		}
	}
	out := cur
	if out == nil {
		out = DefaultTools
	}
	if plain != nil {
		out = plain
	}
	out = append([]string{}, out...)
	for _, e := range edits {
		name := strings.TrimSpace(e[1:])
		if e[0] == '+' {
			out = append(out, name)
			continue
		}
		kept := out[:0]
		for _, t := range out {
			if t != name {
				kept = append(kept, t)
			}
		}
		out = kept
	}
	return out
}

// WithDefaults returns c with every unset field at its default.
func (c Config) WithDefaults() Config {
	if c.Version == 0 {
		c.Version = ConfigVersion
	}
	if c.Tools == nil {
		c.Tools = append([]string(nil), DefaultTools...)
	}
	if c.Limits.MaxSteps == 0 {
		c.Limits.MaxSteps = DefaultMaxSteps
	}
	if c.Limits.MaxCallsPerStep == 0 {
		c.Limits.MaxCallsPerStep = DefaultMaxCallsPerStep
	}
	if c.Limits.ToolTimeout == 0 {
		c.Limits.ToolTimeout = Duration(DefaultToolTimeout)
	}
	if c.Limits.MaxOutputBytes == 0 {
		c.Limits.MaxOutputBytes = DefaultMaxOutputBytes
	}
	if c.Search.Backend == "" {
		c.Search.Backend = SearchDuckDuckGo
	}
	if c.Search.MaxResults == 0 {
		c.Search.MaxResults = DefaultMaxResults
	}
	if c.Fetch.MaxBytes == 0 {
		c.Fetch.MaxBytes = DefaultFetchMaxBytes
	}
	return c
}

// Validate checks a config (after WithDefaults) and reports every problem.
func (c Config) Validate() error {
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

	if c.Version != ConfigVersion {
		add("version %d is not supported (this harness reads version %d)", c.Version, ConfigVersion)
	}
	var unknown []string
	for _, t := range c.Tools {
		if t == ToolsetMCP {
			continue // the MCP servers turn it on; naming it is harmless
		}
		if _, ok := toolsetTools[t]; !ok && ToolsetOf(t) == "" {
			unknown = append(unknown, strconv.Quote(t))
		}
	}
	if len(unknown) > 0 {
		add("unknown tools %s (toolsets: %s; or a single tool such as read_file)", strings.Join(unknown, ", "), strings.Join(toolsetOrder, ", "))
	}
	l := c.Limits
	if l.MaxSteps < 1 || l.MaxSteps > maxMaxSteps {
		add("limits.max_steps must be 1 to %d", maxMaxSteps)
	}
	if l.MaxCallsPerStep < 1 || l.MaxCallsPerStep > maxMaxCallsPerStep {
		add("limits.max_calls_per_step must be 1 to %d", maxMaxCallsPerStep)
	}
	if l.ToolTimeout.D() < time.Second || l.ToolTimeout.D() > maxToolTimeout {
		add("limits.tool_timeout must be 1s to %s", maxToolTimeout)
	}
	if l.MaxOutputBytes < minOutputBytes || l.MaxOutputBytes > maxOutputBytes {
		add("limits.max_output_bytes must be %d to %d", minOutputBytes, maxOutputBytes)
	}

	enabled := c.EnabledTools()
	has := func(set string) bool {
		for _, n := range toolsetTools[set] {
			if enabled[n] {
				return true
			}
		}
		return false
	}
	switch c.Search.Backend {
	case SearchDuckDuckGo, SearchBrave:
	case SearchSearXNG:
		if c.Search.SearXNGURL == "" {
			add("search.searxng_url is required for the searxng backend")
		}
	default:
		add("search.backend %q is not one of duckduckgo, searxng, brave", c.Search.Backend)
	}
	if c.Search.SearXNGURL != "" {
		if u, err := url.Parse(c.Search.SearXNGURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			add("search.searxng_url must be an http(s) URL")
		}
	}
	if c.Search.MaxResults < 1 || c.Search.MaxResults > maxMaxResults {
		add("search.max_results must be 1 to %d", maxMaxResults)
	}
	if c.Fetch.MaxBytes < 1024 || c.Fetch.MaxBytes > maxFetchBytes {
		add("fetch.max_bytes must be 1024 to %d", maxFetchBytes)
	}
	if (has(ToolsetFiles) || has(ToolsetShell)) && c.Workspace == "" {
		add("workspace is required for the files and shell tools")
	}
	if c.Workspace != "" && !isAbs(c.Workspace) {
		add("workspace must be an absolute path")
	}
	if has(ToolsetSkills) && c.SkillsDir == "" {
		add("skills_dir is required for the skills tools")
	}
	if c.SkillsDir != "" && !isAbs(c.SkillsDir) {
		add("skills_dir must be an absolute path")
	}
	if len(c.Shell.Program) > 0 && strings.TrimSpace(c.Shell.Program[0]) == "" {
		add("shell.program must start with a program")
	}
	for _, e := range c.Shell.Env {
		if e == "" || strings.ContainsAny(e, "=\x00") {
			add("shell.env entries are variable names, not %q", e)
		}
	}
	if len(c.Instructions) > maxInstructions {
		add("instructions is longer than %d bytes", maxInstructions)
	}
	if err := ValidateMCP(c.MCP); err != nil {
		add("%v", err)
	}
	if c.HostAlias != "" && !validHostPort(c.HostAlias) {
		add("HARNESS_HOST_ALIAS %q is not a host name", c.HostAlias)
	}
	if len(errs) > 0 {
		return errors.New("harness config: " + strings.Join(errs, "; "))
	}
	return nil
}

// isAbs accepts host-absolute paths and slash-rooted ones, so a config written
// for a Linux container validates on any host.
func isAbs(p string) bool { return filepath.IsAbs(p) || strings.HasPrefix(p, "/") }

// EnabledTools returns the tool functions the "tools" list turns on.
func (c Config) EnabledTools() map[string]bool {
	on := map[string]bool{}
	for _, t := range c.Tools {
		if names, ok := toolsetTools[t]; ok {
			for _, n := range names {
				on[n] = true
			}
		} else if ToolsetOf(t) != "" {
			on[t] = true
		}
	}
	return on
}

// EnabledToolsets returns, in order, the toolsets with at least one tool on.
func (c Config) EnabledToolsets() []string {
	on := c.EnabledTools()
	var out []string
	for _, set := range toolsetOrder {
		for _, n := range toolsetTools[set] {
			if on[n] {
				out = append(out, set)
				break
			}
		}
	}
	return out
}

// expandTools turns a list of toolset or tool names into tool names; unknown
// names are kept as they are (a host's own tools).
func expandTools(list []string) map[string]bool {
	out := map[string]bool{}
	for _, t := range list {
		if names, ok := toolsetTools[t]; ok {
			for _, n := range names {
				out[n] = true
			}
			continue
		}
		out[t] = true
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
