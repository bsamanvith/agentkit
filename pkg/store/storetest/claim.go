package storetest

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/store"
)

// runClaimNext is the part of [RunAdmin] on ClaimNext: the queue order,
// lanes, runs that are not due, the lease it takes, its arguments, and
// claimers racing.
func runClaimNext(t *testing.T, newStore func(t *testing.T, clk clock.Clock) (store.RunStore, store.Admin)) {
	t.Helper()

	// Within a lane: the highest priority first, and within a priority the
	// earliest due, whatever made the run due (its start, a lapsed lease, a
	// next attempt) and whichever tenant it is in; equal due times go by
	// tenant id, then run ID.
	t.Run("ClaimNextTakesPriorityThenDueTime", func(t *testing.T) {
		t.Parallel()
		r := newAdminRig(t, newStore)
		a, b := r.ctx(tenantA), r.ctx(tenantB)
		t0 := r.clk.Now()
		at := func(d time.Duration) store.CreateOption { return store.WithStartAt(t0.Add(d)) }
		lane := store.WithLane("work")

		r.mustCreate(b, "low", "c1", lane, store.WithPriority(-1), at(-time.Hour)) // the oldest, but the lowest priority
		r.mustCreate(a, "p0-late", "c2", lane, at(2*time.Second))
		r.mustCreate(a, "p0-early", "c3", lane, at(time.Second))
		r.mustCreate(b, "p0-early", "c3", lane, at(time.Second)) // same due time: tenant A's goes first
		r.mustCreate(a, "p0-early-z", "c4", lane, at(time.Second))
		r.mustCreate(b, "p5-start", "c5", lane, store.WithPriority(5), at(3*time.Second))
		// Due when its next attempt is, after p5-start's start, though it
		// started at once.
		r.mustCreate(a, "p5-retry", "c6", lane, store.WithPriority(5))
		r.mustMove(a, "p5-retry", store.StatusRunning, store.StatusFailed, store.Details{Error: "boom", Retryable: true, NextAttemptAt: t0.Add(4 * time.Second)})
		// Due when its lease lapses, though it started first.
		r.mustCreate(b, "p5-lapsed", "c7", lane, store.WithPriority(5))
		if _, err := r.s.Claim(b, "p5-lapsed", "dead", 2*time.Second); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		r.mustCreate(a, "p5-max", "c8", lane, store.WithPriority(store.MaxPriority), at(5*time.Second))
		r.clk.Advance(5 * time.Second)

		want := []string{
			tenantA + "/p5-max",
			tenantB + "/p5-lapsed", tenantB + "/p5-start", tenantA + "/p5-retry",
			tenantA + "/p0-early", tenantA + "/p0-early-z", tenantB + "/p0-early", tenantA + "/p0-late",
			tenantB + "/low",
		}
		var got []string
		for range len(want) + 1 {
			tr, err := r.admin.ClaimNext(t.Context(), []string{"work"}, "worker", time.Minute)
			if errors.Is(err, store.ErrNotFound) {
				break
			}
			if err != nil {
				t.Fatalf("ClaimNext after %v: %v", got, err)
			}
			got = append(got, tr.Tenant.ID()+"/"+tr.Run.ID)
		}
		if !slices.Equal(got, want) {
			t.Errorf("ClaimNext took\n%v\nwant\n%v", got, want)
		}
	})

	// A runner claims only from the lanes it names; across several, the
	// order is the same as within one.
	t.Run("ClaimNextKeepsLanesApart", func(t *testing.T) {
		t.Parallel()
		r := newAdminRig(t, newStore)
		a := r.ctx(tenantA)
		r.mustCreate(a, "default", "c1")
		r.mustCreate(a, "batch", "c2", store.WithLane("batch"))
		r.mustCreate(a, "urgent", "c3", store.WithLane("urgent"), store.WithPriority(1))
		r.mustCreate(a, "batch-2", "c4", store.WithLane("batch"))
		r.clk.Advance(time.Second)

		claim := func(lanes ...string) string {
			t.Helper()
			tr, err := r.admin.ClaimNext(t.Context(), lanes, "worker", time.Minute)
			if errors.Is(err, store.ErrNotFound) {
				return ""
			}
			if err != nil {
				t.Fatalf("ClaimNext %q: %v", lanes, err)
			}
			if !slices.Contains(lanes, tr.Run.Lane) {
				t.Errorf("ClaimNext %q took run %q of lane %q", lanes, tr.Run.ID, tr.Run.Lane)
			}
			return tr.Run.ID
		}
		for _, c := range []struct {
			lanes []string
			want  string
		}{
			{[]string{"nobody"}, ""},
			{[]string{"batch"}, "batch"},
			{[]string{"batch", "urgent", ""}, "urgent"},
			{[]string{"", "batch", "batch"}, "batch-2"}, // due when "default" is, and its id sorts first
			{[]string{"batch", ""}, "default"},
			{[]string{"batch", "urgent", ""}, ""},
		} {
			if got := claim(c.lanes...); got != c.want {
				t.Errorf("ClaimNext %q = %q, want %q", c.lanes, got, c.want)
			}
		}
	})

	// Runs that are not due are never claimed, nor runs that are not in the
	// queue at all; once due, they are.
	t.Run("ClaimNextNeverTakesARunThatIsNotDue", func(t *testing.T) {
		t.Parallel()
		r := newAdminRig(t, newStore)
		a, b := r.ctx(tenantA), r.ctx(tenantB)
		t0 := r.clk.Now()
		r.mustCreate(a, "later", "c1", store.WithStartAt(t0.Add(time.Minute)))
		r.mustCreate(a, "leased", "c2")
		if _, err := r.s.Claim(a, "leased", "other", 2*time.Minute); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		r.mustCreate(b, "retry-later", "c3")
		r.mustMove(b, "retry-later", store.StatusRunning, store.StatusFailed, store.Details{Error: "boom", Retryable: true, NextAttemptAt: t0.Add(3 * time.Minute)})
		r.mustCreate(b, "failed-for-good", "c4")
		r.mustMove(b, "failed-for-good", store.StatusRunning, store.StatusFailed, store.Details{Error: "boom"})
		r.mustCreate(b, "suspended", "c5")
		r.mustMove(b, "suspended", store.StatusRunning, store.StatusSuspended, store.Details{Pending: []string{"main/ask"}})
		r.mustCreate(b, "done", "c6")
		r.mustMove(b, "done", store.StatusRunning, store.StatusCompleted, store.Details{})
		r.mustCreate(a, "chat", "c7", store.Interactive())
		r.mustCreate(a, "chat-failed", "c8", store.Interactive())
		r.mustMove(a, "chat-failed", store.StatusRunning, store.StatusFailed, store.Details{Error: "boom", Retryable: true})

		claim := func() string {
			t.Helper()
			tr, err := r.admin.ClaimNext(t.Context(), []string{""}, "worker", time.Hour)
			if errors.Is(err, store.ErrNotFound) {
				return ""
			}
			if err != nil {
				t.Fatalf("ClaimNext: %v", err)
			}
			return tr.Run.ID
		}
		for _, step := range []struct {
			at   time.Duration
			want []string
		}{
			{time.Minute - time.Nanosecond, nil},
			{time.Minute, []string{"later"}},
			{2*time.Minute - time.Nanosecond, nil},
			{2 * time.Minute, []string{"leased"}}, // its lease expires
			{3 * time.Minute, []string{"retry-later"}},
			{50 * time.Minute, nil}, // what was claimed is held; the rest never is
		} {
			r.clk.Advance(t0.Add(step.at).Sub(r.clk.Now()))
			var got []string
			// At most a few: a store that hands out a held run again would
			// loop forever.
			for id := claim(); id != "" && len(got) < 5; id = claim() {
				got = append(got, id)
			}
			if !slices.Equal(got, step.want) {
				t.Errorf("ClaimNext at t0+%v took %v, want %v", step.at, got, step.want)
			}
		}
	})

	// The claim is an ordinary lease: the run comes back as stored, its
	// owner renews and releases it, and nobody else can claim it.
	t.Run("ClaimNextLeasesTheRun", func(t *testing.T) {
		t.Parallel()
		r := newAdminRig(t, newStore)
		b := r.ctx(tenantB)
		r.mustCreate(b, "r", "c", store.WithLane("l"), store.WithPriority(7))
		r.clk.Advance(time.Second)
		const ttl = 10 * time.Second
		tr, err := r.admin.ClaimNext(r.ctx(tenantA), []string{"l"}, "worker", ttl) // a tenant in ctx is ignored
		if err != nil {
			t.Fatalf("ClaimNext: %v", err)
		}
		want := store.Lease{Owner: "worker", Expires: r.clk.Now().Add(ttl)}
		stored := r.mustGet(b, "r")
		switch {
		case tr.Tenant.ID() != tenantB:
			t.Errorf("ClaimNext gave tenant %q, want %q", tr.Tenant.ID(), tenantB)
		case !sameLease(tr.Run.Lease, want):
			t.Errorf("ClaimNext gave lease %+v, want %+v", tr.Run.Lease, want)
		case !sameRun(tr.Run, stored):
			t.Errorf("ClaimNext gave\n%+v\nGet gives\n%+v", tr.Run, stored)
		case tr.Run.Lane != "l" || tr.Run.Priority != 7:
			t.Errorf("ClaimNext gave lane %q, priority %d; want l, 7", tr.Run.Lane, tr.Run.Priority)
		}
		if _, err := r.s.Claim(b, "r", "other", ttl); !errors.Is(err, store.ErrLeased) {
			t.Errorf("Claim by another owner = %v, want ErrLeased", err)
		}
		r.clk.Advance(ttl / 2)
		if _, err := r.s.Renew(b, "r", "worker", ttl); err != nil {
			t.Errorf("Renew by the claimer: %v", err)
		}
		if _, err := r.admin.ClaimNext(t.Context(), []string{"l"}, "other", ttl); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("ClaimNext of a held run = %v, want ErrNotFound", err)
		}
		if err := r.s.Release(b, "r", "worker"); err != nil {
			t.Fatalf("Release: %v", err)
		}
		if tr, err := r.admin.ClaimNext(t.Context(), []string{"l"}, "other", ttl); err != nil || tr.Run.ID != "r" {
			t.Errorf("ClaimNext after the release = %+v, %v; want run r", tr.Run, err)
		}
	})

	t.Run("ClaimNextChecksItsArguments", func(t *testing.T) {
		t.Parallel()
		r := newAdminRig(t, newStore)
		r.mustCreate(r.ctx(tenantA), "r", "c")
		cancelled, cancel := context.WithCancel(t.Context())
		cancel()
		for _, c := range []struct {
			name  string
			ctx   context.Context
			lanes []string
			owner string
			ttl   time.Duration
			want  error
		}{
			{"no lanes", t.Context(), nil, "w", time.Minute, store.ErrInvalid},
			{"an empty list of lanes", t.Context(), []string{}, "w", time.Minute, store.ErrInvalid},
			{"a lane that is not UTF-8", t.Context(), []string{"", "a\xffb"}, "w", time.Minute, store.ErrInvalid},
			{"a lane with a NUL", t.Context(), []string{"a\x00b"}, "w", time.Minute, store.ErrInvalid},
			{"no owner", t.Context(), []string{""}, "", time.Minute, store.ErrInvalid},
			{"an owner that is not UTF-8", t.Context(), []string{""}, "a\xffb", time.Minute, store.ErrInvalid},
			{"no time to live", t.Context(), []string{""}, "w", 0, store.ErrInvalid},
			{"a negative time to live", t.Context(), []string{""}, "w", -time.Second, store.ErrInvalid},
			{"a lease past what a store keeps", t.Context(), []string{""}, "w", time.Duration(math.MaxInt64), store.ErrInvalid},
			{"a cancelled context", cancelled, []string{""}, "w", time.Minute, context.Canceled},
		} {
			if tr, err := r.admin.ClaimNext(c.ctx, c.lanes, c.owner, c.ttl); !errors.Is(err, c.want) {
				t.Errorf("ClaimNext with %s = %+v, %v; want %v", c.name, tr.Run, err, c.want)
			}
		}
		if run := r.mustGet(r.ctx(tenantA), "r"); run.Lease != (store.Lease{}) {
			t.Errorf("after the refused claims run r has lease %+v, want none", run.Lease)
		}
	})

	// Claimers racing for a queue get every run exactly once between them.
	t.Run("ClaimersNeverGetTheSameRun", func(t *testing.T) {
		t.Parallel()
		r := newAdminRig(t, newStore)
		tenants := []context.Context{r.ctx(tenantA), r.ctx(tenantB)}
		const runs, claimers = 40, 8
		for i := range runs {
			id := fmt.Sprintf("r%02d", i)
			r.mustCreate(tenants[i%2], id, id, store.WithPriority(i%3))
		}
		r.clk.Advance(time.Second)
		var mu sync.Mutex
		got := map[string]string{} // tenant/run -> claimer
		errs := race(claimers, func(w int) error {
			owner := fmt.Sprintf("w%d", w)
			for {
				tr, err := r.admin.ClaimNext(context.Background(), []string{""}, owner, time.Hour)
				if errors.Is(err, store.ErrNotFound) {
					return nil
				}
				if err != nil {
					return err
				}
				k := tr.Tenant.ID() + "/" + tr.Run.ID
				mu.Lock()
				prev, twice := got[k]
				got[k] = owner
				mu.Unlock()
				if twice {
					return fmt.Errorf("run %s claimed by %s and %s", k, prev, owner)
				}
				if tr.Run.Lease.Owner != owner {
					return fmt.Errorf("run %s claimed by %s came back with lease %+v", k, owner, tr.Run.Lease)
				}
			}
		})
		for w, err := range errs {
			if err != nil {
				t.Errorf("claimer %d: %v", w, err)
			}
		}
		if len(got) != runs {
			t.Errorf("the claimers took %d runs between them, want all %d", len(got), runs)
		}
		for k, owner := range got {
			tenant, id, _ := cutTenantRun(k)
			if run := r.mustGet(r.ctx(tenant), id); run.Lease.Owner != owner {
				t.Errorf("run %s is leased to %q, want its claimer %q", k, run.Lease.Owner, owner)
			}
		}
	})
}

// cutTenantRun splits a "tenant/id" name.
func cutTenantRun(k string) (tenant, id string, ok bool) { return strings.Cut(k, "/") }
