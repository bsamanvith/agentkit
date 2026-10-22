package agui

import (
	"encoding/json"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
)

// emitter sends one AG-UI run's events: RUN_STARTED first, whatever else is
// sent, and nothing after the run's last event.
type emitter struct {
	out *sseOut
	in  *RunAgentInput
	clk clock.Clock

	convID, runID  string // the conversation and run behind the AG-UI run, once known
	started, ended bool
}

// runError is an AG-UI run that fails: its RUN_ERROR's code and message.
type runError struct {
	code, msg string
}

func (e *runError) Error() string { return e.code + ": " + e.msg }

// The codes of the RUN_ERROR events the server makes up; a run that fails
// in the agent API has the API's code (wire.Code*).
const (
	// CodeInvalidInput: the request cannot be run (no user message, an
	// action on no thread, a resume that does not answer the interrupts).
	CodeInvalidInput = "invalid_input"
	// CodeUnsupported: the request asks for what the server does not do
	// (media parts, approving with edited arguments).
	CodeUnsupported = "unsupported"
	// CodePendingInterrupts: the thread's run waits for answers to its
	// interrupts, which a resume must give, before new input.
	CodePendingInterrupts = "pending_interrupts"
	// CodeUnknownInterrupt: a resume names an interrupt the thread's run
	// does not wait for (or no longer does).
	CodeUnknownInterrupt = "unknown_interrupt"
)

// bind names the conversation and run the AG-UI run maps to, which
// RUN_STARTED's metadata gives.
func (e *emitter) bind(convID, runID string) { e.convID, e.runID = convID, runID }

// start sends RUN_STARTED, once.
func (e *emitter) start() error {
	if e.started {
		return nil
	}
	e.started = true
	ev := Event{Type: EventRunStarted, ThreadID: e.in.ThreadID, RunID: e.in.RunID, ProtocolVersion: ProtocolVersion, Timestamp: e.clk.Now().UnixMilli()}
	if e.runID != "" {
		ev.Metadata = map[string]any{MetadataKey: map[string]string{"conversationId": e.convID, "runId": e.runID}}
	}
	return e.out.send(ev)
}

// send sends ev, after RUN_STARTED. Nothing goes out after the run's end.
func (e *emitter) send(ev Event) error {
	if e.ended {
		return nil
	}
	if err := e.start(); err != nil {
		return err
	}
	if ev.Type == EventRunFinished || ev.Type == EventRunError {
		e.ended = true
	}
	return e.out.send(ev)
}

// finish ends the run with RUN_FINISHED and its outcome.
func (e *emitter) finish(at time.Time, out *Outcome, result json.RawMessage, usage []TokenUsage) error {
	return e.send(Event{Type: EventRunFinished, Timestamp: stamp(at, e.clk), ThreadID: e.in.ThreadID, RunID: e.in.RunID, Outcome: out, Result: result, Usage: usage})
}

// fail ends the run with RUN_ERROR.
func (e *emitter) fail(re *runError) error {
	return e.send(Event{Type: EventRunError, Timestamp: e.clk.Now().UnixMilli(), Code: re.code, Message: re.msg})
}

// stamp is the timestamp of an event that happened at t (now if zero).
func stamp(t time.Time, clk clock.Clock) int64 {
	if t.IsZero() {
		t = clk.Now()
	}
	return t.UnixMilli()
}

func ptr[T any](v T) *T { return &v }

// jsonString is s as a JSON string.
func jsonString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}
