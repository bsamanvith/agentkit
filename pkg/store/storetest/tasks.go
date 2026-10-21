package storetest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// RunIndexStore is what [RunRunIndex] tests: a backend's runs, listed across
// conversations, with the conversation records that name their agents.
type RunIndexStore interface {
	store.RunStore
	store.ConversationIndex
	store.RunIndex
}

// RunRunIndex runs the conformance suite for a [store.RunIndex]. newStore
// must return an empty store that reads its times from clk (a fake clock the
// suite advances). Every test checks, when it ends, that the unfiltered
// ListRuns of each tenant it used, paged in both orders, is every run of the
// tenant's conversations exactly once, in order; and that a tenant that never
// wrote lists none.
func RunRunIndex(t *testing.T, newStore func(t *testing.T, clk clock.Clock) RunIndexStore) {
	t.Helper()

	t.Run("FiltersByStatusKindAndAgent", func(t *testing.T) {
		t.Parallel()
		r := newRunIndexRig(t, newStore)
		a := r.ctx(tenantA)
		r.conv(a, "c-alpha", "alpha")
		r.conv(a, "c-beta", "beta")
		// Runs of the same conversation one after the other: each is final
		// before the next is created.
		r.run(a, "a1", "c-alpha", false, store.StatusCompleted)
		r.run(a, "a2", "c-alpha", true, store.StatusCanceled)
		r.run(a, "a3", "c-alpha", false, store.StatusRunning)
		r.run(a, "b1", "c-beta", true, store.StatusRunning)
		r.run(a, "n1", "no-record", false, store.StatusFailed)

		for _, c := range []struct {
			name string
			f    store.RunFilter
			want []string
		}{
			{"all", store.RunFilter{}, []string{"a1", "a2", "a3", "b1", "n1"}},
			{"running", store.RunFilter{Status: store.StatusRunning}, []string{"a3", "b1"}},
			{"background", store.RunFilter{Kind: store.BackgroundRuns}, []string{"a1", "a3", "n1"}},
			{"interactive", store.RunFilter{Kind: store.InteractiveRuns}, []string{"a2", "b1"}},
			{"running background", store.RunFilter{Status: store.StatusRunning, Kind: store.BackgroundRuns}, []string{"a3"}},
			{"agent alpha", store.RunFilter{Agent: "alpha"}, []string{"a1", "a2", "a3"}},
			{"agent beta, interactive, running", store.RunFilter{Agent: "beta", Kind: store.InteractiveRuns, Status: store.StatusRunning}, []string{"b1"}},
			{"an agent nobody has", store.RunFilter{Agent: "gamma"}, nil},
			{"failed", store.RunFilter{Status: store.StatusFailed}, []string{"n1"}},
		} {
			for _, o := range []store.Order{store.Ascending, store.Descending} {
				want := slices.Clone(c.want)
				if o == store.Descending {
					slices.Reverse(want)
				}
				if got := r.listAll(a, c.f, o, 2); !slices.Equal(got, want) {
					t.Errorf("%s (order %d): ListRuns = %v, want %v", c.name, o, got, want)
				}
			}
		}
	})

	t.Run("TenantsListTheirOwn", func(t *testing.T) {
		t.Parallel()
		r := newRunIndexRig(t, newStore)
		a, b := r.ctx(tenantA), r.ctx(tenantB)
		r.conv(a, "c", "alpha")
		r.conv(b, "c", "alpha")
		r.run(a, "same", "c", false, store.StatusRunning)
		r.run(b, "same", "c", true, store.StatusCompleted)
		r.run(b, "only-b", "c", false, store.StatusRunning)
		if got := r.listAll(a, store.RunFilter{}, store.Ascending, 0); !slices.Equal(got, []string{"same"}) {
			t.Errorf("tenant A lists %v, want [same]", got)
		}
		if got := r.listAll(b, store.RunFilter{Kind: store.BackgroundRuns}, store.Ascending, 0); !slices.Equal(got, []string{"only-b"}) {
			t.Errorf("tenant B's background runs are %v, want [only-b]", got)
		}
	})

	t.Run("PagesSeeEveryRunOnceWhileRunsAreAdded", func(t *testing.T) {
		t.Parallel()
		r := newRunIndexRig(t, newStore)
		a := r.ctx(tenantA)
		for i := range 5 {
			r.run(a, fmt.Sprintf("r%d", i), fmt.Sprintf("c%d", i), false, store.StatusRunning)
		}
		first, err := r.s.ListRuns(a, store.RunFilter{}, store.Page{Limit: 2, Order: store.Descending})
		if err != nil {
			t.Fatal(err)
		}
		r.run(a, "late", "c-late", false, store.StatusRunning) // after the first page
		got := runIDs(first.Runs)
		for p := first; p.Next != ""; {
			if p, err = r.s.ListRuns(a, store.RunFilter{}, store.Page{Limit: 2, Order: store.Descending, Cursor: p.Next}); err != nil {
				t.Fatal(err)
			}
			got = append(got, runIDs(p.Runs)...)
		}
		if want := []string{"r4", "r3", "r2", "r1", "r0"}; !slices.Equal(got, want) {
			t.Errorf("newest first, paged while a run was added: %v, want %v", got, want)
		}
	})

	t.Run("RefusesBadArguments", func(t *testing.T) {
		t.Parallel()
		r := newRunIndexRig(t, newStore)
		a := r.ctx(tenantA)
		for _, c := range []struct {
			name string
			f    store.RunFilter
			p    store.Page
		}{
			{"status", store.RunFilter{Status: "sleeping"}, store.Page{}},
			{"kind", store.RunFilter{Kind: store.InteractiveRuns + 1}, store.Page{}},
			{"agent", store.RunFilter{Agent: "a\x00b"}, store.Page{}},
			{"from seq", store.RunFilter{}, store.Page{FromSeq: 1}},
			{"cursor", store.RunFilter{}, store.Page{Cursor: "nonsense"}},
			{"limit", store.RunFilter{}, store.Page{Limit: -1}},
		} {
			if _, err := r.s.ListRuns(a, c.f, c.p); !errors.Is(err, store.ErrInvalid) {
				t.Errorf("%s: ListRuns = %v, want ErrInvalid", c.name, err)
			}
		}
		if _, err := r.s.ListRuns(t.Context(), store.RunFilter{}, store.Page{}); !errors.Is(err, tenancy.ErrNoTenant) {
			t.Errorf("no tenant: ListRuns = %v, want ErrNoTenant", err)
		}
	})
}

// runIndexRig is the helper of RunRunIndex.
type runIndexRig struct {
	t   *testing.T
	s   RunIndexStore
	clk *clocktest.Fake

	mu   sync.Mutex
	runs map[string][]string // tenant -> run ids, in creation order
}

func newRunIndexRig(t *testing.T, newStore func(t *testing.T, clk clock.Clock) RunIndexStore) *runIndexRig {
	t.Helper()
	clk := clocktest.NewFake(time.Time{})
	r := &runIndexRig{t: t, s: newStore(t, clk), clk: clk, runs: map[string][]string{unusedTenant: nil}}
	t.Cleanup(r.check)
	return r
}

func (r *runIndexRig) ctx(id string) context.Context {
	r.mu.Lock()
	if _, ok := r.runs[id]; !ok {
		r.runs[id] = nil
	}
	r.mu.Unlock()
	return tenantCtx(r.t.Context(), r.t, id)
}

func (r *runIndexRig) conv(ctx context.Context, id, agent string) {
	r.t.Helper()
	if _, err := r.s.CreateConversation(ctx, store.Conversation{ID: id, Agent: agent}); err != nil {
		r.t.Fatalf("CreateConversation %s: %v", id, err)
	}
}

// run creates a run a millisecond after the last, and moves it to status.
func (r *runIndexRig) run(ctx context.Context, id, conv string, interactive bool, status store.Status) {
	r.t.Helper()
	r.clk.Advance(time.Millisecond)
	var opts []store.CreateOption
	if interactive {
		opts = append(opts, store.Interactive())
	}
	if _, err := r.s.Create(ctx, id, conv, opts...); err != nil {
		r.t.Fatalf("Create %s: %v", id, err)
	}
	if status != store.StatusRunning {
		d := store.Details{}
		if status == store.StatusFailed {
			d.Error = "boom"
		}
		if _, err := r.s.Transition(ctx, id, store.StatusRunning, status, d); err != nil {
			r.t.Fatalf("Transition %s to %s: %v", id, status, err)
		}
	}
	tn, _ := tenancy.From(ctx)
	r.mu.Lock()
	r.runs[tn.ID()] = append(r.runs[tn.ID()], id)
	r.mu.Unlock()
}

// listAll pages ListRuns to the end, limit runs a page.
func (r *runIndexRig) listAll(ctx context.Context, f store.RunFilter, o store.Order, limit int) []string {
	r.t.Helper()
	var ids []string
	p := store.Page{Order: o, Limit: limit}
	for range 1000 {
		page, err := r.s.ListRuns(ctx, f, p)
		if err != nil {
			r.t.Fatalf("ListRuns(%+v): %v", f, err)
		}
		if limit > 0 && page.Next != "" && len(page.Runs) != limit {
			r.t.Errorf("ListRuns gave a page of %d runs with a cursor, want %d", len(page.Runs), limit)
		}
		ids = append(ids, runIDs(page.Runs)...)
		if page.Next == "" {
			return ids
		}
		p = store.Page{Order: o, Limit: limit, Cursor: page.Next}
	}
	r.t.Fatal("ListRuns never ended")
	return nil
}

// check is the invariant: every tenant lists exactly its runs, in order.
func (r *runIndexRig) check() {
	r.mu.Lock()
	runs := map[string][]string{}
	for k, v := range r.runs {
		runs[k] = slices.Clone(v)
	}
	r.mu.Unlock()
	ctx := context.WithoutCancel(r.t.Context())
	for tn, want := range runs {
		c := tenantCtx(ctx, r.t, tn)
		if got := r.listAll(c, store.RunFilter{}, store.Ascending, 3); !slices.Equal(got, want) {
			r.t.Errorf("invariant: tenant %s lists %v oldest first, want %v", tn, got, want)
		}
		rev := slices.Clone(want)
		slices.Reverse(rev)
		if got := r.listAll(c, store.RunFilter{}, store.Descending, 2); !slices.Equal(got, rev) {
			r.t.Errorf("invariant: tenant %s lists %v newest first, want %v", tn, got, rev)
		}
	}
}

// NotificationStore is what [RunNotifications] tests: a backend's runs and
// their notifications, and the records whose deletion takes them along.
type NotificationStore interface {
	store.RunStore
	store.ConversationIndex
	store.NotificationLog
}

// RunNotifications runs the conformance suite for a [store.NotificationLog]
// and the notification methods of its [store.Admin]. newStore must return an
// empty store and its Admin view of the same data, both reading their times
// from clk (a fake clock the suite advances). Every test checks, when it
// ends, that each notification it made reads back as the store last accepted
// it (URL, state, attempts in order, next attempt), in its tenant only.
func RunNotifications(t *testing.T, newStore func(t *testing.T, clk clock.Clock) (NotificationStore, store.Admin)) {
	t.Helper()
	const ttl = time.Minute

	t.Run("CreateIsOncePerRun", func(t *testing.T) {
		t.Parallel()
		r := newNotifyRig(t, newStore)
		a, b := r.ctx(tenantA), r.ctx(tenantB)
		r.mustRun(a, "run", "c")
		n := r.mustCreate(a, "run", "https://hooks.example/a")
		if n.State != store.NotificationPending || n.URL != "https://hooks.example/a" || n.RunID != "run" ||
			len(n.Attempts) != 0 || !n.NextAttemptAt.IsZero() || !n.CreatedAt.Equal(r.clk.Now()) {
			t.Errorf("created %+v, want a pending notification to the URL created now", n)
		}
		r.clk.Advance(time.Second)
		if again := r.mustCreate(a, "run", "https://hooks.example/a"); !again.CreatedAt.Equal(n.CreatedAt) {
			t.Errorf("a retry gave %+v, want the stored one", again)
		}
		if _, err := r.s.CreateNotification(a, "run", "https://hooks.example/other"); !errors.Is(err, store.ErrConflict) {
			t.Errorf("another URL: %v, want ErrConflict", err)
		}
		if _, err := r.s.CreateNotification(a, "nope", "https://hooks.example/a"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("no such run: %v, want ErrNotFound", err)
		}
		if _, err := r.s.CreateNotification(b, "run", "https://hooks.example/a"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("another tenant's run: %v, want ErrNotFound", err)
		}
		for _, u := range []string{"", "https://x/\x00"} {
			if _, err := r.s.CreateNotification(a, "run", u); !errors.Is(err, store.ErrInvalid) {
				t.Errorf("URL %q: %v, want ErrInvalid", u, err)
			}
		}
		if _, err := r.s.GetNotification(b, "run"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("another tenant's GetNotification: %v, want ErrNotFound", err)
		}
		if _, err := r.s.CreateNotification(t.Context(), "run", "https://hooks.example/a"); !errors.Is(err, tenancy.ErrNoTenant) {
			t.Errorf("no tenant: %v, want ErrNoTenant", err)
		}
	})

	t.Run("DueOnlyOnceTheRunHasEnded", func(t *testing.T) {
		t.Parallel()
		r := newNotifyRig(t, newStore)
		a, b := r.ctx(tenantA), r.ctx(tenantB)
		for _, id := range []string{"running", "suspended", "retrying", "failed", "completed", "canceled", "rejected"} {
			r.clk.Advance(time.Millisecond)
			r.mustRun(a, id, "c-"+id)
			r.mustCreate(a, id, "https://hooks.example/"+id)
		}
		r.mustRun(b, "other", "c")
		r.mustCreate(b, "other", "https://hooks.example/b")
		move := func(id string, to store.Status, d store.Details) {
			t.Helper()
			if _, err := r.s.Transition(a, id, store.StatusRunning, to, d); err != nil {
				t.Fatalf("Transition %s: %v", id, err)
			}
		}
		move("suspended", store.StatusSuspended, store.Details{Pending: []string{"ask"}})
		move("retrying", store.StatusFailed, store.Details{Error: "busy", Retryable: true, NextAttemptAt: r.clk.Now()})
		move("failed", store.StatusFailed, store.Details{Error: "boom"})
		move("completed", store.StatusCompleted, store.Details{})
		move("canceled", store.StatusCanceled, store.Details{Error: "canceled"})
		move("rejected", store.StatusRejected, store.Details{Error: "no"})

		// In claim order: by creation, since none was tried yet.
		want := []string{tenantA + "/failed", tenantA + "/completed", tenantA + "/canceled", tenantA + "/rejected"}
		if got := r.claimAll("worker", ttl); !slices.Equal(got, want) {
			t.Errorf("claimed %v, want the ended runs' %v", got, want)
		}
		if _, err := r.s.Transition(b, "other", store.StatusRunning, store.StatusCompleted, store.Details{}); err != nil {
			t.Fatal(err)
		}
		if got := r.claimAll("worker", ttl); !slices.Equal(got, []string{tenantB + "/other"}) {
			t.Errorf("then claimed %v, want tenant B's", got)
		}
	})

	t.Run("ClaimHoldsUntilTheLeaseExpires", func(t *testing.T) {
		t.Parallel()
		r := newNotifyRig(t, newStore)
		a := r.ctx(tenantA)
		r.mustEnded(a, "run")
		tn, err := r.admin.ClaimNotification(t.Context(), "one", ttl)
		if err != nil {
			t.Fatal(err)
		}
		if tn.Tenant.ID() != tenantA || tn.Notification.Lease.Owner != "one" || !tn.Notification.Lease.Expires.Equal(r.clk.Now().Add(ttl)) {
			t.Errorf("claimed %+v, want tenant A's, leased to one for the ttl", tn)
		}
		if _, err := r.admin.ClaimNotification(t.Context(), "two", ttl); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("claimed while leased: %v, want ErrNotFound", err)
		}
		r.clk.Advance(ttl)
		if tn, err = r.admin.ClaimNotification(t.Context(), "two", ttl); err != nil || tn.Notification.Lease.Owner != "two" {
			t.Errorf("after the lease expired: %+v, %v; want it claimed by two", tn, err)
		}
		// One's lease expired and two holds it now.
		if _, err := r.admin.RecordDelivery(t.Context(), tn.Tenant, "run", "one", r.delivered(200)); !errors.Is(err, store.ErrLeased) {
			t.Errorf("RecordDelivery by one: %v, want ErrLeased", err)
		}
		r.clk.Advance(ttl)
		if _, err := r.admin.RecordDelivery(t.Context(), tn.Tenant, "run", "two", r.delivered(200)); !errors.Is(err, store.ErrLeaseLost) {
			t.Errorf("RecordDelivery past the lease: %v, want ErrLeaseLost", err)
		}
	})

	t.Run("RecordsAttemptsUntilDelivered", func(t *testing.T) {
		t.Parallel()
		r := newNotifyRig(t, newStore)
		a := r.ctx(tenantA)
		r.mustEnded(a, "run")
		tenant := mustTenant(t, tenantA)

		tn := r.mustClaim("worker", ttl)
		first := r.clk.Now()
		next := first.Add(10 * time.Second)
		n, err := r.admin.RecordDelivery(t.Context(), tenant, "run", "worker", store.Delivery{
			Attempt: store.DeliveryAttempt{At: first, Status: 500, Error: "HTTP 500"},
			State:   store.NotificationPending, NextAttemptAt: next,
		})
		if err != nil {
			t.Fatal(err)
		}
		if n.State != store.NotificationPending || len(n.Attempts) != 1 || n.Attempts[0].Status != 500 ||
			!n.Attempts[0].At.Equal(first) || !n.NextAttemptAt.Equal(next) || n.Lease.Owner != "" || n.RunID != tn.Notification.RunID {
			t.Errorf("after a failed attempt: %+v", n)
		}
		r.record(tenantA, n)
		if _, err := r.admin.ClaimNotification(t.Context(), "worker", ttl); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("claimed before its next attempt: %v, want ErrNotFound", err)
		}
		r.clk.Advance(10 * time.Second)
		r.mustClaim("worker", ttl)
		n, err = r.admin.RecordDelivery(t.Context(), tenant, "run", "worker", r.delivered(204))
		if err != nil {
			t.Fatal(err)
		}
		if n.State != store.NotificationDelivered || len(n.Attempts) != 2 || n.Attempts[1].Status != 204 || !n.NextAttemptAt.IsZero() {
			t.Errorf("after the delivery: %+v", n)
		}
		r.record(tenantA, n)
		if _, err := r.admin.ClaimNotification(t.Context(), "worker", ttl); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("claimed once delivered: %v, want ErrNotFound", err)
		}
		if _, err := r.admin.RecordDelivery(t.Context(), tenant, "run", "worker", r.delivered(200)); !errors.Is(err, store.ErrFinal) {
			t.Errorf("RecordDelivery once delivered: %v, want ErrFinal", err)
		}
	})

	t.Run("GivingUpIsFinal", func(t *testing.T) {
		t.Parallel()
		r := newNotifyRig(t, newStore)
		a := r.ctx(tenantA)
		r.mustEnded(a, "run")
		r.mustClaim("worker", ttl)
		n, err := r.admin.RecordDelivery(t.Context(), mustTenant(t, tenantA), "run", "worker", store.Delivery{
			Attempt: store.DeliveryAttempt{At: r.clk.Now(), Error: "connection refused"}, State: store.NotificationFailed,
		})
		if err != nil || n.State != store.NotificationFailed || n.Attempts[0].Status != 0 || n.Attempts[0].Error != "connection refused" {
			t.Fatalf("gave up: %+v, %v", n, err)
		}
		r.record(tenantA, n)
		r.clk.Advance(time.Hour)
		if _, err := r.admin.ClaimNotification(t.Context(), "worker", ttl); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("claimed once given up: %v, want ErrNotFound", err)
		}
	})

	t.Run("RecordDeliveryRefusesBadArguments", func(t *testing.T) {
		t.Parallel()
		r := newNotifyRig(t, newStore)
		a := r.ctx(tenantA)
		r.mustEnded(a, "run")
		r.mustClaim("worker", ttl)
		tenant := mustTenant(t, tenantA)
		now := r.clk.Now()
		for _, c := range []struct {
			name string
			d    store.Delivery
		}{
			{"no state", store.Delivery{Attempt: store.DeliveryAttempt{At: now}}},
			{"unknown state", store.Delivery{Attempt: store.DeliveryAttempt{At: now}, State: "lost"}},
			{"pending without a next attempt", store.Delivery{Attempt: store.DeliveryAttempt{At: now}, State: store.NotificationPending}},
			{"delivered with a next attempt", store.Delivery{Attempt: store.DeliveryAttempt{At: now}, State: store.NotificationDelivered, NextAttemptAt: now}},
			{"no attempt time", store.Delivery{State: store.NotificationDelivered}},
			{"attempt time out of range", store.Delivery{Attempt: store.DeliveryAttempt{At: farFuture}, State: store.NotificationDelivered}},
			{"error not text", store.Delivery{Attempt: store.DeliveryAttempt{At: now, Error: "a\x00"}, State: store.NotificationFailed}},
		} {
			if _, err := r.admin.RecordDelivery(t.Context(), tenant, "run", "worker", c.d); !errors.Is(err, store.ErrInvalid) {
				t.Errorf("%s: %v, want ErrInvalid", c.name, err)
			}
		}
		if _, err := r.admin.RecordDelivery(t.Context(), tenant, "nope", "worker", r.delivered(200)); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("no such notification: %v, want ErrNotFound", err)
		}
		if _, err := r.admin.RecordDelivery(t.Context(), mustTenant(t, tenantB), "run", "worker", r.delivered(200)); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("another tenant: %v, want ErrNotFound", err)
		}
		for _, c := range []struct {
			owner string
			ttl   time.Duration
		}{{"", ttl}, {"w", 0}} {
			if _, err := r.admin.ClaimNotification(t.Context(), c.owner, c.ttl); !errors.Is(err, store.ErrInvalid) {
				t.Errorf("ClaimNotification(%q, %v): %v, want ErrInvalid", c.owner, c.ttl, err)
			}
		}
	})

	t.Run("DeletedWithItsConversation", func(t *testing.T) {
		t.Parallel()
		r := newNotifyRig(t, newStore)
		a := r.ctx(tenantA)
		if _, err := r.s.CreateConversation(a, store.Conversation{ID: "c", Agent: "x"}); err != nil {
			t.Fatal(err)
		}
		r.mustEnded(a, "run") // in conversation c
		if err := r.s.DeleteConversation(a, "c"); err != nil {
			t.Fatal(err)
		}
		r.forget(tenantA, "run")
		if _, err := r.s.GetNotification(a, "run"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("after the delete: %v, want ErrNotFound", err)
		}
		if _, err := r.admin.ClaimNotification(t.Context(), "worker", ttl); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("claimed after the delete: %v, want ErrNotFound", err)
		}
	})

	t.Run("ClaimersRacingGetEachOnce", func(t *testing.T) {
		t.Parallel()
		r := newNotifyRig(t, newStore)
		const n = 12
		for i := range n {
			ctx := r.ctx([]string{tenantA, tenantB}[i%2])
			r.mustEnded(ctx, fmt.Sprintf("run-%d", i))
		}
		var (
			mu  sync.Mutex
			got []string
			wg  sync.WaitGroup
		)
		for w := range 4 {
			wg.Go(func() {
				for {
					tn, err := r.admin.ClaimNotification(t.Context(), fmt.Sprintf("w%d", w), ttl)
					if errors.Is(err, store.ErrNotFound) {
						return
					}
					if err != nil {
						t.Errorf("ClaimNotification: %v", err)
						return
					}
					mu.Lock()
					got = append(got, tn.Tenant.ID()+"/"+tn.Notification.RunID)
					mu.Unlock()
				}
			})
		}
		wg.Wait()
		slices.Sort(got)
		if len(got) != n || len(slices.Compact(slices.Clone(got))) != n {
			t.Errorf("claimed %v, want each of the %d once", got, n)
		}
	})
}

type notifyKey struct{ tenant, run string }

// notifyRig is the helper of RunNotifications: it models every notification
// created and recorded through it.
type notifyRig struct {
	t     *testing.T
	s     NotificationStore
	admin store.Admin
	clk   *clocktest.Fake

	mu     sync.Mutex
	model  map[notifyKey]store.Notification // (tenant, run id)
	convOf map[string]string                // run id -> conversation, for mustEnded
}

func newNotifyRig(t *testing.T, newStore func(t *testing.T, clk clock.Clock) (NotificationStore, store.Admin)) *notifyRig {
	t.Helper()
	clk := clocktest.NewFake(time.Time{})
	s, admin := newStore(t, clk)
	r := &notifyRig{t: t, s: s, admin: admin, clk: clk, model: map[notifyKey]store.Notification{}, convOf: map[string]string{}}
	t.Cleanup(r.check)
	return r
}

func (r *notifyRig) ctx(id string) context.Context { return tenantCtx(r.t.Context(), r.t, id) }

func (r *notifyRig) mustRun(ctx context.Context, id, conv string) {
	r.t.Helper()
	if _, err := r.s.Create(ctx, id, conv); err != nil {
		r.t.Fatalf("Create %s: %v", id, err)
	}
}

func (r *notifyRig) mustCreate(ctx context.Context, runID, url string) store.Notification {
	r.t.Helper()
	n, err := r.s.CreateNotification(ctx, runID, url)
	if err != nil {
		r.t.Fatalf("CreateNotification %s: %v", runID, err)
	}
	tn, _ := tenancy.From(ctx)
	r.record(tn.ID(), n)
	return n
}

// mustEnded makes a completed run in conversation c with a notification.
func (r *notifyRig) mustEnded(ctx context.Context, id string) {
	r.t.Helper()
	r.clk.Advance(time.Millisecond)
	r.mustRun(ctx, id, "c")
	r.mustCreate(ctx, id, "https://hooks.example/"+id)
	if _, err := r.s.Transition(ctx, id, store.StatusRunning, store.StatusCompleted, store.Details{}); err != nil {
		r.t.Fatalf("Transition %s: %v", id, err)
	}
}

func (r *notifyRig) mustClaim(owner string, ttl time.Duration) store.TenantNotification {
	r.t.Helper()
	tn, err := r.admin.ClaimNotification(r.t.Context(), owner, ttl)
	if err != nil {
		r.t.Fatalf("ClaimNotification: %v", err)
	}
	return tn
}

// claimAll claims every due notification, as tenant/run ids in claim order.
func (r *notifyRig) claimAll(owner string, ttl time.Duration) []string {
	r.t.Helper()
	var out []string
	for range 100 {
		tn, err := r.admin.ClaimNotification(r.t.Context(), owner, ttl)
		if errors.Is(err, store.ErrNotFound) {
			return out
		}
		if err != nil {
			r.t.Fatalf("ClaimNotification: %v", err)
		}
		out = append(out, tn.Tenant.ID()+"/"+tn.Notification.RunID)
	}
	r.t.Fatal("ClaimNotification never ran out")
	return nil
}

func (r *notifyRig) delivered(status int) store.Delivery {
	return store.Delivery{Attempt: store.DeliveryAttempt{At: r.clk.Now(), Status: status}, State: store.NotificationDelivered}
}

func (r *notifyRig) record(tenant string, n store.Notification) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n.Lease = store.Lease{}
	r.model[notifyKey{tenant, n.RunID}] = n
}

func (r *notifyRig) forget(tenant, runID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.model, notifyKey{tenant, runID})
}

// check is the invariant: each notification reads back as last accepted, in
// its tenant only.
func (r *notifyRig) check() {
	r.mu.Lock()
	model := map[notifyKey]store.Notification{}
	for k, v := range r.model {
		model[k] = v
	}
	r.mu.Unlock()
	ctx := context.WithoutCancel(r.t.Context())
	for k, want := range model {
		got, err := r.s.GetNotification(tenantCtx(ctx, r.t, k.tenant), k.run)
		if err != nil {
			r.t.Errorf("invariant: notification %s/%s: %v", k.tenant, k.run, err)
			continue
		}
		if !sameNotification(got, want) {
			r.t.Errorf("invariant: notification %s/%s reads back as %+v, want %+v", k.tenant, k.run, got, want)
		}
		if _, err := r.s.GetNotification(tenantCtx(ctx, r.t, unusedTenant), k.run); !errors.Is(err, store.ErrNotFound) {
			r.t.Errorf("invariant: tenant %s sees the notification of %s/%s: %v", unusedTenant, k.tenant, k.run, err)
		}
	}
}

func sameNotification(a, b store.Notification) bool {
	if a.RunID != b.RunID || a.URL != b.URL || a.State != b.State || !a.NextAttemptAt.Equal(b.NextAttemptAt) ||
		!a.CreatedAt.Equal(b.CreatedAt) || len(a.Attempts) != len(b.Attempts) {
		return false
	}
	for i := range a.Attempts {
		x, y := a.Attempts[i], b.Attempts[i]
		if !x.At.Equal(y.At) || x.Status != y.Status || x.Error != y.Error {
			return false
		}
	}
	return true
}

func mustTenant(t *testing.T, id string) tenancy.Tenant {
	t.Helper()
	tn, err := tenancy.NewTenant(id)
	if err != nil {
		t.Fatal(err)
	}
	return tn
}
