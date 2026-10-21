package harness

import (
	"time"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/policy"
)

// EventKind identifies an [Event].
//
// A run's events come in this order: one EventRunStart; then for each turn the
// EventTextDeltas of the reply, an EventToolStart and EventToolDone for each
// tool call (in any order between calls, but each call's start before its
// done, and its EventToolPolicy and EventToolFlagged, if any, in between),
// and one EventTurnDone; and last one EventRunDone. A run that fails ends with
// an error instead of EventRunDone, and one that suspends ([WithInteraction])
// with EventSuspended: its last turn's calls end with an EventToolDone or an
// EventInterrupt, and the turn has no EventTurnDone. A run that finishes a
// pending turn ([Input.Pending]) announces it first, as turn 0: an
// EventToolStart and EventToolDone (or EventInterrupt) for each call that
// waited, and its EventTurnDone, whose TurnRecord.Turn is 0 too. Consumers
// must ignore kinds they do not know: later versions add more.
type EventKind uint8

// The event kinds.
const (
	// EventRunStart is the first event. Event.Prompt is the user message the run
	// added, nil when it resumed without one.
	EventRunStart EventKind = iota + 1
	// EventTextDelta carries a piece of the model's text in Event.Text.
	EventTextDelta
	// EventToolStart says a tool call began to run: Event.Call is its index in
	// the turn's calls and Event.ToolUse a copy of the call. A call that never
	// began because the run ended first has neither this event nor
	// EventToolDone.
	EventToolStart
	// EventToolDone says a tool call that began has finished: Event.Call, Event.ToolResult
	// (check IsError) and Event.Duration. Events of parallel calls arrive in
	// the order the calls finish.
	EventToolDone
	// EventTurnDone says a turn is complete and in the transcript.
	// Event.TurnRecord holds the messages it added, usage and timings. When a
	// run stops because of the model's reply (a refusal, the output limit),
	// the reply's turn is announced the same way before the error.
	EventTurnDone
	// EventRunDone ends a successful run: Event.Result.
	EventRunDone
	// EventToolPolicy says what the run's tool policy ([WithToolPolicy])
	// decided about a call, when it did not plainly allow it: Event.Call,
	// Event.ToolUse (its ID and name, without the input) and Event.Decision.
	// It comes after the call's EventToolStart and before its EventToolDone.
	EventToolPolicy
	// EventToolFlagged says a call the policy flagged is about to run, with
	// the same fields as its EventToolPolicy, which comes first.
	EventToolFlagged
	// EventInterrupt says a tool call waits for a person ([WithInteraction]):
	// Event.Call, Event.ToolUse (its ID and name) and Event.Interrupt. It
	// ends the call in place of its EventToolDone.
	EventInterrupt
	// EventSuspended ends a run, in place of EventRunDone, whose turn has
	// calls that wait for people: Event.Result is what the run did, with the
	// turn in Result.Pending. The turn has no EventTurnDone; the run that
	// finishes it announces it.
	EventSuspended
)

func (k EventKind) String() string {
	switch k {
	case EventRunStart:
		return "run_start"
	case EventTextDelta:
		return "text_delta"
	case EventToolStart:
		return "tool_start"
	case EventToolDone:
		return "tool_done"
	case EventTurnDone:
		return "turn_done"
	case EventRunDone:
		return "run_done"
	case EventToolPolicy:
		return "tool_policy"
	case EventToolFlagged:
		return "tool_flagged"
	case EventInterrupt:
		return "interrupt"
	case EventSuspended:
		return "suspended"
	}
	return "unknown"
}

// Event is one step of a run. Which fields are set depends on Kind.
type Event struct {
	Kind EventKind
	// Turn is the turn the event belongs to, starting at 1; zero for
	// EventRunStart and EventRunDone.
	Turn int
	// At is when the event happened, by the harness's clock.
	At time.Time

	// Text is the delta of EventTextDelta, and Block the index of the text
	// block of the reply it belongs to (a reply can have several, with tool
	// calls in between).
	Text  string
	Block int
	// Call is the index of the tool call within its turn, for EventToolStart
	// and EventToolDone.
	Call int
	// ToolUse is the call of EventToolStart; for EventToolPolicy and
	// EventToolFlagged, its ID and name.
	ToolUse llm.ToolUse
	// ToolResult and Duration are those of EventToolDone.
	ToolResult llm.ToolResult
	Duration   time.Duration
	// Decision is the policy's decision of EventToolPolicy and
	// EventToolFlagged.
	Decision policy.Decision
	// Interrupt is what the call of EventInterrupt waits for.
	Interrupt *Interrupt

	// Prompt is set for EventRunStart, TurnRecord for EventTurnDone and Result
	// for EventRunDone and EventSuspended. They are copies the consumer may
	// keep.
	Prompt     *TimedMessage
	TurnRecord *TurnRecord
	Result     *Result
}
