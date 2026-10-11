package flow

// EventKind says what happened to a step.
type EventKind string

// The kinds of [Event].
const (
	// EventStarted is emitted when a step begins. Steps skipped on resume
	// emit nothing.
	EventStarted EventKind = "started"
	// EventFinished is emitted when a step has completed and its checkpoint
	// is saved.
	EventFinished EventKind = "finished"
	// EventFailed is emitted when a step ends with an error, including a
	// rejected approval or a cancelled context.
	EventFailed EventKind = "failed"
	// EventSuspended is emitted when a step ends waiting for an approval.
	EventSuspended EventKind = "suspended"
	// EventWarning is emitted by [Resume], before any step runs, when it
	// resumes although the step tree may differ from the one that produced the
	// checkpoint: Err matches [ErrNoShape] (an old checkpoint with no
	// fingerprint) or [ErrShapeChanged] ([WithAllowShapeChange] was given).
	// Path is the root step.
	EventWarning EventKind = "warning"
)

// Event reports one step's progress. Every kind of step emits the same type,
// so logs, UIs and audit trails see one stream.
//
// A combinator emits its own events around those of its children, so a
// failure shows up on the failing step and on each enclosing step.
type Event struct {
	Kind EventKind
	// Path identifies the step, for example "review/gather[1]/search".
	Path string
	// Err is set for [EventFailed], [EventSuspended] and [EventWarning].
	Err error
}
