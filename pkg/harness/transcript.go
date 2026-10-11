package harness

import (
	"time"

	"github.com/bsamanvith/agentkit/pkg/llm"
)

// Transcript is a run's memory: the messages the model sees, oldest first.
//
// It is append-only. The harness adds to a transcript and never edits,
// reorders or drops what is in it, and models get back exactly what they
// produced, opaque blocks included. A transcript marshals to JSON as an array
// of messages with [llm.Message]'s own encoding, which keeps opaque blocks byte
// for byte, so it can be saved at any point where a run is between turns and
// passed back in [Input.Transcript].
//
// A valid transcript alternates user and assistant messages starting with a
// user message, and every tool call is answered in the next message by results
// in the order of the calls. It does not have to start with a user's own
// question: a summary of earlier history is a valid start.
type Transcript []llm.Message

// Clone returns a deep copy of t.
func (t Transcript) Clone() Transcript {
	if t == nil {
		return nil
	}
	out := make(Transcript, len(t))
	for i, m := range t {
		out[i] = m.Clone()
	}
	return out
}

// Validate checks that t can be sent to a model: roles alternate starting with
// the user, every assistant message that calls tools is followed by a user
// message of results for exactly those calls in the same order, and results
// appear nowhere else. A transcript that fails wraps [ErrInvalidInput].
func (t Transcript) Validate() error {
	var pending []llm.ToolUse // calls of the previous assistant message
	for i, m := range t {
		want := llm.RoleUser
		if i%2 == 1 {
			want = llm.RoleAssistant
		}
		if m.Role != want {
			return invalidInput("message %d has role %q, want %q", i, m.Role, want)
		}
		for _, b := range m.Content {
			switch b.(type) {
			case llm.Text, llm.ToolUse, llm.ToolResult, llm.Opaque:
			default:
				return invalidInput("message %d has a block of unsupported type %T", i, b)
			}
		}
		if m.Role == llm.RoleUser {
			if len(m.Content) == 0 {
				return invalidInput("message %d is empty", i)
			}
			if err := checkResults(i, m, pending); err != nil {
				return err
			}
			pending = nil
			continue
		}
		pending = toolUses(m)
		seen := make(map[string]bool, len(pending))
		for _, u := range pending {
			if u.ID == "" || u.Name == "" {
				return invalidInput("message %d has a tool call without an ID or name", i)
			}
			if seen[u.ID] {
				return invalidInput("message %d repeats tool call ID %q", i, u.ID)
			}
			seen[u.ID] = true
		}
	}
	if len(pending) > 0 {
		return invalidInput("the last message calls tools that have no results")
	}
	return nil
}

// checkResults checks the user message at index i against the calls of the
// assistant message before it.
func checkResults(i int, m llm.Message, calls []llm.ToolUse) error {
	if len(calls) == 0 {
		for _, b := range m.Content {
			if _, ok := b.(llm.ToolResult); ok {
				return invalidInput("message %d has a tool result but no tool call precedes it", i)
			}
		}
		return nil
	}
	if len(m.Content) != len(calls) {
		return invalidInput("message %d has %d blocks, want %d tool results", i, len(m.Content), len(calls))
	}
	for j, b := range m.Content {
		r, ok := b.(llm.ToolResult)
		if !ok || r.ToolUseID != calls[j].ID {
			return invalidInput("message %d block %d is not the result of tool call %q", i, j, calls[j].ID)
		}
	}
	return nil
}

// toolUses returns the tool calls in m, in order.
func toolUses(m llm.Message) []llm.ToolUse {
	var out []llm.ToolUse
	for _, b := range m.Content {
		if u, ok := b.(llm.ToolUse); ok {
			out = append(out, u)
		}
	}
	return out
}

// UserText returns a user message of the given text.
func UserText(s string) llm.Message { return llm.User(llm.Text{Text: s}) }

// Input is what a run starts from.
type Input struct {
	// Message is the new user message. It may be left empty only to resume a
	// transcript that already ends with a user message, for example one that
	// stopped after a tool-results message.
	Message llm.Message
	// Transcript is the history so far: empty for a new conversation, the
	// result of earlier runs for a follow-up or a resume. It is not modified.
	Transcript Transcript
	// At is when the user sent Message. The zero value means the time the run
	// started.
	At time.Time
}

// hasMessage reports whether m is set. The zero Message means "none".
func hasMessage(m llm.Message) bool { return m.Role != "" || len(m.Content) > 0 }
