package notify

import (
	"context"
	"time"
)

// Outbox item statuses.
const (
	ItemPending = "pending" // waiting for its time (a retry, the end of quiet hours)
	ItemDone    = "done"    // at least one lane took it
	ItemNothing = "nothing" // every lane skipped it: no address, consent or lane on (it stays in the inbox)
	ItemFailed  = "failed"  // the lanes refused it, or it ran out of tries
	ItemExpired = "expired" // it was no longer worth sending
)

// Item is a message in the outbox.
type Item struct {
	Message  Message `json:"message"`
	Status   string  `json:"status"`
	Attempts int     `json:"attempts"`
	// NextAt is when it is due (again).
	NextAt    time.Time `json:"next_at"`
	LastError string    `json:"last_error,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Delivery is one message on one lane: the record that makes a lane send
// a message at most once, and that delivery status updates land on.
type Delivery struct {
	Message    string `json:"message"`
	Lane       string `json:"lane"`
	Recipient  string `json:"recipient"`
	Status     string `json:"status"`
	ProviderID string `json:"provider_id,omitempty"`
	Cost       int64  `json:"cost,omitempty"`
	Error      string `json:"error,omitempty"`
	// Retryable is a failure that may be tried again: a later
	// BeginDelivery claims it.
	Retryable bool      `json:"retryable,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ItemFilter selects outbox items, newest first.
type ItemFilter struct {
	Recipient string
	Notice    string
	Status    string
	Limit     int // 50 when 0
}

// DeliveryFilter selects deliveries, newest first.
type DeliveryFilter struct {
	Message   string
	Recipient string
	Lane      string
	Limit     int // 50 when 0
}

// Store keeps recipients, the outbox and deliveries. Implementations are
// safe for concurrent use, also across processes sharing the store, and
// pass notifytest.RunStore.
type Store interface {
	// PutRecipient creates or replaces r (CreatedAt is kept on replace).
	PutRecipient(ctx context.Context, r Recipient) error
	GetRecipient(ctx context.Context, id string) (Recipient, error)
	ListRecipients(ctx context.Context) ([]Recipient, error) // by ID
	DeleteRecipient(ctx context.Context, id string) error
	// RecipientsFor lists the recipients who hear about farm: those
	// naming it, and those naming no farm.
	RecipientsFor(ctx context.Context, farm string) ([]Recipient, error)
	// FindRecipient finds the recipient with address addr of kind k, or
	// with link code addr when k is "link".
	FindRecipient(ctx context.Context, k AddressKind, addr string) (Recipient, error)
	// Update reads recipient id, applies f and saves it, atomically.
	Update(ctx context.Context, id string, f func(*Recipient) error) (Recipient, error)

	// Enqueue adds m to the outbox, due at at; false when a message with
	// its ID is there already.
	Enqueue(ctx context.Context, m Message, at time.Time) (bool, error)
	// Claim takes up to limit pending items due at or before now and
	// pushes their NextAt to now+lease, so no other worker takes them
	// while they are sent.
	Claim(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]Item, error)
	// Reschedule sets a pending item's next time, tries and last error.
	Reschedule(ctx context.Context, id string, at time.Time, attempts int, lastErr string) error
	// Finish ends an item with status (not pending).
	Finish(ctx context.Context, id, status string, attempts int, lastErr string, at time.Time) error
	GetItem(ctx context.Context, id string) (Item, error)
	ListItems(ctx context.Context, f ItemFilter) ([]Item, error)

	// BeginDelivery claims message d.Message on lane d.Lane: it records d
	// with status sending and returns true, unless a delivery of the pair
	// is there already and is not a retryable failure; then it returns
	// that delivery and false.
	BeginDelivery(ctx context.Context, d Delivery) (Delivery, bool, error)
	// FinishDelivery saves the outcome of a claimed delivery.
	FinishDelivery(ctx context.Context, d Delivery) error
	// SetStatus moves the delivery with lane's providerID to status, when
	// it is a later status than its current one (a "delivered" arriving
	// after "read" is ignored). It reports whether it changed one.
	SetStatus(ctx context.Context, lane, providerID, status, errText string, at time.Time) (bool, error)
	ListDeliveries(ctx context.Context, f DeliveryFilter) ([]Delivery, error)
	// Spend counts lane's deliveries being sent or taken (sending, sent,
	// delivered, read) created at or after since, and their cost in paise.
	Spend(ctx context.Context, lane string, since time.Time) (int, int64, error)

	// Prune removes finished items and deliveries older than before.
	Prune(ctx context.Context, before time.Time) error
}
