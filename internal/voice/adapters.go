package voice

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Speaker is a speech member as the adapters see it.
type Speaker struct {
	Runtime string // catalog runtime: "cpp" (Piper), "python" (Kokoro), "audiocpp", or "contract"
	Model   string // catalog model id, for messages
	Clone   bool   // the member can speak a cloned voice (catalog voice_clone)
	// NeedsTranscript: a clone must come with the clip's transcript (catalog
	// clone_needs_transcript: the Qwen3-TTS Base models).
	NeedsTranscript bool
	// CloneOnly: the model has no built-in voices, so every request needs a
	// reference clip (Qwen3-TTS Base, XTTS v2; see CloneOnly).
	CloneOnly bool
}

// Call is one HTTP request to a speech member: POST Path with Body.
type Call struct {
	Path        string
	Body        []byte
	ContentType string
}

// Adapter maps a contract SpeechRequest to one runtime's request. Adding a
// speech runtime means adding an adapter here and a case in For.
type Adapter interface {
	Name() string
	Clones() bool
	Call(r SpeechRequest) (Call, error)
}

// CloneError says a speech member can't speak the voice asked for: it can't
// clone (Reason ""), the clone lacks the transcript the model needs
// ("transcript"), or the model has no built-in voice to fall back on ("clip").
type CloneError struct {
	Model   string
	Runtime string
	Reason  string
}

func (e *CloneError) Error() string {
	who := e.Model
	if who == "" {
		who = "this speech model"
	}
	switch e.Reason {
	case "transcript":
		return fmt.Sprintf("the speech member (%s) needs the voice clip's transcript to clone it: "+
			"type what the clip says in the persona's voice settings (or let the ensemble's speech-to-text fill it in)", who)
	case "clip":
		return fmt.Sprintf("the speech member (%s) has no built-in voices: it only speaks a cloned voice. "+
			"Give the persona a voice clip (Personas, Voice, Clone)", who)
	}
	return fmt.Sprintf("the speech member (%s) can't clone voices: it only speaks its built-in voices. "+
		"Give the persona a built-in voice, or make a voice-cloning model (e.g. Pocket TTS) the ensemble's speech member", who)
}

// For picks the adapter for a speech member.
func For(s Speaker) Adapter {
	switch s.Runtime {
	case "audiocpp":
		return audioCpp{s}
	case "python":
		return pygate{s}
	case "", "cpp":
		return piper{s}
	default:
		return contract{s}
	}
}

func jsonCall(body map[string]any) (Call, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return Call{}, err
	}
	return Call{Path: "/v1/audio/speech", Body: b, ContentType: "application/json"}, nil
}

func checkInput(r SpeechRequest) error {
	if strings.TrimSpace(r.Input) == "" {
		return fmt.Errorf("nothing to say: the input is empty")
	}
	if len(r.Input) > MaxInputChars {
		return fmt.Errorf("the input is too long to speak (%d characters, at most %d)", len(r.Input), MaxInputChars)
	}
	return nil
}

// refuseClone checks a request against what the speaker can do.
func (s Speaker) refuseClone(r SpeechRequest) error {
	switch {
	case r.IsClone() && !s.Clone:
		return &CloneError{Model: s.Model, Runtime: s.Runtime}
	case r.IsClone() && s.NeedsTranscript && strings.TrimSpace(r.RefText) == "":
		return &CloneError{Model: s.Model, Runtime: s.Runtime, Reason: "transcript"}
	case !r.IsClone() && s.CloneOnly:
		return &CloneError{Model: s.Model, Runtime: s.Runtime, Reason: "clip"}
	}
	return nil
}

// piper: the factory's Piper image (scripts/docker/runtimes/Dockerfile.tts.cpu) takes {"input"}
// and speaks its one baked voice.
type piper struct{ s Speaker }

func (piper) Name() string { return "piper" }
func (piper) Clones() bool { return false }
func (a piper) Call(r SpeechRequest) (Call, error) {
	if err := checkInput(r); err != nil {
		return Call{}, err
	}
	if r.IsClone() {
		return Call{}, &CloneError{Model: a.s.Model, Runtime: "cpp"}
	}
	return jsonCall(map[string]any{"input": r.Input})
}

// pygate: the Python runtime (cmd/pygate, Kokoro) takes {input, voice, speed}.
// A clone-capable Python model would take the contract's ref_audio/ref_text.
type pygate struct{ s Speaker }

func (pygate) Name() string   { return "pygate" }
func (a pygate) Clones() bool { return a.s.Clone }
func (a pygate) Call(r SpeechRequest) (Call, error) {
	if err := checkInput(r); err != nil {
		return Call{}, err
	}
	if err := a.s.refuseClone(r); err != nil {
		return Call{}, err
	}
	body := map[string]any{"input": r.Input}
	if r.Voice != "" {
		body["voice"] = r.Voice
	}
	if r.Speed > 0 {
		body["speed"] = r.Speed
	}
	if r.IsClone() {
		body["ref_audio"] = r.RefAudio
		body["ref_text"] = r.RefText
	}
	return jsonCall(body)
}

// audioCpp: audio.cpp's server (scripts/docker/runtimes/Dockerfile.audiocpp.cpu) serves its one
// model as id "tts" and takes OpenAI's fields plus, for a clone, voice_ref
// {"type":"base64","data":<wav>} (at most 5 MiB decoded) and reference_text
// (required by the Qwen3-TTS Base models, optional for the others). voice and
// voice_ref never go together: Pocket TTS refuses a preset plus a clip, and the
// clip already replaces the default preset. It rejects a speed its model
// doesn't support, so speed goes only when it isn't 1. stream_format ("sse":
// speech.audio.delta events; "audio": raw PCM), with response_format "pcm",
// streams from a model built in streaming mode. See docs/VOICE-CLONING.md.
type audioCpp struct{ s Speaker }

func (audioCpp) Name() string   { return "audiocpp" }
func (a audioCpp) Clones() bool { return a.s.Clone }
func (a audioCpp) Call(r SpeechRequest) (Call, error) {
	if err := checkInput(r); err != nil {
		return Call{}, err
	}
	if err := a.s.refuseClone(r); err != nil {
		return Call{}, err
	}
	body := map[string]any{"model": "tts", "input": r.Input}
	if r.IsClone() {
		ref, err := FitRef(r.RefAudio, MaxRefBytes)
		if err != nil {
			return Call{}, err
		}
		body["voice_ref"] = map[string]string{"type": "base64", "data": ref}
		if strings.TrimSpace(r.RefText) != "" {
			body["reference_text"] = r.RefText
		}
	} else if r.Voice != "" {
		body["voice"] = r.Voice
	}
	if r.Speed > 0 && r.Speed != 1 {
		body["speed"] = r.Speed
	}
	if strings.TrimSpace(r.Instructions) != "" {
		body["instructions"] = r.Instructions
	}
	if r.StreamFormat != "" {
		body["stream_format"] = r.StreamFormat
		body["response_format"] = "pcm"
	}
	return jsonCall(body)
}

// contract: a runtime that speaks this contract as is.
type contract struct{ s Speaker }

func (contract) Name() string   { return "contract" }
func (a contract) Clones() bool { return a.s.Clone }
func (a contract) Call(r SpeechRequest) (Call, error) {
	if err := checkInput(r); err != nil {
		return Call{}, err
	}
	if err := a.s.refuseClone(r); err != nil {
		return Call{}, err
	}
	if r.ResponseFormat == "" {
		r.ResponseFormat = "wav"
	}
	b, err := json.Marshal(r)
	if err != nil {
		return Call{}, err
	}
	return Call{Path: "/v1/audio/speech", Body: b, ContentType: "application/json"}, nil
}
