package main

import (
	"context"
	"regexp"
	"strings"

	"github.com/seanbrill/local-llm/internal/ensemble"
)

// Routing is one replaceable piece: a Router looks at a chat request and
// decides which member answers it. Today there are two: the heuristic (input
// signals and phrasing) and the conductor (a small model picks, the heuristic
// stands when it can't). The next one is planned on the shared tool-calling
// runtime (internal/agent): the chat member becomes the brain and the other
// members are its tools (the Members methods in members.go: GenerateImage,
// GenerateVideo, Speak, Transcribe, Chat). It plugs in where Gate.answer and
// Gate.streamAnswer call the Router: run the agent loop with Members as its
// tool set instead of executing one Decision. Members, lifecycle, persona and
// the HTTP surface stay as they are.

// Decision is how one chat request is answered.
type Decision struct {
	Tool string // the member tool that answers (ensemble.Tool*)
	// Prompt is what a generative tool makes: the image or video to draw, or
	// the exact words to say. Empty for chat. For speak, empty means "answer
	// with chat, then speak the answer".
	Prompt string
	Why    string // for the log and the X-Ensemble-Route header
}

// Router decides how a chat request is answered.
type Router interface {
	Name() string
	Route(ctx context.Context, req *ChatRequest) Decision
}

// ChatRequest is a parsed /v1/chat/completions body.
type ChatRequest struct {
	Body     map[string]any   // everything the client sent
	Messages []map[string]any // Body["messages"]
	Stream   bool
}

// lastUser returns the last user message's text and whether it carries an image.
func (r *ChatRequest) lastUser() (text string, image bool) {
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i]["role"] == "user" {
			return textOf(r.Messages[i]["content"]), hasImage(r.Messages[i]["content"])
		}
	}
	return "", false
}

// historyHasImage reports an image anywhere in the conversation.
func (r *ChatRequest) historyHasImage() bool {
	for _, m := range r.Messages {
		if hasImage(m["content"]) {
			return true
		}
	}
	return false
}

func hasImage(content any) bool {
	parts, ok := content.([]any)
	if !ok {
		return false
	}
	for _, p := range parts {
		if pm, ok := p.(map[string]any); ok && pm["type"] == "image_url" {
			return true
		}
	}
	return false
}

func textOf(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var b strings.Builder
		for _, p := range c {
			if pm, ok := p.(map[string]any); ok && pm["type"] == "text" {
				if s, ok := pm["text"].(string); ok {
					if b.Len() > 0 {
						b.WriteByte(' ')
					}
					b.WriteString(s)
				}
			}
		}
		return b.String()
	}
	return ""
}

// ---- the heuristic --------------------------------------------------------

// The phrasing rules look for a request at the start of the message ("draw a
// fox", "can you make me a short video of…"), so a question that merely
// mentions images or saying ("how do I make an image smaller in CSS?", "what
// do they say about…") stays with chat.
const lead = `^\s*(?:(?:hey|hi|ok|okay|so|now|and)[,!]?\s+)?(?:please\s+)?` +
	`(?:(?:can|could|would|will)\s+you\s+(?:please\s+)?|i\s+(?:want|need|would\s+like|'d\s+like)\s+(?:you\s+to\s+)?|let'?s\s+|go\s+ahead\s+and\s+)?`

var (
	reVideo = regexp.MustCompile(`(?i)` + lead +
		`(?:(?:generate|make|create|render|produce|film|shoot)\s+(?:me\s+|us\s+)?(?:a|an|the|some|one)?\s*(?:[\w-]+\s+){0,3}?(?:video|clip|animation|movie|gif)s?\b|animate\b)`)
	reImage = regexp.MustCompile(`(?i)` + lead +
		`(?:(?:generate|make|create|render|design|produce)\s+(?:me\s+|us\s+)?(?:a|an|the|some|one)?\s*(?:[\w-]+\s+){0,3}?(?:image|picture|photo|drawing|illustration|painting|portrait|logo|artwork|art|sketch|wallpaper|icon)s?\b|(?:draw|paint|sketch|illustrate)\b)`)
	reSpeak = regexp.MustCompile(`(?i)(?:` + lead + `(?:say|speak|narrate|pronounce|read\s+(?:this|that|it|the\s+following)?\s*(?:out\s+loud|aloud))\b|\b(?:out\s+loud|aloud)\b)`)
	// what to strip off the front of a generation request to leave its subject
	reMediaLead = regexp.MustCompile(`(?i)` + lead +
		`(?:generate|make|create|render|design|produce|film|shoot|draw|paint|sketch|illustrate|animate)\s+(?:me\s+|us\s+)?` +
		`(?:(?:a|an|the|some|one)\s+)?(?:(?:short|quick|little|small|new|nice|cool|\d+[- ]?(?:s|sec|second)s?)\s+)*` +
		`(?:(?:video|clip|animation|movie|gif|image|picture|photo|drawing|illustration|painting|portrait|logo|artwork|art|sketch|wallpaper|icon)s?\s+)?` +
		`(?:(?:of|showing|with|that\s+shows|where|in\s+which|about|for)\s+)?`)
	reQuoted = regexp.MustCompile(`["“”']([^"“”']{2,})["“”']`)
)

// mediaPrompt is the subject of a generation request: "draw me a picture of a
// fox in snow" -> "a fox in snow".
func mediaPrompt(text string) string {
	t := strings.TrimSpace(text)
	p := strings.TrimSpace(reMediaLead.ReplaceAllString(t, ""))
	p = strings.TrimRight(p, " .!?")
	if len(p) < 3 {
		return t
	}
	return p
}

// speakPrompt is the exact text a "say …" request wants spoken, or "" when the
// words are the model's to choose ("say something funny").
func speakPrompt(text string) string {
	if m := reQuoted.FindStringSubmatch(text); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// Heuristic routes on concrete signals: an attached image goes to the vision
// member, "draw…/make a video…/say…" at the start of the message to the
// generator or voice, everything else to chat. A tool the ensemble lacks falls
// back to chat.
type Heuristic struct{ has func(tool string) bool }

func (Heuristic) Name() string { return "heuristic" }

func (h Heuristic) Route(_ context.Context, req *ChatRequest) Decision {
	text, img := req.lastUser()
	switch {
	case img && h.has(ensemble.ToolSeeImage):
		return Decision{Tool: ensemble.ToolSeeImage, Why: "the message has an image"}
	case img:
		return Decision{Tool: ensemble.ToolChat, Why: "the message has an image (no vision member: chat sees it if it can)"}
	case reVideo.MatchString(text) && h.has(ensemble.ToolGenerateVideo):
		return Decision{Tool: ensemble.ToolGenerateVideo, Prompt: mediaPrompt(text), Why: "asks for a video"}
	case reImage.MatchString(text) && h.has(ensemble.ToolGenerateImage):
		return Decision{Tool: ensemble.ToolGenerateImage, Prompt: mediaPrompt(text), Why: "asks for an image"}
	case reSpeak.MatchString(text) && h.has(ensemble.ToolSpeak):
		return Decision{Tool: ensemble.ToolSpeak, Prompt: speakPrompt(text), Why: "asks to hear it"}
	case req.historyHasImage() && h.has(ensemble.ToolSeeImage):
		return Decision{Tool: ensemble.ToolSeeImage, Why: "a follow-up about an image"}
	}
	return Decision{Tool: ensemble.ToolChat, Why: "chat"}
}

// ---- the conductor model --------------------------------------------------

// Conductor asks a small model which tool fits, for phrasing the heuristic
// misses. An attached image still goes straight to vision, and anything the
// model can't decide falls back to the heuristic.
type Conductor struct {
	fallback Heuristic
	tools    []string
	ask      func(ctx context.Context, prompt string) (string, error)
}

func (Conductor) Name() string { return "tool-calling" }

var toolHelp = map[string]string{
	ensemble.ToolChat:          "answer in text (questions, chat, code, explanations)",
	ensemble.ToolGenerateImage: "draw or generate a picture",
	ensemble.ToolGenerateVideo: "generate a short video clip",
	ensemble.ToolSpeak:         "say something out loud",
	ensemble.ToolSeeImage:      "look at an image the user attached",
}

func (c Conductor) Route(ctx context.Context, req *ChatRequest) Decision {
	h := c.fallback.Route(ctx, req)
	text, img := req.lastUser()
	if img || strings.TrimSpace(text) == "" || c.ask == nil {
		return h
	}
	var lines []string
	for _, t := range c.tools {
		if help, ok := toolHelp[t]; ok && t != ensemble.ToolSeeImage {
			lines = append(lines, "- "+t+": "+help)
		}
	}
	prompt := "You route requests to tools. Tools:\n" + strings.Join(lines, "\n") +
		"\nReply with ONLY the one tool name that fits this request best.\n\nRequest: " + text
	reply, err := c.ask(ctx, prompt)
	if err != nil {
		return h
	}
	pick := pickTool(reply, c.tools)
	switch pick {
	case "", ensemble.ToolSeeImage:
		return h
	case ensemble.ToolGenerateImage, ensemble.ToolGenerateVideo:
		return Decision{Tool: pick, Prompt: mediaPrompt(text), Why: "the conductor picked " + pick}
	case ensemble.ToolSpeak:
		return Decision{Tool: pick, Prompt: speakPrompt(text), Why: "the conductor picked speak"}
	}
	return Decision{Tool: pick, Why: "the conductor picked " + pick}
}

// pickTool finds the tool a model named in its reply (the longest match wins,
// so "generate_image" isn't read as something shorter).
func pickTool(reply string, tools []string) string {
	reply = strings.ToLower(stripThink(reply))
	best := ""
	for _, t := range tools {
		if strings.Contains(reply, t) && len(t) > len(best) {
			best = t
		}
	}
	return best
}
