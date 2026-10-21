package runner

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// Recovery is the dispatcher of the run queue: it claims the due background
// runs of its lanes in every tenant, in the queue's order, and runs each with
// its runner. A run is due when it is enqueued and its start time has come
// ([Runner.Enqueue]), when its execution stopped (a crash or a deploy) and
// its lease expired, or when it failed retryably and its next attempt is
// due; see [store.Admin.ClaimNext] for the order. Interactive runs
// ([Interactive]) are never among them. It is the only holder of the store's
// [store.Admin]: build the Admin once, in the wiring, and give it to
// NewRecovery and nothing else.
type Recovery struct {
	r        *Runner
	admin    store.Admin
	lanes    []string
	limit    int
	workers  int
	interval time.Duration
	execute  func(context.Context, *Execution)
}

// RecoveryOption configures [NewRecovery].
type RecoveryOption func(*Recovery) error

// WithLanes sets the lanes the dispatcher claims runs from (default the
// default lane, ""). A run of any other lane is left to the dispatchers that
// serve it.
func WithLanes(lanes ...string) RecoveryOption {
	return func(rc *Recovery) error {
		if len(lanes) == 0 {
			return fmt.Errorf("%w: no lanes", ErrInvalidConfig)
		}
		rc.lanes = slices.Clone(lanes)
		return nil
	}
}

// WithBatch sets how many runs one pass claims at most (default 100).
func WithBatch(n int) RecoveryOption {
	return func(rc *Recovery) error {
		if n < 1 || n > store.MaxPageLimit {
			return fmt.Errorf("%w: batch %d", ErrInvalidConfig, n)
		}
		rc.limit = n
		return nil
	}
}

// WithWorkers sets how many runs a pass runs at once (default 4). A pass
// claims a run only when one of its workers is free to run it.
func WithWorkers(n int) RecoveryOption {
	return func(rc *Recovery) error {
		if n < 1 {
			return fmt.Errorf("%w: workers %d", ErrInvalidConfig, n)
		}
		rc.workers = n
		return nil
	}
}

// WithInterval sets the wait between passes of [Recovery.Run] (default 30s).
func WithInterval(d time.Duration) RecoveryOption {
	return func(rc *Recovery) error {
		if d <= 0 {
			return fmt.Errorf("%w: interval %v", ErrInvalidConfig, d)
		}
		rc.interval = d
		return nil
	}
}

// WithExecute sets how a pass runs an execution it resumed: f must run x to
// its end (default: [Execution.Wait]). A server passes the function that
// runs it as it runs its own, so that the run's events reach its clients.
// ctx carries the run's tenant.
func WithExecute(f func(ctx context.Context, x *Execution)) RecoveryOption {
	return func(rc *Recovery) error {
		if f == nil {
			return fmt.Errorf("%w: nil execute", ErrInvalidConfig)
		}
		rc.execute = f
		return nil
	}
}

// NewRecovery returns the dispatcher of r over admin, the cross-tenant view
// of r's store.
func NewRecovery(r *Runner, admin store.Admin, opts ...RecoveryOption) (*Recovery, error) {
	if r == nil || admin == nil {
		return nil, fmt.Errorf("%w: nil runner or admin", ErrInvalidConfig)
	}
	rc := &Recovery{
		r: r, admin: admin, lanes: []string{""}, limit: 100, workers: 4, interval: 30 * time.Second,
		execute: func(_ context.Context, x *Execution) { _, _ = x.Wait() },
	}
	for _, opt := range opts {
		if err := opt(rc); err != nil {
			return nil, err
		}
	}
	return rc, nil
}

// Report counts what one pass of [Recovery.Recover] did.
type Report struct {
	// Resumed runs were claimed and run; they may have failed again.
	Resumed int
	// Skipped runs were claimed but not run: final by then, or claimed a
	// second time in the pass (its execution ended at once, leaving the
	// run due), which ends the pass.
	Skipped int
	// Errors are runs that could not be resumed (the error is joined into
	// Recover's). Each keeps the lease the pass took until it expires, so
	// it is tried again one lease time later, not at once.
	Errors int
}

// Recover makes one pass: while one of its workers is free, it claims the
// next due run of its lanes ([store.Admin.ClaimNext]) and runs it with its
// tenant in ctx, until no run is due or it has claimed the batch; then it
// waits for the runs it started to end. The store's claim decides who runs
// a run: of two processes dispatching at once, each run is run by one.
// Cancelling ctx stops the runs being run; they stay running, for the next
// pass.
func (rc *Recovery) Recover(ctx context.Context) (Report, error) {
	var (
		mu   sync.Mutex
		rep  Report
		errs []error
		wg   sync.WaitGroup
		sem  = make(chan struct{}, rc.workers)
		seen = map[liveKey]bool{}
	)
	count := func(n *int, err error) {
		mu.Lock()
		defer mu.Unlock()
		*n++
		if err != nil {
			errs = append(errs, err)
		}
	}
	for range rc.limit {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return rep, ctx.Err()
		}
		if rc.r.isClosed() {
			<-sem
			break
		}
		owner := rc.r.nextOwner()
		tr, err := rc.admin.ClaimNext(ctx, rc.lanes, owner, rc.r.ttl)
		if err != nil {
			<-sem
			if !errors.Is(err, store.ErrNotFound) {
				count(&rep.Errors, fmt.Errorf("claim the next run: %w", err))
			}
			break
		}
		tctx := tenancy.With(ctx, tr.Tenant)
		k := liveKey{tr.Tenant.ID(), tr.Run.ID}
		if seen[k] {
			// Its execution ended at once and left it due: it would be
			// claimed again and again. The next pass tries it.
			<-sem
			wctx, cancel := rc.r.detach(tctx)
			_ = rc.r.store.Release(wctx, tr.Run.ID, owner)
			cancel()
			count(&rep.Skipped, nil)
			break
		}
		seen[k] = true
		wg.Go(func() {
			defer func() { <-sem }()
			x, err := rc.r.resume(tctx, tr.Run.ID, resumeConfig{claimed: owner})
			if errors.Is(err, store.ErrFinal) {
				count(&rep.Skipped, nil)
				return
			}
			if err != nil {
				count(&rep.Errors, fmt.Errorf("tenant %q: %w", tr.Tenant.ID(), err))
				return
			}
			if x.Run.Status.Final() {
				// Completed by another process since the claim.
				count(&rep.Skipped, nil)
				return
			}
			// The run's own outcome is stored with it; a failure here is
			// the run's, not the dispatcher's.
			rc.execute(tctx, x)
			count(&rep.Resumed, nil)
		})
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	return rep, errors.Join(errs...)
}

// Run makes a pass at once and then one every interval, by the runner's
// clock, until ctx is done; it then returns nil. Errors of a pass are given to
// report, if set, and do not stop the loop.
func (rc *Recovery) Run(ctx context.Context, report func(Report, error)) error {
	t := rc.r.clk.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C():
		}
		rep, err := rc.Recover(ctx)
		if report != nil && ctx.Err() == nil {
			report(rep, err)
		}
		t.Reset(rc.interval)
	}
}
