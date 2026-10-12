package store

import (
	"context"
	"encoding/json"
	"errors"
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
	// ErrInvalid means an argument cannot be stored or used: an entry or run
	// that breaks the rules on its type, an empty id or owner, a lease time to
	// live that is not positive, or a page with a bad cursor or seek.
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
)

// Entry is one position of a conversation's log.
//
// Rules, enforced by Append ([ErrInvalid] otherwise): Seq is at least 1; Kind
// is one of the kinds above; Messages has at least one message and each one
// marshals to JSON; Record is empty or valid JSON; Covers is Seq-1 for a
// compaction and 0 for any other kind. A compaction thus summarizes everything
// before it: one built from a context that has grown since conflicts at its
// Seq instead of hiding the newer entries.
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
// status: it is a running run whose lease has expired.
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
	// [ErrUnfinished].
	Create(ctx context.Context, id, conversationID string) (Run, error)
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
	// ListResumable returns up to limit runs that should continue at now,
	// ordered by CreatedAt and then ID: running runs whose lease has expired
	// (or that have none), and failed runs that are Retryable with
	// NextAttemptAt at or before now. Limit is read like [Page.Limit].
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

// Admin is the one read that crosses tenants. It exists for the runner's
// recovery loop, which starts with no request and no tenant and must find the
// interrupted runs of every tenant; it is never given to code that serves
// requests. The loop resumes each run it finds with that run's tenant in ctx.
//
// A backend implements Admin on a separate value that its store returns from
// an Admin method (as package memory does), never on the value that
// implements [RunStore]: code holding a tenant-scoped store must not be able
// to type-assert its way to other tenants' runs. That value has no other
// method. Implementations must be safe for concurrent use,
// must honor ctx, and must not keep or share memory with values returned.
// Run storetest.RunAdmin against every implementation.
type Admin interface {
	// ListResumableAll is [RunStore.ListResumable] across every tenant: up
	// to limit runs that should continue at now, by the same rule and with
	// limit read the same way, each with its tenant. Runs are ordered by
	// CreatedAt, then tenant id (compared as bytes), then run ID. It needs
	// no tenant in ctx, and ignores one that is there.
	ListResumableAll(ctx context.Context, now time.Time, limit int) ([]TenantRun, error)
}
