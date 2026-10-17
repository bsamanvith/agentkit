package harness

import (
	"errors"
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/llm"
)

// Errors returned by this package, checked with [errors.Is]. Errors from a
// run arrive wrapped in a [*RunError].
var (
	// ErrInvalidConfig means [New] was given a missing model or registry, or an
	// option with an unusable value.
	ErrInvalidConfig = errors.New("invalid harness configuration")
	// ErrInvalidInput means an [Input] or [Transcript] cannot be run: a
	// malformed history, a missing or unexpected new message.
	ErrInvalidInput = errors.New("invalid input")
	// ErrBudgetExceeded means the run used up its turns or tokens. The
	// transcript ends after a whole turn and can be resumed with a bigger budget.
	ErrBudgetExceeded = errors.New("budget exceeded")
	// ErrMaxTokens means the model stopped because it reached its output limit.
	ErrMaxTokens = errors.New("model reached its output token limit")
	// ErrRefused means the model declined to answer; see [RefusalError].
	ErrRefused = errors.New("model refused")
	// ErrUnexpectedStop means the model's reply cannot be acted on: it asked for
	// tools without naming any, stopped for a reason with no neutral meaning,
	// repeated a tool call ID, or its stream ended without a final message.
	ErrUnexpectedStop = errors.New("unexpected stop")
)

// RefusalError carries the details of a refusal. It matches [ErrRefused].
type RefusalError struct {
	// Category is the provider's classification of the refusal.
	Category string
	// Explanation is the model's or provider's account of it.
	Explanation string
}

func (e *RefusalError) Error() string {
	msg := ErrRefused.Error()
	if e.Category != "" {
		msg += " (" + e.Category + ")"
	}
	if e.Explanation != "" {
		msg += ": " + e.Explanation
	}
	return msg
}
func (e *RefusalError) Unwrap() error { return ErrRefused }

// RunError is the error of a run. It wraps the cause, and keeps what the run
// had done when it stopped, so the work is not lost: save the transcript and
// resume later.
type RunError struct {
	// Err is the cause: one of the sentinels above, the context's error, or an
	// error from the model or a tool.
	Err error

	transcript Transcript
	usage      llm.Usage
	turns      int
	record     Record
}

func (e *RunError) Error() string { return "harness: " + e.Err.Error() }
func (e *RunError) Unwrap() error { return e.Err }

// Transcript returns the messages of the completed turns, ready to be saved or
// passed to a later run. It is a copy.
func (e *RunError) Transcript() Transcript { return e.transcript.Clone() }

// Usage returns the tokens used by the run, including those of a turn that
// got a reply but did not complete (so it is in no [TurnRecord]): billing from
// events alone undercounts such a run, this does not.
func (e *RunError) Usage() llm.Usage { return e.usage }

// Turns returns how many model calls the run made.
func (e *RunError) Turns() int { return e.turns }

// Record returns what the run recorded before it stopped. It is a copy.
func (e *RunError) Record() Record { return e.record.clone() }

// result is the partial [Result] of a failed run.
func (e *RunError) result() Result {
	res := Result{Transcript: e.Transcript(), Usage: e.usage, Turns: e.turns, Record: e.Record()}
	if n := len(res.Transcript); n > 0 && res.Transcript[n-1].Role == llm.RoleAssistant {
		res.Final = res.Transcript[n-1] // Transcript() is already a copy
	}
	return res
}

func invalidInput(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidInput, fmt.Sprintf(format, args...))
}

// NewRunError returns the error of a run that stopped with err, having done
// what partial says (its Transcript, Usage, Turns and Record; Final is
// derived from the transcript). It is for an agent that runs a harness
// somewhere else and reports the failure here, such as a remote runner
// (package runnerrpc): the caller sees the same [*RunError] as from a local
// run.
func NewRunError(err error, partial Result) *RunError {
	return &RunError{
		Err:        err,
		transcript: partial.Transcript.Clone(),
		usage:      partial.Usage,
		turns:      partial.Turns,
		record:     partial.Record.clone(),
	}
}

// Partial returns what the run had done when it stopped, as a [Result]: the
// form [NewRunError] takes back.
func (e *RunError) Partial() Result { return e.result() }
