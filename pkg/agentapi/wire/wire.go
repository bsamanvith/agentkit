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
	// TypeToolPolicy says what the run's tool policy decided about a call,
	// when it did not plainly allow it: Action flag (the call runs), approve
	// or deny (it does not: its tool_result is the error the model was
	// shown), with the policy, rule and reason. It comes between the call's
	// tool_call and tool_result. An approve in a run that can wait for people
	// (interaction interactive or supervised) is followed by the call's
	// interrupt; in an autonomous run, it refuses the call like deny, saying
	// so.
	TypeToolPolicy = "tool_policy"
	// TypeToolFlagged says a call the policy flagged is about to run. It
	// follows the call's tool_policy, with the same fields: a client that
	// notifies on flagged calls listens for it alone.
	TypeToolFlagged = "tool_flagged"
	// TypeInterrupt says a tool call waits for people: a question the tool
	// asks, or an approval the run's tool policy wants (Interrupt.Kind). It
	// comes after the call's tool_call (and tool_policy), in place of its
	// tool_result. Once the turn's calls are done, the run is suspended
	// (run_error with CodeSuspended) until every interrupt of the turn has
	// its answer (POST /v1/runs/{id}/input). Then the run resumes, and the
	// turn's remaining events (the answered calls' tool_call and
	// tool_result, turn_done) follow, as part of the same turn.
	TypeInterrupt = "interrupt"
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
	// TypeUI carries one A2UI message (v0.9) the agent showed the person:
	// the surface it is about (SurfaceID) and the message (UI), checked
	// against the catalog of package a2ui. An agent with UI on (an Agent's
	// UI is "a2ui") shows UI with its ui_render tool; each message of a
	// successful call is a ui event, in the call's order, right after the
	// call's tool_result and before the turn's turn_done. A client keeps a
	// surface's components and data model as the spec says and draws them;
	// the person's action on it goes back as a RunRequest's UIAction.
	TypeUI = "ui"
)

// Event is one event of a run on the wire: the JSON of an SSE "data:" line.
// Every event has the header fields (v, type, id, seq, run_id, at); the
// others depend on Type and are written only for that type. Decoding into
// Event works for every type and ignores unknown fields.
//
// A run's events are: one run_start; then per turn its text_delta events, a
// tool_call and a tool_result per tool call (each call's tool_call first,
// and its tool_policy and tool_flagged, if any, in between), and one
// turn_done; and last exactly one run_done or run_error. A call that waits
// for people has an interrupt where its tool_result would be; the execution
// ends there, suspended, and the run's events go on from it once answered.
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
	// tool_call, tool_policy, tool_flagged: the tool; tool_call: its JSON
	// input.
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// tool_result: what the model is shown, whether it is an error, and how
	// long the call ran.
	Content    string  `json:"content,omitempty"`
	IsError    bool    `json:"is_error,omitempty"`
	DurationMS float64 `json:"duration_ms,omitempty"`

	// tool_policy, tool_flagged: what the policy decided (allow, flag,
	// approve or deny), which policy and rule decided ("default" for the
	// policy's default), why, and the tool's class as the policy saw it
	// (an unclassified tool is write/medium).
	Action   string `json:"action,omitempty"`
	Policy   string `json:"policy,omitempty"`
	Rule     string `json:"rule,omitempty"`
	Reason   string `json:"reason,omitempty"`
	ToolKind string `json:"tool_kind,omitempty"`
	ToolRisk string `json:"tool_risk,omitempty"`

	// interrupt: what the call waits for.
	Interrupt *Interrupt `json:"interrupt,omitempty"`

	// ui: the surface, and the A2UI message about it (JSON object).
	SurfaceID string          `json:"surface_id,omitempty"`
	UI        json.RawMessage `json:"message,omitempty"`

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
	case TypeToolPolicy, TypeToolFlagged:
		return json.Marshal(struct {
			header
			Turn      int    `json:"turn"`
			Call      int    `json:"call"`
			ToolUseID string `json:"tool_use_id"`
			Name      string `json:"name"`
			Action    string `json:"action"`
			Policy    string `json:"policy"`
			Rule      string `json:"rule"`
			Reason    string `json:"reason"`
			ToolKind  string `json:"tool_kind"`
			ToolRisk  string `json:"tool_risk"`
		}{h, e.Turn, e.Call, e.ToolUseID, e.Name, e.Action, e.Policy, e.Rule, e.Reason, e.ToolKind, e.ToolRisk})
	case TypeInterrupt:
		return json.Marshal(struct {
			header
			Turn      int        `json:"turn"`
			Call      int        `json:"call"`
			ToolUseID string     `json:"tool_use_id"`
			Name      string     `json:"name"`
			Interrupt *Interrupt `json:"interrupt"`
		}{h, e.Turn, e.Call, e.ToolUseID, e.Name, e.Interrupt})
	case TypeTurnDone:
		return json.Marshal(struct {
			header
			Turn   int         `json:"turn"`
			Record *TurnRecord `json:"record"`
		}{h, e.Turn, e.Record})
	case TypeUI:
		return json.Marshal(struct {
			header
			Turn      int             `json:"turn"`
			Call      int             `json:"call"`
			ToolUseID string          `json:"tool_use_id"`
			SurfaceID string          `json:"surface_id"`
			UI        json.RawMessage `json:"message"`
		}{h, e.Turn, e.Call, e.ToolUseID, e.SurfaceID, e.UI})
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
	CodeForbidden      = "forbidden"                // HTTP 403: the credential lacks the scope
	CodeNotFound       = "not_found"                // HTTP 404
	CodeTooLarge       = "too_large"                // HTTP 413
	CodeBusy           = "conversation_busy"        // HTTP 409: a run of this conversation is going on; RunID names it
	CodeInterrupted    = "conversation_interrupted" // no longer returned: a new message replaces an interrupted run
	CodeOverCapacity   = "over_capacity"            // HTTP 503 with Retry-After: too many runs
	CodeShuttingDown   = "shutting_down"            // HTTP 503: the server is draining
	// Codes of the toolsets added at run time.
	CodeToolsetExists     = "toolset_exists"     // HTTP 409: a toolset has that name
	CodeConflict          = "conflict"           // HTTP 409: it changed since it was read; read it again
	CodeAuthRequired      = "auth_required"      // HTTP 422: the MCP server wants a credential
	CodeServerUnreachable = "server_unreachable" // HTTP 422: the MCP server could not be used
	// CodeRunInterrupted: the run was stopped (POST /v1/runs/{id}/stop, or
	// its client went away for longer than the reconnect grace). It stays
	// interrupted: resume it, or send a new message.
	CodeRunInterrupted = "interrupted"
	// CodeRunFailed: the run failed in an earlier execution, whose error
	// is no longer known in detail; Message is what was stored.
	CodeRunFailed = "run_failed"
	// CodeRejected: an approval of the run was rejected (a flow run).
	CodeRejected = "rejected"
	// CodeSuspended: the run waits for a decision (a flow run) or for input
	// (a chat run whose tool calls wait for people: see [TypeInterrupt]).
	CodeSuspended = "suspended"
	// CodeRunFinal ("run_final", HTTP 409): the run is completed, rejected
	// or canceled, and cannot be resumed or decided.
	CodeRunFinal = "run_final"
	// CodeRunLeased ("run_leased", HTTP 409): another process runs the run.
	CodeRunLeased = "run_leased"
	// CodeAwaitingDecision ("awaiting_decision", HTTP 409): the run waits for
	// POST /v1/runs/{id}/decisions (a flow run) or POST /v1/runs/{id}/input
	// (a chat run), not a resume.
	CodeAwaitingDecision = "awaiting_decision"
	// CodeNotPending ("not_pending", HTTP 409): the run waits for no
	// decision at that path, or for no answer to that interrupt (it has a
	// different answer already, or the run is no longer suspended).
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
	// StatusSuspended: a flow run waiting for a decision, or a chat run
	// waiting for input (its Interrupts).
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
	V              int    `json:"v"`
	ID             string `json:"id"`
	ConversationID string `json:"conversation_id"`
	Agent          string `json:"agent"` // the conversation's agent
	// Pool is the runner pool the run was placed in: where its agent runs.
	// Empty for a run nobody placed (from before pools, or a flow run).
	Pool      string    `json:"pool,omitempty"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	// FinishedAt, Answer, Error and Record are set once the run has ended.
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Answer     string     `json:"answer,omitempty"`
	Usage      llm.Usage  `json:"usage"`
	Turns      int        `json:"turns"` // model calls so far
	Error      *Error     `json:"error,omitempty"`
	Record     *Record    `json:"record,omitempty"`
	// Interrupts are what a suspended chat run waits for: the interrupts of
	// its pending turn, each with its answer once it has one. The run
	// resumes when they all have one.
	Interrupts []Interrupt `json:"interrupts,omitempty"`
	// Background is set for a task (POST /v1/tasks): a run that goes on
	// whoever watches it.
	Background bool `json:"background,omitempty"`
	// Notify is the run's webhook and its delivery, for a run that has one
	// (GET /v1/runs/{id} only).
	Notify *Notification `json:"notify,omitempty"`
}

// Interrupt kinds.
const (
	// InterruptQuestion: the tool asks people a question (Prompt), whose
	// answer is text, one of Choices if it has any.
	InterruptQuestion = "question"
	// InterruptApproval: the run's tool policy wants people to approve the
	// call (Input) before it runs; Prompt is the rule's reason.
	InterruptApproval = "approval"
)

// Interrupt is a tool call that waits for people.
type Interrupt struct {
	// ID is the call's tool_use_id: what an [Answer] names.
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Name is the tool's name.
	Name    string   `json:"name"`
	Prompt  string   `json:"prompt"`
	Choices []string `json:"choices,omitempty"`
	// Input is an approval's call input: exactly what runs once approved.
	Input json.RawMessage `json:"input,omitempty"`
	// Policy and Rule are what wants an approval.
	Policy string `json:"policy,omitempty"`
	Rule   string `json:"rule,omitempty"`
	// Answer is the answer given so far, in a run's Interrupts.
	Answer *Answer `json:"answer,omitempty"`
}

// Answer answers an [Interrupt]: Text for a question; Approved, and a Reason
// for a denial, for an approval.
type Answer struct {
	ID       string `json:"id"`
	Text     string `json:"text,omitempty"`
	Approved bool   `json:"approved,omitempty"`
	Reason   string `json:"reason,omitempty"`
	// By is who answered, as the server authenticated them. The server
	// ignores it in a request.
	By string `json:"by,omitempty"`
}

// InputRequest is the body of POST /v1/runs/{id}/input: answers to
// interrupts of a suspended chat run. Answers may come one request at a
// time. The same answer again is accepted (a retry is safe); a different
// answer to an interrupt that has one is CodeNotPending.
type InputRequest struct {
	Answers []Answer `json:"answers"`
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
	// ToolPolicy narrows the agent's tool policy for this run: a preset's
	// name ("read_only", "low_risk_writes", "approve_writes", "all") or a
	// policy object ({"rules": [...], "default": "deny"}). Each tool call
	// gets the stricter of the two decisions, so it can only narrow. The run
	// keeps it when it is resumed; a resume (no message) cannot set one.
	ToolPolicy json.RawMessage `json:"tool_policy,omitempty"`
	// Interaction narrows the agent's interaction mode for this run: whether
	// its tool calls may wait for people. "interactive" and "supervised"
	// wait (an ask_user call, a call the policy wants approved); "autonomous"
	// never does: ask_user is not offered, and a call that wants an approval
	// is refused. The stricter of the agent's mode and the run's wins. The
	// run keeps it when it is resumed.
	Interaction string `json:"interaction,omitempty"`
	// UIAction, in place of Message, says what the person did on a surface
	// the agent showed (a ui event): the next user message is the action,
	// as structured text the agent reads. It continues a conversation
	// (ConversationID) whose agent has UI on.
	UIAction *UIAction `json:"ui_action,omitempty"`
}

// UIAction is a person's action on an A2UI surface: a button's action
// event, with its context's bindings resolved to the values chosen.
type UIAction struct {
	// SurfaceID is the surface, and ComponentID the component (the button)
	// whose action it is.
	SurfaceID   string `json:"surface_id"`
	ComponentID string `json:"component_id"`
	// Action is the action event's name.
	Action string `json:"action"`
	// Context is the event's context, resolved: a JSON object of the
	// values. Absent is {}.
	Context json.RawMessage `json:"context,omitempty"`
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
