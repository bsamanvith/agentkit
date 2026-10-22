package store

import (
	"context"
	"time"

	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// RunKind narrows a [RunFilter] to background or interactive runs.
type RunKind uint8

// The kinds of run a [RunFilter] keeps.
const (
	// AnyRun keeps every run.
	AnyRun RunKind = iota
	// BackgroundRuns keeps the runs created without [Interactive].
	BackgroundRuns
	// InteractiveRuns keeps the runs created with [Interactive].
	InteractiveRuns
)

// RunFilter says which of a tenant's runs [RunIndex.ListRuns] lists. The zero
// filter keeps them all.
type RunFilter struct {
	// Status keeps the runs in this status; "" keeps every status.
	Status Status
	// Kind keeps background runs, interactive ones, or both (the zero
	// value).
	Kind RunKind
	// Agent keeps the runs whose conversation's record names this agent
	// ([Conversation.Agent]); "" keeps every run. With an agent, the runs of
	// conversations that have no record are left out.
	Agent string
}

// RunIndex lists a tenant's runs across its conversations (T071): what is
// running in the background, what failed. Implementations must be safe for
// concurrent use, must honor ctx, must scope every call by the tenant in ctx,
// and must not keep or share memory with values passed in or returned. Run
// storetest.RunRunIndex against every implementation, on a value that is also
// the backend's [RunStore] and [ConversationIndex].
type RunIndex interface {
	// ListRuns returns one page of the tenant's runs that match f, ordered
	// by CreatedAt and then ID, as [RunStore.List] pages a conversation's.
	// A Status that is none of the statuses, a Kind that is none of the
	// kinds, or an Agent that is not text is [ErrInvalid]; so is FromSeq.
	ListRuns(ctx context.Context, f RunFilter, p Page) (RunPage, error)
}

// NotificationState says where a run's [Notification] stands.
type NotificationState string

// The states of a notification. Delivered and failed are final.
const (
	// NotificationPending: not delivered yet. It is tried once its run has
	// ended, and again at NextAttemptAt after an attempt failed.
	NotificationPending NotificationState = "pending"
	// NotificationDelivered: an attempt was answered with success.
	NotificationDelivered NotificationState = "delivered"
	// NotificationFailed: given up on, after attempts that all failed.
	NotificationFailed NotificationState = "failed"
)

// Final reports whether s never changes again.
func (s NotificationState) Final() bool {
	return s == NotificationDelivered || s == NotificationFailed
}

// Notification is the webhook a run's end is told to (T071): at most one per
// run, created with the run and deleted with it. It is due once its run has
// ended, that is, once the run is final or failed and not retryable: whoever
// delivers notifications claims it ([Admin.ClaimNotification]), posts to
// URL, and records the attempt ([Admin.RecordDelivery]). A run that ends
// twice (failed for good, then resumed by an operator) is told once.
type Notification struct {
	RunID string
	// URL is where the run's end is posted.
	URL string
	// State is pending until an attempt succeeds or the deliverer gives up.
	State NotificationState
	// Attempts are the attempts so far, oldest first.
	Attempts []DeliveryAttempt
	// NextAttemptAt is when a pending notification may be tried again
	// after a failed attempt; zero before the first attempt.
	NextAttemptAt time.Time
	// CreatedAt is set by the store when the notification is created.
	CreatedAt time.Time
	// Lease is the deliverer's lease as last written; it may have expired.
	// The zero Lease means nobody holds it.
	Lease Lease
}

// DeliveryAttempt is one try at delivering a [Notification].
type DeliveryAttempt struct {
	// At is when the attempt was made, by the deliverer's clock.
	At time.Time
	// Status is the HTTP status of the answer, 0 when none came.
	Status int
	// Error says why the attempt failed; empty for one that succeeded. It
	// is text (see Text in the package documentation).
	Error string
}

// Delivery is what [Admin.RecordDelivery] records: an attempt and what
// becomes of the notification.
type Delivery struct {
	Attempt DeliveryAttempt
	// State is the notification's state after the attempt: pending to try
	// again at NextAttemptAt, delivered, or failed (given up).
	State NotificationState
	// NextAttemptAt is for pending only, where it is required.
	NextAttemptAt time.Time
}

// TenantNotification is a notification with the tenant it belongs to, as
// [Admin.ClaimNotification] returns it.
type TenantNotification struct {
	Tenant       tenancy.Tenant
	Notification Notification
}

// NotificationLog keeps the notifications of runs (T071). It knows the runs
// of the [RunStore] it is part of: a notification is created only for a run
// that exists, and is deleted with it ([ConversationIndex.DeleteConversation]).
// Delivering them crosses tenants, so it is [Admin]'s.
//
// Implementations must be safe for concurrent use, must honor ctx, must scope
// every call by the tenant in ctx, and must not keep or share memory with
// values passed in or returned. Run storetest.RunNotifications against every
// implementation.
type NotificationLog interface {
	// CreateNotification stores a pending notification of run runID to url
	// and returns it. Creating one that exists with the same URL returns the
	// stored one, so a retry is safe; another URL is [ErrConflict]. It fails
	// with [ErrNotFound] for a run that does not exist, and with
	// [ErrInvalid] for an empty url or one that is not text.
	CreateNotification(ctx context.Context, runID, url string) (Notification, error)
	// GetNotification returns the run's notification, or [ErrNotFound].
	GetNotification(ctx context.Context, runID string) (Notification, error)
}
