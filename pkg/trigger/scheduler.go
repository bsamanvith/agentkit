package trigger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/store"
)

// DefaultInterval is how often a [Scheduler] looks for due triggers by
// default. Schedules are to the minute; a firing comes up to this late.
const DefaultInterval = 10 * time.Second

// DefaultBatch is the most due triggers one pass fires by default.
const DefaultBatch = 100

// ErrInvalidConfig means [NewScheduler] was given a missing argument or an
// unusable option.
var ErrInvalidConfig = errors.New("invalid trigger scheduler configuration")

// Admin is the part of [store.TriggerAdmin] a scheduler uses.
type Admin interface {
	DueTriggers(ctx context.Context, now time.Time, limit int) ([]store.TenantTrigger, error)
}

// Fire fires one due trigger: it claims the due time through the store
// ([store.TriggerStore.StartFiring]), so that of the schedulers of several
// replicas only one fires it, and starts its run; it returns an error
// matching [store.ErrConflict] when another replica fired it first. The
// agent API server's FireDue is one.
type Fire func(ctx context.Context, t store.TenantTrigger) error

// Scheduler fires the triggers of every tenant when they are due. Make one
// with [NewScheduler]. Run one per process: several, on replicas sharing a
// store, fire each due time once between them.
type Scheduler struct {
	admin    Admin
	fire     Fire
	clk      clock.Clock
	interval time.Duration
	batch    int
}

// Option configures [NewScheduler].
type Option func(*Scheduler) error

// WithClock sets the clock that says what is due (default [clock.Real]).
func WithClock(c clock.Clock) Option {
	return func(s *Scheduler) error {
		if c == nil {
			return fmt.Errorf("%w: nil clock", ErrInvalidConfig)
		}
		s.clk = c
		return nil
	}
}

// WithInterval sets how long [Scheduler.Run] waits between passes (default
// [DefaultInterval]).
func WithInterval(d time.Duration) Option {
	return func(s *Scheduler) error {
		if d <= 0 {
			return fmt.Errorf("%w: interval %v is not positive", ErrInvalidConfig, d)
		}
		s.interval = d
		return nil
	}
}

// WithBatch sets the most due triggers a pass fires (default [DefaultBatch]).
func WithBatch(n int) Option {
	return func(s *Scheduler) error {
		if n <= 0 || n > store.MaxPageLimit {
			return fmt.Errorf("%w: batch %d is not from 1 to %d", ErrInvalidConfig, n, store.MaxPageLimit)
		}
		s.batch = n
		return nil
	}
}

// NewScheduler returns a scheduler that finds due triggers in admin and fires
// each with fire.
func NewScheduler(admin Admin, fire Fire, opts ...Option) (*Scheduler, error) {
	if admin == nil || fire == nil {
		return nil, fmt.Errorf("%w: a scheduler needs an admin and a fire function", ErrInvalidConfig)
	}
	s := &Scheduler{admin: admin, fire: fire, clk: clock.Real(), interval: DefaultInterval, batch: DefaultBatch}
	for _, o := range opts {
		if err := o(s); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Pass fires the triggers due now, one after another, and returns how many
// it fired (that another replica did not fire first). It goes on past a
// trigger that fails, and returns their errors joined.
func (s *Scheduler) Pass(ctx context.Context) (int, error) {
	due, err := s.admin.DueTriggers(ctx, s.clk.Now(), s.batch)
	if err != nil {
		return 0, fmt.Errorf("listing due triggers: %w", err)
	}
	var (
		n    int
		errs []error
	)
	for _, t := range due {
		if ctx.Err() != nil {
			break
		}
		switch err := s.fire(ctx, t); {
		case err == nil:
			n++
		case errors.Is(err, store.ErrConflict):
			// Another replica fired it, or it changed since it was listed.
		default:
			errs = append(errs, fmt.Errorf("trigger %q of tenant %q: %w", t.Trigger.ID, t.Tenant.ID(), err))
		}
	}
	return n, errors.Join(errs...)
}

// Run makes a pass every interval until ctx is done, telling report (if not
// nil) of each pass that fired a trigger or failed. It returns nil when ctx
// is done.
func (s *Scheduler) Run(ctx context.Context, report func(fired int, err error)) error {
	for {
		n, err := s.Pass(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if report != nil && (err != nil || n > 0) {
			report(n, err)
		}
		t := s.clk.NewTimer(s.interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C():
		}
	}
}
