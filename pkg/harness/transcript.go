package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/policy"
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
// in the order of the calls. That message may go on with text after its
// results: what a person said while the tools ran (steering), which the model
// reads before it goes on. It does not have to start with a user's own
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

// Validate checks that t can be sent to a model and saved: roles alternate
// starting with the user; every message is well formed ([shapeProblem]);
// every assistant message that calls tools is followed by a user message of
// results for exactly those calls in the same order, then nothing but text;
// and results and calls appear nowhere else. A transcript that fails wraps [ErrInvalidInput].
//
// It checks structure only. Whether a provider accepts the content (an empty
// reply, say) is for its adapter.
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
		if err := shapeProblem(m); err != nil {
			return invalidInput("message %d: %v", i, err)
		}
		if m.Role == llm.RoleUser {
			if err := checkResults(i, m, pending); err != nil {
				return err
			}
			pending = nil
			continue
		}
		pending = toolUses(m)
	}
	if len(pending) > 0 {
		return invalidInput("the last message calls tools that have no results")
	}
	return nil
}

// shapeProblem reports what is wrong with one message on its own, apart from
// where it sits in a conversation. Both [Transcript.Validate] and the check on
// the model's replies use it, so the harness never commits a message that
// Validate or JSON encoding would reject.
func shapeProblem(m llm.Message) error {
	for _, b := range m.Content {
		switch b := b.(type) {
		case llm.Text:
		case llm.ToolUse:
			if m.Role != llm.RoleAssistant {
				return errors.New("a tool call outside an assistant message")
			}
			if b.ID == "" || b.Name == "" {
				return errors.New("a tool call without an ID or name")
			}
			if len(b.Input) > 0 && !json.Valid(b.Input) {
				return fmt.Errorf("tool call %q has input that is not JSON", b.ID)
			}
		case llm.ToolResult:
			if m.Role != llm.RoleUser {
				return errors.New("a tool result outside a user message")
			}
			if b.ToolUseID == "" {
				return errors.New("a tool result without a tool call ID")
			}
		case llm.Opaque:
			if b.Provider == "" || !json.Valid(b.Data) {
				return errors.New("an opaque block without a provider or with data that is not JSON")
			}
		default:
			return fmt.Errorf("a block of unsupported type %T", b)
		}
	}
	switch m.Role {
	case llm.RoleUser:
		if len(m.Content) == 0 {
			return errors.New("an empty user message")
		}
	case llm.RoleAssistant:
		seen := map[string]bool{}
		for _, u := range toolUses(m) {
			if seen[u.ID] {
				return fmt.Errorf("tool call ID %q is repeated", u.ID)
			}
			seen[u.ID] = true
		}
	default:
		return fmt.Errorf("unknown role %q", m.Role)
	}
	return nil
}

// replyProblem reports what is wrong with a reply that would be added to the
// transcript.
func replyProblem(m llm.Message) error {
	if m.Role != llm.RoleAssistant {
		return fmt.Errorf("the reply has role %q, want %q", m.Role, llm.RoleAssistant)
	}
	return shapeProblem(m)
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
	if len(m.Content) < len(calls) {
		return invalidInput("message %d has %d blocks, want %d tool results", i, len(m.Content), len(calls))
	}
	for j, b := range m.Content {
		if j >= len(calls) {
			// After the results, text only: what a person added at the turn
			// boundary (steering).
			if _, ok := b.(llm.Text); !ok {
				return invalidInput("message %d block %d after the tool results is not text", i, j)
			}
			continue
		}
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
	// ToolPolicy, when set, narrows the harness's tool policy for this run
	// ([WithToolPolicy]): each call gets the stricter of the two decisions.
	// Without a harness policy, it is the run's policy. It must be valid
	// ([policy.Policy.Validate]), or the run fails with [ErrInvalidInput].
	ToolPolicy *policy.Policy
	// Interaction, when set, narrows the harness's [WithInteraction] for this
	// run ([NarrowInteraction]). It must be one of [Interactions].
	Interaction Interaction
	// Pending is a turn an earlier run suspended in ([EventSuspended]), to
	// finish first with Answers, one for each of its interrupts. The run
	// then has no Message, and Transcript is the transcript the suspended
	// run ended with (it ends with the user message the pending reply
	// answers). Answers without Pending are [ErrInvalidInput], and so are
	// answers that do not fit ([ErrInvalidAnswer]).
	Pending *Pending
	Answers []Answer
}

// hasMessage reports whether m is set. The zero Message means "none".
func hasMessage(m llm.Message) bool { return m.Role != "" || len(m.Content) > 0 }
