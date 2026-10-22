package wire

import (
	"encoding/json"
	"time"
)

// Thread statuses.
const (
	ThreadOpen    = "open"    // takes messages
	ThreadStopped = "stopped" // over a limit (Error says which); takes none
)

// Message kinds.
const (
	MessageRequest = "request" // asks its recipient for an answer, which comes back as a reply
	MessageReply   = "reply"   // answers the request in_reply_to names
	MessageNotice  = "notice"  // tells, expects nothing back (a stopped thread)
)

// Message states.
const (
	MessageQueued  = "queued"  // not processed yet: its recipient was busy, or nobody took it up yet
	MessageRunning = "running" // a run of its recipient (run_id) is answering it
	MessageDone    = "done"    // processed
)

// Thread is a thread of messages between agents (T073), as GET
// /v1/threads/{id} shows it (with its messages) and GET /v1/threads lists it
// (without).
type Thread struct {
	V      int    `json:"v"`
	ID     string `json:"id"`
	Status string `json:"status"`
	// Error says why a stopped thread was stopped.
	Error *Error `json:"error,omitempty"`
	// Hops is the number of messages in the thread.
	Hops int `json:"hops"`
	// Tokens is what the runs the thread started used so far.
	Tokens    int64     `json:"tokens"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Messages are the thread's messages in order (GET /v1/threads/{id}
	// only).
	Messages []ThreadMessage `json:"messages,omitempty"`
}

// ThreadMessage is one message of a [Thread].
type ThreadMessage struct {
	ID  string `json:"id"`
	Hop int    `json:"hop"`
	// From and To are addresses: "agent:<name>", or "agentkit" for the
	// notices the server sends.
	From string `json:"from"`
	To   string `json:"to"`
	// FromConversation and ToConversation are where the sender sent it
	// from and where its recipient processes it.
	FromConversation string          `json:"from_conversation,omitempty"`
	ToConversation   string          `json:"to_conversation"`
	Kind             string          `json:"kind"`
	Text             string          `json:"text"`
	Data             json.RawMessage `json:"data,omitempty"`
	InReplyTo        string          `json:"in_reply_to,omitempty"`
	State            string          `json:"state"`
	// RunID is the run of its recipient that processed it.
	RunID       string     `json:"run_id,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
	ProcessedAt *time.Time `json:"processed_at,omitempty"`
}

// ThreadList is the answer of GET /v1/threads, newest first.
type ThreadList struct {
	V       int      `json:"v"`
	Threads []Thread `json:"threads"`
	// NextCursor, when set, fetches the next page (?cursor=). It is opaque.
	NextCursor string `json:"next_cursor,omitempty"`
}
