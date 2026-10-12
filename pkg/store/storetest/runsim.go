package storetest

import (
	"cmp"
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/store"
)

// SimulateRuns runs one seeded simulation against a fresh store from
// newStore: runs created (and retried) in a few conversations of two tenants,
// claims, renewals and releases by several owners, transitions from the right
// and the wrong status with valid and invalid details, clock moves, and
// ListResumable and Unfinished reads. Every result, and the stored lease
// after every step, is predicted by an independent model of the lease rules
// and the lifecycle; the invariants listed on runRig are checked at the end.
// A failure names its seed and step.
func SimulateRuns(t *testing.T, newStore func(t *testing.T, clk clock.Clock) store.RunStore, seed uint64) {
	t.Helper()
	simulateRuns(t, newRunRig(t, newStore), seed)
}

// SimulateAdmin is [SimulateRuns] for a store with an [store.Admin] view, as
// [RunAdmin] takes it: every ListResumable read is followed by a
// ListResumableAll at the same time, compared with the model of both tenants.
func SimulateAdmin(t *testing.T, newStore func(t *testing.T, clk clock.Clock) (store.RunStore, store.Admin), seed uint64) {
	t.Helper()
	simulateRuns(t, newAdminRig(t, newStore), seed)
}

func simulateRuns(t *testing.T, r *runRig, seed uint64) {
	t.Helper()
	s := &runSim{
		t:     t,
		r:     r,
		rng:   rand.New(rand.NewPCG(seed, 0x5851f42d4c957f2d)),
		seed:  seed,
		ctxs:  map[string]context.Context{tenantA: r.ctx(tenantA), tenantB: r.ctx(tenantB)},
		runs:  map[logKey]*simRun{},
		busy:  map[logKey]string{},
		order: map[string][]string{},
	}
	steps := 30 + s.rng.IntN(150)
	for s.step = 0; s.step < steps; s.step++ {
		s.do()
	}
}

type runSim struct {
	t    *testing.T
	r    *runRig
	rng  *rand.Rand
	seed uint64
	step int
	n    int
	ctxs map[string]context.Context

	runs  map[logKey]*simRun // (tenant, run id)
	busy  map[logKey]string  // (tenant, conversation) -> its unfinished run
	order map[string][]string
}

type simRun struct {
	conv    string
	created time.Time
	status  store.Status
	lease   store.Lease
	retry   bool
	next    time.Time
}

// resumable restates the rule of ListResumable for the model.
func (m *simRun) resumable(now time.Time) bool {
	return (m.status == store.StatusRunning && (m.lease.Owner == "" || !now.Before(m.lease.Expires))) ||
		(m.status == store.StatusFailed && m.retry && !now.Before(m.next))
}

var simOwners = []string{"o0", "o1", "o2"}

func (s *runSim) fatalf(format string, args ...any) {
	s.t.Helper()
	s.t.Fatalf("seed %d step %d: %s", s.seed, s.step, fmt.Sprintf(format, args...))
}

func (s *runSim) tenant() string {
	return []string{tenantA, tenantB}[s.rng.IntN(2)]
}

// pick returns a run of the tenant, or false if it has none.
func (s *runSim) pick(tenant string) (string, *simRun, bool) {
	ids := s.order[tenant]
	if len(ids) == 0 {
		return "", nil, false
	}
	id := ids[s.rng.IntN(len(ids))]
	return id, s.runs[logKey{tenant, id}], true
}

func (s *runSim) do() {
	tenant := s.tenant()
	ctx := s.ctxs[tenant]
	switch op := s.rng.IntN(100); {
	case op < 15:
		s.create(ctx, tenant)
	case op < 50:
		s.lease(ctx, tenant, op < 35)
	case op < 60:
		s.release(ctx, tenant)
	case op < 82:
		s.transition(ctx, tenant)
	case op < 92:
		s.r.clk.Advance(time.Duration(s.rng.IntN(4000)) * time.Millisecond)
	default:
		s.checkReads(ctx, tenant)
	}
}

func (s *runSim) create(ctx context.Context, tenant string) {
	conv := fmt.Sprintf("c%d", s.rng.IntN(3))
	id := fmt.Sprintf("r%03d", s.n) // ids sort in creation order
	if old, _, ok := s.pick(tenant); ok && s.rng.IntN(4) == 0 {
		id = old // a retry, possibly into another conversation
	} else {
		s.n++
	}
	m, exists := s.runs[logKey{tenant, id}]
	busy, isBusy := s.busy[logKey{tenant, conv}]
	var want error
	switch {
	case exists && m.conv != conv:
		want = store.ErrConflict
	case exists:
	case isBusy:
		want = store.ErrUnfinished
	}
	if _, err := s.r.create(ctx, id, conv); !isErr(err, want) {
		s.fatalf("Create %s/%s in %s (exists %v, busy with %q) = %v, want %v", tenant, id, conv, exists, busy, err, want)
	}
	if want == nil && !exists {
		s.runs[logKey{tenant, id}] = &simRun{conv: conv, created: s.r.clk.Now(), status: store.StatusRunning}
		s.busy[logKey{tenant, conv}] = id
		s.order[tenant] = append(s.order[tenant], id)
	}
}

func (s *runSim) lease(ctx context.Context, tenant string, claim bool) {
	id, m, ok := s.pick(tenant)
	if !ok {
		return
	}
	owner := simOwners[s.rng.IntN(len(simOwners))]
	ttl := time.Duration(1+s.rng.IntN(5)) * time.Second
	now := s.r.clk.Now()
	held := m.lease.Owner != "" && now.Before(m.lease.Expires)
	var want error
	switch {
	case m.status.Final():
		want = store.ErrFinal
	case held && m.lease.Owner != owner:
		want = store.ErrLeased
	case !held && !claim:
		want = store.ErrLeaseLost
	}
	var l store.Lease
	var err error
	if claim {
		l, err = s.r.s.Claim(ctx, id, owner, ttl)
	} else {
		l, err = s.r.s.Renew(ctx, id, owner, ttl)
	}
	if !isErr(err, want) {
		s.fatalf("Claim (%v) %s/%s by %s with lease %+v at %v = %v, want %v", claim, tenant, id, owner, m.lease, now, err, want)
	}
	if err == nil {
		m.lease = store.Lease{Owner: owner, Expires: now.Add(ttl)}
		if !sameLease(l, m.lease) {
			s.fatalf("lease = %+v, want %+v", l, m.lease)
		}
	}
	s.checkLease(ctx, tenant, id, m)
}

func (s *runSim) release(ctx context.Context, tenant string) {
	id, m, ok := s.pick(tenant)
	if !ok {
		return
	}
	owner := simOwners[s.rng.IntN(len(simOwners))]
	now := s.r.clk.Now()
	var want error
	if m.lease.Owner != "" && m.lease.Owner != owner && now.Before(m.lease.Expires) {
		want = store.ErrLeased
	}
	if err := s.r.s.Release(ctx, id, owner); !isErr(err, want) {
		s.fatalf("Release %s/%s by %s with lease %+v at %v = %v, want %v", tenant, id, owner, m.lease, now, err, want)
	}
	if m.lease.Owner == owner {
		m.lease = store.Lease{}
	}
	s.checkLease(ctx, tenant, id, m)
}

func (s *runSim) transition(ctx context.Context, tenant string) {
	id, m, ok := s.pick(tenant)
	if !ok {
		return
	}
	from := m.status
	if s.rng.IntN(3) == 0 {
		from = allStatuses[s.rng.IntN(len(allStatuses))]
	}
	to := allStatuses[s.rng.IntN(len(allStatuses))]
	now := s.r.clk.Now()
	d := detailsFor(to, s.step, now)
	valid := true
	if to == store.StatusFailed {
		d.Retryable = s.rng.IntN(3) > 0
		d.NextAttemptAt = time.Time{}
		if d.Retryable {
			d.NextAttemptAt = now.Add(time.Duration(s.rng.IntN(6000)-2000) * time.Millisecond)
		}
	}
	if s.rng.IntN(8) == 0 { // details that do not fit
		valid = false
		switch to {
		case store.StatusSuspended:
			d.Pending = nil
		case store.StatusFailed:
			d.Retryable, d.NextAttemptAt = false, now
		default:
			d.Pending = []string{"main/x"}
		}
	}
	var want error
	switch {
	case m.status.Final():
		want = store.ErrFinal
	case !allowedMoves[[2]store.Status{from, to}] || !valid:
		want = store.ErrInvalid
	case m.status != from:
		want = store.ErrConflict
	}
	if _, err := s.r.transition(ctx, id, from, to, d); !isErr(err, want) {
		s.fatalf("Transition %s/%s (%s) %s → %s (valid details %v) = %v, want %v", tenant, id, m.status, from, to, valid, err, want)
	}
	if want != nil {
		return
	}
	m.status, m.retry, m.next = to, d.Retryable, d.NextAttemptAt
	if to.Final() {
		delete(s.busy, logKey{tenant, m.conv})
	}
}

// checkReads compares ListResumable (at a time near now), ListResumableAll
// when there is an Admin, and Unfinished with the model.
func (s *runSim) checkReads(ctx context.Context, tenant string) {
	now := s.r.clk.Now().Add(time.Duration(s.rng.IntN(4000)-2000) * time.Millisecond)
	var want []string
	for _, id := range s.order[tenant] {
		if s.runs[logKey{tenant, id}].resumable(now) {
			want = append(want, id)
		}
	}
	got, err := s.r.s.ListResumable(ctx, now, store.MaxPageLimit)
	// Runs are created in id order at non-decreasing times, so creation
	// order is the order ListResumable gives.
	if err != nil || !slices.Equal(runIDs(got), want) {
		s.fatalf("ListResumable %s at %v = %v, %v; want %v", tenant, now, runIDs(got), err, want)
	}
	if s.r.admin != nil {
		s.checkAdmin(ctx, now)
	}
	for c := range 3 {
		conv := fmt.Sprintf("c%d", c)
		run, err := s.r.s.Unfinished(ctx, conv)
		busy, ok := s.busy[logKey{tenant, conv}]
		if ok != (err == nil) || run.ID != busy {
			s.fatalf("Unfinished %s/%s = %q, %v; want %q", tenant, conv, run.ID, err, busy)
		}
	}
}

func (s *runSim) checkLease(ctx context.Context, tenant, id string, m *simRun) {
	if got := s.r.mustGet(ctx, id).Lease; !sameLease(got, m.lease) {
		s.fatalf("%s/%s lease is %+v, want %+v", tenant, id, got, m.lease)
	}
}

// checkAdmin compares ListResumableAll at now with the model of both tenants.
// ctx carries a tenant, which the call must ignore.
func (s *runSim) checkAdmin(ctx context.Context, now time.Time) {
	type item struct {
		created    time.Time
		tenant, id string
	}
	var items []item
	for k, m := range s.runs {
		if m.resumable(now) {
			items = append(items, item{m.created, k.tenant, k.conv})
		}
	}
	slices.SortFunc(items, func(a, b item) int {
		return cmp.Or(a.created.Compare(b.created), strings.Compare(a.tenant, b.tenant), strings.Compare(a.id, b.id))
	})
	want := make([]string, len(items))
	for i, it := range items {
		want[i] = it.tenant + "/" + it.id
	}
	got, err := s.r.admin.ListResumableAll(ctx, now, store.MaxPageLimit)
	if err != nil || !slices.Equal(tenantRunIDs(got), want) {
		s.fatalf("ListResumableAll at %v = %v, %v; want %v", now, tenantRunIDs(got), err, want)
	}
}
