package storetest

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// RunAdmin runs the conformance suite for a [store.Admin]. newStore must
// return an empty store's tenant-scoped [store.RunStore] and its Admin view of
// the same data, both reading run times and lease expiry from clk (a fake
// clock the suite advances).
//
// Every test goes through the helper of [RunRunStore], which checks its
// invariants when the test ends and, with an Admin, one more: ListResumableAll
// is the union of every tenant's ListResumable, re-sorted. ClaimNext is tested
// for the queue's order, lanes, runs that are not due, the lease it takes,
// its arguments, and claimers racing. The suite ends with seeded simulations
// ([SimulateAdmin], which claims too) through the same helper.
func RunAdmin(t *testing.T, newStore func(t *testing.T, clk clock.Clock) (store.RunStore, store.Admin)) {
	t.Helper()

	t.Run("ListsTheResumableRunsOfEveryTenant", func(t *testing.T) {
		t.Parallel()
		r := newAdminRig(t, newStore)
		a, b, c := r.ctx(tenantA), r.ctx(tenantB), r.ctx(tenantC)
		const ttl = 10 * time.Second
		t0 := r.clk.Now()

		// Tenants A and B get the same runs, with the same run and
		// conversation ids, one per case, each created a millisecond after
		// the last.
		ab := []context.Context{a, b}
		run := func(id string) {
			r.clk.Advance(time.Millisecond)
			for _, ctx := range ab {
				r.mustCreate(ctx, id, id)
			}
		}
		claim := func(ctx context.Context, id string, ttl time.Duration) {
			if _, err := r.s.Claim(ctx, id, "worker", ttl); err != nil {
				t.Fatalf("Claim %s: %v", id, err)
			}
		}
		fail := func(ctx context.Context, id string, d store.Details) {
			d.Error = "boom"
			r.mustMove(ctx, id, store.StatusRunning, store.StatusFailed, d)
		}

		r.mustCreate(c, "only-c", "conv") // the oldest run, though its tenant sorts last
		run("live")
		claim(a, "live", time.Hour)
		claim(b, "live", time.Hour)
		run("expired")
		claim(a, "expired", ttl)
		claim(b, "expired", ttl)
		expires := r.clk.Now().Add(ttl)
		run("unclaimed")
		run("released")
		for _, ctx := range ab {
			claim(ctx, "released", time.Hour)
			if err := r.s.Release(ctx, "released", "worker"); err != nil {
				t.Fatalf("Release: %v", err)
			}
		}
		run("suspended")
		run("paused")
		run("due")
		run("due-now")
		run("not-due")
		run("failed-for-good")
		for _, ctx := range ab {
			r.mustMove(ctx, "suspended", store.StatusRunning, store.StatusSuspended, store.Details{Pending: []string{"main/ask"}})
			r.mustMove(ctx, "paused", store.StatusRunning, store.StatusPaused, store.Details{})
			fail(ctx, "due", store.Details{Retryable: true, NextAttemptAt: t0.Add(5 * time.Second)})
			fail(ctx, "due-now", store.Details{Retryable: true, NextAttemptAt: expires})
			fail(ctx, "not-due", store.Details{Retryable: true, NextAttemptAt: expires.Add(time.Nanosecond)})
			fail(ctx, "failed-for-good", store.Details{})
		}
		for _, s := range []store.Status{store.StatusCompleted, store.StatusRejected, store.StatusCanceled} {
			run(string(s))
			for _, ctx := range ab {
				r.mustMove(ctx, string(s), store.StatusRunning, s, store.Details{})
			}
		}
		// The same id resumable in both tenants as two different runs, and
		// resumable in one tenant only.
		run("shared")
		fail(b, "shared", store.Details{Retryable: true, NextAttemptAt: t0})
		run("one-side")
		claim(a, "one-side", time.Hour)

		r.clk.Advance(expires.Sub(r.clk.Now())) // "expired" expires now
		now := r.clk.Now()
		list := func(now time.Time) []string {
			t.Helper()
			got, err := r.admin.ListResumableAll(t.Context(), now, 0)
			if err != nil {
				t.Fatalf("ListResumableAll: %v", err)
			}
			return tenantRunIDs(got)
		}
		inAB := func(ids ...string) []string {
			var out []string
			for _, id := range ids {
				out = append(out, tenantA+"/"+id, tenantB+"/"+id)
			}
			return out
		}
		want := slices.Concat(
			[]string{tenantC + "/only-c"},
			inAB("expired", "unclaimed", "released", "due", "due-now", "shared"),
			[]string{tenantB + "/one-side"},
		)
		if got := list(now); !slices.Equal(got, want) {
			t.Errorf("ListResumableAll at %v =\n%v\nwant\n%v", now, got, want)
		}
		got, err := r.admin.ListResumableAll(t.Context(), now, 0)
		if err != nil {
			t.Fatalf("ListResumableAll: %v", err)
		}
		for _, tr := range got {
			if tr.Run.ID != "shared" {
				continue
			}
			wantStatus := map[string]store.Status{tenantA: store.StatusRunning, tenantB: store.StatusFailed}[tr.Tenant.ID()]
			if tr.Run.Status != wantStatus {
				t.Errorf("run shared of tenant %s is %s, want that tenant's run, %s", tr.Tenant.ID(), tr.Run.Status, wantStatus)
			}
		}

		// The time passed in decides, not the store's clock, in both
		// directions.
		want = slices.Concat(
			[]string{tenantC + "/only-c"},
			inAB("unclaimed", "released", "due", "shared"),
			[]string{tenantB + "/one-side"},
		)
		if got := list(now.Add(-time.Nanosecond)); !slices.Equal(got, want) {
			t.Errorf("ListResumableAll just before the lease expired =\n%v\nwant\n%v", got, want)
		}
		want = slices.Concat(
			[]string{tenantC + "/only-c"},
			inAB("expired", "unclaimed", "released", "due", "due-now", "not-due", "shared"),
			[]string{tenantB + "/one-side"},
		)
		if got := list(now.Add(time.Nanosecond)); !slices.Equal(got, want) {
			t.Errorf("ListResumableAll just after the next attempt was due =\n%v\nwant\n%v", got, want)
		}
		want = slices.Concat(
			[]string{tenantC + "/only-c"},
			inAB("live", "expired", "unclaimed", "released", "due", "due-now", "not-due", "shared", "one-side"),
		)
		if got := list(now.Add(2 * time.Hour)); !slices.Equal(got, want) {
			t.Errorf("ListResumableAll once every lease expired =\n%v\nwant\n%v", got, want)
		}
	})

	t.Run("OrderIsStableAndLimitIsHonored", func(t *testing.T) {
		t.Parallel()
		r := newAdminRig(t, newStore)
		a, b, c := r.ctx(tenantA), r.ctx(tenantB), r.ctx(tenantC)
		r.mustCreate(c, "m", "m") // the oldest, though its tenant sorts last
		r.clk.Advance(time.Second)
		// Created at one instant, in an order that is neither the tenants'
		// nor the ids'.
		r.mustCreate(c, "a", "a")
		r.mustCreate(b, "b", "b")
		r.mustCreate(a, "z", "z")
		r.mustCreate(a, "y", "y")
		r.mustCreate(b, "a", "a")
		r.clk.Advance(time.Second)
		r.mustCreate(a, "a", "a") // the newest, though its tenant and id sort first
		want := []string{tenantC + "/m", tenantA + "/y", tenantA + "/z", tenantB + "/a", tenantB + "/b", tenantC + "/a", tenantA + "/a"}

		now := r.clk.Now()
		list := func(limit int) []store.TenantRun {
			t.Helper()
			got, err := r.admin.ListResumableAll(t.Context(), now, limit)
			if err != nil {
				t.Fatalf("ListResumableAll with limit %d: %v", limit, err)
			}
			return got
		}
		first := list(0)
		if got := tenantRunIDs(first); !slices.Equal(got, want) {
			t.Errorf("ListResumableAll = %v, want %v ordered by CreatedAt, tenant and ID", got, want)
		}
		for limit := 1; limit <= len(want)+1; limit++ {
			if got := tenantRunIDs(list(limit)); !slices.Equal(got, want[:min(limit, len(want))]) {
				t.Errorf("ListResumableAll with limit %d = %v, want %v", limit, got, want[:min(limit, len(want))])
			}
		}
		if again := list(0); !sameTenantRuns(again, first) {
			t.Errorf("ListResumableAll again = %v, want the same as before %v", tenantRunIDs(again), tenantRunIDs(first))
		}
		if _, err := r.admin.ListResumableAll(t.Context(), now, -1); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("ListResumableAll with a negative limit = %v, want ErrInvalid", err)
		}

		// A zero limit is the default page size, as for ListResumable.
		tenants := []context.Context{a, b, c}
		for i := range store.DefaultPageLimit {
			r.mustCreate(tenants[i%len(tenants)], fmt.Sprintf("more-%03d", i), fmt.Sprintf("more-%03d", i))
		}
		all := list(store.MaxPageLimit)
		if len(all) != len(want)+store.DefaultPageLimit {
			t.Fatalf("ListResumableAll = %d runs, want %d", len(all), len(want)+store.DefaultPageLimit)
		}
		if got := list(0); !sameTenantRuns(got, all[:store.DefaultPageLimit]) {
			t.Errorf("ListResumableAll with limit 0 = %d runs, want the first %d", len(got), store.DefaultPageLimit)
		}
	})

	t.Run("LimitAboveTheMaximumIsLowered", func(t *testing.T) {
		t.Parallel()
		r := newAdminRig(t, newStore)
		tenants := []context.Context{r.ctx(tenantA), r.ctx(tenantB), r.ctx(tenantC)}
		for i := range store.MaxPageLimit + 1 {
			id := fmt.Sprintf("r-%04d", i)
			r.mustCreate(tenants[i%len(tenants)], id, id)
		}
		got, err := r.admin.ListResumableAll(t.Context(), r.clk.Now(), store.MaxPageLimit+50)
		if err != nil || len(got) != store.MaxPageLimit {
			t.Errorf("ListResumableAll with a limit above the maximum = %d runs, %v; want %d", len(got), err, store.MaxPageLimit)
		}
	})

	// Writers in two tenants and readers with no tenant, released at once.
	// Under the race detector this checks that the Admin is safe for
	// concurrent use; whatever the schedule, every read is in order, without
	// duplicates, and holds only resumable runs.
	t.Run("Concurrently", func(t *testing.T) {
		t.Parallel()
		r := newAdminRig(t, newStore)
		tenants := []context.Context{r.ctx(tenantA), r.ctx(tenantB)}
		now := r.clk.Now()
		const writers, readers, steps = 4, 4, 25
		errs := race(writers+readers, func(w int) error {
			if w < writers {
				ctx := tenants[w%len(tenants)]
				for i := range steps {
					id := fmt.Sprintf("w%d-%02d", w, i)
					if _, err := r.create(ctx, id, id); err != nil {
						return err
					}
					if i%2 == 0 {
						continue
					}
					if _, err := r.transition(ctx, id, store.StatusRunning, store.StatusCompleted, store.Details{}); err != nil {
						return err
					}
				}
				return nil
			}
			for range steps {
				got, err := r.admin.ListResumableAll(context.Background(), now, store.MaxPageLimit)
				if err != nil {
					return err
				}
				for i, tr := range got {
					if !resumable(tr.Run, now) {
						return fmt.Errorf("ListResumableAll returned %s/%s, which is %s", tr.Tenant.ID(), tr.Run.ID, tr.Run.Status)
					}
					if i > 0 && adminOrder(got[i-1], tr) >= 0 {
						return fmt.Errorf("ListResumableAll is out of order or repeats a run: %v", tenantRunIDs(got))
					}
				}
			}
			return nil
		})
		for w, err := range errs {
			if err != nil {
				t.Errorf("worker %d: %v", w, err)
			}
		}
	})

	// The rules of ListResumable hold across tenants too: a retryable
	// failure with no next attempt time is due as soon as it has started,
	// and now may be any time, even one no store could keep.
	t.Run("RetryableFailureWithNoNextAttemptAndAnyNow", func(t *testing.T) {
		t.Parallel()
		r := newAdminRig(t, newStore)
		a, b := r.ctx(tenantA), r.ctx(tenantB)
		r.mustCreate(a, "f", "c")
		r.mustMove(a, "f", store.StatusRunning, store.StatusFailed, store.Details{Error: "boom", Retryable: true})
		r.clk.Advance(time.Millisecond)
		r.mustCreate(b, "r", "c")
		for _, c := range []struct {
			now  time.Time
			want []string
		}{
			{r.clk.Now(), []string{"f", "r"}},
			{farFuture, []string{"f", "r"}},
			{r.clk.Now().Add(-time.Millisecond), []string{"f"}}, // r has not started
			{farPast, nil}, // before any run could start (T099: StartAt)
		} {
			got, err := r.admin.ListResumableAll(context.Background(), c.now, 0)
			var ids []string
			for _, tr := range got {
				ids = append(ids, tr.Run.ID)
			}
			if err != nil || !slices.Equal(ids, c.want) {
				t.Errorf("ListResumableAll in the year %d = %v, %v; want %v", c.now.Year(), ids, err, c.want)
			}
		}
	})

	t.Run("NeedsNoTenantAndIgnoresOne", func(t *testing.T) {
		t.Parallel()
		r := newAdminRig(t, newStore)
		r.mustCreate(r.ctx(tenantA), "r", "c")
		r.mustCreate(r.ctx(tenantB), "r", "c")
		want := []string{tenantA + "/r", tenantB + "/r"}
		for name, ctx := range map[string]context.Context{
			"no tenant":            t.Context(),
			"tenant A":             r.ctx(tenantA),
			"a tenant with no run": r.ctx(unusedTenant),
		} {
			got, err := r.admin.ListResumableAll(ctx, r.clk.Now(), 0)
			if err != nil || !slices.Equal(tenantRunIDs(got), want) {
				t.Errorf("ListResumableAll with %s = %v, %v; want %v", name, tenantRunIDs(got), err, want)
			}
		}
	})

	t.Run("CancelledContextIsReported", func(t *testing.T) {
		t.Parallel()
		r := newAdminRig(t, newStore)
		r.mustCreate(r.ctx(tenantA), "r", "c")
		for name, parent := range map[string]context.Context{"no tenant": t.Context(), "a tenant": r.ctx(tenantA)} {
			ctx, cancel := context.WithCancel(parent)
			cancel()
			if got, err := r.admin.ListResumableAll(ctx, r.clk.Now(), 0); !errors.Is(err, context.Canceled) {
				t.Errorf("ListResumableAll with %s = %v, %v; want context.Canceled", name, tenantRunIDs(got), err)
			}
		}
	})

	// Interactive runs of every tenant are left out, whatever their lease;
	// background runs beside them are listed.
	t.Run("SkipsInteractiveRuns", func(t *testing.T) {
		t.Parallel()
		r := newAdminRig(t, newStore)
		for _, ctx := range []context.Context{r.ctx(tenantA), r.ctx(tenantB)} {
			r.mustCreate(ctx, "chat", "c1", store.Interactive())
			r.mustCreate(ctx, "chat-failed", "c2", store.Interactive())
			r.mustMove(ctx, "chat-failed", store.StatusRunning, store.StatusFailed, store.Details{Error: "boom", Retryable: true})
			r.mustCreate(ctx, "job", "c3")
		}
		got, err := r.admin.ListResumableAll(context.Background(), r.clk.Now(), 0)
		if want := []string{tenantA + "/job", tenantB + "/job"}; err != nil || !slices.Equal(tenantRunIDs(got), want) {
			t.Errorf("ListResumableAll = %v, %v; want %v", tenantRunIDs(got), err, want)
		}
	})

	runClaimNext(t, newStore)

	t.Run("Simulation", func(t *testing.T) {
		t.Parallel()
		for seed := range uint64(24) {
			t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
				t.Parallel()
				SimulateAdmin(t, newStore, seed)
			})
		}
	})
}

// checkAdmin checks run invariant 7: ListResumableAll at the clock's time,
// with no tenant in ctx, is the union of every tenant's ListResumable (which
// invariant 6 checks against the rule), each run with its tenant, re-sorted.
// The caller holds r.mu.
func (r *runRig) checkAdmin() {
	t := r.t
	now := r.clk.Now()
	var want []store.TenantRun
	for _, tenant := range sortedKeys(r.tenants) {
		ctx := tenantCtx(context.Background(), t, tenant)
		runs, err := r.s.ListResumable(ctx, now, store.MaxPageLimit)
		if err != nil {
			t.Errorf("tenant %s: ListResumable: %v", tenant, err)
			return
		}
		for _, run := range runs {
			want = append(want, store.TenantRun{Tenant: tenancy.MustFrom(ctx), Run: run})
		}
	}
	slices.SortFunc(want, adminOrder)
	want = want[:min(len(want), store.MaxPageLimit)]
	got, err := r.admin.ListResumableAll(context.Background(), now, store.MaxPageLimit)
	if err != nil || !sameTenantRuns(got, want) {
		t.Errorf("ListResumableAll at %v = %v, %v; want the union of every tenant's ListResumable %v", now, tenantRunIDs(got), err, tenantRunIDs(want))
	}
}

// adminOrder restates the order of ListResumableAll: CreatedAt, then tenant
// id, then run ID.
func adminOrder(a, b store.TenantRun) int {
	return cmp.Or(
		a.Run.CreatedAt.Compare(b.Run.CreatedAt),
		strings.Compare(a.Tenant.ID(), b.Tenant.ID()),
		strings.Compare(a.Run.ID, b.Run.ID),
	)
}

// tenantRunIDs names each run as "tenant/id".
func tenantRunIDs(rs []store.TenantRun) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Tenant.ID() + "/" + r.Run.ID
	}
	return out
}

func sameTenantRuns(a, b []store.TenantRun) bool {
	return slices.EqualFunc(a, b, func(x, y store.TenantRun) bool {
		return x.Tenant == y.Tenant && sameRun(x.Run, y.Run)
	})
}
