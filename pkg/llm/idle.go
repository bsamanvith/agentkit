package llm

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"time"
)

// WithIdleTimeout returns m with a limit on how long a reply's stream may go
// without an event: a stream silent for d is canceled and fails with
// [ErrOverloaded], so a retry loop tries again instead of waiting until the
// run's own deadline. Only time spent waiting on the model counts, not time
// the caller spends handling an event. A d of zero or less returns m.
//
// It is for adapters that have no such limit of their own. One that sees the
// provider's keep-alives (the anthropic adapter's WithIdleTimeout counts the
// API's pings) can tell a long silent think from a dead connection, which
// this wrapper, seeing only events, cannot: give it a d longer than the
// model's longest pause between events.
func WithIdleTimeout(m Model, d time.Duration) Model {
	if d <= 0 {
		return m
	}
	return idleModel{m, d}
}

type idleModel struct {
	inner Model
	d     time.Duration
}

// errStalled is the cause of a stream given up by [WithIdleTimeout].
var errStalled = errors.New("llm: stream stalled")

func (m idleModel) Generate(ctx context.Context, req Request) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		parent := ctx
		ctx, cancel := context.WithCancelCause(ctx)
		defer cancel(nil)
		watchdog := time.AfterFunc(m.d, func() {
			cancel(fmt.Errorf("%w: no event for %v", errStalled, m.d))
		})
		defer watchdog.Stop()
		for ev, err := range m.inner.Generate(ctx, req) {
			watchdog.Stop()
			if err != nil && parent.Err() == nil {
				if cause := context.Cause(ctx); errors.Is(cause, errStalled) {
					err = fmt.Errorf("%w: %w", ErrOverloaded, cause)
				}
			}
			if !yield(ev, err) || err != nil {
				return
			}
			watchdog.Reset(m.d)
		}
	}
}
