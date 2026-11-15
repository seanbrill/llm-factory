package main

import (
	"encoding/base64"
	"fmt"
	"strings"
	"sync"

	"github.com/seanbrill/local-llm/internal/ensemble"
	"github.com/seanbrill/local-llm/internal/voice"
)

// personaState holds the persona and voice the factory pushed (POST
// /ensemble/config). It lives in memory only: a cloned voice's clip is never
// written into the image or onto disk here, and the factory pushes it again
// after a restart. SYSTEM_PROMPT (the Library's run-time persona) is the
// fallback prompt until the factory has pushed one.
type personaState struct {
	mu        sync.RWMutex
	cfg       ensemble.Config
	envPrompt string
}

// validateConfig checks a pushed config: a built-in voice has a name, a clone
// a usable clip.
func validateConfig(c *ensemble.Config) error {
	if c.Persona != nil {
		c.Persona.Prompt = strings.TrimSpace(c.Persona.Prompt)
		c.Persona.Name = strings.TrimSpace(c.Persona.Name)
	}
	if v := c.Voice; v != nil {
		switch v.Kind {
		case voice.KindBuiltin:
			if strings.TrimSpace(v.Name) == "" {
				return fmt.Errorf("a built-in voice needs a name")
			}
			v.RefAudio, v.RefText = "", ""
		case voice.KindClone:
			wav, err := base64.StdEncoding.DecodeString(v.RefAudio)
			if err != nil || len(wav) == 0 {
				return fmt.Errorf("the cloned voice's clip isn't base64 WAV")
			}
			if _, err := voice.CheckClip(wav); err != nil {
				return err
			}
		case "":
			c.Voice = nil
		default:
			return fmt.Errorf("unknown voice kind %q", v.Kind)
		}
	}
	c.Hash = c.ComputeHash()
	return nil
}

func (p *personaState) set(c ensemble.Config) {
	p.mu.Lock()
	p.cfg = c
	p.mu.Unlock()
}

func (p *personaState) get() ensemble.Config {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cfg
}

// prompt is the persona's system prompt ("" for none).
func (p *personaState) prompt() string {
	c := p.get()
	if c.Persona != nil && c.Persona.Prompt != "" {
		return c.Persona.Prompt
	}
	return p.envPrompt
}

func (p *personaState) status() *ensemble.PersonaStatus {
	c := p.get()
	ps := &ensemble.PersonaStatus{Hash: c.Hash}
	if c.Persona != nil {
		ps.Name = c.Persona.Name
	} else if p.envPrompt != "" {
		ps.Name = "(run-time prompt)"
	}
	if c.Voice != nil {
		ps.Voice = voice.Voice{Kind: c.Voice.Kind, Name: c.Voice.Name}.Label()
	}
	return ps
}

// stockVoices are OpenAI's voice names. Clients such as Open WebUI send one by
// default; for an ensemble they mean "your voice", i.e. the persona's.
var stockVoices = map[string]bool{
	"": true, "default": true, "persona": true, "alloy": true, "ash": true, "ballad": true, "coral": true,
	"echo": true, "fable": true, "onyx": true, "nova": true, "sage": true, "shimmer": true, "verse": true,
}

// resolve fills a speech request with the persona's voice unless the client
// named a voice of its own or sent its own reference clip.
func (p *personaState) resolve(r voice.SpeechRequest) voice.SpeechRequest {
	if r.RefAudio != "" || !stockVoices[strings.ToLower(strings.TrimSpace(r.Voice))] {
		return r
	}
	r.Voice = ""
	c := p.get()
	if c.Voice == nil {
		return r
	}
	switch c.Voice.Kind {
	case voice.KindBuiltin:
		r.Voice = c.Voice.Name
	case voice.KindClone:
		r.RefAudio, r.RefText = c.Voice.RefAudio, c.Voice.RefText
	}
	return r
}

// prepareMessages is the conversation a chat or vision member gets: the
// persona's prompt first (merged with the client's own system message), earlier
// replies without their multi-MB media, and, for a member that can't see,
// images replaced by a note.
func prepareMessages(msgs []map[string]any, persona string, vision bool) []map[string]any {
	out := make([]map[string]any, 0, len(msgs)+1)
	for _, m := range msgs {
		c := make(map[string]any, len(m))
		for k, v := range m {
			c[k] = v
		}
		switch c["role"] {
		case "assistant":
			if s, ok := c["content"].(string); ok {
				c["content"] = historyText(s)
			} else if parts, ok := c["content"].([]any); ok {
				c["content"] = historyText(textOf(parts))
			}
		case "user":
			if !vision && hasImage(c["content"]) {
				c["content"] = strings.TrimSpace(textOf(c["content"]) + " [an image was attached, but this model can't see images]")
			}
		}
		out = append(out, c)
	}
	persona = strings.TrimSpace(persona)
	if persona == "" {
		return out
	}
	if len(out) > 0 && out[0]["role"] == "system" {
		if own := strings.TrimSpace(textOf(out[0]["content"])); own != "" {
			persona += "\n\n" + own
		}
		out[0]["content"] = persona
		return out
	}
	return append([]map[string]any{{"role": "system", "content": persona}}, out...)
}
