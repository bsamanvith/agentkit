package storetest

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// allStatuses are the run statuses, and allowedMoves the moves the lifecycle
// allows, restated for the suite.
var (
	allStatuses = []store.Status{
		store.StatusRunning, store.StatusSuspended, store.StatusFailed,
		store.StatusCompleted, store.StatusRejected, store.StatusCanceled,
	}
	allowedMoves = map[[2]store.Status]bool{
		{store.StatusRunning, store.StatusSuspended}:  true,
		{store.StatusRunning, store.StatusFailed}:     true,
		{store.StatusRunning, store.StatusCompleted}:  true,
		{store.StatusRunning, store.StatusRejected}:   true,
		{store.StatusRunning, store.StatusCanceled}:   true,
		{store.StatusSuspended, store.StatusRunning}:  true,
		{store.StatusSuspended, store.StatusRejected}: true,
		{store.StatusSuspended, store.StatusCanceled}: true,
		{store.StatusFailed, store.StatusRunning}:     true,
		{store.StatusFailed, store.StatusCanceled}:    true,
	}
)

// detailsFor returns valid details for a move to status to.
func detailsFor(to store.Status, i int, now time.Time) store.Details {
	d := store.Details{Usage: llm.Usage{InputTokens: 10 * i, OutputTokens: i, CacheReadTokens: 2}}
	switch to {
	case store.StatusSuspended:
		d.Pending = []string{fmt.Sprintf("main/z%d", i), "main/ask"}
	case store.StatusFailed:
		d.Error, d.Retryable, d.NextAttemptAt = "overloaded", true, now.Add(time.Duration(i)*time.Second)
	case store.StatusRejected, store.StatusCanceled:
		d.Error = "because"
	}
	return d
}

// moveTo brings a new running run to status s in one move.
func (r *runRig) moveTo(ctx context.Context, id string, s store.Status) {
	r.t.Helper()
	if s != store.StatusRunning {
		r.mustMove(ctx, id, store.StatusRunning, s, detailsFor(s, 1, r.clk.Now()))
	}
}

// RunRunStore runs the conformance suite for a [store.RunStore]. newStore must
// return an empty store that reads run times and lease expiry from clk (a fake
// clock the suite advances).
//
// Every test goes through one helper that checks the invariants listed on
// runRig when the test ends; the suite ends with seeded simulations
// ([SimulateRuns]) through the same helper.
func RunRunStore(t *testing.T, newStore func(t *testing.T, clk clock.Clock) store.RunStore) {
	t.Helper()

	t.Run("CreateGetList", func(t *testing.T) {
		t.Parallel()
		r := newRunRig(t, newStore)
		ctx := r.ctx(tenantA)
		start := r.clk.Now()
		r1 := r.mustCreate(ctx, "r1", "c1")
		if r1.ID != "r1" || r1.ConversationID != "c1" || r1.Status != store.StatusRunning || !r1.CreatedAt.Equal(start) {
			t.Errorf("Create = %+v, want r1 in c1, running, created at %v", r1, start)
		}
		if got := r.mustGet(ctx, "r1"); !sameRun(got, r1) {
			t.Errorf("Get = %+v, want what Create returned %+v", got, r1)
		}
		r.mustMove(ctx, "r1", store.StatusRunning, store.StatusCompleted, store.Details{})
		r.clk.Advance(time.Second)
		r.mustCreate(ctx, "r2", "c1")
		r.mustCreate(ctx, "r3", "c2")
		if got := runIDs(listAll(ctx, t, r.s, "c1", store.Page{})); !slices.Equal(got, []string{"r1", "r2"}) {
			t.Errorf("List c1 = %v, want [r1 r2]", got)
		}
		if _, err := r.s.Get(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Get of a missing run = %v, want ErrNotFound", err)
		}
		if got := listAll(ctx, t, r.s, "empty", store.Page{}); len(got) != 0 {
			t.Errorf("List of a conversation with no runs = %v", runIDs(got))
		}
	})

	t.Run("CreateIsCreateIfAbsent", func(t *testing.T) {
		t.Parallel()
		r := newRunRig(t, newStore)
		ctx := r.ctx(tenantA)
		first := r.mustCreateFinal(ctx, "r1", "c1")
		r.clk.Advance(time.Hour)
		again, err := r.create(ctx, "r1", "c1")
		if err != nil || !sameRun(again, first) {
			t.Errorf("Create again = %+v, %v; want the stored run %+v", again, err, first)
		}
		if _, err := r.create(ctx, "r1", "c2"); !errors.Is(err, store.ErrConflict) {
			t.Errorf("Create in another conversation = %v, want ErrConflict", err)
		}
		for name, ids := range map[string][2]string{"empty id": {"", "c1"}, "empty conversation": {"r2", ""}} {
			if _, err := r.create(ctx, ids[0], ids[1]); !errors.Is(err, store.ErrInvalid) {
				t.Errorf("Create with %s = %v, want ErrInvalid", name, err)
			}
		}
	})

	t.Run("OneUnfinishedRunPerConversation", func(t *testing.T) {
		t.Parallel()
		r := newRunRig(t, newStore)
		ctx := r.ctx(tenantA)
		if _, err := r.s.Unfinished(ctx, "c"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Unfinished of an empty conversation = %v, want ErrNotFound", err)
		}
		r.mustCreate(ctx, "r1", "c")
		// Every status that is not final keeps the conversation busy.
		for _, s := range []store.Status{store.StatusRunning, store.StatusSuspended, store.StatusRunning, store.StatusFailed} {
			if cur := r.mustGet(ctx, "r1").Status; cur != s {
				r.mustMove(ctx, "r1", cur, s, detailsFor(s, 1, r.clk.Now()))
			}
			if _, err := r.create(ctx, "r2", "c"); !errors.Is(err, store.ErrUnfinished) {
				t.Errorf("Create while the conversation's run is %s = %v, want ErrUnfinished", s, err)
			}
			if got, err := r.s.Unfinished(ctx, "c"); err != nil || got.ID != "r1" {
				t.Errorf("Unfinished while r1 is %s = %q, %v; want r1", s, got.ID, err)
			}
		}
		if _, err := r.create(ctx, "r1", "c"); err != nil {
			t.Errorf("retry of the unfinished run's own Create = %v, want nil", err)
		}
		r.mustCreate(ctx, "other", "c2") // other conversations are free
		r.mustMove(ctx, "r1", store.StatusFailed, store.StatusCanceled, store.Details{Error: "user"})
		if _, err := r.s.Unfinished(ctx, "c"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Unfinished after the run was canceled = %v, want ErrNotFound", err)
		}
		r.mustCreate(ctx, "r2", "c")
		// Creators released at once into one conversation: one run is made.
		errs := race(8, func(w int) error {
			_, err := r.create(ctx, fmt.Sprintf("race-%d", w), "busy")
			return err
		})
		won := 0
		for w, err := range errs {
			switch {
			case err == nil:
				won++
			case !errors.Is(err, store.ErrUnfinished):
				t.Errorf("creator %d: Create = %v, want nil or ErrUnfinished", w, err)
			}
		}
		if won != 1 {
			t.Errorf("%d racing creators made a run in one conversation, want 1", won)
		}
	})

	t.Run("ListPagesVisitEveryRunOnce", func(t *testing.T) {
		t.Parallel()
		for _, order := range []store.Order{store.Ascending, store.Descending} {
			for _, limit := range []int{1, 2, 3, 0} {
				t.Run(fmt.Sprintf("order=%d/limit=%d", order, limit), func(t *testing.T) {
					t.Parallel()
					r := newRunRig(t, newStore)
					ctx := r.ctx(tenantA)
					// Runs created at the same time are ordered by id, not by
					// when they were created.
					for i, id := range []string{"e", "b", "d", "a", "c", "f", "g"} {
						if i%3 == 0 {
							r.clk.Advance(time.Second)
						}
						r.mustCreateFinal(ctx, id, "c")
					}
					want := runIDs(listAll(ctx, t, r.s, "c", store.Page{Order: order}))
					var visited []string
					p := store.Page{Limit: limit, Order: order}
					for i := 0; ; i++ {
						page, err := r.s.List(ctx, "c", p)
						if err != nil {
							t.Fatalf("List: %v", err)
						}
						visited = append(visited, runIDs(page.Runs)...)
						// Created between the first pages: may be visited, at
						// most once.
						if i < 4 {
							r.clk.Advance(time.Duration(i%2) * time.Second)
							r.mustCreateFinal(ctx, fmt.Sprintf("new-%d", i), "c")
						}
						if page.Next == "" {
							break
						}
						p = store.Page{Cursor: page.Next, Limit: limit, Order: order}
					}
					var old []string
					seen := map[string]bool{}
					for _, id := range visited {
						if seen[id] {
							t.Fatalf("List visited %q twice: %v", id, visited)
						}
						seen[id] = true
						if id[0] != 'n' {
							old = append(old, id)
						}
					}
					if !slices.Equal(old, want) {
						t.Errorf("List visited the first runs as %v, want %v", old, want)
					}
				})
			}
		}
	})

	t.Run("ListSeeksByTime", func(t *testing.T) {
		t.Parallel()
		r := newRunRig(t, newStore)
		ctx := r.ctx(tenantA)
		for _, id := range []string{"a", "b", "c"} {
			r.clk.Advance(time.Second)
			r.mustCreateFinal(ctx, id, "c")
		}
		at := r.mustGet(ctx, "b").CreatedAt
		if got := runIDs(listAll(ctx, t, r.s, "c", store.Page{FromTime: at, Limit: 1})); !slices.Equal(got, []string{"b", "c"}) {
			t.Errorf("List from b's time up = %v, want [b c]", got)
		}
		if got := runIDs(listAll(ctx, t, r.s, "c", store.Page{FromTime: at, Order: store.Descending})); !slices.Equal(got, []string{"b", "a"}) {
			t.Errorf("List from b's time down = %v, want [b a]", got)
		}
		page, err := r.s.List(ctx, "c", store.Page{Limit: 1})
		if err != nil || page.Next == "" {
			t.Fatalf("List = %v, next %q; want a next page", err, page.Next)
		}
		for name, p := range map[string]store.Page{
			"seq":                       {FromSeq: 1},
			"negative limit":            {Limit: -1},
			"unknown order":             {Order: store.Order(-1)},
			"garbage cursor":            {Cursor: "%%%"},
			"cursor of the other order": {Cursor: page.Next, Order: store.Descending},
			"cursor and time":           {Cursor: page.Next, FromTime: at},
		} {
			if _, err := r.s.List(ctx, "c", p); !errors.Is(err, store.ErrInvalid) {
				t.Errorf("List with %s = %v, want ErrInvalid", name, err)
			}
			if _, err := r.s.ListSuspended(ctx, p); !errors.Is(err, store.ErrInvalid) {
				t.Errorf("ListSuspended with %s = %v, want ErrInvalid", name, err)
			}
		}
	})

	t.Run("Lifecycle", func(t *testing.T) {
		t.Parallel()

		// Every pair of statuses: allowed moves succeed and record their
		// details, the others are ErrInvalid, and nothing leaves a final
		// status (ErrFinal).
		t.Run("AllowedTransitionsSucceed", func(t *testing.T) {
			t.Parallel()
			r := newRunRig(t, newStore)
			ctx := r.ctx(tenantA)
			for i, from := range allStatuses {
				for j, to := range allStatuses {
					id := fmt.Sprintf("%s-%s", from, to)
					r.mustCreate(ctx, id, id)
					r.moveTo(ctx, id, from)
					r.clk.Advance(time.Second)
					d := detailsFor(to, 10*i+j, r.clk.Now())
					var want error
					switch {
					case from.Final():
						want = store.ErrFinal
					case !allowedMoves[[2]store.Status{from, to}]:
						want = store.ErrInvalid
					}
					before := r.mustGet(ctx, id)
					run, err := r.transition(ctx, id, from, to, d)
					if !isErr(err, want) {
						t.Errorf("%s → %s = %v, want %v", from, to, err, want)
						continue
					}
					if err != nil {
						continue
					}
					wantPending := slices.Sorted(slices.Values(d.Pending))
					if run.Status != to || run.Error != d.Error || run.Retryable != d.Retryable || !run.NextAttemptAt.Equal(d.NextAttemptAt) ||
						!slices.Equal(run.Pending, wantPending) || run.Usage != sumUsage(before.Usage, d.Usage) {
						t.Errorf("%s → %s returned %+v, want the details %+v recorded", from, to, run, d)
					}
					if to.Final() != run.FinishedAt.Equal(r.clk.Now()) {
						t.Errorf("%s → %s: FinishedAt = %v at %v", from, to, run.FinishedAt, r.clk.Now())
					}
					if got := r.mustGet(ctx, id); !sameRun(got, run) {
						t.Errorf("%s → %s: Get = %+v, want what Transition returned %+v", from, to, got, run)
					}
				}
			}
		})

		t.Run("WrongFromIsErrConflict", func(t *testing.T) {
			t.Parallel()
			r := newRunRig(t, newStore)
			ctx := r.ctx(tenantA)
			r.mustCreate(ctx, "r", "c")
			for _, move := range [][2]store.Status{
				{store.StatusSuspended, store.StatusRunning},
				{store.StatusFailed, store.StatusCanceled},
			} {
				if _, err := r.transition(ctx, "r", move[0], move[1], detailsFor(move[1], 1, r.clk.Now())); !errors.Is(err, store.ErrConflict) {
					t.Errorf("%s → %s of a running run = %v, want ErrConflict", move[0], move[1], err)
				}
			}
			r.mustMove(ctx, "r", store.StatusRunning, store.StatusCompleted, store.Details{})
			for _, from := range []store.Status{store.StatusRunning, store.StatusSuspended, store.StatusFailed} {
				if _, err := r.transition(ctx, "r", from, store.StatusCanceled, store.Details{}); !errors.Is(err, store.ErrFinal) {
					t.Errorf("%s → canceled of a completed run = %v, want ErrFinal", from, err)
				}
			}
			if _, err := r.transition(ctx, "nope", store.StatusRunning, store.StatusCompleted, store.Details{}); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("Transition of a missing run = %v, want ErrNotFound", err)
			}
		})

		t.Run("DetailsMustFitTheStatus", func(t *testing.T) {
			t.Parallel()
			r := newRunRig(t, newStore)
			ctx := r.ctx(tenantA)
			r.mustCreate(ctx, "r", "c")
			now := r.clk.Now()
			for name, tc := range map[string]struct {
				to store.Status
				d  store.Details
			}{
				"suspended without pending paths":  {store.StatusSuspended, store.Details{}},
				"suspended with an empty path":     {store.StatusSuspended, store.Details{Pending: []string{"a", ""}}},
				"pending paths on completed":       {store.StatusCompleted, store.Details{Pending: []string{"a"}}},
				"an error on completed":            {store.StatusCompleted, store.Details{Error: "x"}},
				"an error on suspended":            {store.StatusSuspended, store.Details{Error: "x", Pending: []string{"a"}}},
				"a retry on canceled":              {store.StatusCanceled, store.Details{Retryable: true}},
				"a next attempt on completed":      {store.StatusCompleted, store.Details{NextAttemptAt: now}},
				"a next attempt without Retryable": {store.StatusFailed, store.Details{NextAttemptAt: now}},
			} {
				if _, err := r.transition(ctx, "r", store.StatusRunning, tc.to, tc.d); !errors.Is(err, store.ErrInvalid) {
					t.Errorf("%s: Transition = %v, want ErrInvalid", name, err)
				}
			}
		})

		// Usage adds up over transitions, Attempts counts the failures, and
		// Pending is stored sorted and as a copy.
		t.Run("FieldsAccumulate", func(t *testing.T) {
			t.Parallel()
			r := newRunRig(t, newStore)
			ctx := r.ctx(tenantA)
			r.mustCreate(ctx, "r", "c")
			pending := []string{"main/b", "main/a"}
			run := r.mustMove(ctx, "r", store.StatusRunning, store.StatusSuspended, store.Details{Pending: pending, Usage: llm.Usage{InputTokens: 5}})
			pending[0] = "scribbled"
			run.Pending[0] = "scribbled"
			if got := r.mustGet(ctx, "r").Pending; !slices.Equal(got, []string{"main/a", "main/b"}) {
				t.Errorf("Pending = %v, want [main/a main/b], unchanged by callers", got)
			}
			for range 2 {
				r.mustMove(ctx, "r", r.mustGet(ctx, "r").Status, store.StatusRunning, store.Details{Usage: llm.Usage{InputTokens: 1, OutputTokens: 2}})
				r.mustMove(ctx, "r", store.StatusRunning, store.StatusFailed, store.Details{Error: "boom", Usage: llm.Usage{CacheWriteTokens: 3}})
			}
			got := r.mustGet(ctx, "r")
			want := llm.Usage{InputTokens: 7, OutputTokens: 4, CacheWriteTokens: 6}
			if got.Usage != want || got.Attempts != 2 || got.Retryable || len(got.Pending) != 0 || got.Error != "boom" {
				t.Errorf("after two failures the run is %+v, want usage %+v, 2 attempts, not retryable, no pending paths", got, want)
			}
		})

		// Writers released at once, all moving the same run from running:
		// whatever the schedule, exactly one wins.
		t.Run("RacingTransitionsExactlyOneWins", func(t *testing.T) {
			t.Parallel()
			r := newRunRig(t, newStore)
			ctx := r.ctx(tenantA)
			targets := []store.Status{store.StatusSuspended, store.StatusFailed, store.StatusCompleted, store.StatusCanceled}
			for round := range 10 {
				id := fmt.Sprintf("r%d", round)
				r.mustCreate(ctx, id, id)
				errs := race(8, func(w int) error {
					to := targets[(w+round)%len(targets)]
					_, err := r.transition(ctx, id, store.StatusRunning, to, detailsFor(to, w, r.clk.Now()))
					return err
				})
				var won []int
				for w, err := range errs {
					switch {
					case err == nil:
						won = append(won, w)
					case !errors.Is(err, store.ErrConflict) && !errors.Is(err, store.ErrFinal):
						t.Errorf("round %d writer %d: Transition = %v, want nil, ErrConflict or ErrFinal", round, w, err)
					}
				}
				if len(won) != 1 {
					t.Fatalf("round %d: %d writers moved the run (%v), want exactly one", round, len(won), won)
				}
				if got, want := r.mustGet(ctx, id).Status, targets[(won[0]+round)%len(targets)]; got != want {
					t.Errorf("round %d: run is %s, want the winner's %s", round, got, want)
				}
			}
		})
	})

	t.Run("ListResumableFindsInterruptedAndDueRuns", func(t *testing.T) {
		t.Parallel()
		r := newRunRig(t, newStore)
		ctx := r.ctx(tenantA)
		const ttl = 10 * time.Second
		t0 := r.clk.Now()
		run := func(id string) {
			r.clk.Advance(time.Millisecond)
			r.mustCreate(ctx, id, id)
		}
		claim := func(id string, ttl time.Duration) {
			if _, err := r.s.Claim(ctx, id, "worker", ttl); err != nil {
				t.Fatalf("Claim %s: %v", id, err)
			}
		}
		fail := func(id string, d store.Details) {
			d.Error = "boom"
			r.mustMove(ctx, id, store.StatusRunning, store.StatusFailed, d)
		}

		run("live")
		claim("live", time.Hour)
		run("expired")
		claim("expired", ttl)
		expires := r.clk.Now().Add(ttl)
		run("unclaimed")
		run("released")
		claim("released", time.Hour)
		if err := r.s.Release(ctx, "released", "worker"); err != nil {
			t.Fatalf("Release: %v", err)
		}
		run("suspended")
		r.mustMove(ctx, "suspended", store.StatusRunning, store.StatusSuspended, store.Details{Pending: []string{"main/ask"}})
		run("due")
		fail("due", store.Details{Retryable: true, NextAttemptAt: t0.Add(5 * time.Second)})
		run("due-now")
		fail("due-now", store.Details{Retryable: true, NextAttemptAt: expires})
		run("not-due")
		fail("not-due", store.Details{Retryable: true, NextAttemptAt: expires.Add(time.Nanosecond)})
		run("failed-for-good")
		fail("failed-for-good", store.Details{})
		for _, s := range []store.Status{store.StatusCompleted, store.StatusRejected, store.StatusCanceled} {
			run(string(s))
			r.mustMove(ctx, string(s), store.StatusRunning, s, store.Details{})
		}
		r.mustCreate(r.ctx(tenantB), "other-tenant", "c")

		list := func(now time.Time, limit int) []string {
			t.Helper()
			got, err := r.s.ListResumable(ctx, now, limit)
			if err != nil {
				t.Fatalf("ListResumable: %v", err)
			}
			return runIDs(got)
		}
		r.clk.Advance(expires.Sub(r.clk.Now())) // "expired" expires now
		now := r.clk.Now()
		if got, want := list(now, 0), []string{"expired", "unclaimed", "released", "due", "due-now"}; !slices.Equal(got, want) {
			t.Errorf("ListResumable at %v = %v, want %v", now, got, want)
		}
		if got, want := list(now, 2), []string{"expired", "unclaimed"}; !slices.Equal(got, want) {
			t.Errorf("ListResumable with limit 2 = %v, want %v", got, want)
		}
		// The time passed in decides, not the store's clock.
		if got, want := list(now.Add(-time.Nanosecond), 0), []string{"unclaimed", "released", "due"}; !slices.Equal(got, want) {
			t.Errorf("ListResumable just before the lease expired = %v, want %v", got, want)
		}
		if _, err := r.s.ListResumable(ctx, now, -1); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("ListResumable with a negative limit = %v, want ErrInvalid", err)
		}
	})

	// A failed run marked retryable with no next attempt time is due at
	// once: the zero NextAttemptAt is not after any now.
	t.Run("RetryableFailureWithNoNextAttemptIsDueAtOnce", func(t *testing.T) {
		t.Parallel()
		r := newRunRig(t, newStore)
		ctx := r.ctx(tenantA)
		r.mustCreate(ctx, "r", "c")
		if _, err := r.s.Claim(ctx, "r", "worker", time.Hour); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		r.mustMove(ctx, "r", store.StatusRunning, store.StatusFailed, store.Details{Error: "boom", Retryable: true})
		runs, err := r.s.ListResumable(ctx, r.clk.Now(), 0)
		if err != nil || !slices.Equal(runIDs(runs), []string{"r"}) {
			t.Errorf("ListResumable = %v, %v; want the failed run", runIDs(runs), err)
		}
	})

	// A time the caller asks the store to keep (a next attempt time, a
	// lease's expiry) outside 1678 to 2262 is ErrInvalid and changes nothing.
	// A time a read only compares with (now, a seek) may be anything.
	t.Run("TimesOutsideTheStoredRange", func(t *testing.T) {
		t.Parallel()
		r := newRunRig(t, newStore)
		ctx := r.ctx(tenantA)
		r.mustCreate(ctx, "r", "c-r")
		r.clk.Advance(time.Millisecond)
		r.mustCreate(ctx, "f", "c-f")

		for _, next := range []time.Time{farFuture, farPast} {
			if _, err := r.transition(ctx, "f", store.StatusRunning, store.StatusFailed,
				store.Details{Retryable: true, NextAttemptAt: next}); !errors.Is(err, store.ErrInvalid) {
				t.Errorf("Transition with a next attempt in the year %d = %v, want ErrInvalid", next.Year(), err)
			}
		}
		if _, err := r.s.Claim(ctx, "r", "worker", time.Duration(math.MaxInt64)); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("Claim for 292 years = %v, want ErrInvalid", err)
		}
		if run := r.mustGet(ctx, "r"); run.Lease != (store.Lease{}) {
			t.Errorf("after the refused claim the run's lease is %+v, want none", run.Lease)
		}

		r.mustMove(ctx, "f", store.StatusRunning, store.StatusFailed, store.Details{Retryable: true, NextAttemptAt: r.clk.Now()})
		for _, c := range []struct {
			now  time.Time
			want []string
		}{
			{farFuture, []string{"r", "f"}},
			{farPast, []string{"r"}}, // unclaimed; f is not due yet
		} {
			runs, err := r.s.ListResumable(ctx, c.now, 0)
			if err != nil || !slices.Equal(runIDs(runs), c.want) {
				t.Errorf("ListResumable in the year %d = %v, %v; want %v", c.now.Year(), runIDs(runs), err, c.want)
			}
		}
		for _, c := range []struct {
			p    store.Page
			want []string
		}{
			{store.Page{FromTime: farFuture}, nil},
			{store.Page{FromTime: farFuture, Order: store.Descending}, []string{"r"}},
			{store.Page{FromTime: farPast}, []string{"r"}},
			{store.Page{FromTime: farPast, Order: store.Descending}, nil},
		} {
			if got := runIDs(listAll(ctx, t, r.s, "c-r", c.p)); !slices.Equal(got, c.want) {
				t.Errorf("List %+v = %v, want %v", c.p, got, c.want)
			}
		}
	})

	// Text a store cannot keep as it is (not UTF-8, or holding a NUL) is
	// ErrInvalid wherever it would be kept or searched for, and a run with
	// such an id is not found: it could never have been created.
	t.Run("TextAStoreCannotHold", func(t *testing.T) {
		t.Parallel()
		r := newRunRig(t, newStore)
		ctx := r.ctx(tenantA)
		r.mustCreate(ctx, "r", "c")
		const ttl = time.Minute
		for _, bad := range []string{"a\x00b", "a\xffb"} {
			for name, f := range map[string]func() error{
				"Create with that run id":          func() error { _, err := r.create(ctx, bad, "c2"); return err },
				"Create with that conversation id": func() error { _, err := r.create(ctx, "r2", bad); return err },
				"List":                             func() error { _, err := r.s.List(ctx, bad, store.Page{}); return err },
				"Unfinished":                       func() error { _, err := r.s.Unfinished(ctx, bad); return err },
				"Claim by that owner":              func() error { _, err := r.s.Claim(ctx, "r", bad, ttl); return err },
				"Renew by that owner":              func() error { _, err := r.s.Renew(ctx, "r", bad, ttl); return err },
				"Release by that owner":            func() error { return r.s.Release(ctx, "r", bad) },
				"Transition with that error": func() error {
					_, err := r.transition(ctx, "r", store.StatusRunning, store.StatusFailed, store.Details{Error: bad})
					return err
				},
				"Transition with that pending path": func() error {
					_, err := r.transition(ctx, "r", store.StatusRunning, store.StatusSuspended, store.Details{Pending: []string{"ok", bad}})
					return err
				},
			} {
				if err := f(); !errors.Is(err, store.ErrInvalid) {
					t.Errorf("%s %q = %v, want ErrInvalid", name, bad, err)
				}
			}
			for name, f := range map[string]func() error{
				"Get":     func() error { _, err := r.s.Get(ctx, bad); return err },
				"Claim":   func() error { _, err := r.s.Claim(ctx, bad, "a", ttl); return err },
				"Renew":   func() error { _, err := r.s.Renew(ctx, bad, "a", ttl); return err },
				"Release": func() error { return r.s.Release(ctx, bad, "a") },
				"Transition": func() error {
					_, err := r.transition(ctx, bad, store.StatusRunning, store.StatusCompleted, store.Details{})
					return err
				},
			} {
				if err := f(); !errors.Is(err, store.ErrNotFound) {
					t.Errorf("%s of run %q = %v, want ErrNotFound", name, bad, err)
				}
			}
		}
		if run := r.mustGet(ctx, "r"); run.Status != store.StatusRunning || run.Lease != (store.Lease{}) {
			t.Errorf("after the refused calls run r is %+v, want it running and unclaimed", run)
		}
	})

	t.Run("ListSuspendedPages", func(t *testing.T) {
		t.Parallel()
		r := newRunRig(t, newStore)
		ctx := r.ctx(tenantA)
		var want []string
		for i := range 7 {
			id := fmt.Sprintf("r%d", i)
			r.mustCreate(ctx, id, id)
			if i%3 != 1 {
				r.mustMove(ctx, id, store.StatusRunning, store.StatusSuspended, store.Details{Pending: []string{"main/ask"}})
				want = append(want, id)
			}
		}
		for _, limit := range []int{1, 2, 0} {
			if got := runIDs(suspendedAll(ctx, t, r.s, store.Page{Limit: limit})); !slices.Equal(got, want) {
				t.Errorf("ListSuspended in pages of %d = %v, want %v", limit, got, want)
			}
			if got := runIDs(suspendedAll(ctx, t, r.s, store.Page{Limit: limit, Order: store.Descending})); !slices.Equal(got, reverse(want)) {
				t.Errorf("ListSuspended newest first in pages of %d = %v, want %v", limit, got, reverse(want))
			}
		}
	})

	// Runs created at the same time are ordered by ID compared as bytes, as
	// Go compares strings: "B" before "_x" before "a" before "a-b" before
	// "ab", and "é" last. A database that orders text by a language's rules
	// (where "a" comes before "B" and punctuation is ignored) fails this.
	t.Run("IDsBreakTiesAsBytes", func(t *testing.T) {
		t.Parallel()
		r := newRunRig(t, newStore)
		ctx := r.ctx(tenantA)
		ids := []string{"ab", "é", "a-b", "B", "a", "Z9", "_x"}
		want := slices.Sorted(slices.Values(ids))
		for _, id := range ids {
			r.mustCreateFinal(ctx, id, "c")
			r.mustCreate(ctx, "run-"+id, "c-"+id)
			r.mustMove(ctx, "run-"+id, store.StatusRunning, store.StatusSuspended, store.Details{Pending: []string{"main/ask"}})
		}
		if got := runIDs(listAll(ctx, t, r.s, "c", store.Page{Limit: 2})); !slices.Equal(got, want) {
			t.Errorf("List = %v, want %v", got, want)
		}
		wantRuns := make([]string, len(want))
		for i, id := range want {
			wantRuns[i] = "run-" + id
		}
		if got := runIDs(suspendedAll(ctx, t, r.s, store.Page{Limit: 2})); !slices.Equal(got, wantRuns) {
			t.Errorf("ListSuspended = %v, want %v", got, wantRuns)
		}
		// Running again, unleased: resumable, in the same order.
		for _, id := range wantRuns {
			r.mustMove(ctx, id, store.StatusSuspended, store.StatusRunning, store.Details{})
		}
		runs, err := r.s.ListResumable(ctx, r.clk.Now(), 0)
		if got := runIDs(runs); err != nil || !slices.Equal(got, wantRuns) {
			t.Errorf("ListResumable = %v, %v; want %v", got, err, wantRuns)
		}
	})

	t.Run("Leases", func(t *testing.T) {
		t.Parallel()
		const ttl = 10 * time.Second

		t.Run("SecondOwnerCannotClaimUntilExpiry", func(t *testing.T) {
			t.Parallel()
			r := newRunRig(t, newStore)
			ctx := r.ctx(tenantA)
			r.mustCreate(ctx, "r", "c")
			l, err := r.s.Claim(ctx, "r", "a", ttl)
			if want := (store.Lease{Owner: "a", Expires: r.clk.Now().Add(ttl)}); err != nil || !sameLease(l, want) {
				t.Fatalf("Claim = %+v, %v; want %+v", l, err, want)
			}
			if _, err := r.s.Claim(ctx, "r", "b", ttl); !errors.Is(err, store.ErrLeased) {
				t.Errorf("second owner's Claim = %v, want ErrLeased", err)
			}
			r.clk.Advance(ttl - time.Nanosecond)
			if _, err := r.s.Claim(ctx, "r", "b", ttl); !errors.Is(err, store.ErrLeased) {
				t.Errorf("second owner's Claim just before expiry = %v, want ErrLeased", err)
			}
			r.clk.Advance(time.Nanosecond)
			l, err = r.s.Claim(ctx, "r", "b", ttl)
			if want := (store.Lease{Owner: "b", Expires: r.clk.Now().Add(ttl)}); err != nil || !sameLease(l, want) {
				t.Fatalf("second owner's Claim at expiry = %+v, %v; want %+v", l, err, want)
			}
			if got := r.mustGet(ctx, "r").Lease; !sameLease(got, l) {
				t.Errorf("Get shows lease %+v, want %+v", got, l)
			}
			if _, err := r.s.Renew(ctx, "r", "a", ttl); !errors.Is(err, store.ErrLeased) {
				t.Errorf("first owner's Renew after losing the lease = %v, want ErrLeased", err)
			}
		})

		t.Run("RenewByNonOwnerFails", func(t *testing.T) {
			t.Parallel()
			r := newRunRig(t, newStore)
			ctx := r.ctx(tenantA)
			r.mustCreate(ctx, "r", "c")
			if _, err := r.s.Renew(ctx, "r", "b", ttl); !errors.Is(err, store.ErrLeaseLost) {
				t.Errorf("Renew of a lease nobody holds = %v, want ErrLeaseLost", err)
			}
			held, err := r.s.Claim(ctx, "r", "a", ttl)
			if err != nil {
				t.Fatalf("Claim: %v", err)
			}
			r.clk.Advance(time.Second)
			if _, err := r.s.Renew(ctx, "r", "b", ttl); !errors.Is(err, store.ErrLeased) {
				t.Errorf("Renew by a non-owner = %v, want ErrLeased", err)
			}
			if got := r.mustGet(ctx, "r").Lease; !sameLease(got, held) {
				t.Errorf("after a non-owner's Renew the lease is %+v, want it unchanged %+v", got, held)
			}
		})

		t.Run("RenewExtendsUntilExpiry", func(t *testing.T) {
			t.Parallel()
			r := newRunRig(t, newStore)
			ctx := r.ctx(tenantA)
			r.mustCreate(ctx, "r", "c")
			if _, err := r.s.Claim(ctx, "r", "a", ttl); err != nil {
				t.Fatalf("Claim: %v", err)
			}
			r.clk.Advance(6 * time.Second)
			l, err := r.s.Renew(ctx, "r", "a", ttl)
			if want := (store.Lease{Owner: "a", Expires: r.clk.Now().Add(ttl)}); err != nil || !sameLease(l, want) {
				t.Fatalf("Renew = %+v, %v; want %+v", l, err, want)
			}
			r.clk.Advance(ttl - time.Nanosecond) // past the first expiry
			if _, err := r.s.Claim(ctx, "r", "b", ttl); !errors.Is(err, store.ErrLeased) {
				t.Errorf("Claim before the renewed expiry = %v, want ErrLeased", err)
			}
			r.clk.Advance(time.Nanosecond)
			if _, err := r.s.Renew(ctx, "r", "a", ttl); !errors.Is(err, store.ErrLeaseLost) {
				t.Errorf("Renew after expiry = %v, want ErrLeaseLost", err)
			}
			// The holder claims again, which also extends a held lease.
			if _, err := r.s.Claim(ctx, "r", "a", ttl); err != nil {
				t.Errorf("Claim by the last owner after expiry = %v, want nil", err)
			}
			r.clk.Advance(time.Second)
			l, err = r.s.Claim(ctx, "r", "a", ttl)
			if want := (store.Lease{Owner: "a", Expires: r.clk.Now().Add(ttl)}); err != nil || !sameLease(l, want) {
				t.Errorf("Claim by the holder = %+v, %v; want %+v", l, err, want)
			}
		})

		t.Run("ReleaseLetsAnotherOwnerClaim", func(t *testing.T) {
			t.Parallel()
			r := newRunRig(t, newStore)
			ctx := r.ctx(tenantA)
			r.mustCreate(ctx, "r", "c")
			if err := r.s.Release(ctx, "r", "a"); err != nil {
				t.Errorf("Release of a lease nobody holds = %v, want nil", err)
			}
			if _, err := r.s.Claim(ctx, "r", "a", ttl); err != nil {
				t.Fatalf("Claim: %v", err)
			}
			if err := r.s.Release(ctx, "r", "b"); !errors.Is(err, store.ErrLeased) {
				t.Errorf("Release by a non-owner = %v, want ErrLeased", err)
			}
			if err := r.s.Release(ctx, "r", "a"); err != nil {
				t.Fatalf("Release: %v", err)
			}
			if got := r.mustGet(ctx, "r").Lease; got != (store.Lease{}) {
				t.Errorf("after Release the lease is %+v, want none", got)
			}
			if err := r.s.Release(ctx, "r", "a"); err != nil {
				t.Errorf("Release again = %v, want nil", err)
			}
			if _, err := r.s.Renew(ctx, "r", "a", ttl); !errors.Is(err, store.ErrLeaseLost) {
				t.Errorf("Renew after Release = %v, want ErrLeaseLost", err)
			}
			if _, err := r.s.Claim(ctx, "r", "b", ttl); err != nil {
				t.Errorf("another owner's Claim after Release = %v, want nil", err)
			}
		})

		t.Run("OnlyUnfinishedRunsCanBeLeased", func(t *testing.T) {
			t.Parallel()
			r := newRunRig(t, newStore)
			ctx := r.ctx(tenantA)
			for _, s := range []store.Status{store.StatusSuspended, store.StatusFailed} {
				id := string(s)
				r.mustCreate(ctx, id, id)
				r.moveTo(ctx, id, s)
				if _, err := r.s.Claim(ctx, id, "a", ttl); err != nil {
					t.Errorf("Claim of a %s run = %v, want nil", s, err)
				}
			}
			r.mustCreate(ctx, "r", "c")
			if _, err := r.s.Claim(ctx, "r", "a", ttl); err != nil {
				t.Fatalf("Claim: %v", err)
			}
			r.mustMove(ctx, "r", store.StatusRunning, store.StatusCompleted, store.Details{})
			if _, err := r.s.Renew(ctx, "r", "a", ttl); !errors.Is(err, store.ErrFinal) {
				t.Errorf("Renew of a completed run = %v, want ErrFinal", err)
			}
			r.clk.Advance(ttl)
			if _, err := r.s.Claim(ctx, "r", "b", ttl); !errors.Is(err, store.ErrFinal) {
				t.Errorf("Claim of a completed run = %v, want ErrFinal", err)
			}
			if err := r.s.Release(ctx, "r", "a"); err != nil {
				t.Errorf("Release of a completed run's lease = %v, want nil", err)
			}
		})

		t.Run("ArgumentsAreChecked", func(t *testing.T) {
			t.Parallel()
			r := newRunRig(t, newStore)
			ctx := r.ctx(tenantA)
			r.mustCreate(ctx, "r", "c")
			for name, f := range map[string]func() error{
				"Claim with no owner":   func() error { _, err := r.s.Claim(ctx, "r", "", ttl); return err },
				"Claim with zero ttl":   func() error { _, err := r.s.Claim(ctx, "r", "a", 0); return err },
				"Claim with ttl < 0":    func() error { _, err := r.s.Claim(ctx, "r", "a", -time.Second); return err },
				"Renew with no owner":   func() error { _, err := r.s.Renew(ctx, "r", "", ttl); return err },
				"Renew with zero ttl":   func() error { _, err := r.s.Renew(ctx, "r", "a", 0); return err },
				"Release with no owner": func() error { return r.s.Release(ctx, "r", "") },
			} {
				if err := f(); !errors.Is(err, store.ErrInvalid) {
					t.Errorf("%s = %v, want ErrInvalid", name, err)
				}
			}
			for name, f := range map[string]func() error{
				"Claim":   func() error { _, err := r.s.Claim(ctx, "nope", "a", ttl); return err },
				"Renew":   func() error { _, err := r.s.Renew(ctx, "nope", "a", ttl); return err },
				"Release": func() error { return r.s.Release(ctx, "nope", "a") },
			} {
				if err := f(); !errors.Is(err, store.ErrNotFound) {
					t.Errorf("%s of a missing run = %v, want ErrNotFound", name, err)
				}
			}
		})

		// Owners released at once: whatever the schedule, exactly one gets
		// the lease.
		t.Run("RacingClaimsExactlyOneWins", func(t *testing.T) {
			t.Parallel()
			r := newRunRig(t, newStore)
			ctx := r.ctx(tenantA)
			for round := range 10 {
				id := fmt.Sprintf("r%d", round)
				r.mustCreate(ctx, id, id)
				leases := make([]store.Lease, 8)
				errs := race(len(leases), func(w int) error {
					var err error
					leases[w], err = r.s.Claim(ctx, id, fmt.Sprintf("owner-%d", w), ttl)
					return err
				})
				var won []int
				for w, err := range errs {
					switch {
					case err == nil:
						won = append(won, w)
					case !errors.Is(err, store.ErrLeased):
						t.Errorf("round %d owner %d: Claim = %v, want nil or ErrLeased", round, w, err)
					}
				}
				if len(won) != 1 {
					t.Fatalf("round %d: %d owners claimed the lease (%v), want exactly one", round, len(won), won)
				}
				if got := r.mustGet(ctx, id).Lease; !sameLease(got, leases[won[0]]) {
					t.Errorf("round %d: stored lease %+v, want the winner's %+v", round, got, leases[won[0]])
				}
			}
		})
	})

	t.Run("TenantIsolation", func(t *testing.T) {
		t.Parallel()
		r := newRunRig(t, newStore)
		a, b := r.ctx(tenantA), r.ctx(tenantB)
		for name, f := range runMethods(t.Context(), r) {
			if err := f(); !errors.Is(err, tenancy.ErrNoTenant) {
				t.Errorf("%s without a tenant = %v, want ErrNoTenant", name, err)
			}
		}

		// The same run and conversation ids in two tenants are two runs.
		r.mustCreate(a, "shared", "c")
		r.mustCreate(b, "shared", "c")
		r.mustCreate(a, "only-a", "c-a")
		if _, err := r.s.Claim(a, "shared", "owner-a", time.Hour); err != nil {
			t.Fatalf("Claim in A: %v", err)
		}
		if _, err := r.s.Claim(b, "shared", "owner-b", time.Hour); err != nil {
			t.Errorf("Claim in B while A holds its own run's lease = %v, want nil", err)
		}
		r.mustMove(b, "shared", store.StatusRunning, store.StatusSuspended, store.Details{Pending: []string{"p"}})
		if run := r.mustGet(a, "shared"); run.Status != store.StatusRunning || run.Lease.Owner != "owner-a" {
			t.Errorf("A's run after B moved its own: %+v", run)
		}
		r.mustCreate(b, "second", "c-a") // A's conversation has an unfinished run; B's does not
		for name, f := range map[string]func() error{
			"Transition": func() error {
				_, err := r.transition(b, "only-a", store.StatusRunning, store.StatusCanceled, store.Details{})
				return err
			},
			"Claim":   func() error { _, err := r.s.Claim(b, "only-a", "b", time.Second); return err },
			"Renew":   func() error { _, err := r.s.Renew(b, "only-a", "b", time.Second); return err },
			"Release": func() error { return r.s.Release(b, "only-a", "b") },
		} {
			if err := f(); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("%s in B of A's run = %v, want ErrNotFound", name, err)
			}
		}
		// The invariant check reads every run and conversation, and the
		// suspended and resumable runs, through every tenant.
	})

	t.Run("CancelledContextIsReported", func(t *testing.T) {
		t.Parallel()
		r := newRunRig(t, newStore)
		r.mustCreate(r.ctx(tenantA), "r", "c")
		ctx, cancel := context.WithCancel(r.ctx(tenantA))
		cancel()
		for name, f := range runMethods(ctx, r) {
			if err := f(); !errors.Is(err, context.Canceled) {
				t.Errorf("%s = %v, want context.Canceled", name, err)
			}
		}
	})

	t.Run("Simulation", func(t *testing.T) {
		t.Parallel()
		for seed := range uint64(48) {
			t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
				t.Parallel()
				SimulateRuns(t, newStore, seed)
			})
		}
	})
}

// runMethods calls every RunStore method with ctx, with arguments that would
// otherwise succeed on a running run "r" in conversation "c".
func runMethods(ctx context.Context, r *runRig) map[string]func() error {
	return map[string]func() error{
		"Create": func() error { _, err := r.create(ctx, "r2", "c2"); return err },
		"Get":    func() error { _, err := r.s.Get(ctx, "r"); return err },
		"List":   func() error { _, err := r.s.List(ctx, "c", store.Page{}); return err },
		"Transition": func() error {
			_, err := r.transition(ctx, "r", store.StatusRunning, store.StatusCompleted, store.Details{})
			return err
		},
		"ListResumable": func() error { _, err := r.s.ListResumable(ctx, r.clk.Now(), 0); return err },
		"ListSuspended": func() error { _, err := r.s.ListSuspended(ctx, store.Page{}); return err },
		"Unfinished":    func() error { _, err := r.s.Unfinished(ctx, "c"); return err },
		"Claim":         func() error { _, err := r.s.Claim(ctx, "r", "a", time.Second); return err },
		"Renew":         func() error { _, err := r.s.Renew(ctx, "r", "a", time.Second); return err },
		"Release":       func() error { return r.s.Release(ctx, "r", "a") },
	}
}
