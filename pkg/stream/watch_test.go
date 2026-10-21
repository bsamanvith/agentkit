package stream_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/memory"
	"github.com/bsamanvith/agentkit/pkg/stream"
)

// A watch from the start replays what was stored, follows what comes next,
// deltas included, and ends after the terminal event.
func TestWatchReplaysThenFollows(t *testing.T) {
	t.Parallel()
	forEachBus(t, roomy, func(t *testing.T, b backend) {
		ctx := tenantCtx(t, "acme")
		log := memory.New()
		p, _ := stream.NewPublisher(log, b.bus, quiet)
		r := newRun(ctx, t, p, log, "run")
		r.durable("run_start")
		r.delta()
		r.durable("tool_call")
		w := startWatch(ctx, t, log, b.bus, "run", bus.ID{})
		// The log has no delta: those of a turn that ended are gone.
		var got []stream.Event
		for range 2 {
			got = append(got, w.next())
		}
		r.delta()
		r.delta()
		r.durable("turn_done")
		r.durable("run_done")
		r.durable("after_the_end") // never delivered
		rest, err := w.end()
		if err != nil {
			t.Fatalf("watch ended with %v, want nil after run_done", err)
		}
		got = append(got, rest...)
		if g := ids(got); len(g) != 6 || g[2] != "2.1" || g[5] != "4" {
			t.Fatalf("got %v, want 1 2 2.1 2.2 3 4", g)
		}
		if err := r.check(bus.ID{}, got, true); err != nil {
			t.Error(err)
		}
	})
}

// A client reconnecting with any ID it was sent, a delta's included, gets
// exactly the durable events after it (the deltas of turns that ended are
// in their turn_done). One that saw the end gets nothing.
func TestWatchResumesAfterEveryID(t *testing.T) {
	t.Parallel()
	forEachBus(t, roomy, func(t *testing.T, b backend) {
		ctx := tenantCtx(t, "acme")
		log := memory.New()
		p, _ := stream.NewPublisher(log, b.bus, quiet)
		r := newRun(ctx, t, p, log, "run")
		r.durable("run_start")
		for range 3 {
			r.delta()
			r.delta()
			r.durable("turn_done")
		}
		r.durable("run_done")
		for _, from := range append([]stream.Event{{}}, r.events...) {
			got, err := startWatch(ctx, t, log, b.bus, "run", from.ID).end()
			if err != nil {
				t.Errorf("after %s: err = %v", from.ID, err)
			}
			var want []string
			for _, e := range r.events {
				if e.ID.Compare(from.ID) > 0 && !r.live[e.ID] {
					want = append(want, e.ID.String())
				}
			}
			if g := ids(got); len(g) != len(want) || r.check(from.ID, got, true) != nil {
				t.Errorf("after %s: got %v, want %v (%v)", from.ID, g, want, r.check(from.ID, got, true))
			}
		}
	})
}

// With history trimmed to a few messages, a watch from the start still gets
// every durable event, from the log, and the end.
func TestWatchTrimmedHistory(t *testing.T) {
	t.Parallel()
	tight := func(bus.Topic) bus.Trim { return bus.Trim{MaxLen: 3, MaxAge: time.Hour} }
	forEachBus(t, tight, func(t *testing.T, b backend) {
		ctx := tenantCtx(t, "acme")
		log := memory.New()
		p, _ := stream.NewPublisher(log, b.bus, quiet)
		r := newRun(ctx, t, p, log, "run")
		r.durable("run_start")
		w := startWatch(ctx, t, log, b.bus, "run", bus.ID{})
		got := []stream.Event{w.next()}
		w.cancel() // a client that went away and comes back later
		<-w.done
		for range 150 { // Redis trims only whole nodes of 100
			r.delta()
			r.delta()
			r.durable("turn_done")
		}
		w2 := startWatch(ctx, t, log, b.bus, "run", got[0].ID)
		got2 := []stream.Event{w2.next()} // a while behind
		for range 5 {
			r.delta()
			r.durable("turn_done")
		}
		r.durable("run_done")
		rest, err := w2.end()
		if err != nil {
			t.Fatal(err)
		}
		got2 = append(got2, rest...)
		if err := r.check(got[0].ID, got2, true); err != nil {
			t.Error(err)
		}
	})
}

// A failed publish leaves a hole the watch fills from the log, mid-run and
// at the very end, where no later message reports it.
func TestWatchFailedPublishes(t *testing.T) {
	t.Parallel()
	forEachBus(t, roomy, func(t *testing.T, b backend) {
		ctx := tenantCtx(t, "acme")
		log := memory.New()
		f := &faulty{Bus: b.bus}
		p, _ := stream.NewPublisher(log, f, quiet)
		r := newRun(ctx, t, p, log, "run")
		r.durable("run_start")
		w := startWatch(ctx, t, log, b.bus, "run", bus.ID{})
		got := []stream.Event{w.next()}
		f.failNext(1)
		r.durable("tool_call")
		f.failNext(1)
		r.delta()
		r.durable("tool_result")
		f.failNext(1)
		r.durable("run_done")
		rest, err := w.end()
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, rest...)
		if err := r.check(bus.ID{}, got, true); err != nil {
			t.Error(err)
		}
	})
}

// A process that stored an event and died before publishing it leaves a
// hole that no reader on the bus could see: the process taking the run over
// republishes it first.
func TestTakeoverRepublishes(t *testing.T) {
	t.Parallel()
	forEachBus(t, roomy, func(t *testing.T, b backend) {
		ctx := tenantCtx(t, "acme")
		log := memory.New()
		dying := &faulty{Bus: b.bus}
		p, _ := stream.NewPublisher(log, dying, quiet)
		r := newRun(ctx, t, p, log, "run")
		r.durable("run_start")
		r.durable("tool_call")
		// A subscriber straight on the bus, as a watch tailing it.
		sub, err := b.bus.Subscribe(ctx, runTopic(ctx, t, "run"), bus.ID{Seq: 2})
		if err != nil {
			t.Fatal(err)
		}
		dying.dropNext(2)
		r.durable("tool_result")
		r.durable("turn_done")
		p2, _ := stream.NewPublisher(log, b.bus, quiet)
		r.reopen(p2)
		r.durable("run_done")
		for _, want := range []uint64{3, 4, 5} {
			select {
			case m := <-sub.C():
				if m.ID != (bus.ID{Seq: want}) {
					t.Fatalf("the bus delivered %s, want %d", m.ID, want)
				}
			case <-time.After(wait):
				t.Fatalf("no message %d in %v (err %v)", want, wait, sub.Err())
			}
		}
	})
}

// A topic whose history is lost (Redis restarted) mid-run: watches waiting
// on it and new ones get every durable event.
func TestWatchLostHistory(t *testing.T) {
	t.Parallel()
	forEachBus(t, roomy, func(t *testing.T, b backend) {
		ctx := tenantCtx(t, "acme")
		log := memory.New()
		p, _ := stream.NewPublisher(log, b.bus, quiet)
		r := newRun(ctx, t, p, log, "run")
		r.durable("run_start")
		r.delta()
		r.durable("tool_call")
		w := startWatch(ctx, t, log, b.bus, "run", bus.ID{})
		got := []stream.Event{w.next(), w.next()}
		b.lose(t, runTopic(ctx, t, "run"))
		late := startWatch(ctx, t, log, b.bus, "run", bus.ID{Seq: 1})
		r.delta()
		r.durable("tool_result")
		r.delta()
		r.durable("run_done")
		rest, err := w.end()
		if err != nil {
			t.Fatal(err)
		}
		if err := r.check(bus.ID{}, append(got, rest...), true); err != nil {
			t.Error(err)
		}
		lateGot, err := late.end()
		if err != nil {
			t.Fatal(err)
		}
		if err := r.check(bus.ID{Seq: 1}, lateGot, true); err != nil {
			t.Error(err)
		}
	})
}

// A run's events are its tenant's: the same run ID in another tenant is
// another run.
func TestWatchTenantsAreSeparate(t *testing.T) {
	t.Parallel()
	forEachBus(t, roomy, func(t *testing.T, b backend) {
		acme, other := tenantCtx(t, "acme"), tenantCtx(t, "other")
		log := memory.New()
		p, _ := stream.NewPublisher(log, b.bus, quiet)
		ra := newRun(acme, t, p, log, "run")
		ro := newRun(other, t, p, log, "run")
		ro.durable("run_start")
		ra.durable("run_start")
		ro.durable("secret")
		ro.durable("run_done")
		ra.durable("run_done")
		for _, r := range []*run{ra, ro} {
			got, err := startWatch(r.ctx, t, log, b.bus, "run", bus.ID{}).end()
			if err != nil {
				t.Fatal(err)
			}
			if err := r.check(bus.ID{}, got, true); err != nil || len(got) != len(r.events) {
				t.Errorf("got %v: %v", ids(got), err)
			}
		}
	})
}

// A watch ends with its context's error when the context is done, and with
// ErrInvalid for arguments it cannot use.
func TestWatchErrors(t *testing.T) {
	t.Parallel()
	ctx := tenantCtx(t, "acme")
	log := memory.New()
	b := memoryBackend(t, roomy).bus
	w := startWatch(ctx, t, log, b, "run", bus.ID{})
	w.cancel()
	if _, err := w.end(); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled watch: err = %v, want context.Canceled", err)
	}
	for name, seq := range map[string]func(yield func(stream.Event, error) bool){
		"nil log":   stream.Watch(ctx, nil, b, "run", bus.ID{}, isDone),
		"nil bus":   stream.Watch(ctx, log, nil, "run", bus.ID{}, isDone),
		"empty run": stream.Watch(ctx, log, b, "", bus.ID{}, isDone),
		"no tenant": stream.Watch(t.Context(), log, b, "run", bus.ID{}, isDone),
	} {
		var errs []error
		for _, err := range seq {
			errs = append(errs, err)
		}
		if len(errs) != 1 || errs[0] == nil {
			t.Errorf("%s: got %v, want one error", name, errs)
		} else if name != "no tenant" && !errors.Is(errs[0], stream.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, errs[0])
		}
	}
}

// Durable checks before it stores, and a store error leaves the stream where
// it was.
func TestDurableErrors(t *testing.T) {
	t.Parallel()
	ctx := tenantCtx(t, "acme")
	log := memory.New()
	p, _ := stream.NewPublisher(log, memoryBackend(t, roomy).bus)
	s, err := p.Open(ctx, "run")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Durable(ctx, "", nil); !errors.Is(err, stream.ErrInvalid) {
		t.Errorf("no type: err = %v, want ErrInvalid", err)
	}
	if _, err := s.Live(ctx, "x", json.RawMessage("{")); !errors.Is(err, stream.ErrInvalid) {
		t.Errorf("bad data: err = %v, want ErrInvalid", err)
	}
	// Another writer took Seq 1: the store refuses, and the stream stays.
	if err := log.AppendEvents(ctx, "run", []store.Event{{Seq: 1, Type: "other"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Durable(ctx, "mine", nil); err == nil {
		t.Error("Durable over another writer's event succeeded")
	}
	if got := s.Last(); !got.IsZero() {
		t.Errorf("after a failed Durable, Last = %s, want 0", got)
	}
	if _, err := stream.NewPublisher(nil, nil); !errors.Is(err, stream.ErrInvalid) {
		t.Errorf("NewPublisher(nil, nil): err = %v, want ErrInvalid", err)
	}
}

// A process that takes a run over after live events with no durable event
// since goes on from the last one's ID: its live events never repeat an ID.
func TestReopenContinuesLiveIDs(t *testing.T) {
	t.Parallel()
	forEachBus(t, roomy, func(t *testing.T, b backend) {
		ctx := tenantCtx(t, "acme")
		log := memory.New()
		p, _ := stream.NewPublisher(log, b.bus, quiet)
		r := newRun(ctx, t, p, log, "run")
		r.durable("run_start")
		r.delta()
		r.delta()
		r.reopen(p)
		if e := r.delta(); e.ID != (bus.ID{Seq: 1, N: 3}) {
			t.Errorf("after the reopen a live event has ID %s, want 1.3", e.ID)
		}
		r.durable("run_done")
	})
}

// Pending brings what the bus has after an ID, deltas included, up to the
// topic's newest when it is called, and ends there: nothing published later
// is waited for. It ends before a message that would skip a durable event.
func TestPendingUpToTheNewest(t *testing.T) {
	t.Parallel()
	forEachBus(t, roomy, func(t *testing.T, b backend) {
		ctx := tenantCtx(t, "acme")
		log := memory.New()
		p, _ := stream.NewPublisher(log, b.bus, quiet)
		r := newRun(ctx, t, p, log, "run")
		start := r.durable("run_start")
		r.delta()
		r.delta()
		r.durable("turn_done")
		pending := func(after bus.ID) []string {
			t.Helper()
			var got []stream.Event
			for e, err := range stream.Pending(ctx, b.bus, "run", after, isDone) {
				if err != nil {
					t.Fatalf("after %s: err = %v", after, err)
				}
				got = append(got, e)
			}
			return ids(got)
		}
		if g := pending(start.ID); !slices.Equal(g, []string{"1.1", "1.2", "2"}) {
			t.Errorf("after run_start got %v, want 1.1 1.2 2", g)
		}
		if g := pending(r.events[len(r.events)-1].ID); len(g) != 0 {
			t.Errorf("after the newest got %v, want nothing", g)
		}
		r.durable("run_done")
		r.durable("after_the_end")
		if g := pending(start.ID); !slices.Equal(g, []string{"1.1", "1.2", "2", "3"}) {
			t.Errorf("after run_start got %v, want up to run_done", g)
		}
		if g := pending(bus.ID{Seq: 1, N: 1}); !slices.Equal(g, []string{"1.2", "2", "3"}) {
			t.Errorf("after 1.1 got %v, want 1.2 2 3", g)
		}
	})
}

// Pending ends before a message that would skip a durable event the bus
// never had: the log has it.
func TestPendingStopsAtAHole(t *testing.T) {
	t.Parallel()
	forEachBus(t, roomy, func(t *testing.T, b backend) {
		ctx := tenantCtx(t, "acme")
		log := memory.New()
		f := &faulty{Bus: b.bus}
		p, _ := stream.NewPublisher(log, f, quiet)
		r := newRun(ctx, t, p, log, "run")
		start := r.durable("run_start")
		r.delta()
		f.dropNext(1)
		r.durable("turn_done") // stored, never published
		r.delta()
		r.durable("run_done")
		var got []stream.Event
		for e, err := range stream.Pending(ctx, b.bus, "run", start.ID, isDone) {
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, e)
		}
		if g := ids(got); !slices.Equal(g, []string{"1.1"}) {
			t.Errorf("got %v, want 1.1 then the end before the hole", g)
		}
	})
}
