package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// RunEventLog runs the conformance suite for a [store.EventLog]. newLog must
// return an empty log that reads event times from clk (a fake clock the suite
// advances).
//
// Every test goes through one helper that checks the invariants listed on
// eventRig when the test ends, and the suite ends with a seeded simulation
// ([SimulateEvents]) through the same helper.
func RunEventLog(t *testing.T, newLog func(t *testing.T, clk clock.Clock) store.EventLog) {
	t.Helper()

	t.Run("DataRoundTripsByteForByte", func(t *testing.T) {
		t.Parallel()
		r := newEventRig(t, newLog)
		ctx := r.ctx(tenantA)
		in := eventsFrom(1, 3, 1)
		in[1].Data = nil
		in[2].Data = json.RawMessage(`  "just a string"  `)
		r.mustAppend(ctx, "r", in...)
		got := readEvents(ctx, t, r.s, "r", 0, 0)
		for i, e := range got {
			if snapEvent(e) != snapEvent(in[i]) {
				t.Errorf("event %d reads back as %+v, want %+v", e.Seq, snapEvent(e), snapEvent(in[i]))
			}
		}
		if got[1].Data != nil {
			t.Errorf("event with no data reads back as %q, want nil", got[1].Data)
		}
		// Empty and nil data are the same event.
		empty := in[1]
		empty.Data = json.RawMessage{}
		if err := r.append(ctx, "r", empty); err != nil {
			t.Errorf("retry of event 2 with empty instead of nil data = %v, want nil", err)
		}
	})

	t.Run("AppendRules", func(t *testing.T) {
		t.Parallel()
		r := newEventRig(t, newLog)
		ctx := r.ctx(tenantA)
		r.mustAppend(ctx, "r", eventsFrom(1, 2, 1)...)
		with := func(e store.Event, f func(*store.Event)) store.Event { f(&e); return e }
		cases := []struct {
			name   string
			run    string
			events []store.Event
			want   error
		}{
			{"seq zero", "r", []store.Event{eventAt(0, 9)}, store.ErrInvalid},
			{"negative seq", "r", []store.Event{eventAt(-1, 9)}, store.ErrInvalid},
			{"seqs not consecutive", "r", []store.Event{eventAt(3, 9), eventAt(5, 9)}, store.ErrInvalid},
			{"seqs repeated", "r", []store.Event{eventAt(3, 9), eventAt(3, 9)}, store.ErrInvalid},
			{"seqs going down", "r", []store.Event{eventAt(3, 9), eventAt(2, 2)}, store.ErrInvalid},
			{"no type", "r", []store.Event{with(eventAt(3, 9), func(e *store.Event) { e.Type = "" })}, store.ErrInvalid},
			{"data not JSON", "r", []store.Event{with(eventAt(3, 9), func(e *store.Event) { e.Data = json.RawMessage(`{"a":`) })}, store.ErrInvalid},
			{"data not UTF-8", "r", []store.Event{with(eventAt(3, 9), func(e *store.Event) { e.Data = json.RawMessage("\"\xff\"") })}, store.ErrInvalid},
			{"type not UTF-8", "r", []store.Event{with(eventAt(3, 9), func(e *store.Event) { e.Type = "bad\xff" })}, store.ErrInvalid},
			{"type with a NUL", "r", []store.Event{with(eventAt(3, 9), func(e *store.Event) { e.Type = "a\x00b" })}, store.ErrInvalid},
			{"empty run id", "", eventsFrom(1, 1, 9), store.ErrInvalid},
			{"run id with a NUL", "r\x00", eventsFrom(1, 1, 9), store.ErrInvalid},
			{"run id not UTF-8", "r\xff", eventsFrom(1, 1, 9), store.ErrInvalid},
			{"empty batch, empty run id", "", nil, store.ErrInvalid},
			{"gap", "r", eventsFrom(4, 1, 9), store.ErrGap},
			{"gap on a new run", "other", eventsFrom(2, 1, 9), store.ErrGap},
			// A bad event anywhere fails the whole batch, the good ones before
			// it included.
			{"bad last event", "r", []store.Event{eventAt(3, 3), with(eventAt(4, 4), func(e *store.Event) { e.Type = "" })}, store.ErrInvalid},
			{"conflict after a new event", "r", append(eventsFrom(2, 1, 99), eventAt(3, 3)), store.ErrConflict},
			{"conflict before a new event", "r", []store.Event{eventAt(1, 99), eventAt(2, 2), eventAt(3, 3)}, store.ErrConflict},
			{"conflict in the type only", "r", []store.Event{with(eventAt(2, 2), func(e *store.Event) { e.Type = "other" })}, store.ErrConflict},
			{"conflict in whitespace only", "r", []store.Event{with(eventAt(2, 2), func(e *store.Event) { e.Data = append(e.Data, ' ') })}, store.ErrConflict},
			{"data where there was none", "r", []store.Event{with(eventAt(2, 2), func(e *store.Event) { e.Data = nil })}, store.ErrConflict},
		}
		for _, c := range cases {
			if err := r.append(ctx, c.run, c.events...); !errors.Is(err, c.want) {
				t.Errorf("%s: AppendEvents = %v, want %v", c.name, err, c.want)
			}
		}
		// Nothing was stored by any of them (the rig checks the whole log).
		if last, err := r.s.LastEventSeq(ctx, "r"); err != nil || last != 2 {
			t.Errorf("after refused appends LastEventSeq = %d, %v; want 2", last, err)
		}
	})

	t.Run("RetriesAreNoOps", func(t *testing.T) {
		t.Parallel()
		r := newEventRig(t, newLog)
		ctx := r.ctx(tenantA)
		r.mustAppend(ctx, "r", eventsFrom(1, 3, 1)...)
		before := readEvents(ctx, t, r.s, "r", 0, 0)
		r.clk.Advance(time.Hour)

		r.mustAppend(ctx, "r", eventsFrom(1, 3, 1)...) // the whole batch
		r.mustAppend(ctx, "r", eventsFrom(2, 1, 2)...) // one event of it
		if got := readEvents(ctx, t, r.s, "r", 0, 0); !sameEvents(got, before) {
			t.Errorf("after retries the log is %v, want it unchanged with its times", eventSeqs(got))
		}

		// A batch whose start is stored and whose end is not appends the
		// end: the retry of a batch that a crash cut short.
		r.mustAppend(ctx, "r", eventsFrom(2, 4, 2)...)
		got := readEvents(ctx, t, r.s, "r", 0, 0)
		if len(got) != 5 || !sameEvents(got[:3], before) {
			t.Fatalf("after an overlapping batch the log is %v, want 1-3 unchanged and 4-5 added", eventSeqs(got))
		}
		if want := before[0].CreatedAt.Add(time.Hour); !got[3].CreatedAt.Equal(want) || !got[4].CreatedAt.Equal(want) {
			t.Errorf("events 4 and 5 were created at %v and %v, want the clock's time %v", got[3].CreatedAt, got[4].CreatedAt, want)
		}
	})

	t.Run("EmptyBatchIsANoOp", func(t *testing.T) {
		t.Parallel()
		r := newEventRig(t, newLog)
		ctx := r.ctx(tenantA)
		for _, events := range [][]store.Event{nil, {}} {
			if err := r.append(ctx, "r", events...); err != nil {
				t.Errorf("AppendEvents(%v) = %v, want nil", events, err)
			}
		}
		if err := r.s.AppendEvents(t.Context(), "r", nil); !errors.Is(err, tenancy.ErrNoTenant) {
			t.Errorf("AppendEvents of nothing without a tenant = %v, want ErrNoTenant", err)
		}
		if last, err := r.s.LastEventSeq(ctx, "r"); err != nil || last != 0 {
			t.Errorf("LastEventSeq = %d, %v; want 0", last, err)
		}
	})

	t.Run("EventsAfterLimits", func(t *testing.T) {
		t.Parallel()
		r := newEventRig(t, newLog)
		ctx := r.ctx(tenantA)
		r.mustAppend(ctx, "r", eventsFrom(1, store.MaxPageLimit+5, 1)...)
		for _, c := range []struct {
			after int64
			limit int
			want  int
		}{
			{0, 0, store.DefaultPageLimit},
			{0, 1, 1},
			{0, store.MaxPageLimit + 1, store.MaxPageLimit},
			{3, store.MaxPageLimit * 5, store.MaxPageLimit},
			{store.MaxPageLimit, 0, 5},
			{store.MaxPageLimit + 4, 3, 1},
			{store.MaxPageLimit + 5, 3, 0},
			{store.MaxPageLimit + 500, 3, 0},
		} {
			got, err := r.s.EventsAfter(ctx, "r", c.after, c.limit)
			if err != nil || len(got) != c.want {
				t.Errorf("EventsAfter(%d, %d) = %d events, %v; want %d", c.after, c.limit, len(got), err, c.want)
				continue
			}
			if len(got) > 0 && got[0].Seq != c.after+1 {
				t.Errorf("EventsAfter(%d, %d) starts at %d", c.after, c.limit, got[0].Seq)
			}
		}
		if _, err := r.s.EventsAfter(ctx, "r", 0, -1); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("EventsAfter with a negative limit = %v, want ErrInvalid", err)
		}
		if _, err := r.s.EventsAfter(ctx, "r", -1, 0); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("EventsAfter a negative seq = %v, want ErrInvalid", err)
		}
		for _, id := range []string{"", "r\x00", "r\xff"} {
			if _, err := r.s.EventsAfter(ctx, id, 0, 0); !errors.Is(err, store.ErrInvalid) {
				t.Errorf("EventsAfter of run %q = %v, want ErrInvalid", id, err)
			}
			if _, err := r.s.LastEventSeq(ctx, id); !errors.Is(err, store.ErrInvalid) {
				t.Errorf("LastEventSeq of run %q = %v, want ErrInvalid", id, err)
			}
		}
		if got, err := r.s.EventsAfter(ctx, "none", 0, 0); err != nil || len(got) != 0 {
			t.Errorf("EventsAfter of a run with no events = %v, %v; want none", eventSeqs(got), err)
		}
	})

	t.Run("TimesNeverGoBackwards", func(t *testing.T) {
		t.Parallel()
		r := newEventRig(t, newLog)
		ctx := r.ctx(tenantA)
		t0 := r.clk.Now()
		r.mustAppend(ctx, "r", eventsFrom(1, 2, 1)...)
		// A second writer whose clock is an hour behind.
		r.clk.skew(-time.Hour)
		r.clk.Advance(time.Second)
		r.mustAppend(ctx, "r", eventsFrom(3, 1, 3)...)
		r.clk.skew(0)
		r.clk.Advance(time.Second)
		r.mustAppend(ctx, "r", eventsFrom(4, 1, 4)...)
		got := readEvents(ctx, t, r.s, "r", 0, 0)
		want := []time.Time{t0, t0, t0, t0.Add(2 * time.Second)}
		for i, e := range got {
			if !e.CreatedAt.Equal(want[i]) {
				t.Errorf("event %d created at %v, want %v", e.Seq, e.CreatedAt, want[i])
			}
		}
	})

	t.Run("ConcurrentAppendsAreDenseOrConflict", func(t *testing.T) {
		t.Parallel()

		// Writers building different batches from the same view: exactly
		// one wins, the others conflict, and the log is the winner's.
		t.Run("SameSeq", func(t *testing.T) {
			t.Parallel()
			r := newEventRig(t, newLog)
			ctx := r.ctx(tenantA)
			const writers = 8
			for round := range 10 {
				run := fmt.Sprintf("race-%d", round)
				r.mustAppend(ctx, run, eventAt(1, 0))
				errs := race(writers, func(w int) error { return r.append(ctx, run, eventsFrom(2, 1+w%3, 100*(w+1))...) })
				var won []int
				for w, err := range errs {
					switch {
					case err == nil:
						won = append(won, w)
					case !errors.Is(err, store.ErrConflict):
						t.Errorf("round %d writer %d: AppendEvents = %v, want nil or ErrConflict", round, w, err)
					}
				}
				if len(won) != 1 {
					t.Fatalf("round %d: %d writers won seq 2 (%v), want exactly one", round, len(won), won)
				}
				w := won[0]
				if got := readEvents(ctx, t, r.s, run, 1, 0); len(got) != 1+w%3 || snapEvent(got[0]) != snapEvent(eventAt(2, 100*(w+1))) {
					t.Errorf("round %d: the log after 1 is %v, not the winner's batch", round, eventSeqs(got))
				}
				// Identical batches racing all succeed and are stored once.
				next := 2 + int64(1+w%3)
				errs = race(writers, func(int) error { return r.append(ctx, run, eventsFrom(next, 2, 7)...) })
				for w, err := range errs {
					if err != nil {
						t.Errorf("round %d writer %d: identical AppendEvents = %v, want nil", round, w, err)
					}
				}
			}
		})

		// Writers that each append their own events at the end of the log,
		// reading LastEventSeq again after each conflict: every event lands
		// exactly once and the log has no gap, whatever the schedule.
		t.Run("CatchingUp", func(t *testing.T) {
			t.Parallel()
			r := newEventRig(t, newLog)
			ctx := r.ctx(tenantA)
			const writers, each = 6, 8
			errs := race(writers, func(w int) error {
				for i := 0; i < each; {
					last, err := r.s.LastEventSeq(ctx, "r")
					if err != nil {
						return err
					}
					e := eventAt(last+1, w*1000+i)
					switch err := r.append(ctx, "r", e); {
					case err == nil:
						i++
					case !errors.Is(err, store.ErrConflict) && !errors.Is(err, store.ErrGap):
						return err
					}
				}
				return nil
			})
			for w, err := range errs {
				if err != nil {
					t.Errorf("writer %d: %v", w, err)
				}
			}
			got := readEvents(ctx, t, r.s, "r", 0, 0)
			if len(got) != writers*each {
				t.Fatalf("the log has %d events, want %d", len(got), writers*each)
			}
			seen := map[string]bool{}
			for _, e := range got {
				if seen[string(e.Data)] {
					t.Errorf("event %d is stored twice", e.Seq)
				}
				seen[string(e.Data)] = true
			}
		})
	})

	t.Run("TenantIsolation", func(t *testing.T) {
		t.Parallel()
		r := newEventRig(t, newLog)
		a, b := r.ctx(tenantA), r.ctx(tenantB)

		none := t.Context()
		if err := r.append(none, "r", eventAt(1, 1)); !errors.Is(err, tenancy.ErrNoTenant) {
			t.Errorf("AppendEvents without a tenant = %v, want ErrNoTenant", err)
		}
		if _, err := r.s.EventsAfter(none, "r", 0, 0); !errors.Is(err, tenancy.ErrNoTenant) {
			t.Errorf("EventsAfter without a tenant = %v, want ErrNoTenant", err)
		}
		if _, err := r.s.LastEventSeq(none, "r"); !errors.Is(err, tenancy.ErrNoTenant) {
			t.Errorf("LastEventSeq without a tenant = %v, want ErrNoTenant", err)
		}

		// The same run id in two tenants is two logs.
		r.mustAppend(a, "shared", eventsFrom(1, 3, 1)...)
		r.mustAppend(b, "shared", eventAt(1, 100))
		r.mustAppend(a, "only-a", eventAt(1, 1))
		if err := r.append(b, "only-a", eventAt(2, 2)); !errors.Is(err, store.ErrGap) {
			t.Errorf("tenant B appending event 2 to A's run = %v, want ErrGap (B's log is empty)", err)
		}
		// The invariant check reads every run through every tenant.
	})

	t.Run("CancelledContextIsReported", func(t *testing.T) {
		t.Parallel()
		r := newEventRig(t, newLog)
		ctx, cancel := context.WithCancel(r.ctx(tenantA))
		cancel()
		if err := r.append(ctx, "r", eventAt(1, 1)); !errors.Is(err, context.Canceled) {
			t.Errorf("AppendEvents = %v, want context.Canceled", err)
		}
		if _, err := r.s.EventsAfter(ctx, "r", 0, 0); !errors.Is(err, context.Canceled) {
			t.Errorf("EventsAfter = %v, want context.Canceled", err)
		}
		if _, err := r.s.LastEventSeq(ctx, "r"); !errors.Is(err, context.Canceled) {
			t.Errorf("LastEventSeq = %v, want context.Canceled", err)
		}
	})

	t.Run("KeepsCopies", func(t *testing.T) {
		t.Parallel()
		r := newEventRig(t, newLog)
		ctx := r.ctx(tenantA)
		in := eventsFrom(1, 2, 1)
		r.mustAppend(ctx, "r", in...)
		// Scribbling over what was appended changes nothing stored. (The rig
		// took its snapshot before the append.)
		for _, e := range in {
			for i := range e.Data {
				e.Data[i] = 'X'
			}
		}
		// Neither does scribbling over what was read.
		got, err := r.s.EventsAfter(ctx, "r", 0, 0)
		if err != nil {
			t.Fatalf("EventsAfter: %v", err)
		}
		for _, e := range got {
			for i := range e.Data {
				e.Data[i] = 'X'
			}
		}
	})

	t.Run("Simulation", func(t *testing.T) {
		t.Parallel()
		for seed := range uint64(48) {
			t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
				t.Parallel()
				SimulateEvents(t, newLog, seed)
			})
		}
	})
}
