package store

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// Errors returned by stores, checked with [errors.Is]. A context with no tenant
// is reported as [tenancy.ErrNoTenant] and a done context as the context's own
// error; flow checkpoints that do not exist are [flow.ErrNotFound].
var (
	// ErrNotFound means a run does not exist (in the caller's tenant).
	ErrNotFound = errors.New("not found")
	// ErrInvalid means an argument cannot be stored or used: an entry, event
	// or run that breaks the rules on its type, an empty id or owner, text
	// that is not UTF-8 or holds a NUL byte (see Text in the package
	// documentation), a lease time to live that is not positive, a time to
	// keep outside 1678 to 2262 (see Times), or a page with a bad cursor or
	// seek.
	ErrInvalid = errors.New("invalid argument")
	// ErrConflict means the stored state is not what the caller expected:
	// another entry at the same Seq of a conversation, a run with the same id
	// in another conversation, or a run that is not in the status a
	// Transition starts from. Reload and decide again; never retry as is.
	ErrConflict = errors.New("conflicts with the stored value")
	// ErrGap means an entry's Seq is past the next free position of its
	// conversation. Entries are numbered densely from 1.
	ErrGap = errors.New("sequence number past the end of the log")
	// ErrLeased means another owner holds an unexpired lease on the run.
	ErrLeased = errors.New("run is leased by another owner")
	// ErrLeaseLost means the caller does not hold the run's lease: it never
	// claimed it, or its lease expired or was released. Claim again, and
	// reload what others may have written in between.
	ErrLeaseLost = errors.New("lease not held")
	// ErrFinal means the run has a final status (completed, rejected or
	// canceled): it never changes again and cannot be claimed or renewed.
	ErrFinal = errors.New("run is final")
	// ErrUnfinished means the conversation already has an unfinished run:
	// finish or cancel it before creating another.
	ErrUnfinished = errors.New("conversation has an unfinished run")
)

// Kind says what an [Entry] holds.
type Kind string

// The kinds of entry.
const (
	// KindPrompt is the message that starts a run.
	KindPrompt Kind = "prompt"
	// KindTurn is one finished turn of a run: the assistant's reply and the
	// tool results it led to.
	KindTurn Kind = "turn"
	// KindCompaction is a summary that replaces everything before it in the
	// context. Its Covers is the last sequence number it summarizes.
	KindCompaction Kind = "compaction"
	// KindInterrupt is a turn that waits for people: the assistant's reply
	// whose tool calls wait for an answer or an approval, with the rest kept
	// in Record by the writer. It is not part of the context: the turn entry
	// that finishes it holds the reply again with its results. Like a
	// compaction, it adds nothing to [Conversation.Messages].
	KindInterrupt Kind = "interrupt"
)

// Entry is one position of a conversation's log.
//
// Rules, enforced by Append ([ErrInvalid] otherwise): Seq is at least 1; Kind
// is one of the kinds above; Messages has at least one message and each one
// marshals to JSON and has no [llm.Unknown] block; Record is empty or valid
// JSON in UTF-8; RunID is text (see Text in the package documentation);
// Covers is Seq-1 for a compaction and 0 for any other kind. A compaction thus
// summarizes everything before it: one built from a context that has grown
// since conflicts at its Seq instead of hiding the newer entries.
type Entry struct {
	// Seq is the entry's position, from 1, chosen by the writer: the
	// [ContextView.Seq] it read plus one.
	Seq int64
	// Kind says what the entry holds.
	Kind Kind
	// Messages are stored as the JSON [llm.Message] marshals to and decoded
	// from those exact bytes on every read, never re-encoded.
	Messages []llm.Message
	// Record holds what sits beside the messages: times, usage and tool-call
	// timings. It is JSON, opaque to the store and kept byte for byte. Empty
	// is read back as nil.
	Record json.RawMessage
	// RunID is the run that wrote the entry. It is a label: the store does not
	// check that the run exists. It may be empty.
	RunID string
	// Covers is, for a compaction, the last sequence number it summarizes.
	Covers int64
	// CreatedAt is set by the store when the entry is first written; the
	// value passed to Append is ignored. It never decreases along the log, so
	// History can seek by time.
	CreatedAt time.Time
}

// Order is the direction of a page.
type Order int

// The orders of a page.
const (
	// Ascending reads the oldest first.
	Ascending Order = iota
	// Descending reads the newest first.
	Descending
)

// Page limits.
const (
	// DefaultPageLimit is the page size when Page.Limit is 0.
	DefaultPageLimit = 100
	// MaxPageLimit is the largest page; a larger Limit is lowered to it.
	MaxPageLimit = 1000
)

// Page asks for one page of a list.
//
// The first page starts at the beginning of the list in the given order, or
// where FromSeq or FromTime says (at most one of them). Later pages pass the
// cursor of the page before, with the same Order and no seek. A cursor is
// opaque, and valid only for the backend, conversation and order that issued
// it; a cursor the backend cannot read is [ErrInvalid].
type Page struct {
	// Cursor is the Next of the previous page, or empty for the first page.
	Cursor string
	// Limit is the most items to return: 0 means [DefaultPageLimit], and
	// anything above [MaxPageLimit] means MaxPageLimit. Negative is
	// [ErrInvalid].
	Limit int
	// Order is the direction.
	Order Order
	// FromSeq starts History at this sequence number: the first page holds
	// the entries from it onwards (Ascending) or from it backwards
	// (Descending). Zero means unset. RunStore.List does not support it.
	FromSeq int64
	// FromTime starts the first page at the first entry or run created at or
	// after it (Ascending), or the last one created at or before it
	// (Descending). The zero time means unset.
	FromTime time.Time
}

// EntryPage is one page of [ConversationStore.History].
type EntryPage struct {
	Entries []Entry
	// Next is the cursor of the following page, or empty if this page reached
	// the end of the log as it was when read. A page with a Next holds exactly
	// the page's limit of entries.
	Next string
}

// ContextView is what [ConversationStore.Context] returns: the part of the log
// that goes back into a request.
type ContextView struct {
	// Entries are the latest compaction and every entry after it, in order,
	// or the whole log if there is no compaction.
	Entries []Entry
	// Seq is the last sequence number of the log when it was read, 0 for an
	// empty conversation. The next entry goes at Seq+1, and a cache of the
	// context is brought up to date by reading History from Seq+1.
	Seq int64
}

// ConversationStore keeps the logs of conversations. Implementations must be
// safe for concurrent use, must honor ctx, must scope every call by the tenant
// in ctx, and must not keep or share memory with values passed in or returned.
// Run storetest.RunConversationStore against every implementation.
//
// A conversation exists once it has an entry; reading one that has none gives
// an empty page or view, not an error.
type ConversationStore interface {
	// Append writes e at e.Seq of conversation id if that position is free.
	// If it is taken by an identical entry (same Kind, Messages JSON, Record
	// bytes, RunID and Covers; CreatedAt is not compared), Append does
	// nothing and returns nil, so a retry is safe. If it is taken by a
	// different entry the error is [ErrConflict]; if e.Seq is past the next
	// free position it is [ErrGap]; an entry that breaks the rules on [Entry]
	// is [ErrInvalid].
	Append(ctx context.Context, id string, e Entry) error
	// AppendBatch writes entries to conversation id in one write, all or
	// none, by Append's rules for each: a position taken by an identical
	// entry is left as it is, so a retry of a whole or partly stored batch is
	// safe; a different entry at a taken position is [ErrConflict]; a first
	// Seq past the next free position is [ErrGap]. Their Seqs must be
	// consecutive ([ErrInvalid] otherwise), and every entry follow the rules
	// on [Entry]. No entries is a no-op, once ctx's tenant and id have been
	// checked.
	AppendBatch(ctx context.Context, id string, entries []Entry) error
	// History returns one page of the conversation's entries. Paging in either
	// order visits every entry that existed at the first page exactly once,
	// and never one twice, however many are appended between pages; in
	// Ascending order the appended ones are visited too.
	History(ctx context.Context, id string, p Page) (EntryPage, error)
	// Context returns the latest compaction and every entry after it, or the
	// whole log if there is no compaction, read at one point in time.
	Context(ctx context.Context, id string) (ContextView, error)
}

// Status says where a run stands.
type Status string

// The statuses of a run. A run is created running. "Interrupted" is not a
// status: it is a running run whose lease is not held (see [Run.Interrupted]).
const (
	// StatusRunning: a process is running it, or was until it crashed.
	StatusRunning Status = "running"
	// StatusSuspended: waiting for an approval; Run.Pending holds the paths.
	StatusSuspended Status = "suspended"
	// StatusFailed: stopped by an error. It continues if Retryable once
	// NextAttemptAt is due, or when an operator resumes it.
	StatusFailed Status = "failed"
	// StatusCompleted, StatusRejected and StatusCanceled are final.
	StatusCompleted Status = "completed"
	StatusRejected  Status = "rejected"
	StatusCanceled  Status = "canceled"
)

// Final reports whether s never changes again.
func (s Status) Final() bool {
	return s == StatusCompleted || s == StatusRejected || s == StatusCanceled
}

// CanMoveTo reports whether a run may move from s to to:
//
//	running   → suspended, failed, completed, rejected, canceled
//	suspended → running, rejected, canceled
//	failed    → running, canceled
//
// A final status moves nowhere.
func (s Status) CanMoveTo(to Status) bool {
	switch s {
	case StatusRunning:
		return to == StatusSuspended || to == StatusFailed || to.Final()
	case StatusSuspended:
		return to == StatusRunning || to == StatusRejected || to == StatusCanceled
	case StatusFailed:
		return to == StatusRunning || to == StatusCanceled
	}
	return false
}

// Run is the stored record of one run.
type Run struct {
	ID             string
	ConversationID string
	Status         Status
	// Usage is the run's total so far: the sum of every Transition's
	// Details.Usage.
	Usage llm.Usage
	// Error describes why the run failed, was rejected or was canceled.
	Error string
	// Retryable and NextAttemptAt are set for a failed run that should be
	// tried again automatically, at or after NextAttemptAt.
	Retryable     bool
	NextAttemptAt time.Time
	// Attempts counts the times the run has failed.
	Attempts int
	// Pending lists the step paths a suspended run waits for, sorted.
	Pending []string
	// CreatedAt is set by the store when the run is created, and FinishedAt
	// when it reaches a final status (zero until then).
	CreatedAt  time.Time
	FinishedAt time.Time
	// Lease is the run's lease as last written; it may have expired. The zero
	// Lease means nobody has claimed the run, or the last owner released it.
	Lease Lease
	// Interactive is set for a run created with [Interactive]: a chat run
	// with a user on the other end. It never changes. An interactive run is
	// never resumable ([RunStore.ListResumable]): when its process stops
	// running it, it is interrupted, and only its user continues it.
	Interactive bool
	// Lane is the queue the run waits in, for [Admin.ClaimNext]: a name,
	// "" for the default lane. It is set by [WithLane] and never changes.
	Lane string
	// Priority orders the runs of a lane: a higher one is claimed first. It
	// is set by [WithPriority] (default 0) and never changes.
	Priority int
	// StartAt is the time before which the run is neither resumable nor
	// claimed: a run scheduled for later. It is set by [WithStartAt], to
	// CreatedAt by default, and never changes.
	StartAt time.Time
}

// Due returns the time from which a background run that is running, or
// failed and retryable, may be taken up ([Admin.ClaimNext]): the latest of
// its StartAt, its lease's expiry (while it has a lease) and its
// NextAttemptAt (set only on a failed run). A running run that nobody
// leases is due at StartAt; one whose lease is held is due when the lease
// expires.
func (r Run) Due() time.Time {
	due := r.StartAt
	for _, t := range []time.Time{r.Lease.Expires, r.NextAttemptAt} {
		if t.After(due) {
			due = t
		}
	}
	return due
}

// Interrupted reports whether r is an interactive run that nobody is running
// at now: running, with its lease released or expired. A run whose process
// crashed shows as interrupted once its lease expires.
func (r Run) Interrupted(now time.Time) bool {
	return r.Interactive && r.Status == StatusRunning && (r.Lease.Owner == "" || !now.Before(r.Lease.Expires))
}

// CreateOption sets a property of a run [RunStore.Create] stores with it.
type CreateOption func(*CreateOptions)

// CreateOptions are the properties [CreateOption]s set. Backends read them
// with [NewCreateOptions].
type CreateOptions struct {
	// Interactive is set by [Interactive].
	Interactive bool
	// Lane, Priority and StartAt are set by [WithLane], [WithPriority] and
	// [WithStartAt]. The zero StartAt means the run's CreatedAt.
	Lane     string
	Priority int
	StartAt  time.Time
	// LeaseOwner and LeaseTTL are set by [WithLease]; an empty owner means
	// the run is created unclaimed.
	LeaseOwner string
	LeaseTTL   time.Duration
}

// The range of [Run.Priority]: what a 32-bit integer holds. Create refuses
// one outside it with [ErrInvalid].
const (
	MinPriority = math.MinInt32
	MaxPriority = math.MaxInt32
)

// NewCreateOptions applies opts in order.
func NewCreateOptions(opts ...CreateOption) CreateOptions {
	var o CreateOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	return o
}

// Interactive creates the run as interactive ([Run.Interactive]): a chat run
// that stops with its user and is never listed as resumable. Runs created
// without it are background runs.
func Interactive() CreateOption {
	return func(o *CreateOptions) { o.Interactive = true }
}

// WithLane creates the run in the named lane ([Run.Lane]); runners claim
// from the lanes they serve ([Admin.ClaimNext]). The name is text (see Text
// in the package documentation); "" is the default lane.
func WithLane(lane string) CreateOption {
	return func(o *CreateOptions) { o.Lane = lane }
}

// WithPriority creates the run with priority p ([Run.Priority]): of the due
// runs of a lane, the highest priority is claimed first. p must be within
// [MinPriority, MaxPriority].
func WithPriority(p int) CreateOption {
	return func(o *CreateOptions) { o.Priority = p }
}

// WithStartAt creates the run due at t ([Run.StartAt]) instead of at once:
// it is not resumable, and not claimed, before t. A time the store cannot
// keep (see Times in the package documentation) is [ErrInvalid].
func WithStartAt(t time.Time) CreateOption {
	return func(o *CreateOptions) { o.StartAt = t }
}

// WithLease creates the run claimed by owner for ttl from the store's clock,
// in the same write: nobody can claim it ([Leaser.Claim],
// [Admin.ClaimNext]) between its creation and its creator's claim. owner and
// ttl are checked as Claim checks them. A retry of the Create returns the
// run with its lease as stored, and claims nothing.
func WithLease(owner string, ttl time.Duration) CreateOption {
	return func(o *CreateOptions) { o.LeaseOwner, o.LeaseTTL = owner, ttl }
}

// Details is what a [RunStore.Transition] records with the new status. A field
// that does not apply to the new status must be zero ([ErrInvalid] otherwise).
type Details struct {
	// Usage is what the run spent since its last transition; it is added to
	// Run.Usage. Any status.
	Usage llm.Usage
	// Error says why: for failed, rejected and canceled.
	Error string
	// Retryable and NextAttemptAt: for failed. NextAttemptAt needs Retryable.
	Retryable     bool
	NextAttemptAt time.Time
	// Pending: for suspended, where it is required.
	Pending []string
}

// Lease says who may run a run until when. It is held while the backend's
// clock reads before Expires; from Expires on, anyone can claim it.
type Lease struct {
	Owner   string
	Expires time.Time
}

// RunPage is one page of [RunStore.List].
type RunPage struct {
	Runs []Run
	// Next is the cursor of the following page, or empty if this page reached
	// the end. A page with a Next holds exactly the page's limit of runs.
	Next string
}

// Leaser gives one owner at a time the right to run a run. Leases protect
// against two processes running the same run; they are not locks on the
// conversation log, whose writes are safe on their own (see Append).
//
// Times come from the backend's injected clock. An owner is any non-empty
// label (a process id, say); ttl must be positive. All three methods return
// [ErrNotFound] for a run that does not exist.
type Leaser interface {
	// Claim gives owner the lease for ttl from now, if nobody else holds it
	// (it was never claimed, was released, or has expired). Claiming a lease
	// the owner already holds extends it, so a retry is safe. It fails with
	// [ErrLeased] while another owner's lease is unexpired, and with
	// [ErrFinal] for a final run. Any other status can be claimed: a suspended
	// or failed run is claimed before it is resumed.
	Claim(ctx context.Context, runID, owner string, ttl time.Duration) (Lease, error)
	// Renew extends owner's unexpired lease to ttl from now. It fails with
	// [ErrLeased] if another owner holds the lease, with [ErrLeaseLost] if
	// owner's lease has expired or nobody holds it, and with [ErrFinal] for
	// a final run.
	Renew(ctx context.Context, runID, owner string, ttl time.Duration) (Lease, error)
	// Release gives up owner's lease, expired or not. Releasing a lease
	// nobody holds is a no-op; releasing another owner's unexpired lease
	// fails with [ErrLeased].
	Release(ctx context.Context, runID, owner string) error
}

// RunStore keeps the records and leases of runs. Implementations must be safe
// for concurrent use, must honor ctx, must scope every call by the tenant in
// ctx, and must not keep or share memory with values passed in or returned.
// Run storetest.RunRunStore against every implementation.
type RunStore interface {
	// Create stores a new running run in conversation conversationID and
	// returns it. Creating a run that exists in the same conversation returns
	// the stored run, so a retry is safe; one that exists in another
	// conversation is [ErrConflict]. A conversation has at most one
	// unfinished run: creating another while one is not final is
	// [ErrUnfinished]. opts set properties stored with the run
	// ([Interactive], [WithLane], [WithPriority], [WithStartAt],
	// [WithLease]; a lane that is not text, a priority out of range, a
	// start time the store cannot keep or a lease Claim would refuse is
	// [ErrInvalid]); a retry returns the run as first stored, whatever its
	// opts.
	Create(ctx context.Context, id, conversationID string, opts ...CreateOption) (Run, error)
	// Get returns the run, or [ErrNotFound].
	Get(ctx context.Context, id string) (Run, error)
	// List returns one page of the runs of a conversation, ordered by
	// CreatedAt and then ID. Paging visits every run that existed when the
	// first page was read exactly once. FromSeq is [ErrInvalid] here.
	List(ctx context.Context, conversationID string, p Page) (RunPage, error)
	// Transition moves the run from status from to status to, recording d,
	// and returns the run as stored. It fails, checked in this order, with
	// [ErrNotFound]; with [ErrFinal] if the run is final, whatever from and
	// to; with [ErrInvalid] for a move [Status.CanMoveTo] does not allow or
	// details that do not fit to; and with [ErrConflict] if the run's status
	// is not from, so that of two processes moving a run, one wins. It does
	// not check or change the lease.
	Transition(ctx context.Context, id string, from, to Status, d Details) (Run, error)
	// ListResumable returns up to limit background runs that should
	// continue at now, ordered by CreatedAt and then ID: running runs whose
	// lease has expired (or that have none), and failed runs that are
	// Retryable with NextAttemptAt at or before now; in both cases only
	// runs whose StartAt is at or before now. An interactive run
	// ([Run.Interactive]) is never listed. Limit is read like [Page.Limit].
	ListResumable(ctx context.Context, now time.Time, limit int) ([]Run, error)
	// ListSuspended returns one page of the suspended runs, ordered by
	// CreatedAt and then ID, like List.
	ListSuspended(ctx context.Context, p Page) (RunPage, error)
	// Unfinished returns the conversation's run that is not final, or
	// [ErrNotFound] if it has none.
	Unfinished(ctx context.Context, conversationID string) (Run, error)
	Leaser
}

// TenantRun is a run with the tenant it belongs to, as [Admin] lists it.
type TenantRun struct {
	Tenant tenancy.Tenant
	Run    Run
}

// Admin is the one view that crosses tenants. It exists for the runner's
// dispatcher (its recovery loop), which starts with no request and no tenant
// and must find the due runs of every tenant; it is never given to code that
// serves requests. The dispatcher resumes each run it claims with that run's
// tenant in ctx.
//
// A backend implements Admin on a separate value with no other method,
// returned by a package-level function that takes its store (as package
// memory's Admin does). The store must have no method that implements Admin or
// returns one: code holding a tenant-scoped store must not be able to
// type-assert its way to other tenants' runs. It can only get there by
// asserting to the backend's own type, which needs an import of the backend
// that request-handling code has no reason to make.
//
// Implementations must be safe for concurrent use, must honor ctx, and must
// not keep or share memory with values returned. Run storetest.RunAdmin
// against every implementation.
type Admin interface {
	// ListResumableAll is [RunStore.ListResumable] across every tenant: up
	// to limit runs that should continue at now, by the same rule and with
	// limit read the same way, each with its tenant. Runs are ordered by
	// CreatedAt, then tenant id (compared as bytes), then run ID. It needs
	// no tenant in ctx, and ignores one that is there.
	ListResumableAll(ctx context.Context, now time.Time, limit int) ([]TenantRun, error)
	// ClaimNext claims for owner, for ttl from the store's clock, the next
	// run of the given lanes that is due, and returns it as claimed, with
	// its tenant. The runs it considers are the background runs (never an
	// interactive one) that are running, or failed and Retryable, with
	// [Run.Due] at or before now: so a run whose lease is held, or whose
	// StartAt or NextAttemptAt is still to come, is never claimed. Of those
	// it takes the one with the highest Priority, then the earliest Due,
	// then the lowest tenant id and run ID (compared as bytes), whichever
	// of the lanes it is in. It fails with [ErrNotFound] when no run of the
	// lanes is due. Callers claiming at once never get the same run, and
	// on Postgres never wait on each other's choice (SKIP LOCKED).
	//
	// lanes must not be empty and each must be text (see Text in the
	// package documentation); owner and ttl are checked as [Leaser.Claim]
	// checks them ([ErrInvalid]). It needs no tenant in ctx, and ignores
	// one that is there.
	ClaimNext(ctx context.Context, lanes []string, owner string, ttl time.Duration) (TenantRun, error)
}

// Event is one position of a run's event log.
//
// Rules, enforced by AppendEvents ([ErrInvalid] otherwise): Seq is at least 1;
// Type is not empty and is text (see Text in the package documentation); Data
// is empty or valid JSON in UTF-8.
type Event struct {
	// Seq is the event's position in its run's log, from 1, chosen by the
	// writer: the [EventLog.LastEventSeq] it read plus one, then counting on.
	Seq int64
	// Type names the event. The store does not interpret it.
	Type string
	// Data is the event's JSON, opaque to the store and kept byte for byte.
	// Empty is read back as nil.
	Data json.RawMessage
	// CreatedAt is set by the store when the event is first written; the
	// value passed to AppendEvents is ignored. It never decreases along a
	// run's log.
	CreatedAt time.Time
}

// EventLog keeps the durable events of runs, numbered densely from 1 per run,
// so a client that watches a run can reconnect and read what it missed. The
// run id is a label: the store does not check that the run exists.
//
// Implementations must be safe for concurrent use, must honor ctx, must scope
// every call by the tenant in ctx, and must not keep or share memory with
// values passed in or returned. Run storetest.RunEventLog against every
// implementation.
type EventLog interface {
	// AppendEvents writes events to run runID's log, all or none. Their Seqs
	// must be consecutive ([ErrInvalid] otherwise). Each position that is
	// already taken must hold an identical event (same Type and Data bytes;
	// CreatedAt is not compared), and is left as it is, so a retry of a
	// whole or partly stored batch is safe; a different event at a taken
	// position is [ErrConflict]. A first Seq past the next free position is
	// [ErrGap]. An event that breaks the rules on [Event] is [ErrInvalid].
	// No events is a no-op, once ctx's tenant and runID have been checked.
	AppendEvents(ctx context.Context, runID string, events []Event) error
	// EventsAfter returns up to limit events of the run with a Seq greater
	// than after, in order. limit is read as [Page.Limit] is; a negative
	// limit or after is [ErrInvalid]. Fewer than limit events means the end
	// of the log as it was when read. A run with no events gives none.
	EventsAfter(ctx context.Context, runID string, after int64, limit int) ([]Event, error)
	// LastEventSeq returns the Seq of the run's last event, or 0 if it has
	// none.
	LastEventSeq(ctx context.Context, runID string) (int64, error)
}

// Command is one message to a run while it runs: a steering message, an
// interrupt's answer, a cancel. It is stored before it is signalled, so a
// process that takes the run over after a crash still finds it (E18). The
// runner holding the run's lease consumes commands at turn boundaries.
//
// Rules, enforced by SendCommand ([ErrInvalid] otherwise): Key and Type are
// not empty and are text (see Text in the package documentation); Data is
// empty or valid JSON in UTF-8.
type Command struct {
	// Seq is the command's position among its run's commands, from 1, set by
	// the store; the value passed to SendCommand is ignored.
	Seq int64
	// Key is the sender's idempotency key, unique per run: sending a command
	// whose Key the run already has returns the stored one.
	Key string
	// Type names the command. The store does not interpret it.
	Type string
	// Data is the command's JSON, opaque to the store and kept byte for byte.
	// Empty is read back as nil.
	Data json.RawMessage
	// CreatedAt is set by the store when the command is sent. It never
	// decreases along a run's commands.
	CreatedAt time.Time
}

// CommandLog keeps the commands sent to runs, numbered densely from 1 per
// run. Unlike [EventLog], it knows the runs of the [RunStore] it is part of:
// a command goes only to a run that exists and is not final, and only the
// run's lease holder consumes it, each checked in the same write as the
// command.
//
// Implementations must be safe for concurrent use, must honor ctx, must scope
// every call by the tenant in ctx, and must not keep or share memory with
// values passed in or returned. Run storetest.RunCommandLog against every
// implementation.
type CommandLog interface {
	// SendCommand stores c as the run's next command and returns it as
	// stored. If the run already has a command with c.Key, it returns that
	// one when its Type and Data bytes are the same as c's, so a retry is
	// safe, and fails with [ErrConflict] when they differ; this is checked
	// first, so a retry still succeeds once the run is final. Otherwise it
	// fails with [ErrNotFound] for a run that does not exist and [ErrFinal]
	// for a final one. A command that breaks the rules on [Command] is
	// [ErrInvalid].
	SendCommand(ctx context.Context, runID string, c Command) (Command, error)
	// PendingCommands returns up to limit of the run's commands that are not
	// consumed, in order. limit is read as [Page.Limit] is. A run with no
	// commands, or that does not exist, gives none.
	PendingCommands(ctx context.Context, runID string, limit int) ([]Command, error)
	// ConsumeCommands marks the run's commands up to and including Seq
	// through as consumed, so that PendingCommands no longer returns them,
	// as owner, who must hold the run's unexpired lease:
	// it fails with [ErrLeased] if another owner holds it, [ErrLeaseLost] if
	// nobody does, [ErrFinal] for a final run and [ErrNotFound] for one that
	// does not exist. Consuming a consumed command again is a no-op, so a
	// retry is safe. through must be at least 1 and at most the run's last
	// command ([ErrInvalid] otherwise).
	ConsumeCommands(ctx context.Context, runID, owner string, through int64) error
}
