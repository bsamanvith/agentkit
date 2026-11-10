package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
)

// Dispatcher keeps the outbox and sends its messages through a tree of
// senders. It is safe for concurrent use, and several processes may share
// one store: each item is claimed before it is sent.
type Dispatcher struct {
	store    Store
	root     Sender
	clock    clock.Clock
	log      *slog.Logger
	tick     time.Duration
	lease    time.Duration
	tries    int
	backoff  time.Duration
	maxWait  time.Duration
	batch    int
	keep     time.Duration
	expiries map[string]time.Duration

	wake chan struct{}
	mu   sync.Mutex // one pass at a time in this process
}

// DispatchOption configures [NewDispatcher].
type DispatchOption func(*Dispatcher)

// WithClock sets the clock (default the machine's).
func WithClock(c clock.Clock) DispatchOption { return func(d *Dispatcher) { d.clock = c } }

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) DispatchOption { return func(d *Dispatcher) { d.log = l } }

// WithTick sets how often the outbox is looked at (default 10s); a new
// message is looked at at once.
func WithTick(t time.Duration) DispatchOption { return func(d *Dispatcher) { d.tick = t } }

// WithTries sets how many times a message is tried while lanes fail for a
// while (default 6) and the first wait, doubled each time up to an hour
// (default 30s).
func WithTries(n int, backoff time.Duration) DispatchOption {
	return func(d *Dispatcher) { d.tries, d.backoff = n, backoff }
}

// WithExpiry sets how long a message of severity is worth sending
// (defaults: urgent 12h, warning 24h, advice 72h), unless it has its own.
func WithExpiry(severity string, ttl time.Duration) DispatchOption {
	return func(d *Dispatcher) { d.expiries[severity] = ttl }
}

// NewDispatcher returns a dispatcher over store that sends with root.
func NewDispatcher(store Store, root Sender, opts ...DispatchOption) (*Dispatcher, error) {
	if store == nil || root == nil {
		return nil, fmt.Errorf("%w: a dispatcher needs a store and a sender", ErrInvalid)
	}
	d := &Dispatcher{
		store: store, root: root, clock: clock.Real(), log: slog.New(slog.DiscardHandler),
		tick: 10 * time.Second, lease: 2 * time.Minute, tries: 6, backoff: 30 * time.Second, maxWait: time.Hour,
		batch: 32, keep: 60 * 24 * time.Hour,
		expiries: map[string]time.Duration{Urgent: 12 * time.Hour, Warning: 24 * time.Hour, Advice: 72 * time.Hour},
		wake:     make(chan struct{}, 1),
	}
	for _, o := range opts {
		o(d)
	}
	return d, nil
}

// Root is the tree messages are sent with.
func (d *Dispatcher) Root() Sender { return d.root }

// Store is the outbox's store.
func (d *Dispatcher) Store() Store { return d.store }

// Enqueue adds m to the outbox, to be sent at once (or after the person's
// quiet hours). It reports false when a message with its ID was queued
// before: queueing is idempotent.
func (d *Dispatcher) Enqueue(ctx context.Context, m Message) (bool, error) {
	if m.Kind == "" {
		m.Kind = KindAlert
	}
	if err := m.check(); err != nil {
		return false, err
	}
	now := d.clock.Now().UTC()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now
	}
	if m.ExpiresAt.IsZero() {
		if ttl := d.expiries[m.Severity]; ttl > 0 {
			m.ExpiresAt = m.CreatedAt.Add(ttl)
		}
	}
	m.Recipient, m.Sent = Recipient{}, nil
	added, err := d.store.Enqueue(ctx, m, now)
	if added {
		select {
		case d.wake <- struct{}{}:
		default:
		}
	}
	return added, err
}

// Run sends due messages until ctx is done: every tick, and at once when
// one is queued.
func (d *Dispatcher) Run(ctx context.Context) error {
	t := d.clock.NewTicker(d.tick)
	defer t.Stop()
	prune := d.clock.NewTicker(24 * time.Hour)
	defer prune.Stop()
	for {
		d.Pass(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-t.C():
		case <-d.wake:
		case <-prune.C():
			if err := d.store.Prune(ctx, d.clock.Now().Add(-d.keep)); err != nil {
				d.log.Warn("notify: prune", "err", err)
			}
		}
	}
}

// Pass sends every message that is due now, and returns how many it
// looked at. Run calls it; tests call it directly.
func (d *Dispatcher) Pass(ctx context.Context) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for ctx.Err() == nil {
		items, err := d.store.Claim(ctx, d.clock.Now().UTC(), d.lease, d.batch)
		if err != nil {
			d.log.Error("notify: claim", "err", err)
			return n
		}
		if len(items) == 0 {
			return n
		}
		var wg sync.WaitGroup
		for _, it := range items {
			wg.Go(func() { d.process(ctx, it) })
		}
		wg.Wait()
		n += len(items)
	}
	return n
}

// process sends one claimed item and records what happened.
func (d *Dispatcher) process(ctx context.Context, it Item) {
	m := it.Message
	now := d.clock.Now().UTC()
	finish := func(status, why string) {
		if err := d.store.Finish(context.WithoutCancel(ctx), m.ID, status, it.Attempts, why, now); err != nil {
			d.log.Error("notify: finish", "message", m.ID, "err", err)
		}
	}
	if !m.ExpiresAt.IsZero() && !now.Before(m.ExpiresAt) {
		finish(ItemExpired, "no longer worth sending")
		return
	}
	r, err := d.store.GetRecipient(ctx, m.To)
	if errors.Is(err, ErrNotFound) {
		finish(ItemNothing, "the recipient was removed")
		return
	}
	if err != nil {
		d.retry(ctx, it, err)
		return
	}
	if m.Severity != Urgent && m.Kind != KindTest {
		if end, quiet := r.QuietUntil(now); quiet {
			if err := d.store.Reschedule(ctx, m.ID, end, it.Attempts, "held for quiet hours"); err != nil {
				d.log.Error("notify: hold", "message", m.ID, "err", err)
			}
			return
		}
	}
	m.Recipient = r
	if m.Sent, err = d.sentLanes(ctx, m.ID); err != nil {
		d.retry(ctx, it, err)
		return
	}
	it.Attempts++
	rcs, err := d.root.Send(ctx, m)
	d.log.Info("notify: sent", "message", m.ID, "to", m.To, "severity", m.Severity, "receipts", summary(rcs), "err", err)
	switch {
	case err == nil:
		finish(ItemDone, "")
	case errors.Is(err, ErrTemporary):
		d.retry(ctx, it, err)
	case errors.Is(err, ErrRejected), errors.Is(err, ErrGone):
		finish(ItemFailed, err.Error())
	case errors.Is(err, ErrSkipped):
		finish(ItemNothing, err.Error())
	default:
		finish(ItemFailed, err.Error())
	}
}

func (d *Dispatcher) retry(ctx context.Context, it Item, cause error) {
	m := it.Message
	if it.Attempts >= d.tries {
		if err := d.store.Finish(context.WithoutCancel(ctx), m.ID, ItemFailed, it.Attempts, "gave up: "+cause.Error(), d.clock.Now().UTC()); err != nil {
			d.log.Error("notify: finish", "message", m.ID, "err", err)
		}
		return
	}
	wait := d.backoff
	for range max(it.Attempts-1, 0) {
		wait *= 2
		if wait >= d.maxWait {
			wait = d.maxWait
			break
		}
	}
	if err := d.store.Reschedule(context.WithoutCancel(ctx), m.ID, d.clock.Now().UTC().Add(wait), it.Attempts, cause.Error()); err != nil {
		d.log.Error("notify: reschedule", "message", m.ID, "err", err)
	}
}

func (d *Dispatcher) sentLanes(ctx context.Context, id string) ([]string, error) {
	ds, err := d.store.ListDeliveries(ctx, DeliveryFilter{Message: id, Limit: 100})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, x := range ds {
		if Accepted(x.Status) || x.Status == StatusSending {
			out = append(out, x.Lane)
		}
	}
	return out, nil
}

// SendNow sends m at once through the tree, past quiet hours, and waits:
// for a test message from the settings page. It is recorded like any
// other, and its ID still makes it idempotent.
func (d *Dispatcher) SendNow(ctx context.Context, m Message) ([]Receipt, error) {
	if m.Kind == "" {
		m.Kind = KindTest
	}
	if err := m.check(); err != nil {
		return nil, err
	}
	r, err := d.store.GetRecipient(ctx, m.To)
	if err != nil {
		return nil, err
	}
	now := d.clock.Now().UTC()
	m.CreatedAt = now
	m.Recipient, m.Sent = Recipient{}, nil
	if _, err := d.store.Enqueue(ctx, m, now.Add(d.lease)); err != nil {
		return nil, err
	}
	m.Recipient = r
	if m.Sent, err = d.sentLanes(ctx, m.ID); err != nil {
		return nil, err
	}
	rcs, err := d.root.Send(ctx, m)
	status, why := ItemDone, ""
	switch {
	case err == nil:
	case errors.Is(err, ErrSkipped) && !errors.Is(err, ErrRejected) && !errors.Is(err, ErrTemporary):
		status, why = ItemNothing, err.Error()
	default:
		status, why = ItemFailed, err.Error()
	}
	if ferr := d.store.Finish(context.WithoutCancel(ctx), m.ID, status, 1, why, d.clock.Now().UTC()); ferr != nil {
		d.log.Error("notify: finish", "message", m.ID, "err", ferr)
	}
	return rcs, err
}

func summary(rcs []Receipt) string {
	parts := make([]string, len(rcs))
	for i, r := range rcs {
		parts[i] = r.Lane + ":" + r.Status
	}
	return strings.Join(parts, ",")
}
