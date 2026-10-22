package backend

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/store"
)

// statuses are every run status.
var statuses = []store.Status{
	store.StatusRunning, store.StatusSuspended, store.StatusPaused, store.StatusFailed,
	store.StatusCompleted, store.StatusRejected, store.StatusCanceled,
}

// CheckRunFilter refuses a filter [store.RunIndex.ListRuns] cannot apply.
func CheckRunFilter(f store.RunFilter) error {
	if f.Status != "" && !isStatus(f.Status) {
		return fmt.Errorf("%w: no run status %q", store.ErrInvalid, f.Status)
	}
	if f.Kind > store.InteractiveRuns {
		return fmt.Errorf("%w: no run kind %d", store.ErrInvalid, f.Kind)
	}
	return CheckText("agent", f.Agent)
}

func isStatus(s store.Status) bool {
	for _, x := range statuses {
		if s == x {
			return true
		}
	}
	return false
}

// MatchesFilter reports whether r passes f; agent is the agent of r's
// conversation's record, and hasRecord whether it has one.
func MatchesFilter(f store.RunFilter, r store.Run, agent string, hasRecord bool) bool {
	switch {
	case f.Status != "" && r.Status != f.Status:
		return false
	case f.Kind == store.BackgroundRuns && r.Interactive, f.Kind == store.InteractiveRuns && !r.Interactive:
		return false
	case f.Agent != "" && (!hasRecord || agent != f.Agent):
		return false
	}
	return true
}

// CheckNotificationURL refuses an empty URL or one that is not text.
func CheckNotificationURL(url string) error {
	if url == "" {
		return fmt.Errorf("%w: empty notification URL", store.ErrInvalid)
	}
	return CheckText("notification URL", url)
}

// CheckDelivery refuses a delivery [store.Admin.RecordDelivery] cannot
// record.
func CheckDelivery(d store.Delivery) error {
	switch d.State {
	case store.NotificationPending:
		if d.NextAttemptAt.IsZero() {
			return fmt.Errorf("%w: a pending delivery needs its next attempt time", store.ErrInvalid)
		}
	case store.NotificationDelivered, store.NotificationFailed:
		if !d.NextAttemptAt.IsZero() {
			return fmt.Errorf("%w: a %s notification has no next attempt", store.ErrInvalid, d.State)
		}
	default:
		return fmt.Errorf("%w: no notification state %q", store.ErrInvalid, d.State)
	}
	if d.Attempt.At.IsZero() {
		return fmt.Errorf("%w: a delivery attempt needs its time", store.ErrInvalid)
	}
	if err := CheckTime("attempt time", d.Attempt.At); err != nil {
		return err
	}
	if err := CheckTime("next attempt time", d.NextAttemptAt); err != nil {
		return err
	}
	return CheckText("delivery error", d.Attempt.Error)
}

// Ended reports whether a run's notification is due as far as the run goes:
// the run is final, or failed and not retryable.
func Ended(r store.Run) bool {
	return r.Status.Final() || (r.Status == store.StatusFailed && !r.Retryable)
}

// NotificationDue reports whether [store.Admin.ClaimNotification] may take
// n, of run r, at now.
func NotificationDue(n store.Notification, r store.Run, now time.Time) bool {
	return n.State == store.NotificationPending && !Held(n.Lease, now) && !n.NextAttemptAt.After(now) && Ended(r)
}

// NotificationOrder compares two due notifications, each with its tenant id,
// in the order ClaimNotification takes them: earliest NextAttemptAt (zero
// first), then CreatedAt, then tenant id and run ID as bytes.
func NotificationOrder(at string, a store.Notification, bt string, b store.Notification) int {
	if c := a.NextAttemptAt.Compare(b.NextAttemptAt); c != 0 {
		return c
	}
	if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
		return c
	}
	if c := strings.Compare(at, bt); c != 0 {
		return c
	}
	return strings.Compare(a.RunID, b.RunID)
}

// attempt is a [store.DeliveryAttempt] as the backends keep it, in JSON:
// its time as Unix nanoseconds, so that it reads back the same everywhere.
type attempt struct {
	At     int64  `json:"at"`
	Status int    `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
}

// EncodeAttempts is the JSON a backend keeps of a notification's attempts.
func EncodeAttempts(as []store.DeliveryAttempt) string {
	out := make([]attempt, len(as))
	for i, a := range as {
		out[i] = attempt{At: a.At.UnixNano(), Status: a.Status, Error: a.Error}
	}
	b, _ := json.Marshal(out) // plain values: cannot fail
	return string(b)
}

// DecodeAttempts reads what EncodeAttempts wrote; "" is none.
func DecodeAttempts(s string) ([]store.DeliveryAttempt, error) {
	if s == "" {
		return nil, nil
	}
	var in []attempt
	if err := json.Unmarshal([]byte(s), &in); err != nil {
		return nil, fmt.Errorf("delivery attempts: %w", err)
	}
	var out []store.DeliveryAttempt
	for _, a := range in {
		out = append(out, store.DeliveryAttempt{At: time.Unix(0, a.At).UTC(), Status: a.Status, Error: a.Error})
	}
	return out, nil
}

// Attempt is a as a store keeps it: its time in UTC without a monotonic
// reading.
func Attempt(a store.DeliveryAttempt) store.DeliveryAttempt {
	a.At = a.At.UTC().Round(0)
	return a
}

// DeliveryError is why RecordDelivery refuses owner's delivery to a
// notification n at now, or nil if owner holds its lease.
func DeliveryError(n store.Notification, owner string, now time.Time) error {
	switch {
	case n.State.Final():
		return fmt.Errorf("%w: the notification of run %q is %s", store.ErrFinal, n.RunID, n.State)
	case Held(n.Lease, now) && n.Lease.Owner != owner:
		return fmt.Errorf("%w: the notification of run %q", store.ErrLeased, n.RunID)
	case !Held(n.Lease, now):
		return fmt.Errorf("%w: the notification of run %q", store.ErrLeaseLost, n.RunID)
	}
	return nil
}
