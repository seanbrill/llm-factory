package agent

// Habits a small model lacks, kept by the loop: it repeats a call that
// failed instead of reading the error; it writes a tool call longer than the
// client's max_tokens, which arrives cut off; and it says a task is done
// without reading back what it changed. The loop notices each and tells the
// model, in the tool result or a short note, once per run where it matters.
// HARNESS_CHECKS=off turns the notes off (the plan-and-check line in the
// system note stays: it is advice, not a step).

import (
	"fmt"
	"strings"
)

// planNote is added to the system note when tools are offered.
const planNote = "For a task that takes several tool calls, start with a short numbered plan and follow it. " +
	"When a call fails, read the error before trying again: it often says what to change. " +
	"Before you say a task is done, read back what you changed with a tool and fix what is wrong."

// cutCallNote replaces "not valid JSON" when the reply hit its length limit.
const cutCallNote = "your reply reached its length limit (max_tokens) in the middle of this call, so its arguments were cut off. " +
	"Make one tool call per reply, and keep its arguments shorter (split a long piece of work into several calls), then call it again"

// repeatNote is added to a failed call's result when the same call, or the
// same tool with the same error, already failed in this run.
func repeatNote(n int) string {
	return fmt.Sprintf("\n\n[harness] This call has now failed %d times the same way. Don't send it again unchanged: read the error "+
		"above (it may list the valid values or what is missing), change the arguments, use another tool, or tell the user what is blocking you.", n)
}

// checkNote asks the model to read back its changes before answering.
func checkNote(changed []string) string {
	return "[harness] Before you finish: you changed things with " + strings.Join(changed, ", ") + " and haven't read the result back. " +
		"Check it now with a tool that reads it (for example a get_, list_, read_ or view_ tool) and fix what is wrong, then give your answer. " +
		"If you can't check it, say so in your answer."
}

var (
	writeVerbs = []string{"set", "create", "update", "edit", "write", "add", "arrange", "insert", "replace", "delete", "remove",
		"move", "rename", "apply", "save", "upload", "post", "put", "patch", "make", "draw", "generate", "import", "fill", "tag"}
	readVerbs = []string{"get", "list", "read", "view", "describe", "check", "preview", "search", "find", "fetch", "show",
		"glob", "grep", "inspect", "query"}
)

// toolKind classifies a tool by the verb its name starts with (after an MCP
// server's "<server>__" prefix): write_file and knitted__set_design_frame
// change things; read_file and knitted__get_design read them.
func toolKind(name string) (write, read bool) {
	if i := strings.LastIndex(name, "__"); i >= 0 {
		name = name[i+2:]
	}
	name = strings.ToLower(name)
	verb, _, _ := strings.Cut(name, "_")
	for _, v := range writeVerbs {
		if verb == v {
			return true, false
		}
	}
	for _, v := range readVerbs {
		if verb == v {
			return false, true
		}
	}
	return false, false
}

// checks is one run's record for the notes above.
type checks struct {
	fails     map[string]int
	cut       bool // the current step's reply hit its length limit
	lastWrite int  // the step of the last successful change (0 = none)
	lastRead  int  // the step of the last successful read
	changed   []string
	asked     bool // the model was asked to check its work
}

// called records a successful call (name is the tool that ran: call_tool's
// inner tool).
func (c *checks) called(step int, name string) {
	w, r := toolKind(name)
	switch {
	case w:
		c.lastWrite = step
		for _, n := range c.changed {
			if n == name {
				return
			}
		}
		if len(c.changed) < 6 {
			c.changed = append(c.changed, name)
		}
	case r:
		c.lastRead = step
	}
}

// failed records a failed call and returns how often it failed the same way.
func (c *checks) failed(name, args, msg string) int {
	if c.fails == nil {
		c.fails = map[string]int{}
	}
	k1 := name + "\x00a" + strings.Join(strings.Fields(args), "")
	k2 := name + "\x00e" + short(msg, 200)
	c.fails[k1]++
	c.fails[k2]++
	return max(c.fails[k1], c.fails[k2])
}

// shouldAsk reports whether the model should check its work before this
// answer: it changed something after its last read, it hasn't been asked
// yet, and it has a tool that reads.
func (c *checks) shouldAsk(canRead bool) bool {
	return !c.asked && c.lastWrite > 0 && c.lastWrite >= c.lastRead && canRead
}

// innerName is the tool a call really runs: call_tool's "name" argument.
func innerName(c toolCall) string {
	if c.Name != callToolName {
		return c.Name
	}
	if name, _, err := callToolArgs(argsJSON(c.Args)); err == nil {
		return name
	}
	return c.Name
}
