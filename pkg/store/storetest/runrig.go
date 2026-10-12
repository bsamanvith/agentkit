package storetest

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// runRig is the one helper every run test and the run simulation go through.
// It models every run created and moved through it and, when the test ends,
// checks the run invariants on everything the test touched, through every
// tenant the test used plus one that never wrote:
//
//  1. Tenants: Get finds exactly the runs the tenant created; others are
//     ErrNotFound.
//  2. Records: a run keeps its conversation; its status and lifecycle fields
//     (usage summed over transitions, error, retry, attempts counting the
//     failures, pending paths) are those of the last transition the store
//     accepted; FinishedAt is set exactly when the status is final; a lease
//     has an owner exactly when it has an expiry.
//  3. List in both orders, paged, gives each conversation's runs once,
//     ordered by CreatedAt and ID, newest first the reverse.
//  4. A conversation has at most one unfinished run, and Unfinished returns
//     it (ErrNotFound when there is none).
//  5. ListSuspended, paged, gives exactly the suspended runs, in order.
//  6. ListResumable at the clock's time gives exactly the running runs whose
//     lease is not held and the retryable failed runs that are due, in order.
//  7. With an Admin (RunAdmin), ListResumableAll at the clock's time, with no
//     tenant in ctx, is the union of every tenant's ListResumable, each run
//     with its tenant, ordered by CreatedAt, tenant and ID.
type runRig struct {
	t     *testing.T
	s     store.RunStore
	admin store.Admin // nil outside RunAdmin
	clk   *clocktest.Fake

	mu      sync.Mutex
	runs    map[logKey]*runModel // (tenant, run id)
	ids     map[string]bool
	convs   map[string]bool
	tenants map[string]bool
}

// runModel is what a run must read back as.
type runModel struct {
	conv     string
	status   store.Status
	usage    llm.Usage
	err      string
	retry    bool
	next     time.Time
	attempts int
	pending  []string
}

func newRunRig(t *testing.T, newStore func(t *testing.T, clk clock.Clock) store.RunStore) *runRig {
	t.Helper()
	return newAdminRig(t, func(t *testing.T, clk clock.Clock) (store.RunStore, store.Admin) {
		return newStore(t, clk), nil
	})
}

// newAdminRig is newRunRig for a store with an Admin view; a nil Admin skips
// invariant 7.
func newAdminRig(t *testing.T, newStore func(t *testing.T, clk clock.Clock) (store.RunStore, store.Admin)) *runRig {
	t.Helper()
	clk := clocktest.NewFake(time.Time{})
	s, admin := newStore(t, clk)
	r := &runRig{
		t:       t,
		s:       s,
		admin:   admin,
		clk:     clk,
		runs:    map[logKey]*runModel{},
		ids:     map[string]bool{},
		convs:   map[string]bool{},
		tenants: map[string]bool{unusedTenant: true},
	}
	t.Cleanup(r.check)
	return r
}

func (r *runRig) ctx(id string) context.Context {
	r.mu.Lock()
	r.tenants[id] = true
	r.mu.Unlock()
	return tenantCtx(r.t.Context(), r.t, id)
}

func (r *runRig) create(ctx context.Context, id, conv string) (store.Run, error) {
	run, err := r.s.Create(ctx, id, conv)
	r.mu.Lock()
	defer r.mu.Unlock()
	if id != "" && conv != "" && textOK(conv) { // the invariant check reads them back
		r.ids[id], r.convs[conv] = true, true
	}
	tn, ok := tenancy.From(ctx)
	if !ok || err != nil {
		return run, err
	}
	k := logKey{tn.ID(), id}
	switch m, ok := r.runs[k]; {
	case !ok:
		r.runs[k] = &runModel{conv: conv, status: store.StatusRunning}
	case m.conv != conv:
		r.t.Errorf("Create moved run %q from conversation %q to %q", id, m.conv, conv)
	}
	return run, err
}

func (r *runRig) mustCreate(ctx context.Context, id, conv string) store.Run {
	r.t.Helper()
	run, err := r.create(ctx, id, conv)
	if err != nil {
		r.t.Fatalf("Create %q: %v", id, err)
	}
	return run
}

// mustCreateFinal creates a run and completes it, so that the conversation
// can take another.
func (r *runRig) mustCreateFinal(ctx context.Context, id, conv string) store.Run {
	r.t.Helper()
	r.mustCreate(ctx, id, conv)
	return r.mustMove(ctx, id, store.StatusRunning, store.StatusCompleted, store.Details{})
}

// transition calls Transition and models an accepted one. It is safe for
// concurrent use.
func (r *runRig) transition(ctx context.Context, id string, from, to store.Status, d store.Details) (store.Run, error) {
	run, err := r.s.Transition(ctx, id, from, to, d)
	r.mu.Lock()
	defer r.mu.Unlock()
	tn, ok := tenancy.From(ctx)
	if !ok || err != nil {
		return run, err
	}
	m := r.runs[logKey{tn.ID(), id}]
	switch {
	case m == nil:
		r.t.Errorf("Transition of run %q, which the test never created, succeeded", id)
		return run, err
	case m.status != from:
		r.t.Errorf("Transition of run %q from %s succeeded while it was %s", id, from, m.status)
	}
	m.status, m.err, m.retry, m.next = to, d.Error, d.Retryable, d.NextAttemptAt
	m.usage = sumUsage(m.usage, d.Usage)
	m.pending = slices.Sorted(slices.Values(d.Pending))
	if to == store.StatusFailed {
		m.attempts++
	}
	return run, err
}

func (r *runRig) mustMove(ctx context.Context, id string, from, to store.Status, d store.Details) store.Run {
	r.t.Helper()
	run, err := r.transition(ctx, id, from, to, d)
	if err != nil {
		r.t.Fatalf("Transition %q %s → %s: %v", id, from, to, err)
	}
	return run
}

func (r *runRig) mustGet(ctx context.Context, id string) store.Run {
	r.t.Helper()
	run, err := r.s.Get(ctx, id)
	if err != nil {
		r.t.Fatalf("Get %q: %v", id, err)
	}
	return run
}

func sumUsage(a, b llm.Usage) llm.Usage {
	a.InputTokens += b.InputTokens
	a.OutputTokens += b.OutputTokens
	a.CacheReadTokens += b.CacheReadTokens
	a.CacheWriteTokens += b.CacheWriteTokens
	return a
}

// pageAll pages through a run list and checks each page's shape on the way.
func pageAll(t *testing.T, what string, list func(store.Page) (store.RunPage, error), p store.Page) []store.Run {
	t.Helper()
	limit := pageLimit(p.Limit)
	var out []store.Run
	for range 100_000 {
		page, err := list(p)
		if err != nil {
			t.Fatalf("%s %+v: %v", what, p, err)
		}
		if page.Next != "" && len(page.Runs) != limit {
			t.Fatalf("%s: a page with a Next holds %d runs, want the limit %d", what, len(page.Runs), limit)
		}
		out = append(out, page.Runs...)
		if page.Next == "" {
			return out
		}
		p = store.Page{Cursor: page.Next, Limit: p.Limit, Order: p.Order}
	}
	t.Fatalf("%s never ended", what)
	return nil
}

func listAll(ctx context.Context, t *testing.T, s store.RunStore, conv string, p store.Page) []store.Run {
	t.Helper()
	return pageAll(t, "List "+conv, func(p store.Page) (store.RunPage, error) { return s.List(ctx, conv, p) }, p)
}

func suspendedAll(ctx context.Context, t *testing.T, s store.RunStore, p store.Page) []store.Run {
	t.Helper()
	return pageAll(t, "ListSuspended", func(p store.Page) (store.RunPage, error) { return s.ListSuspended(ctx, p) }, p)
}

func runIDs(rs []store.Run) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.ID
	}
	return out
}

func runBefore(a, b store.Run) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.ID < b.ID
}

// resumable restates which runs ListResumable returns.
func resumable(r store.Run, now time.Time) bool {
	switch r.Status {
	case store.StatusRunning:
		return r.Lease.Owner == "" || !now.Before(r.Lease.Expires)
	case store.StatusFailed:
		return r.Retryable && !now.Before(r.NextAttemptAt)
	}
	return false
}

func (r *runRig) check() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, tenant := range sortedKeys(r.tenants) {
		r.checkTenant(tenant)
	}
	if r.admin != nil {
		r.checkAdmin()
	}
}

func (r *runRig) checkTenant(tenant string) {
	t := r.t
	ctx := tenantCtx(context.Background(), t, tenant)
	var all []store.Run
	for _, id := range sortedKeys(r.ids) {
		m := r.runs[logKey{tenant, id}]
		run, err := r.s.Get(ctx, id)
		if m == nil {
			if !errors.Is(err, store.ErrNotFound) {
				t.Errorf("tenant %s: Get of run %q it never created = %+v, %v; want ErrNotFound", tenant, id, run, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("tenant %s: Get %q: %v", tenant, id, err)
			continue
		}
		all = append(all, run)
		switch {
		case run.ID != id || run.ConversationID != m.conv:
			t.Errorf("tenant %s: run %q reads back as %q in conversation %q, want conversation %q", tenant, id, run.ID, run.ConversationID, m.conv)
		case run.Status != m.status || run.Usage != m.usage || run.Error != m.err || run.Retryable != m.retry ||
			!run.NextAttemptAt.Equal(m.next) || run.Attempts != m.attempts || !slices.Equal(run.Pending, m.pending):
			t.Errorf("tenant %s: run %q reads back as\n%+v\nwant what its transitions recorded\n%+v", tenant, id, run, *m)
		case run.CreatedAt.IsZero():
			t.Errorf("tenant %s: run %q has no CreatedAt", tenant, id)
		case run.Status.Final() == run.FinishedAt.IsZero():
			t.Errorf("tenant %s: run %q is %s with FinishedAt %v", tenant, id, run.Status, run.FinishedAt)
		case (run.Lease.Owner == "") != run.Lease.Expires.IsZero():
			t.Errorf("tenant %s: run %q has lease %+v", tenant, id, run.Lease)
		}
	}
	slices.SortFunc(all, func(a, b store.Run) int {
		if runBefore(a, b) {
			return -1
		}
		return 1
	})

	for _, conv := range sortedKeys(r.convs) {
		var want, unfinished []string
		for _, run := range all {
			if run.ConversationID == conv {
				want = append(want, run.ID)
				if !run.Status.Final() {
					unfinished = append(unfinished, run.ID)
				}
			}
		}
		asc := listAll(ctx, t, r.s, conv, store.Page{Limit: 2})
		if !slices.Equal(runIDs(asc), want) {
			t.Errorf("tenant %s: List %q gives runs %v, want %v ordered by CreatedAt and ID", tenant, conv, runIDs(asc), want)
		}
		desc := listAll(ctx, t, r.s, conv, store.Page{Order: store.Descending, Limit: 3})
		if !slices.Equal(runIDs(desc), reverse(want)) {
			t.Errorf("tenant %s: List %q newest first is %v, want %v", tenant, conv, runIDs(desc), reverse(want))
		}
		got, err := r.s.Unfinished(ctx, conv)
		switch {
		case len(unfinished) > 1:
			t.Errorf("tenant %s: conversation %q has %d unfinished runs %v, want at most one", tenant, conv, len(unfinished), unfinished)
		case len(unfinished) == 0 && !errors.Is(err, store.ErrNotFound):
			t.Errorf("tenant %s: Unfinished %q = %q, %v; want ErrNotFound", tenant, conv, got.ID, err)
		case len(unfinished) == 1 && (err != nil || got.ID != unfinished[0]):
			t.Errorf("tenant %s: Unfinished %q = %q, %v; want %q", tenant, conv, got.ID, err, unfinished[0])
		}
	}

	var suspended, due []string
	now := r.clk.Now()
	for _, run := range all {
		if run.Status == store.StatusSuspended {
			suspended = append(suspended, run.ID)
		}
		if resumable(run, now) {
			due = append(due, run.ID)
		}
	}
	if got := runIDs(suspendedAll(ctx, t, r.s, store.Page{Limit: 2})); !slices.Equal(got, suspended) {
		t.Errorf("tenant %s: ListSuspended = %v, want %v", tenant, got, suspended)
	}
	got, err := r.s.ListResumable(ctx, now, store.MaxPageLimit)
	if err != nil || !slices.Equal(runIDs(got), due) {
		t.Errorf("tenant %s: ListResumable at %v = %v, %v; want %v", tenant, now, runIDs(got), err, due)
	}
}

func reverse[T any](s []T) []T {
	out := slices.Clone(s)
	slices.Reverse(out)
	return out
}

func sameRun(a, b store.Run) bool {
	return a.ID == b.ID && a.ConversationID == b.ConversationID && a.Status == b.Status &&
		a.Usage == b.Usage && a.Error == b.Error && a.Retryable == b.Retryable &&
		a.NextAttemptAt.Equal(b.NextAttemptAt) && a.Attempts == b.Attempts && slices.Equal(a.Pending, b.Pending) &&
		a.CreatedAt.Equal(b.CreatedAt) && a.FinishedAt.Equal(b.FinishedAt) && sameLease(a.Lease, b.Lease)
}

func sameLease(a, b store.Lease) bool {
	return a.Owner == b.Owner && a.Expires.Equal(b.Expires)
}
