package agent

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

const maxSSEEvent = 16 << 20

// sseReader reads the data of server-sent events.
type sseReader struct {
	br *bufio.Reader
}

func newSSEReader(r io.Reader) *sseReader { return &sseReader{br: bufio.NewReaderSize(r, 64<<10)} }

// next returns the data of the next event ("[DONE]" included), or io.EOF.
func (s *sseReader) next() (string, error) {
	var data []string
	size := 0
	for {
		line, err := s.br.ReadString('\n')
		if line == "" && err != nil {
			if len(data) > 0 {
				return strings.Join(data, "\n"), nil
			}
			return "", err
		}
		size += len(line)
		if size > maxSSEEvent {
			return "", errors.New("upstream sent an oversized event")
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if len(data) > 0 {
				return strings.Join(data, "\n"), nil
			}
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		case strings.HasPrefix(line, "error:"):
			// Older llama-server versions report a failed stream this way.
			return "", &upstreamError{msg: strings.TrimSpace(strings.TrimPrefix(line, "error:"))}
		}
		if err != nil {
			if len(data) > 0 {
				return strings.Join(data, "\n"), nil
			}
			return "", err
		}
	}
}

// Usage is OpenAI token usage. The harness reports the sum over all steps.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (u *Usage) add(o *Usage) {
	if o == nil {
		return
	}
	u.PromptTokens += o.PromptTokens
	u.CompletionTokens += o.CompletionTokens
	u.TotalTokens += o.TotalTokens
}

// Event is one tool step, as reported to clients: in the "harness" field of a
// streamed chunk, and in the "harness.events" list of a non-streamed response.
type Event struct {
	// Type is "tool_call" (the model asked for a tool), "tool_result" (the
	// tool finished), "limit" (the step limit stopped the loop), "retry"
	// (the model stopped without answering and is asked again), "empty" (it
	// still didn't answer; the client gets a message instead), "compact"
	// (older tool outputs were shortened to fit the context), "error" (the
	// reply failed after it started) or "done" (on the final chunk of a
	// stream).
	Type string `json:"type"`
	// Step is the model turn the call came from, starting at 1.
	Step int `json:"step"`
	// ID is the tool call id; Name the tool; Toolset its toolset.
	ID      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	Toolset string `json:"toolset,omitempty"`
	// Arguments are the call's arguments (a JSON object, or the raw text as a
	// JSON string when the model sent invalid JSON).
	Arguments json.RawMessage `json:"arguments,omitempty"`
	// OK, Output, Error, Bytes, Truncated and DurationMS describe a result.
	// Output is the text the model received (before the untrusted-data
	// wrapper), already capped at limits.max_output_bytes.
	OK         *bool  `json:"ok,omitempty"`
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
	Bytes      int    `json:"bytes,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	// Message explains a limit event.
	Message string `json:"message,omitempty"`
	// Steps is the number of tool steps, on the done event.
	Steps int `json:"steps,omitempty"`
}

// output collects the reply and, for streaming requests, writes it as
// OpenAI chat.completion.chunk events while it is produced.
type output struct {
	w       http.ResponseWriter
	fl      http.Flusher
	stream  bool
	started bool

	id      string
	model   string
	created int64

	content    strings.Builder
	reasoning  strings.Builder
	sepPending bool // a tool step ran since the last content
	events     []Event
}

func newOutput(w http.ResponseWriter, stream bool, model string) *output {
	fl, _ := w.(http.Flusher)
	return &output{w: w, fl: fl, stream: stream, model: model, id: "chatcmpl-" + randomHex(12), created: time.Now().Unix()}
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "harness"
	}
	return hex.EncodeToString(b)
}

type chunkChoice struct {
	Index        int            `json:"index"`
	Delta        map[string]any `json:"delta"`
	FinishReason *string        `json:"finish_reason"`
}

type chunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []chunkChoice `json:"choices"`
	Usage   *Usage        `json:"usage,omitempty"`
	Harness *Event        `json:"harness,omitempty"`
}

// start writes the SSE headers and the opening chunk.
func (o *output) start() {
	if o.started || !o.stream {
		return
	}
	o.started = true
	h := o.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	h.Del("Content-Length")
	o.w.WriteHeader(http.StatusOK)
	o.send(chunk{Choices: []chunkChoice{{Delta: map[string]any{"role": "assistant", "content": ""}}}})
}

func (o *output) send(c chunk) {
	c.ID, c.Object, c.Created, c.Model = o.id, "chat.completion.chunk", o.created, o.model
	if c.Choices == nil {
		c.Choices = []chunkChoice{}
	}
	b, _ := json.Marshal(c)
	o.writeRaw("data: " + string(b) + "\n\n")
}

func (o *output) writeRaw(s string) {
	_, _ = io.WriteString(o.w, s)
	if o.fl != nil {
		o.fl.Flush()
	}
}

func (o *output) delta(d map[string]any, ev *Event) {
	if !o.stream {
		return
	}
	o.start()
	o.send(chunk{Choices: []chunkChoice{{Delta: d}}, Harness: ev})
}

// addContent appends answer text. Text after a tool step is separated from
// the text before it by a blank line.
func (o *output) addContent(s string) {
	if s == "" {
		return
	}
	if o.sepPending && o.content.Len() > 0 {
		s = "\n\n" + s
	}
	o.sepPending = false
	o.content.WriteString(s)
	o.delta(map[string]any{"content": s}, nil)
}

func (o *output) addReasoning(s string) {
	if s == "" {
		return
	}
	o.reasoning.WriteString(s)
	o.delta(map[string]any{"reasoning_content": s}, nil)
}

// event records a tool step and shows it as reasoning text.
func (o *output) event(ev Event, text string) {
	o.events = append(o.events, ev)
	o.reasoning.WriteString(text)
	o.delta(map[string]any{"reasoning_content": text}, &ev)
}

func (o *output) keepalive() {
	if o.stream && o.started {
		o.writeRaw(": keepalive\n\n")
	}
}
