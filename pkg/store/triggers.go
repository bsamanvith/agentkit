package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// MaxTriggerBytes is the largest [TriggerRecord.Data] a store keeps.
const MaxTriggerBytes = 64 << 10

// MaxFirings is how many firings a store keeps per trigger: starting one
// more removes the oldest beyond it.
const MaxFirings = 200

// TriggerRecord is a trigger (T072): what starts background runs without a
// person, on a schedule or when a webhook is called. The store keeps its
// definition as JSON it does not interpret, and the few fields it selects
// on: whether it is paused, when it is next due, and its webhook's ID.
//
// Rules, enforced by PutTrigger ([ErrInvalid] otherwise): ID matches
// ^[a-zA-Z0-9_-]{1,64}$; Data is valid JSON in UTF-8 (see Text in the package
// documentation), not empty, of at most [MaxTriggerBytes]; HookID is empty
// or matches ^[a-zA-Z0-9_-]{1,64}$; NextFireAt is a time the store can keep.
type TriggerRecord struct {
	// ID names the trigger, unique in its tenant.
	ID string
	// Data is the trigger's definition, opaque to the store and kept byte
	// for byte.
	Data json.RawMessage
	// HookID is the random ID of the trigger's inbound webhook URL, unique
	// across every tenant ([TriggerAdmin.TriggerByHook]); empty for a
	// trigger with no webhook.
	HookID string
	// Paused triggers are never due.
	Paused bool
	// NextFireAt is when the trigger's schedule is next due; zero for none
	// (a webhook trigger).
	NextFireAt time.Time
	// Version is set by the store: 1 when the trigger is created, one more
	// on each replace. The value passed to PutTrigger is ignored.
	Version int64
	// CreatedAt and UpdatedAt are set by the store.
	CreatedAt, UpdatedAt time.Time
}

// FiringState says where a [Firing] stands. All but pending are final.
type FiringState string

// The states of a firing.
const (
	// FiringPending: claimed, and its run not started yet.
	FiringPending FiringState = "pending"
	// FiringStarted: its run was started (RunID).
	FiringStarted FiringState = "started"
	// FiringSkipped: no run was started, on purpose (Reason): the trigger's
	// previous run was still going on, say.
	FiringSkipped FiringState = "skipped"
	// FiringFailed: its run could not be started (Reason). A firing that
	// failed may be started again under its ID.
	FiringFailed FiringState = "failed"
)

// Firing is one time a trigger fired: a schedule's due time, or a webhook's
// delivery. Every firing is recorded, whatever became of it.
//
// Rules, enforced by StartFiring ([ErrInvalid] otherwise): ID is not empty,
// is text and at most 128 bytes; ScheduledFor is a time the store can keep.
type Firing struct {
	// ID is the firer's idempotency key, unique per trigger: the schedule's
	// due time, or the webhook's delivery ID.
	ID string
	// Seq is set by the store: one more than the trigger's previous firing.
	// A firing started again gets a new one.
	Seq int64
	// ScheduledFor is the schedule's due time this firing is for; zero for
	// a webhook.
	ScheduledFor time.Time
	// At is set by the store when the firing is started.
	At time.Time
	// State is pending until EndFiring.
	State FiringState
	// RunID is the run it started, if any.
	RunID string
	// Reason says why it was skipped or failed. It is text.
	Reason string
	// EndedAt is set by the store when the firing ends; zero while pending.
	EndedAt time.Time
}

// FiringEnd is what becomes of a pending firing ([TriggerStore.EndFiring]).
type FiringEnd struct {
	// State is started, skipped or failed.
	State FiringState
	// RunID is the run it started; text.
	RunID string
	// Reason says why it was skipped or failed; text.
	Reason string
}

// TenantTrigger is a trigger with the tenant it belongs to, as
// [TriggerAdmin] returns it.
type TenantTrigger struct {
	Tenant  tenancy.Tenant
	Trigger TriggerRecord
}

// TriggerStore keeps a tenant's triggers and their firings (T072). Writes of
// a trigger are compare-and-swap on [TriggerRecord.Version], as toolsets'
// are. Firing is claimed through the store: of several processes starting
// the same firing, or the same due time of a schedule, one succeeds and the
// others get [ErrConflict], so each firing happens once however many
// replicas share the store.
//
// Implementations must be safe for concurrent use, must honor ctx, must scope
// every call by the tenant in ctx, and must not keep or share memory with
// values passed in or returned. Run storetest.RunTriggerStore against every
// implementation.
type TriggerStore interface {
	// PutTrigger creates the trigger when ifVersion is 0, failing with
	// [ErrConflict] if one has its ID, or replaces version ifVersion of it
	// (its Data, HookID, Paused and NextFireAt), failing with [ErrNotFound]
	// if there is none and [ErrConflict] if its version is another. A
	// HookID another trigger has, in any tenant, is [ErrConflict]. It
	// returns the trigger as stored. A record that breaks the rules on
	// [TriggerRecord], or a negative ifVersion, is [ErrInvalid].
	PutTrigger(ctx context.Context, t TriggerRecord, ifVersion int64) (TriggerRecord, error)
	// GetTrigger returns the trigger, or [ErrNotFound].
	GetTrigger(ctx context.Context, id string) (TriggerRecord, error)
	// ListTriggers returns every trigger of the tenant, sorted by ID.
	ListTriggers(ctx context.Context) ([]TriggerRecord, error)
	// DeleteTrigger removes the trigger and its firings: whatever its
	// version when ifVersion is 0, else only version ifVersion
	// ([ErrConflict] for another). A trigger that does not exist is
	// [ErrNotFound].
	DeleteTrigger(ctx context.Context, id string, ifVersion int64) error
	// StartFiring records f as a pending firing of trigger triggerID and
	// returns it as stored. f.State, Seq, At, RunID, Reason and EndedAt are
	// ignored. A firing with f.ID that exists is [ErrConflict], unless it
	// failed: then it is started again (pending, with a new Seq and At).
	//
	// With a due time, the firing is the schedule's: the trigger must be
	// due at exactly due and not paused ([ErrConflict] otherwise, so that
	// of two processes firing one due time, one wins), and its NextFireAt
	// becomes next, which must be zero or after due ([ErrInvalid]). With
	// the zero due, next must be zero too and the trigger's NextFireAt is
	// left as it is.
	//
	// It fails with [ErrNotFound] for a trigger that does not exist, and
	// with [ErrInvalid] for a firing that breaks the rules on [Firing]. It
	// keeps the newest [MaxFirings] firings of the trigger.
	StartFiring(ctx context.Context, triggerID string, f Firing, due, next time.Time) (Firing, error)
	// EndFiring moves the pending firing firingID of trigger triggerID to
	// end.State and returns it as stored. It fails with [ErrNotFound] for no
	// such firing, with [ErrFinal] for one that has ended, and with
	// [ErrInvalid] for a state that is not started, skipped or failed, or a
	// RunID or Reason that is not text.
	EndFiring(ctx context.Context, triggerID, firingID string, end FiringEnd) (Firing, error)
	// ListFirings returns up to limit of the trigger's firings, newest
	// (highest Seq) first; limit is read as [Page.Limit] is. A trigger that
	// does not exist is [ErrNotFound].
	ListFirings(ctx context.Context, triggerID string, limit int) ([]Firing, error)
}

// TriggerAdmin finds triggers across tenants, for the scheduler that fires
// them and the endpoint inbound webhooks are posted to, which know no
// tenant until they have found the trigger. Like [Admin], it needs no tenant
// in ctx and ignores one that is there. Run storetest.RunTriggerStore against
// every implementation, with its backend's TriggerStore.
type TriggerAdmin interface {
	// DueTriggers returns up to limit triggers that are due at now: not
	// paused, with a NextFireAt that is not zero and at or before now,
	// ordered by NextFireAt, then tenant id (compared as bytes), then ID.
	// limit is read as [Page.Limit] is.
	DueTriggers(ctx context.Context, now time.Time, limit int) ([]TenantTrigger, error)
	// TriggerByHook returns the trigger whose HookID is hookID, with its
	// tenant, or [ErrNotFound] (also for an empty hookID).
	TriggerByHook(ctx context.Context, hookID string) (TenantTrigger, error)
}
