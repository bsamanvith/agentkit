package agentapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/bus/memory"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// replicas are two servers on one store and one bus (T090): a holds the
// runs, b serves them; each rig checks the streams it read.
func replicas(t *testing.T, m *model) (a, b *rig) {
	t.Helper()
	return replicasWith(t, m, replicaConfig{})
}

// replicaConfig changes what replicasWith builds.
type replicaConfig struct {
	tools  tool.Registry                       // a's tools
	storeA func(agentapi.Store) agentapi.Store // wraps a's view of the store
	storeB func(agentapi.Store) agentapi.Store // wraps b's view of the store
	busB   func(bus.Bus) bus.Bus               // wraps b's view of the bus
	apiB   []agentapi.Option                   // more options for b
}

// replicasWith is replicas, changed by cfg; clk and the shared store and bus
// are for more replicas (replica).
func replicasWith(t *testing.T, m *model, cfg replicaConfig) (a, b *rig) {
	t.Helper()
	sh := newShared(t)
	wrap := func(f func(agentapi.Store) agentapi.Store) agentapi.Store {
		if f == nil {
			return sh.store
		}
		return f(sh.store)
	}
	subs := &countingBus{Bus: sh.bus}
	busB := bus.Bus(subs)
	if cfg.busB != nil {
		busB = cfg.busB(subs)
	}
	a = newRig(t, m, rigConfig{clock: sh.clock, store: wrap(cfg.storeA), tools: cfg.tools, api: sh.api(sh.bus)})
	b = newRig(t, echo(t), rigConfig{clock: sh.clock, store: wrap(cfg.storeB), api: append(sh.api(busB), cfg.apiB...)})
	b.models = nil // b runs nothing: a's model requests are a's
	a.shared, b.shared = sh, sh
	b.subs = subs
	return a, b
}

// shared is what replicas share: a clock, a store and a bus.
type shared struct {
	clock *clocktest.Fake
	store agentapi.Store
	bus   *memory.Bus
}

func newShared(t *testing.T) *shared {
	clk := clocktest.NewFake(time.Time{})
	b := memory.New()
	t.Cleanup(func() { _ = b.Close() })
	return &shared{clock: clk, store: scoped.Memory(clk), bus: b}
}

func (sh *shared) api(b bus.Bus) []agentapi.Option {
	return []agentapi.Option{agentapi.WithBus(b), agentapi.WithStreamPoll(testPoll)}
}

// testPoll is how often replicas check the store in these tests.
const testPoll = 10 * time.Millisecond

// replica is one more server on the store and bus a and b share, running m.
func (r *rig) replica(t *testing.T, m *model) *rig {
	t.Helper()
	c := newRig(t, m, rigConfig{clock: r.shared.clock, store: r.shared.store, api: r.shared.api(r.shared.bus)})
	c.shared = r.shared
	return c
}

// TestFollowAcrossReplicas: a run held by one server streams from another,
// live, from its start and from after an event (Last-Event-ID), with the IDs
// the holder gave them.
func TestFollowAcrossReplicas(t *testing.T) {
	t.Parallel()
	m := echo(t)
	m.gate = make(chan struct{}, 1)
	a, b := replicas(t, m)
	s := a.start(t.Context(), "hi", "")
	start := s.until(agentapi.TypeRunStart, 0)

	whole := b.follow(start.RunID, "")
	if e := whole.until(agentapi.TypeRunStart, 0); e.ID != start.ID {
		t.Fatalf("b's run_start has ID %q, want the holder's %q", e.ID, start.ID)
	}
	tail := b.follow(start.RunID, start.ID)
	m.gate <- struct{}{}
	got, rest := s.all(), tail.all()
	whole.all()
	if end := got[len(got)-1]; end.Type != agentapi.TypeRunDone || !jsonEqual(rest, got[1:]) {
		t.Errorf("from after run_start b streamed\n%s\nwant the holder's\n%s", jsonOf(rest), jsonOf(got[1:]))
	}
}

// TestStopAndCancelAcrossReplicas: stop and cancel sent to a server that
// does not hold the run reach the one that does, and answer with the run as
// it ended.
func TestStopAndCancelAcrossReplicas(t *testing.T) {
	t.Parallel()
	m := echo(t)
	m.gate = make(chan struct{}) // never answers: the runs end by the commands
	a, b := replicas(t, m)
	for _, c := range []struct{ path, status, code string }{
		{"stop", agentapi.StatusInterrupted, agentapi.CodeRunInterrupted},
		{"cancel", agentapi.StatusCanceled, agentapi.CodeCancelled},
	} {
		s := a.start(t.Context(), "hi", "")
		s.until(agentapi.TypeRunStart, 0)
		var run agentapi.Run
		resp, body := b.do(http.MethodPost, "/v1/runs/"+s.runID()+"/"+c.path, "")
		if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &run) != nil || run.Status != c.status {
			t.Fatalf("%s through the other server = %d %s, want 200 and the run %s", c.path, resp.StatusCode, body, c.status)
		}
		if e := s.all(); e[len(e)-1].Error == nil || e[len(e)-1].Error.Code != c.code {
			t.Errorf("after %s the holder's stream ended with %+v, want run_error %s", c.path, e[len(e)-1], c.code)
		}
	}
}

// TestFollowWhenTheBusLagsTheRelease: a follower whose bus has not brought
// a turn's text when the holder lets the run go still streams that text with
// the IDs the holder gave it, from the bus, not as text replayed from the
// store with other IDs.
func TestFollowWhenTheBusLagsTheRelease(t *testing.T) {
	t.Parallel()
	m := echo(t)
	m.gate = make(chan struct{}, 1)
	lag := &laggingBus{}
	a, b := replicasWith(t, m, replicaConfig{busB: func(bb bus.Bus) bus.Bus { lag.Bus = bb; return lag }})
	s := a.start(t.Context(), "hi", "")
	start := s.until(agentapi.TypeRunStart, 0)
	// b's watch has read the store: the turn reaches it by the bus only.
	whole := b.follow(start.RunID, "")
	whole.until(agentapi.TypeRunStart, 0)
	tail := b.follow(start.RunID, start.ID)
	m.gate <- struct{}{}
	got, rest := s.all(), tail.all()
	whole.all()
	if !jsonEqual(rest, got[1:]) {
		t.Errorf("from after run_start b streamed\n%s\nwant the holder's\n%s", jsonOf(rest), jsonOf(got[1:]))
	}
}

// laggingBus is a bus whose first subscription to a run's topic brings no
// live event: it holds the first one, and the rest behind it, until the
// subscriber gives up (it saw the run let go). Later subscriptions are
// served as usual.
type laggingBus struct {
	bus.Bus
	once sync.Once
}

func (b *laggingBus) Subscribe(ctx context.Context, topic bus.Topic, after bus.ID) (bus.Subscription, error) {
	sub, err := b.Bus.Subscribe(ctx, topic, after)
	first := false
	if err == nil && topic.Kind() == bus.KindRun {
		b.once.Do(func() { first = true })
	}
	if !first {
		return sub, err
	}
	l := &laggingSub{Subscription: sub, c: make(chan bus.Message)}
	go func() {
		defer close(l.c)
		for m := range sub.C() {
			if m.ID.N != 0 {
				<-ctx.Done()
			}
			if ctx.Err() != nil { // given up on: what it held is lost
				for range sub.C() {
				}
				return
			}
			select {
			case l.c <- m:
			case <-ctx.Done():
			}
		}
	}()
	return l, nil
}

type laggingSub struct {
	bus.Subscription
	c chan bus.Message
}

func (s *laggingSub) C() <-chan bus.Message { return s.c }
