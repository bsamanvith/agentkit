// Package wire is the agent API's wire format (`/v1`, see package
// [github.com/bsamanvith/agentkit/pkg/agentapi]): the events of a run's
// Server-Sent Events stream, the run and conversation resources, and the
// error body. The server writes these types and clients read them, so both
// agree by construction.
//
// It imports only [llm] (messages and usage), never the harness, so a client
// can use it without pulling in the agent loop.
//
// Within [Version] the format only grows: new fields, event types, error codes
// and message block types. Decoding into these types ignores unknown fields,
// messages included, and keeps a block of an unknown type as an
// [llm.Unknown] block (see [llm.UnmarshalMessageLenient]); a client must pass
// unknown event types and codes through rather than fail on them.
package wire

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/llm"
)

// Version is the "v" of every event and resource the agent API writes.
// Changes within a version are additive only: new fields, new event types,
// new error codes, new message block types. A client must ignore what it does
// not know.
const Version = 1

// Event types, the "type" of an [Event] and the SSE event name.
const (
	// TypeRunStart is the first event of a run: the run and conversation IDs,
	// and the prompt the run added (absent only for a run that stopped before
	// it stored its prompt, or a flow run). A resumed run keeps its events:
	// its stream goes on from the seq it had reached.
	TypeRunStart = "run_start"
	// TypeTextDelta is a piece of the model's text.
	TypeTextDelta = "text_delta"
	// TypeToolCall says a tool call began to run.
	TypeToolCall = "tool_call"
	// TypeToolResult says a tool call finished.
	TypeToolResult = "tool_result"
	// TypeTurnDone says a turn (one model call and its tools) is complete and
	// in the conversation; it carries the turn's record.
	TypeTurnDone = "turn_done"
	// TypeRunDone ends a run that answered.
	TypeRunDone = "run_done"
	// TypeRunError ends a run that failed, or an execution of it that
	// stopped (Error.Code CodeRunInterrupted, CodeShutdown, CodeTimeout).
	TypeRunError = "run_error"
	// TypeRetrying says the model failed in a way worth retrying and the run
	// tries again after DelayMS, as attempt Attempt (2 for the first retry).
	// It does not end the run. The turn in progress is dropped: discard its
	// text deltas; the attempt starts that turn again.
	TypeRetrying = "retrying"
	// TypeRewind says the run goes on from an earlier point than its events
	// so far: the process running it stopped (it crashed, or was killed)
	// before it kept its last turns, and the run, resumed, does them again.
	// Only the first Turns turns stand: discard the events of the later
	// ones, and of the turn that was in progress. It does not end the run.
	TypeRewind = "rewind"
)

// Event is one event of a run on the wire: the JSON of an SSE "data:" line.
// Every event has the header fields (v, type, id, seq, run_id, at); the
// others depend on Type and are written only for that type. Decoding into
// Event works for every type and ignores unknown fields.
//
// A run's events are: one run_start; then per turn its text_delta events, a
// tool_call and a tool_result per tool call (each call's tool_call first),
// and one turn_done; and last exactly one run_done or run_error.
//
// Every event but text_delta is stored: Seq numbers the stored events from 1
// with no gaps, and such an event's ID is its Seq ("12"). A text_delta is
// only streamed: it has the Seq of the stored event before it, and the ID
// "12.3", counting from 1 after it. IDs order a run's events (see
// [CompareIDs]) and are the same from every server, so a client resumes with
// the last ID it has (the SSE Last-Event-ID) from any of them. A client that
// missed text_delta events (it resumed after the server let them go) has the
// turn's whole reply in its turn_done.
type Event struct {
	V     int       `json:"v"`
	Type  string    `json:"type"`
	ID    string    `json:"id"`
	Seq   int       `json:"seq"`
	RunID string    `json:"run_id"`
	At    time.Time `json:"at"`

	// run_start
	ConversationID string        `json:"conversation_id,omitempty"`
	Prompt         *TimedMessage `json:"prompt,omitempty"`

	// text_delta, tool_call, tool_result, turn_done: the turn, from 1.
	Turn int `json:"turn,omitempty"`
	// text_delta: the index of the text block in the reply, and the text.
	Block int    `json:"block,omitempty"`
	Text  string `json:"text,omitempty"`
	// tool_call, tool_result: the index of the call in its turn, and its ID.
	Call      int    `json:"call,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	// tool_call: the tool and its JSON input.
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// tool_result: what the model is shown, whether it is an error, and how
	// long the call ran.
	Content    string  `json:"content,omitempty"`
	IsError    bool    `json:"is_error,omitempty"`
	DurationMS float64 `json:"duration_ms,omitempty"`

	// turn_done
	Record *TurnRecord `json:"record,omitempty"`

	// run_done: the answer (the text of the final message) and the message.
	Answer string       `json:"answer,omitempty"`
	Final  *llm.Message `json:"final,omitempty"`
	// run_done, run_error: totals of the run; Turns counts model calls
	// (in run_error it can include a call whose turn did not complete).
	Usage *llm.Usage `json:"usage,omitempty"`
	Turns int        `json:"turns,omitempty"`

	// run_error, retrying
	Error *Error `json:"error,omitempty"`

	// retrying: the attempt about to be made, and how long until it.
	Attempt int     `json:"attempt,omitempty"`
	DelayMS float64 `json:"delay_ms,omitempty"`
}

// UnmarshalJSON decodes any event, ignoring unknown fields; its final message
// is decoded leniently, like every message on the wire (see [TimedMessage]).
// Like encoding/json, it merges into e (decode into a zero Event), except that
// Final is always replaced.
func (e *Event) UnmarshalJSON(data []byte) error {
	type plain Event // without methods, so decoding it does not recurse
	var w struct {
		*plain
		Final json.RawMessage `json:"final"` // shadows plain.Final
	}
	w.plain = (*plain)(e)
	e.Final = nil
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	if len(w.Final) == 0 || string(w.Final) == "null" {
		return nil
	}
	m, err := llm.UnmarshalMessageLenient(w.Final)
	if err != nil {
		return err
	}
	e.Final = &m
	return nil
}

// header is the part of every event.
type header struct {
	V     int       `json:"v"`
	Type  string    `json:"type"`
	ID    string    `json:"id"`
	Seq   int       `json:"seq"`
	RunID string    `json:"run_id"`
	At    time.Time `json:"at"`
}

// MarshalJSON writes the header and the fields of e's type, zero values
// included, so that "call": 0 or "is_error": false are explicit.
func (e Event) MarshalJSON() ([]byte, error) {
	h := header{V: e.V, Type: e.Type, ID: e.ID, Seq: e.Seq, RunID: e.RunID, At: e.At}
	switch e.Type {
	case TypeRunStart:
		return json.Marshal(struct {
			header
			ConversationID string        `json:"conversation_id"`
			Prompt         *TimedMessage `json:"prompt,omitempty"`
		}{h, e.ConversationID, e.Prompt})
	case TypeTextDelta:
		return json.Marshal(struct {
			header
			Turn  int    `json:"turn"`
			Block int    `json:"block"`
			Text  string `json:"text"`
		}{h, e.Turn, e.Block, e.Text})
	case TypeToolCall:
		return json.Marshal(struct {
			header
			Turn      int             `json:"turn"`
			Call      int             `json:"call"`
			ToolUseID string          `json:"tool_use_id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
		}{h, e.Turn, e.Call, e.ToolUseID, e.Name, e.Input})
	case TypeToolResult:
		return json.Marshal(struct {
			header
			Turn       int     `json:"turn"`
			Call       int     `json:"call"`
			ToolUseID  string  `json:"tool_use_id"`
			Content    string  `json:"content"`
			IsError    bool    `json:"is_error"`
			DurationMS float64 `json:"duration_ms"`
		}{h, e.Turn, e.Call, e.ToolUseID, e.Content, e.IsError, e.DurationMS})
	case TypeTurnDone:
		return json.Marshal(struct {
			header
			Turn   int         `json:"turn"`
			Record *TurnRecord `json:"record"`
		}{h, e.Turn, e.Record})
	case TypeRunDone:
		return json.Marshal(struct {
			header
			Answer string       `json:"answer"`
			Final  *llm.Message `json:"final"`
			Usage  *llm.Usage   `json:"usage"`
			Turns  int          `json:"turns"`
		}{h, e.Answer, e.Final, e.Usage, e.Turns})
	case TypeRunError:
		return json.Marshal(struct {
			header
			Error *Error     `json:"error"`
			Usage *llm.Usage `json:"usage"`
			Turns int        `json:"turns"`
		}{h, e.Error, e.Usage, e.Turns})
	case TypeRetrying:
		return json.Marshal(struct {
			header
			Error   *Error  `json:"error"`
			Attempt int     `json:"attempt"`
			DelayMS float64 `json:"delay_ms"`
		}{h, e.Error, e.Attempt, e.DelayMS})
	case TypeRewind:
		return json.Marshal(struct {
			header
			Turns int `json:"turns"`
		}{h, e.Turns})
	default:
		return json.Marshal(h)
	}
}

// TimedMessage is a message and when it was written. Message has
// [llm.Message]'s own JSON form: {"role": ..., "content": [blocks]}, every
// block with a "type"; opaque provider blocks (such as Claude's thinking)
// carry their data as a string.
type TimedMessage struct {
	At      time.Time   `json:"at"`
	Message llm.Message `json:"message"`
}

// UnmarshalJSON decodes the message leniently ([llm.UnmarshalMessageLenient]):
// a newer server may add fields to messages and blocks, and new block types,
// which come back as [llm.Unknown] blocks.
func (m *TimedMessage) UnmarshalJSON(data []byte) error {
	var w struct {
		At      time.Time       `json:"at"`
		Message json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	*m = TimedMessage{At: w.At}
	if len(w.Message) == 0 {
		return nil
	}
	msg, err := llm.UnmarshalMessageLenient(w.Message)
	if err != nil {
		return err
	}
	m.Message = msg
	return nil
}

// TurnRecord is the wire form of harness.TurnRecord: one model call and the
// tools it asked for.
type TurnRecord struct {
	Turn int `json:"turn"`
	// Messages are the messages the turn added to the conversation: the reply
	// and, if it called tools, the user message of results.
	Messages []TimedMessage `json:"messages"`
	// RequestedAt is when the model was called, FirstEventAt when its first
	// event arrived, and DoneAt when the reply was complete.
	RequestedAt  time.Time      `json:"requested_at"`
	FirstEventAt time.Time      `json:"first_event_at"`
	DoneAt       time.Time      `json:"done_at"`
	Usage        llm.Usage      `json:"usage"`
	Stop         llm.StopReason `json:"stop"`
	Tools        []ToolRecord   `json:"tools"`
}

// ToolRecord is the wire form of harness.ToolRecord.
type ToolRecord struct {
	ToolUseID  string    `json:"tool_use_id"`
	Name       string    `json:"name"`
	StartedAt  time.Time `json:"started_at"`
	DurationMS float64   `json:"duration_ms"`
	IsError    bool      `json:"is_error"`
}

// Record is the wire form of harness.Record: what a run did around its
// messages.
type Record struct {
	StartedAt  time.Time     `json:"started_at"`
	FinishedAt time.Time     `json:"finished_at"`
	Prompt     *TimedMessage `json:"prompt,omitempty"`
	Turns      []TurnRecord  `json:"turns"`
}

// Error is a typed error on the wire: in a run_error event, a failed [Run],
// or the body of an HTTP error response.
type Error struct {
	// Code is one of the Code constants. New codes may be added.
	Code string `json:"code"`
	// Message says what happened, for people. Do not parse it.
	Message string `json:"message"`
	// Retryable means the same request may succeed later.
	Retryable bool `json:"retryable,omitempty"`
	// Category and Explanation are the model's or provider's account of a
	// refusal (Code CodeRefused).
	Category    string `json:"category,omitempty"`
	Explanation string `json:"explanation,omitempty"`
	// RunID names the run an HTTP error is about: the unfinished run that
	// keeps a conversation busy (CodeBusy).
	RunID string `json:"run_id,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Error codes of a failed run.
const (
	CodeBudgetExceeded = "budget_exceeded"          // max turns or tokens reached; the conversation can go on
	CodeMaxTokens      = "max_tokens"               // the reply hit the output limit
	CodeRefused        = "refused"                  // the model declined; see Category, Explanation
	CodeUnexpectedStop = "unexpected_stop"          // the model's reply could not be acted on
	CodeCancelled      = "cancelled"                // the run was canceled (POST /v1/runs/{id}/cancel)
	CodeTimeout        = "timeout"                  // the run timeout passed
	CodeShutdown       = "shutdown"                 // the server shut down before the run finished
	CodeRateLimited    = "rate_limited"             // the provider throttled us (retryable)
	CodeOverloaded     = "overloaded"               // the provider could not serve (retryable)
	CodeModelAuth      = "model_auth"               // the server's model credentials were refused
	CodeContextTooLong = "context_too_long"         // the conversation no longer fits the model
	CodeModelRejected  = "model_rejected"           // the provider rejected the request
	CodeInternal       = "internal"                 // anything else; see the server log
	CodeInvalidRequest = "invalid_request"          // HTTP 400
	CodeUnauthorized   = "unauthorized"             // HTTP 401
	CodeNotFound       = "not_found"                // HTTP 404
	CodeTooLarge       = "too_large"                // HTTP 413
	CodeBusy           = "conversation_busy"        // HTTP 409: a run of this conversation is going on; RunID names it
	CodeInterrupted    = "conversation_interrupted" // no longer returned: a new message replaces an interrupted run
	CodeOverCapacity   = "over_capacity"            // HTTP 503 with Retry-After: too many runs
	CodeShuttingDown   = "shutting_down"            // HTTP 503: the server is draining
	// CodeRunInterrupted: the run was stopped (POST /v1/runs/{id}/stop, or
	// its client went away for longer than the reconnect grace). It stays
	// interrupted: resume it, or send a new message.
	CodeRunInterrupted = "interrupted"
	// CodeRunFailed: the run failed in an earlier execution, whose error
	// is no longer known in detail; Message is what was stored.
	CodeRunFailed = "run_failed"
	// CodeRejected: an approval of the run was rejected (a flow run).
	CodeRejected = "rejected"
	// CodeSuspended: the run waits for a decision (a flow run).
	CodeSuspended = "suspended"
	// CodeRunFinal ("run_final", HTTP 409): the run is completed, rejected
	// or canceled, and cannot be resumed or decided.
	CodeRunFinal = "run_final"
	// CodeRunLeased ("run_leased", HTTP 409): another process runs the run.
	CodeRunLeased = "run_leased"
	// CodeAwaitingDecision ("awaiting_decision", HTTP 409): the run waits for
	// POST /v1/runs/{id}/decisions, not a resume.
	CodeAwaitingDecision = "awaiting_decision"
	// CodeNotPending ("not_pending", HTTP 409): the run waits for no
	// decision at that path.
	CodeNotPending = "not_pending"
	// CodeShapeChanged ("shape_changed", HTTP 409): the flow's steps
	// changed since the run started; an operator decides.
	CodeShapeChanged = "shape_changed"
	// CodeAgentMismatch ("agent_mismatch", HTTP 409) is in catalog.go.
)

// Run statuses. A run is running, interrupted or suspended until it ends:
// done, failed, canceled or rejected. A failed run can still be resumed.
const (
	StatusRunning = "running"
	StatusDone    = "done"
	StatusFailed  = "failed"
	// StatusCanceled: canceled (POST /v1/runs/{id}/cancel, or replaced by a
	// new message after an interruption).
	StatusCanceled = "canceled"
	// StatusRejected: a flow run whose approval was rejected.
	StatusRejected = "rejected"
	// StatusSuspended: a flow run waiting for a decision.
	StatusSuspended = "suspended"
	// StatusInterrupted (shared with conversations): stopped, with nobody
	// running it; resume it or send a new message.
)

// Conversation statuses.
const (
	// StatusIdle: the conversation ends with an answer (or is empty); post a
	// message to go on.
	StatusIdle = "idle"
	// StatusInterrupted: a run stopped before answering; post with no message
	// (or POST /v1/runs/{id}/resume) to resume it, or a new message to start
	// over.
	StatusInterrupted = "interrupted"
	// StatusRunning (shared with runs): a run of the conversation is going on.
)

// Run is a run as GET /v1/runs/{id} returns it, and the response to a POST
// /v1/runs that asked for JSON.
type Run struct {
	V              int       `json:"v"`
	ID             string    `json:"id"`
	ConversationID string    `json:"conversation_id"`
	Agent          string    `json:"agent"` // the conversation's agent
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	// FinishedAt, Answer, Error and Record are set once the run has ended.
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Answer     string     `json:"answer,omitempty"`
	Usage      llm.Usage  `json:"usage"`
	Turns      int        `json:"turns"` // model calls so far
	Error      *Error     `json:"error,omitempty"`
	Record     *Record    `json:"record,omitempty"`
}

// Conversation is a conversation as GET /v1/conversations/{id} returns it.
type Conversation struct {
	V         int            `json:"v"`
	ID        string         `json:"id"`
	Agent     string         `json:"agent"` // the agent it belongs to
	Status    string         `json:"status"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	Messages  []TimedMessage `json:"messages"`
}

// RunList is the body of GET /v1/runs: a page of runs, newest first. The
// runs carry no Record.
type RunList struct {
	V    int   `json:"v"`
	Runs []Run `json:"runs"`
	// NextCursor, when set, fetches the next page (?cursor=). It is opaque.
	NextCursor string `json:"next_cursor,omitempty"`
}

// DecisionRequest is the body of POST /v1/runs/{id}/decisions: the answer to
// the approval at Path of a suspended flow run.
type DecisionRequest struct {
	Path     string `json:"path"`
	Approved bool   `json:"approved"`
	// Reason is kept with a rejection.
	Reason string `json:"reason,omitempty"`
}

// RunRequest is the body of POST /v1/runs.
type RunRequest struct {
	// Message is the user's message. It may be empty only to resume an
	// interrupted conversation.
	Message string `json:"message"`
	// ConversationID continues a conversation; empty starts a new one.
	ConversationID string `json:"conversation_id,omitempty"`
	// Agent names the agent of a new conversation (empty: the default
	// agent). A conversation stays with its agent: continuing it with another
	// is CodeAgentMismatch.
	Agent string `json:"agent,omitempty"`
}

// ErrorResponse is the body of every HTTP error.
type ErrorResponse struct {
	V     int    `json:"v"`
	Error *Error `json:"error"`
}

// CompareIDs compares two event IDs of one run by their order in the run:
// -1 if a comes before b, 0 if they are the same, +1 if a comes after b. An
// ID that is not one (see [ParseID]) comes before every ID.
func CompareIDs(a, b string) int {
	sa, na, oka := ParseID(a)
	sb, nb, okb := ParseID(b)
	switch {
	case !oka || !okb:
		return compareBool(oka, okb)
	case sa != sb:
		return compareUint(sa, sb)
	}
	return compareUint(na, nb)
}

// ParseID splits an event ID into the Seq of its stored event and the count
// of the text_delta after it (0 for a stored event): "12" is 12, 0 and
// "12.3" is 12, 3. ok is false for text that is not an ID.
func ParseID(id string) (seq, n uint64, ok bool) {
	s, d, dot := strings.Cut(id, ".")
	seq, err := parseDecimal(s)
	if err != nil {
		return 0, 0, false
	}
	if !dot {
		return seq, 0, true
	}
	n, err = parseDecimal(d)
	if err != nil || n == 0 {
		return 0, 0, false
	}
	return seq, n, true
}

func parseDecimal(s string) (uint64, error) {
	if s == "" || (len(s) > 1 && s[0] == '0') || s[0] < '0' || s[0] > '9' {
		return 0, strconv.ErrSyntax
	}
	return strconv.ParseUint(s, 10, 64)
}

func compareUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case b:
		return -1
	}
	return 1
}
