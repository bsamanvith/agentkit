package agentapi

import (
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/harness"
)

// The wire types are defined in package [wire], which clients import without
// the harness; these names keep the server's API in one place.

// WireVersion is the "v" of every event and resource this package writes:
// [wire.Version].
const WireVersion = wire.Version

// Event types, the "type" of an [Event] and the SSE event name.
const (
	TypeRunStart   = wire.TypeRunStart
	TypeTextDelta  = wire.TypeTextDelta
	TypeToolCall   = wire.TypeToolCall
	TypeToolResult = wire.TypeToolResult
	TypeTurnDone   = wire.TypeTurnDone
	TypeRunDone    = wire.TypeRunDone
	TypeRunError   = wire.TypeRunError
	TypeRetrying   = wire.TypeRetrying
)

// Error codes of a failed run, and of HTTP errors.
const (
	CodeBudgetExceeded = wire.CodeBudgetExceeded
	CodeMaxTokens      = wire.CodeMaxTokens
	CodeRefused        = wire.CodeRefused
	CodeUnexpectedStop = wire.CodeUnexpectedStop
	CodeCancelled      = wire.CodeCancelled
	CodeTimeout        = wire.CodeTimeout
	CodeShutdown       = wire.CodeShutdown
	CodeRateLimited    = wire.CodeRateLimited
	CodeOverloaded     = wire.CodeOverloaded
	CodeModelAuth      = wire.CodeModelAuth
	CodeContextTooLong = wire.CodeContextTooLong
	CodeModelRejected  = wire.CodeModelRejected
	CodeInternal       = wire.CodeInternal
	CodeInvalidRequest = wire.CodeInvalidRequest
	CodeUnauthorized   = wire.CodeUnauthorized
	CodeNotFound       = wire.CodeNotFound
	CodeTooLarge       = wire.CodeTooLarge
	CodeBusy           = wire.CodeBusy
	CodeInterrupted    = wire.CodeInterrupted
	CodeOverCapacity   = wire.CodeOverCapacity
	CodeShuttingDown   = wire.CodeShuttingDown
	CodeAgentMismatch  = wire.CodeAgentMismatch

	CodeRunInterrupted   = wire.CodeRunInterrupted
	CodeRunFailed        = wire.CodeRunFailed
	CodeRejected         = wire.CodeRejected
	CodeSuspended        = wire.CodeSuspended
	CodeRunFinal         = wire.CodeRunFinal
	CodeRunLeased        = wire.CodeRunLeased
	CodeAwaitingDecision = wire.CodeAwaitingDecision
	CodeNotPending       = wire.CodeNotPending
	CodeShapeChanged     = wire.CodeShapeChanged
)

// Run statuses.
const (
	StatusRunning = wire.StatusRunning
	StatusDone    = wire.StatusDone
	StatusFailed  = wire.StatusFailed

	StatusCanceled  = wire.StatusCanceled
	StatusRejected  = wire.StatusRejected
	StatusSuspended = wire.StatusSuspended
)

// Conversation statuses (and [StatusRunning]).
const (
	StatusIdle        = wire.StatusIdle
	StatusInterrupted = wire.StatusInterrupted
)

type (
	// Event is [wire.Event]: one event of a run.
	Event = wire.Event
	// TimedMessage is [wire.TimedMessage]: a message and when it was written.
	TimedMessage = wire.TimedMessage
	// TurnRecord is [wire.TurnRecord]: one model call and its tools.
	TurnRecord = wire.TurnRecord
	// ToolRecord is [wire.ToolRecord]: one tool call of a turn.
	ToolRecord = wire.ToolRecord
	// Record is [wire.Record]: what a run did around its messages.
	Record = wire.Record
	// Error is [wire.Error]: a typed error on the wire.
	Error = wire.Error
	// Run is [wire.Run]: a run as GET /v1/runs/{id} returns it.
	Run = wire.Run
	// Conversation is [wire.Conversation]: a conversation and its messages.
	Conversation = wire.Conversation
	// RunRequest is [wire.RunRequest]: the body of POST /v1/runs.
	RunRequest = wire.RunRequest
	// ErrorResponse is [wire.ErrorResponse]: the body of every HTTP error.
	ErrorResponse = wire.ErrorResponse
	// RunList is [wire.RunList]: the body of GET /v1/runs.
	RunList = wire.RunList
	// DecisionRequest is [wire.DecisionRequest]: the body of POST
	// /v1/runs/{id}/decisions.
	DecisionRequest = wire.DecisionRequest
	// Credential is [wire.Credential]: where a toolset's credential comes
	// from. The other catalog types (agents, toolsets, tools, conversation
	// lists) are used from package wire: [Agent] and [Toolset] here are what
	// the server is given, not what it writes.
	Credential = wire.Credential
)

// The conversions from harness types.

func timedMessage(m harness.TimedMessage) TimedMessage {
	return TimedMessage{At: m.At, Message: m.Message}
}

func timedMessages(ms []harness.TimedMessage) []TimedMessage {
	out := make([]TimedMessage, len(ms))
	for i, m := range ms {
		out[i] = timedMessage(m)
	}
	return out
}

func turnRecord(r harness.TurnRecord) TurnRecord {
	tools := make([]ToolRecord, len(r.Tools))
	for i, t := range r.Tools {
		tools[i] = ToolRecord{ToolUseID: t.ToolUseID, Name: t.Name, StartedAt: t.StartedAt, DurationMS: ms(t.Duration), IsError: t.IsError}
	}
	return TurnRecord{
		Turn: r.Turn, Messages: timedMessages(r.Messages),
		RequestedAt: r.RequestedAt, FirstEventAt: r.FirstEventAt, DoneAt: r.DoneAt,
		Usage: r.Usage, Stop: r.Stop, Tools: tools,
	}
}

// ms is a duration in milliseconds, to the microsecond.
func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// wireEvent converts a harness event other than EventRunDone (which the
// server turns into run_done itself). ok is false for a kind it does not know.
// turns is added to the event's turn: the turns of the run before this
// execution, which the harness does not count.
func wireEvent(runID, convID string, turns int, ev harness.Event) (Event, bool) {
	e := Event{V: WireVersion, RunID: runID, At: ev.At, Turn: ev.Turn}
	if e.Turn > 0 {
		e.Turn += turns
	}
	switch ev.Kind {
	case harness.EventRunStart:
		e.Type, e.ConversationID = TypeRunStart, convID
		if ev.Prompt != nil {
			p := timedMessage(*ev.Prompt)
			e.Prompt = &p
		}
	case harness.EventTextDelta:
		e.Type, e.Block, e.Text = TypeTextDelta, ev.Block, ev.Text
	case harness.EventToolStart:
		e.Type, e.Call, e.ToolUseID, e.Name, e.Input = TypeToolCall, ev.Call, ev.ToolUse.ID, ev.ToolUse.Name, ev.ToolUse.Input
	case harness.EventToolDone:
		e.Type, e.Call, e.ToolUseID = TypeToolResult, ev.Call, ev.ToolResult.ToolUseID
		e.Content, e.IsError, e.DurationMS = ev.ToolResult.Content, ev.ToolResult.IsError, ms(ev.Duration)
	case harness.EventTurnDone:
		r := turnRecord(*ev.TurnRecord)
		r.Turn += turns
		e.Type, e.Record = TypeTurnDone, &r
	default:
		return Event{}, false
	}
	return e, true
}
