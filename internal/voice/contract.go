// Package voice is the voice contract between an Ensemble and its speech
// members, and the store for the reference clips cloned voices are made from.
//
// The contract (docs/ENSEMBLE.md, "Voices"):
//
//   - A persona voice is the speech member's default, one of its built-in
//     voices, or a clone: a stored reference clip (mono WAV, 3–30 s) plus its
//     transcript.
//   - A speech member is asked through POST /v1/audio/speech with
//     SpeechRequest: OpenAI's {model, input, voice, response_format, speed},
//     plus ref_audio (base64 WAV) and ref_text for a clone.
//   - Each speech runtime maps that request to its own shape through an
//     Adapter (adapters.go), the one place that knows runtime request formats.
//
// Stdlib only: the ensemble Conductor (cmd/ensemblegate) compiles this package
// inside its image build (builder.stageEnsemblegate copies it in).
package voice

import (
	"fmt"
	"strings"
)

// Voice kinds.
const (
	KindDefault = ""        // the speech member's own default voice
	KindBuiltin = "builtin" // one of the speech member's built-in voices, by name
	KindClone   = "clone"   // a reference clip (and its transcript) to imitate
)

// Voice is what a persona speaks with. It names a clip by id; the audio stays in
// the clip store on this machine.
type Voice struct {
	Kind string `json:"kind,omitempty"` // KindDefault | KindBuiltin | KindClone
	Name string `json:"name,omitempty"` // KindBuiltin: the member's voice id, e.g. "alba"
	Clip string `json:"clip,omitempty"` // KindClone: a clip id in the store
}

// Normalize trims the fields and drops the ones the kind doesn't use.
func (v Voice) Normalize() Voice {
	v.Kind = strings.ToLower(strings.TrimSpace(v.Kind))
	v.Name = strings.TrimSpace(v.Name)
	v.Clip = strings.TrimSpace(v.Clip)
	switch v.Kind {
	case KindBuiltin:
		v.Clip = ""
	case KindClone:
		v.Name = ""
	default:
		v.Name, v.Clip = "", ""
	}
	return v
}

// Validate reports a voice that can't be used, as a sentence for the UI.
func (v Voice) Validate() error {
	switch v.Kind {
	case KindDefault:
		return nil
	case KindBuiltin:
		if v.Name == "" {
			return fmt.Errorf("name the built-in voice, or choose the speech member's default voice")
		}
		return nil
	case KindClone:
		if !ValidClipID(v.Clip) {
			return fmt.Errorf("a cloned voice needs a saved clip: record or upload one first")
		}
		return nil
	}
	return fmt.Errorf("unknown voice kind %q: use builtin, clone, or leave it empty for the default voice", v.Kind)
}

// Label is a short description for status lines: "", "builtin:alba" or "clone".
func (v Voice) Label() string {
	switch v.Kind {
	case KindBuiltin:
		return "builtin:" + v.Name
	case KindClone:
		return "clone"
	}
	return ""
}

// SpeechRequest is the body of POST /v1/audio/speech under the voice contract:
// what a client sends an ensemble, and (after the ensemble resolves the
// persona's voice) what the ensemble asks a speech member for, before an
// Adapter maps it to the member runtime's own shape.
type SpeechRequest struct {
	Model          string  `json:"model,omitempty"`
	Input          string  `json:"input"`
	Voice          string  `json:"voice,omitempty"`           // a built-in voice; "" = the persona's voice (or the member default)
	ResponseFormat string  `json:"response_format,omitempty"` // "wav" (members answer WAV)
	Speed          float64 `json:"speed,omitempty"`           // 0 = the member default
	// A clone: the reference clip as base64 WAV (mono, 3–30 s) and what it says.
	RefAudio string `json:"ref_audio,omitempty"`
	RefText  string `json:"ref_text,omitempty"`
	// Optional extras some runtimes take: a style instruction (Qwen3-TTS), and
	// a streaming format (audio.cpp "sse" | "audio"), kept for real-time voice.
	Instructions string `json:"instructions,omitempty"`
	StreamFormat string `json:"stream_format,omitempty"`
}

// IsClone reports whether the request asks for a cloned voice.
func (r SpeechRequest) IsClone() bool { return r.RefAudio != "" }

// MaxInputChars caps how much text one speech request carries: long replies
// take a long time to speak, so callers trim to a sentence boundary first.
const MaxInputChars = 4000

// CloneCapable reports whether a speech runtime can speak a cloned voice.
//
// audio.cpp clones (Pocket TTS from the clip alone, Qwen3-TTS Base with the
// transcript); Piper and Kokoro speak built-in voices only. This is the one
// rule the factory and the Conductor share; when the catalog's per-model
// voice_clone flag lands, the factory should set ensemble.Member.VoiceClone
// from that flag instead of calling this.
func CloneCapable(runtime string) bool { return runtime == "audiocpp" }
