package stream_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/stream"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// userWatch is one stream.WatchUser running in the background.
type userWatch struct {
	t      *testing.T
	events chan stream.Event
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

func startUserWatch(t *testing.T, ctx context.Context, b bus.Bus, user string, from bus.ID) *userWatch {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	w := &userWatch{t: t, events: make(chan stream.Event, 1000), cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(w.done)
		for e, err := range stream.WatchUser(ctx, b, user, from) {
			if err != nil {
				w.err = err
				return
			}
			w.events <- e
		}
	}()
	t.Cleanup(func() { cancel(); <-w.done })
	return w
}

func (w *userWatch) next() stream.Event {
	w.t.Helper()
	select {
	case e := <-w.events:
		return e
	case <-time.After(wait):
		w.t.Fatalf("no notification in %v", wait)
	}
	return stream.Event{}
}

func notifier(t *testing.T, b bus.Bus, opts ...stream.Option) *stream.Notifier {
	t.Helper()
	n, err := stream.NewNotifier(b, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func notify(t *testing.T, ctx context.Context, n *stream.Notifier, user string, i int) bus.ID {
	t.Helper()
	id, err := n.Notify(ctx, user, "run_status", []byte(fmt.Sprint(i)))
	if err != nil {
		t.Fatalf("Notify(%q, %d): %v", user, i, err)
	}
	return id
}

// A notification reaches its user in its tenant, nobody else. Notifications
// published in the same millisecond, by several processes, all go out, in
// order.
func TestNotificationsReachOnlyTheirUser(t *testing.T) {
	t.Parallel()
	forEachBus(t, roomy, func(t *testing.T, b backend) {
		acme, other := tenantCtx(t, "acme"), tenantCtx(t, "other")
		clk := clocktest.NewFake(time.UnixMilli(1_700_000_000_000)) // never advances
		n1, n2 := notifier(t, b.bus, stream.WithClock(clk)), notifier(t, b.bus, stream.WithClock(clk))
		type target struct {
			ctx  context.Context
			user string
		}
		targets := []target{{acme, "u"}, {acme, "v"}, {other, "u"}}
		var watches []*userWatch
		for _, tg := range targets {
			w := startUserWatch(t, tg.ctx, b.bus, tg.user, bus.ID{})
			watches = append(watches, w)
		}
		for i, tg := range targets {
			testWaitSubscribed(t, tg.ctx, n1, watches[i], tg.user)
		}
		var sent [3][]bus.ID
		for i := range 12 {
			k := i % 3
			n := n1
			if i%2 == 1 {
				n = n2
			}
			sent[k] = append(sent[k], notify(t, targets[k].ctx, n, targets[k].user, 100*k+i))
		}
		for k, w := range watches {
			for j, id := range sent[k] {
				e := w.next()
				want := fmt.Sprint(100*k + 3*j + k)
				if e.ID != id || string(e.Data) != want || e.Type != "run_status" {
					t.Errorf("%v: got %s %s %s, want %s %s", targets[k].user, e.ID, e.Type, e.Data, id, want)
				}
			}
			select {
			case e := <-w.events:
				t.Errorf("%s got an extra notification %s %s", targets[k].user, e.ID, e.Data)
			default:
			}
		}
	})
}

// testWaitSubscribed notifies until the watch gets one, so later
// notifications are not lost to a watch not yet following.
func testWaitSubscribed(t *testing.T, ctx context.Context, n *stream.Notifier, w *userWatch, user string) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		notify(t, ctx, n, user, -1)
		select {
		case <-w.events:
			// Drain the other pings that made it.
			for {
				select {
				case <-w.events:
				case <-time.After(50 * time.Millisecond):
					return
				}
			}
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatal("the watch never got a notification")
}

// A watch from the zero ID starts now; one from an ID resumes after it; one
// from an ID no longer kept gets a resync, at the topic's newest, and goes
// on from there.
func TestWatchUserPositions(t *testing.T) {
	t.Parallel()
	tight := func(bus.Topic) bus.Trim { return bus.Trim{MaxLen: 3, MaxAge: time.Hour} }
	forEachBus(t, tight, func(t *testing.T, b backend) {
		ctx := tenantCtx(t, "acme")
		clk := clocktest.NewFake(time.UnixMilli(1_700_000_000_000))
		n := notifier(t, b.bus, stream.WithClock(clk))
		var ids []bus.ID
		for i := range 300 { // Redis trims only whole nodes of 100

			ids = append(ids, notify(t, ctx, n, "u", i))
			clk.Advance(time.Millisecond)
		}
		// From an ID still kept: the ones after it.
		w := startUserWatch(t, ctx, b.bus, "u", ids[296])
		for _, want := range ids[297:] {
			if e := w.next(); e.ID != want {
				t.Errorf("resumed watch got %s, want %s", e.ID, want)
			}
		}
		// From an ID trimmed away: a resync at the newest, then new ones.
		old := startUserWatch(t, ctx, b.bus, "u", ids[0])
		if e := old.next(); e.Type != stream.TypeResync || e.ID != ids[299] {
			t.Errorf("trimmed watch got %s %s first, want a resync at %s", e.ID, e.Type, ids[299])
		}
		// From zero: nothing before now.
		fresh := startUserWatch(t, ctx, b.bus, "u", bus.ID{})
		testWaitSubscribed(t, ctx, n, fresh, "u")
		last := notify(t, ctx, n, "u", 99)
		for _, w := range []*userWatch{w, old, fresh} {
			for {
				e := w.next()
				if string(e.Data) == "-1" { // the pings
					continue
				}
				if e.ID != last {
					t.Errorf("got %s %s, want the last notification %s", e.ID, e.Data, last)
				}
				break
			}
		}
	})
}

// A watch whose topic loses its history while it waits gets a resync.
func TestWatchUserLostHistory(t *testing.T) {
	t.Parallel()
	forEachBus(t, roomy, func(t *testing.T, b backend) {
		ctx := tenantCtx(t, "acme")
		n := notifier(t, b.bus)
		w := startUserWatch(t, ctx, b.bus, "u", bus.ID{})
		testWaitSubscribed(t, ctx, n, w, "u")
		topic, _ := bus.UserTopic(tenancy.MustFrom(ctx), "u")
		b.lose(t, topic)
		for {
			notify(t, ctx, n, "u", 1)
			e := w.next()
			if e.Type == stream.TypeResync {
				break
			}
			if b.name == "redis" { // the bus remembered the stream: no gap
				return
			}
			t.Fatalf("after the loss got %s %s, want a resync", e.ID, e.Type)
		}
	})
}

func TestNotifyErrors(t *testing.T) {
	t.Parallel()
	ctx := tenantCtx(t, "acme")
	b := memoryBackend(t, roomy).bus
	n := notifier(t, b)
	if _, err := stream.NewNotifier(nil); !errors.Is(err, stream.ErrInvalid) {
		t.Errorf("NewNotifier(nil): err = %v, want ErrInvalid", err)
	}
	if _, err := n.Notify(ctx, "", "x", nil); !errors.Is(err, stream.ErrInvalid) {
		t.Errorf("empty user: err = %v, want ErrInvalid", err)
	}
	if _, err := n.Notify(ctx, "u", "", nil); !errors.Is(err, stream.ErrInvalid) {
		t.Errorf("no type: err = %v, want ErrInvalid", err)
	}
	if _, err := n.Notify(t.Context(), "u", "x", nil); err == nil {
		t.Error("Notify without a tenant succeeded")
	}
	for name, seq := range map[string]func(yield func(stream.Event, error) bool){
		"nil bus":    stream.WatchUser(ctx, nil, "u", bus.ID{}),
		"empty user": stream.WatchUser(ctx, b, "", bus.ID{}),
		"no tenant":  stream.WatchUser(t.Context(), b, "u", bus.ID{}),
	} {
		var errs []error
		for _, err := range seq {
			errs = append(errs, err)
		}
		if len(errs) != 1 || errs[0] == nil {
			t.Errorf("%s: got %v, want one error", name, errs)
		}
	}
	cctx, cancel := context.WithCancel(ctx)
	w := startUserWatch(t, cctx, b, "u", bus.ID{})
	cancel()
	<-w.done
	if !errors.Is(w.err, context.Canceled) {
		t.Errorf("cancelled: err = %v, want context.Canceled", w.err)
	}
}
