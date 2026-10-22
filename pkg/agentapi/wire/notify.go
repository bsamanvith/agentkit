package wire

import "time"

// User event types: the events of a caller's notification stream (GET
// /v1/events, T090).
const (
	// TypeRunStatus: one of the caller's runs changed status. It is running
	// once an execution of it starts (a task started, resumed or taken over
	// after a crash), and when that execution stops, in the status it left
	// the run in: done, failed, canceled, rejected, interrupted, paused, or
	// suspended (waiting for input or a decision).
	TypeRunStatus = "run_status"
	// TypeConversationUpdated: one of the caller's runs added to its
	// conversation: sent after the run_status of an execution that
	// stopped, unless the run goes on (a task whose process stopped, which
	// another takes up).
	TypeConversationUpdated = "conversation_updated"
	// TypeResync: notifications were lost (the stream's short history no
	// longer has those after Last-Event-ID, or the bus lost some): refetch
	// what the client shows. Its id is where the stream goes on from.
	TypeResync = "resync"
)

// UserEvent is one event of the caller's notification stream (GET
// /v1/events): the JSON of an SSE data line, whose event name is its type
// and whose id is its ID. A notification is a hint to refetch, not a record:
// the stream keeps a short history (an hour) for a client that reconnects
// with Last-Event-ID, and says resync when that is not enough.
//
// The stream is the caller's, in the caller's tenant: it has the
// notifications of the runs started for the caller's user (a request
// authenticated as that user, or a task or message it started), and a
// caller with no user (an open server, or a credential that names none) has
// those of the runs started for no user. Nobody else's are ever on it.
//
// IDs are "<unix ms>" or "<unix ms>.<n>": the time the notification was
// sent, and n telling apart those of one millisecond. They order one
// stream's notifications.
type UserEvent struct {
	V    int       `json:"v"`
	Type string    `json:"type"`
	ID   string    `json:"id"`
	At   time.Time `json:"at"`

	// run_status and conversation_updated.
	RunID          string `json:"run_id,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	// run_status: the run's status (a Run's), whether it is a task, and the
	// error code it stopped with, if any.
	Status     string `json:"status,omitempty"`
	Background bool   `json:"background,omitempty"`
	Code       string `json:"code,omitempty"`
}
