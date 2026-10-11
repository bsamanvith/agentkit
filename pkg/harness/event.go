package harness

import (
	"time"

	"github.com/bsamanvith/agentkit/pkg/llm"
)

// EventKind identifies an [Event].
//
// A run's events come in this order: one EventRunStart; then for each turn the
// EventTextDeltas of the reply, an EventToolStart and EventToolDone for each
// tool call (in any order between calls, but each call's start before its
// done), and one EventTurnDone; and last one EventRunDone. A run that fails
// ends with an error instead of EventRunDone.
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
	// ToolUse is the call of EventToolStart.
	ToolUse llm.ToolUse
	// ToolResult and Duration are those of EventToolDone.
	ToolResult llm.ToolResult
	Duration   time.Duration

	// Prompt is set for EventRunStart, TurnRecord for EventTurnDone and Result
	// for EventRunDone. They are copies the consumer may keep.
	Prompt     *TimedMessage
	TurnRecord *TurnRecord
	Result     *Result
}
