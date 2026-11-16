package agent

// Keeping a conversation inside the model's context window, and the step
// log. A small local model (8K tokens) fills its context after a couple of
// web pages; llama-server then refuses the request or cuts it. When the agent
// knows the window (Options.ContextSize), it estimates each step's prompt
// before sending it and shortens the oldest tool outputs once the estimate
// passes contextBudget of the window. Each new tool output is capped at
// toolShare of the window too.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

const (
	contextBudget = 0.80 // compact older tool outputs above this share of the window
	// contextTarget is where a compaction brings the prompt down to. Each
	// compaction changes text the server has cached, so it re-reads the
	// prompt from there: shortening well below the budget in one go makes
	// that happen rarely instead of on every step.
	contextTarget = 0.60
	toolShare     = 0.35 // one tool output may take at most this share
	bytesPerToken = 3    // a conservative bytes-to-tokens ratio for estimates
	stubBytes     = 400  // what is kept of a shortened tool output
	imageTokens   = 1024 // what an image in a request is counted as
	compactNote   = "[older tool output removed to save context]"
)

var dataURIRe = regexp.MustCompile(`data:[A-Za-z0-9.+/-]{1,64};base64,[A-Za-z0-9+/=]+`)

// promptBytes is a request's size for estimates: base64 images count as
// imageTokens each instead of their length.
func promptBytes(body []byte) int {
	n := len(body)
	for _, loc := range dataURIRe.FindAllIndex(body, -1) {
		n -= loc[1] - loc[0]
		n += imageTokens * bytesPerToken
	}
	return n
}

// estimate is the prompt size of body in tokens: the size the model reported
// for the last step plus the growth since, or bytes/bytesPerToken before any
// report.
func (rn *run) estimate(body []byte) int {
	b := promptBytes(body)
	if rn.lastPrompt > 0 {
		est := rn.lastPrompt + (b-rn.lastBytes)/bytesPerToken
		if est < 0 {
			est = 0
		}
		return est
	}
	return b / bytesPerToken
}

// maxOutput caps one tool output: limits.max_output_bytes, or toolShare of
// the context window when that is smaller.
func (rn *run) maxOutput() int {
	max := rn.a.cfg.Limits.MaxOutputBytes
	if n := rn.a.ContextSize(); n > 0 {
		if c := int(float64(n) * toolShare * bytesPerToken); c < max {
			max = c
		}
		if max < minOutputBytes {
			max = minOutputBytes
		}
	}
	return max
}

// fit shortens older tool outputs, when the step's estimated prompt is over
// contextBudget of the window, until it is within contextTarget (or nothing
// is left to shorten).
func (rn *run) fit(msgs []json.RawMessage, body []byte, step int, final bool, window int) ([]json.RawMessage, []byte) {
	budget := int(float64(window) * contextBudget)
	before := rn.estimate(body)
	if before <= budget {
		return msgs, body
	}
	need := (before - int(float64(window)*contextTarget)) * bytesPerToken
	// First the outputs of earlier steps; then, if that isn't enough, the
	// latest step's too, except its very last result.
	out, n, saved := compactToolOutputs(msgs, need, false)
	if saved < need {
		var n2, s2 int
		out, n2, s2 = compactToolOutputs(out, need-saved, true)
		n += n2
		saved += s2
	}
	if n == 0 {
		rn.a.logf("step %d: the prompt is ≈%d tokens of a %d-token context and there is no older tool output to shorten", step, before, window)
		return msgs, body
	}
	body = rn.body(out, step, final)
	after := rn.estimate(body)
	rn.compacted(step, n, fmt.Sprintf("≈%d → ≈%d tokens of a %d-token context", before, after, window))
	return out, body
}

// compacted logs and reports shortened tool outputs.
func (rn *run) compacted(step, n int, detail string) {
	msg := fmt.Sprintf("Shortened %d older tool %s to fit the model's context (%s).", n, plural(n, "output", "outputs"), detail)
	rn.a.logf("step %d: shortened %d older tool %s (%s)", step, n, plural(n, "output", "outputs"), detail)
	rn.out.event(Event{Type: "compact", Step: step, Message: msg}, "\n[harness] "+msg+"\n")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// compactToolOutputs replaces the content of tool messages, oldest first,
// with a short stub until need bytes are saved. The last tool message is
// never touched; neither are the tool messages after the last assistant
// message (the latest step's results) unless latest is true.
func compactToolOutputs(msgs []json.RawMessage, need int, latest bool) ([]json.RawMessage, int, int) {
	lastTool, lastAsst := -1, -1
	for i, m := range msgs {
		switch roleOf(m) {
		case "tool":
			lastTool = i
		case "assistant":
			lastAsst = i
		}
	}
	out := append([]json.RawMessage{}, msgs...)
	n, saved := 0, 0
	for i, m := range out {
		if saved >= need {
			break
		}
		if i == lastTool || (!latest && i > lastAsst) || roleOf(m) != "tool" {
			continue
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(m, &obj) != nil {
			continue
		}
		text := contentText(obj["content"])
		if strings.Contains(text, compactNote) || len(text) <= stubBytes+len(compactNote)+64 {
			continue
		}
		stub := stubOf(text)
		obj["content"], _ = json.Marshal(stub)
		b, err := json.Marshal(obj)
		if err != nil {
			continue
		}
		saved += len(m) - len(b)
		out[i] = b
		n++
	}
	return out, n, saved
}

// stubOf keeps the start of a tool output, inside its <tool_output> wrapper
// when it has one.
func stubOf(text string) string {
	const open = "<tool_output tool=\""
	const closeTag = "\n</tool_output>"
	if strings.HasPrefix(text, open) && strings.HasSuffix(text, closeTag) {
		if nl := strings.Index(text, "\n"); nl > 0 {
			head := text[:nl+1]
			inner := strings.TrimSuffix(text[nl+1:], closeTag)
			return head + truncateUTF8(inner, stubBytes) + "\n" + compactNote + closeTag
		}
	}
	return truncateUTF8(text, stubBytes) + "\n" + compactNote
}

func roleOf(m json.RawMessage) string {
	var r struct {
		Role string `json:"role"`
	}
	_ = json.Unmarshal(m, &r)
	return r.Role
}

// isContextError recognizes llama-server's (and OpenAI's) "too long" errors.
func isContextError(ue *upstreamError) bool {
	text := strings.ToLower(ue.msg + " " + string(ue.body))
	for _, k := range []string{"exceed_context_size", "exceeds the available context size", "context size has been exceeded",
		"context_length_exceeded", "maximum context length", "context shift is disabled"} {
		if strings.Contains(text, k) {
			return true
		}
	}
	return false
}

// contextError is the error a client gets for a conversation that doesn't
// fit the model's context.
func (rn *run) contextError(ue *upstreamError) *upstreamError {
	window := rn.a.ContextSize()
	var e struct {
		Error struct {
			NCtx int `json:"n_ctx"`
		} `json:"error"`
	}
	if json.Unmarshal(ue.body, &e) == nil && e.Error.NCtx > 0 {
		window = e.Error.NCtx
	}
	size := ""
	if window > 0 {
		size = fmt.Sprintf(" (%d tokens)", window)
	}
	msg := "The conversation is too long for this model's context window" + size +
		". Raise Context in the Run dialog or start a new chat."
	rn.a.logf("the request doesn't fit the model's context%s", size)
	return &upstreamError{status: http.StatusBadRequest, msg: msg, typ: "exceed_context_size_error"}
}

// withNudge adds the harness's nudge to the last user or tool message (a new
// message could break a template's strict role alternation).
func withNudge(msgs []json.RawMessage, note string) []json.RawMessage {
	out := append([]json.RawMessage{}, msgs...)
	if len(out) > 0 {
		last := out[len(out)-1]
		if r := roleOf(last); r == "user" || r == "tool" {
			var obj map[string]json.RawMessage
			if json.Unmarshal(last, &obj) == nil {
				var s string
				var parts []json.RawMessage
				switch {
				case json.Unmarshal(obj["content"], &s) == nil:
					obj["content"], _ = json.Marshal(s + "\n\n" + note)
				case json.Unmarshal(obj["content"], &parts) == nil:
					p, _ := json.Marshal(map[string]string{"type": "text", "text": note})
					obj["content"], _ = json.Marshal(append(parts, p))
				default:
					obj["content"], _ = json.Marshal(note)
				}
				out[len(out)-1], _ = json.Marshal(obj)
				return out
			}
		}
	}
	m, _ := json.Marshal(map[string]string{"role": "user", "content": note})
	return append(out, m)
}

// emptyAnswer gives the client a visible message for a reply the model left
// empty, so it never shows a blank answer.
func (rn *run) emptyAnswer(step int, t *turn) {
	msg := "The model ended its turn without an answer. Try again or ask more directly."
	if t.finish == "length" {
		msg = "The model ran out of room before answering (its context window or max_tokens filled up while it was thinking). " +
			"Ask something shorter, raise Context in the Run dialog, or start a new chat."
	}
	rn.a.logf("step %d: still no answer (finish %s); told the client", step, orDash(t.finish))
	rn.out.event(Event{Type: "empty", Step: step, Message: msg}, "\n[harness] "+msg+"\n")
	rn.out.addContent(msg)
}

// logStep logs one model step: no user content, only sizes and tool names.
func (rn *run) logStep(step int, t *turn) {
	names := make([]string, 0, len(t.calls))
	for _, c := range t.calls {
		names = append(names, logName(c.Name))
	}
	calls := "none"
	if len(names) > 0 {
		calls = strings.Join(names, ",")
	}
	tok := ""
	if t.usage != nil {
		tok = fmt.Sprintf(", tokens %d prompt + %d completion", t.usage.PromptTokens, t.usage.CompletionTokens)
	}
	rn.a.logf("step %d: finish %s, tool calls %s, content %d B, reasoning %d B%s",
		step, orDash(t.finish), calls, len(t.content), len(t.reasoning), tok)
}

var logNameRe = regexp.MustCompile(`[^A-Za-z0-9_.:-]`)

// logName is a tool name fit for a log: a model may invent anything.
func logName(s string) string {
	s = logNameRe.ReplaceAllString(s, "?")
	if len(s) > 64 {
		s = s[:64] + "..."
	}
	if s == "" {
		s = "(none)"
	}
	return s
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
