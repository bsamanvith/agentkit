package runner

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// Recovery finds the background runs that should continue (interrupted by a
// crash or a deploy, or failed retryably and now due) in every tenant, and
// resumes them with their runner. Interactive runs ([Interactive]) are never
// among them: the store does not list them. It is the only holder of the store's [store.Admin]: build
// the Admin once, in the wiring, and give it to NewRecovery and nothing else.
type Recovery struct {
	r        *Runner
	admin    store.Admin
	limit    int
	workers  int
	interval time.Duration
	execute  func(context.Context, *Execution)
}

// RecoveryOption configures [NewRecovery].
type RecoveryOption func(*Recovery) error

// WithBatch sets how many runs one pass lists (default 100).
func WithBatch(n int) RecoveryOption {
	return func(rc *Recovery) error {
		if n < 1 || n > store.MaxPageLimit {
			return fmt.Errorf("%w: batch %d", ErrInvalidConfig, n)
		}
		rc.limit = n
		return nil
	}
}

// WithWorkers sets how many runs a pass resumes at once (default 4).
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

// NewRecovery returns the recovery loop of r over admin, the cross-tenant
// view of r's store.
func NewRecovery(r *Runner, admin store.Admin, opts ...RecoveryOption) (*Recovery, error) {
	if r == nil || admin == nil {
		return nil, fmt.Errorf("%w: nil runner or admin", ErrInvalidConfig)
	}
	rc := &Recovery{
		r: r, admin: admin, limit: 100, workers: 4, interval: 30 * time.Second,
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
	// Skipped runs were claimed by another process first, became final in
	// between, or are too new: a running run that was never claimed within
	// one lease time of its creation is left to the execution that made it.
	Skipped int
	// Errors are runs that could not be resumed (the error is joined into
	// Recover's).
	Errors int
}

// Recover makes one pass: it lists up to the batch of runs that should
// continue now, oldest first, and resumes each with its tenant in ctx, at
// most the workers at a time, running each to its end. The lease decides who
// resumes a run: of two processes recovering at once, each run is resumed by
// one. Cancelling ctx stops the runs being resumed; they stay running, for
// the next pass.
func (rc *Recovery) Recover(ctx context.Context) (Report, error) {
	now := rc.r.clk.Now()
	runs, err := rc.admin.ListResumableAll(ctx, now, rc.limit)
	if err != nil {
		return Report{}, err
	}
	var (
		mu   sync.Mutex
		rep  Report
		errs []error
		wg   sync.WaitGroup
		sem  = make(chan struct{}, rc.workers)
	)
	count := func(n *int, err error) {
		mu.Lock()
		defer mu.Unlock()
		*n++
		if err != nil {
			errs = append(errs, err)
		}
	}
	for _, tr := range runs {
		run := tr.Run
		if run.Status == store.StatusRunning && run.Lease == (store.Lease{}) && run.Attempts == 0 &&
			now.Before(run.CreatedAt.Add(rc.r.ttl)) {
			// Probably between its creation and its claim.
			count(&rep.Skipped, nil)
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return rep, ctx.Err()
		}
		wg.Go(func() {
			defer func() { <-sem }()
			tctx := tenancy.With(ctx, tr.Tenant)
			x, err := rc.r.Resume(tctx, run.ID)
			if errors.Is(err, store.ErrLeased) || errors.Is(err, store.ErrFinal) {
				count(&rep.Skipped, nil)
				return
			}
			if err != nil {
				count(&rep.Errors, fmt.Errorf("tenant %q: %w", tr.Tenant.ID(), err))
				return
			}
			if x.Run.Status.Final() {
				// Completed by another process since the listing.
				count(&rep.Skipped, nil)
				return
			}
			// The run's own outcome is stored with it; a failure here is
			// the run's, not recovery's.
			rc.execute(tctx, x)
			count(&rep.Resumed, nil)
		})
	}
	wg.Wait()
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
