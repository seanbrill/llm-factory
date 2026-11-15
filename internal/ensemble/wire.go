package ensemble

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Wire types between the factory and a running Conductor.

// Container labels.
const (
	// LabelMember marks a member container with its ensemble's id.
	LabelMember = "local-llm.ensemble-member"
	// LabelTool marks a member container with the tool it backs.
	LabelTool = "local-llm.ensemble-tool"
	// LabelEnsemble is on an Ensemble image (and the containers run from it).
	LabelEnsemble = "local-llm.ensemble"
	// LabelKind is "ensemble" on an Ensemble image.
	LabelKind = "local-llm.kind"
)

// MemberContainer is the container name for an ensemble's member.
func MemberContainer(ensembleID, tool string) string { return "ens-" + ensembleID + "-" + tool }

// Member states, as the Conductor reports them.
const (
	StateStopped  = "stopped"  // not running; the next request that needs it starts it
	StateStarting = "starting" // started, waiting for it to answer its health check
	StateReady    = "ready"    // answering
	StateFailed   = "failed"   // the last start failed (Error says why); the next request retries
	StateStopping = "stopping" // being removed (evicted, or stopped on request)
)

// Config is what the factory pushes to a running Conductor (POST
// /ensemble/config): the persona and its resolved voice. It is never baked into
// the image, so a cloned voice's clip stays on this machine and a persona edit
// applies without a rebuild.
type Config struct {
	Hash    string         `json:"hash"`
	Persona *ConfigPersona `json:"persona,omitempty"`
	Voice   *ConfigVoice   `json:"voice,omitempty"`
}

// ConfigPersona is the persona applied to chat.
type ConfigPersona struct {
	ID     string `json:"id,omitempty"`
	Name   string `json:"name"`
	Prompt string `json:"prompt"`
}

// ConfigVoice is the persona's voice, resolved: a built-in voice's name, or a
// clone's clip as base64 WAV plus its transcript.
type ConfigVoice struct {
	Kind     string `json:"kind"` // voice.KindBuiltin | voice.KindClone
	Name     string `json:"name,omitempty"`
	Clip     string `json:"clip,omitempty"`      // clip id, for status lines
	RefAudio string `json:"ref_audio,omitempty"` // base64 WAV
	RefText  string `json:"ref_text,omitempty"`
}

// ComputeHash returns the hash identifying c's content (Hash itself excluded).
func (c Config) ComputeHash() string {
	c.Hash = ""
	b, _ := json.Marshal(c)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:12])
}

// MemberStatus is one member's state in GET /ensemble/status.
type MemberStatus struct {
	Tool       string  `json:"tool"`
	Modality   string  `json:"modality"`
	Image      string  `json:"image"`
	Model      string  `json:"model,omitempty"`
	State      string  `json:"state"`
	Error      string  `json:"error,omitempty"`
	Since      int64   `json:"since,omitempty"` // unix seconds the state began
	Port       int     `json:"port,omitempty"`  // host port while running
	MemGB      float64 `json:"mem_gb"`
	Pinned     bool    `json:"pinned,omitempty"`
	Busy       int     `json:"busy,omitempty"` // requests in flight
	VoiceClone bool    `json:"voice_clone,omitempty"`
	LastUsed   int64   `json:"last_used,omitempty"`
}

// PersonaStatus says which persona and voice the Conductor applies.
type PersonaStatus struct {
	Name   string `json:"name"`
	Voice  string `json:"voice"` // "" | "builtin:<name>" | "clone"
	Hash   string `json:"hash"`
	Synced bool   `json:"synced"` // set by the factory: the Conductor has its current persona
}

// Status is GET /ensemble/status.
type Status struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Router   string         `json:"router"`
	BudgetGB float64        `json:"budget_gb"`
	UsedGB   float64        `json:"used_gb"`
	Persona  *PersonaStatus `json:"persona,omitempty"`
	Members  []MemberStatus `json:"members"`
}

// Media is a generated image, video or audio clip in a reply: a data URL from
// the Conductor (the factory rewrites it to an /api/media URL for its UI).
type Media struct {
	Type string `json:"type"` // image | video | audio
	URL  string `json:"url"`
	MIME string `json:"mime,omitempty"`
}
