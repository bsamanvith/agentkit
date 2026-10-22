package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// MessageKind says what a [Message] is for.
type MessageKind string

// The kinds of message.
const (
	// MessageRequest asks its recipient for an answer: the recipient's run
	// on it ends with an answer, which goes back as a reply.
	MessageRequest MessageKind = "request"
	// MessageReply answers the request InReplyTo names.
	MessageReply MessageKind = "reply"
	// MessageNotice tells its recipient something and expects nothing back,
	// such as that its thread was stopped. A stopped thread takes notices
	// only.
	MessageNotice MessageKind = "notice"
)

// Valid reports whether k is one of the kinds.
func (k MessageKind) Valid() bool {
	return k == MessageRequest || k == MessageReply || k == MessageNotice
}

// MessageState says where a [Message] stands in its processing.
type MessageState string

// The states of a message. Done is final.
const (
	// MessageQueued: not processed yet. Whoever processes it claims it
	// ([MessageLog.ClaimMessage]); it is due again at NextAttemptAt after a
	// try that could not process it (its recipient was busy).
	MessageQueued MessageState = "queued"
	// MessageRunning: processed, by a run of its recipient (RunID) whose end
	// is still to be acted on, such as sending its answer back.
	MessageRunning MessageState = "running"
	// MessageDone: processed, and nothing is left to do about it.
	MessageDone MessageState = "done"
)

// Valid reports whether s is one of the states.
func (s MessageState) Valid() bool {
	return s == MessageQueued || s == MessageRunning || s == MessageDone
}

// Message is one message between agents (T073), in a thread of a tenant's
// message log. Its ID is its idempotency key: it is processed once, however
// often it is delivered.
//
// Rules, enforced by [MessageLog.SendMessage] ([ErrInvalid] otherwise): ID,
// ThreadID and ToConversation are ids (not empty, text); From and To are not
// empty and are text, as are FromConversation, Text and InReplyTo; Kind is one
// of the kinds; Data is empty or valid JSON in UTF-8; a reply's InReplyTo
// names a message of the same thread, and only a reply has one.
type Message struct {
	ID       string
	ThreadID string
	// From and To are the sender's and the recipient's addresses
	// ("agent:<name>"). The store does not interpret them.
	From, To string
	// FromConversation is the sender's conversation, where a reply goes
	// back; empty when the sender has none. ToConversation is where the
	// recipient processes the message.
	FromConversation, ToConversation string
	Kind                             MessageKind
	Text                             string
	// Data is JSON beside the text, opaque to the store and kept byte for
	// byte. Empty is read back as nil.
	Data      json.RawMessage
	InReplyTo string

	// The fields below are the store's, ignored by SendMessage.

	// Hop is the message's position in its thread, from 1, dense: the
	// number of messages the thread had, plus one, when it was sent.
	Hop int
	// State is queued until the message is processed.
	State MessageState
	// RunID is the run that processed the message, once there is one.
	RunID string
	// Attempts counts the claims of the message: its deliveries.
	Attempts int
	// CreatedAt is when it was sent, DeliveredAt when it was first claimed,
	// ProcessedAt when it left queued; zero until then.
	CreatedAt, DeliveredAt, ProcessedAt time.Time
	// NextAttemptAt is when a message that is not done is due again; zero
	// is at once.
	NextAttemptAt time.Time
	// Lease is the processor's lease as last written; it may have expired.
	Lease Lease
}

// ThreadStatus says whether a [Thread] takes messages.
type ThreadStatus string

// The statuses of a thread.
const (
	// ThreadOpen takes messages.
	ThreadOpen ThreadStatus = "open"
	// ThreadStopped was stopped (over a limit, or by an operator) and takes
	// notices only. It never opens again.
	ThreadStopped ThreadStatus = "stopped"
)

// Thread is a conversation between agents: the messages with its ID, in
// order. It is created by its first message.
type Thread struct {
	ID     string
	Status ThreadStatus
	// StopCode and StopReason say why a stopped thread was stopped: a code
	// for programs and a sentence for people. Both are text.
	StopCode, StopReason string
	// Hops is the number of messages in the thread.
	Hops int
	// Tokens is the thread's usage so far: what [MessageUpdate.Tokens]
	// added.
	Tokens int64
	// CreatedAt is when its first message was sent, UpdatedAt when it last
	// changed.
	CreatedAt, UpdatedAt time.Time
}

// ThreadPage is one page of [MessageLog.ListThreads].
type ThreadPage struct {
	Threads []Thread
	// Next is the cursor of the following page, or empty if this page
	// reached the end. A page with a Next holds exactly the page's limit.
	Next string
}

// MessageUpdate is what [MessageLog.UpdateMessage] records about a message
// its caller has claimed.
type MessageUpdate struct {
	// State is the message's state after the update. queued and running are
	// due again at NextAttemptAt; a message never goes back to queued once
	// it has left it.
	State MessageState
	// RunID records the run that processed the message. Empty keeps the
	// stored one; another run than the stored one is [ErrConflict].
	RunID string
	// NextAttemptAt is when the message is due again (queued or running);
	// zero is at once.
	NextAttemptAt time.Time
	// Tokens are added to the thread's Tokens; not negative.
	Tokens int64
}

// MessageLog keeps the messages between a tenant's agents (T073), in
// threads. Delivery is at least once: whoever processes a message claims it
// with a lease, and a claim whose holder stopped expires; processing is once
// per message ID, which the processor checks against what it did before.
// Messages of a thread to one conversation are processed in Hop order
// ([MessageAdmin.ClaimDueMessage]).
//
// Implementations must be safe for concurrent use, must honor ctx, must scope
// every call by the tenant in ctx, and must not keep or share memory with
// values passed in or returned. Run storetest.RunMessageLog against every
// implementation.
type MessageLog interface {
	// SendMessage appends m to its thread, creating the thread with it,
	// and returns it as stored: queued, with the next Hop. Sending a
	// message whose ID the tenant has returns the stored one when it is the
	// same message (every field the rules on [Message] name, Data byte for
	// byte), so a retry is safe, and fails with [ErrConflict] when it is
	// not; this is checked first. A stopped thread takes notices only
	// ([ErrFinal] for any other kind). A message that breaks the rules is
	// [ErrInvalid].
	SendMessage(ctx context.Context, m Message) (Message, error)
	// GetMessage returns the message, or [ErrNotFound].
	GetMessage(ctx context.Context, id string) (Message, error)
	// ThreadMessages returns up to limit messages of the thread with a Hop
	// greater than after, in Hop order. limit is read as [Page.Limit] is;
	// a negative limit or after is [ErrInvalid]. A thread that does not
	// exist has none.
	ThreadMessages(ctx context.Context, threadID string, after, limit int) ([]Message, error)
	// GetThread returns the thread, or [ErrNotFound].
	GetThread(ctx context.Context, id string) (Thread, error)
	// ListThreads returns one page of the tenant's threads, ordered by
	// CreatedAt and then ID, as [RunStore.List] pages runs (FromSeq is
	// [ErrInvalid]).
	ListThreads(ctx context.Context, p Page) (ThreadPage, error)
	// StopThread stops the thread with a code and a reason, both text and
	// the code not empty ([ErrInvalid]), and returns it. Stopping a stopped
	// thread keeps its first code and reason. A thread that does not exist
	// is [ErrNotFound].
	StopThread(ctx context.Context, id, code, reason string) (Thread, error)
	// ClaimMessage gives owner the message's lease for ttl from the store's
	// clock, counts the attempt (setting DeliveredAt on the first), and
	// returns the message. It fails with [ErrNotFound] for no such message,
	// [ErrFinal] for one that is done, and [ErrLeased] while another owner
	// holds an unexpired lease; owner and ttl are checked as [Leaser.Claim]
	// checks them ([ErrInvalid]). Claiming a message owner holds renews it.
	ClaimMessage(ctx context.Context, id, owner string, ttl time.Duration) (Message, error)
	// UpdateMessage records u on the message as owner, who must hold its
	// unexpired lease ([ErrLeased] if another owner does, [ErrLeaseLost] if
	// nobody does), releases the lease, adds u.Tokens to the thread and
	// returns the message. ProcessedAt is set when the message leaves
	// queued. It fails with [ErrNotFound] for no such message, [ErrFinal]
	// for one that is done, [ErrConflict] for another RunID than the
	// stored one, and [ErrInvalid] for a state that is none of the states,
	// a move back to queued, negative Tokens or a RunID that is not text.
	UpdateMessage(ctx context.Context, id, owner string, u MessageUpdate) (Message, error)
}

// TenantMessage is a message with its tenant, as
// [MessageAdmin.ClaimDueMessage] returns it.
type TenantMessage struct {
	Tenant  tenancy.Tenant
	Message Message
}

// MessageAdmin finds the messages due across tenants, for the process that
// delivers them, which starts with no tenant (T073). Like [Admin], a backend
// implements it on its Admin value only, never on its store.
//
// Implementations must be safe for concurrent use and must honor ctx. Run
// storetest.RunMessageLog against every implementation.
type MessageAdmin interface {
	// ClaimDueMessage claims for owner, for ttl from the store's clock, the
	// next message that is due, as [MessageLog.ClaimMessage] claims one, and
	// returns it with its tenant. A message is due when it is not done,
	// nobody holds its lease, its NextAttemptAt is at or before now (or
	// zero), and, while it is queued, no message of its thread to the same
	// ToConversation with a lower Hop is queued. Of those it takes the
	// earliest NextAttemptAt (zero first), then the earliest CreatedAt, then
	// the lowest tenant id and message ID (compared as bytes). It fails
	// with [ErrNotFound] when none is due. Callers claiming at once never
	// get the same one. It needs no tenant in ctx, and ignores one that is
	// there.
	ClaimDueMessage(ctx context.Context, owner string, ttl time.Duration) (TenantMessage, error)
}
