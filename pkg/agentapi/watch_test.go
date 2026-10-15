package agentapi_test

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/store"
)

// countingBus counts the subscriptions to run topics: those going on, the
// most there ever were at once, and all of them.
type countingBus struct {
	bus.Bus
	active, most, total atomic.Int64
}

func (b *countingBus) Subscribe(ctx context.Context, topic bus.Topic, after bus.ID) (bus.Subscription, error) {
	if topic.Kind() != bus.KindRun {
		return b.Bus.Subscribe(ctx, topic, after)
	}
	sub, err := b.Bus.Subscribe(ctx, topic, after)
	if err != nil {
		return nil, err
	}
	b.total.Add(1)
	n := b.active.Add(1)
	for m := b.most.Load(); n > m && !b.most.CompareAndSwap(m, n); m = b.most.Load() {
	}
	// A watch ends its subscription by canceling the context it gave it.
	context.AfterFunc(ctx, func() { b.active.Add(-1) })
	return sub, nil
}

// countingStore counts the reads of runs, the held-checks among them; armed,
// it lets one read see the run let go, then holds every read until release.
type countingStore struct {
	agentapi.Store
	clk  clock.Clock
	gets atomic.Int64

	armed   atomic.Bool
	letGo   chan struct{} // closed when a read, armed, saw nobody hold the run
	once    sync.Once
	release chan struct{}
}

func newCountingStore(st agentapi.Store, clk clock.Clock) *countingStore {
	return &countingStore{Store: st, clk: clk, letGo: make(chan struct{}), release: make(chan struct{})}
}

func (s *countingStore) Get(ctx context.Context, id string) (store.Run, error) {
	s.gets.Add(1)
	if s.armed.Load() {
		select {
		case <-s.letGo:
			select {
			case <-s.release:
			case <-ctx.Done():
				return store.Run{}, ctx.Err()
			}
		default:
		}
	}
	run, err := s.Store.Get(ctx, id)
	if s.armed.Load() && err == nil && (run.Lease.Owner == "" || !s.clk.Now().Before(run.Lease.Expires)) {
		s.once.Do(func() { close(s.letGo) })
	}
	return run, err
}

// hangingStore is a store whose lease renewals hang once crash is set: the
// process holding a run stops, without letting it go.
type hangingStore struct {
	agentapi.Store
	crash atomic.Bool
}

func (s *hangingStore) Renew(ctx context.Context, runID, owner string, ttl time.Duration) (store.Lease, error) {
	if s.crash.Load() {
		<-ctx.Done()
		return store.Lease{}, ctx.Err()
	}
	return s.Store.Renew(ctx, runID, owner, ttl)
}

// threeTurns is a model that calls echo twice, a turn each, then answers;
// each reply waits for a value on its gate.
func threeTurns(t *testing.T) *model {
	m := script(t,
		textAndCalls("one", call("c1", "echo", `{"text":"x"}`)),
		textAndCalls("two", call("c2", "echo", `{"text":"y"}`)),
		llmtest.Text("done"),
	)
	m.gate = make(chan struct{}, 3)
	return m
}

// watching waits until b runs the given shared watches with that many
// viewers, each watch following the bus: from then on b gets the run's live
// events (a watch that starts after events were stored skips the live ones
// before them).
func watching(t *testing.T, b *rig, watches, viewers int) {
	t.Helper()
	testkit.Eventually(t, 10*time.Second, func() bool {
		w, v := b.srv.Watching()
		return w == watches && v == viewers && b.subs.active.Load() == int64(watches)
	})
}

// storedIDs returns the IDs of the stored events of evs.
func storedIDs(evs []agentapi.Event) []string {
	var out []string
	for _, e := range evs {
		if _, n, _ := wire.ParseID(e.ID); n == 0 {
			out = append(out, e.ID)
		}
	}
	return out
}

// checkViewer checks that a viewer from after lastID got the holder's stored
// events after it, each once, in order, and its last event.
func checkViewer(t *testing.T, name string, got, holder []agentapi.Event, lastID string) {
	t.Helper()
	want := storedIDs(holder[after(holder, lastID):])
	if g := storedIDs(got); !slices.Equal(g, want) {
		t.Errorf("viewer %s got the stored events %v, want %v", name, g, want)
	}
	if len(got) == 0 || got[len(got)-1].ID != holder[len(holder)-1].ID {
		t.Errorf("viewer %s streamed %v %v, want it to end with the run's last event %s", name, idsOf(got), types(got), holder[len(holder)-1].ID)
	}
}

// TestSharedWatch: viewers of a run held elsewhere, joining at its start,
// mid-run and from before the watch's start with different Last-Event-IDs,
// share one bus subscription and one held-check, and each gets the run from
// its own position, every stored event once, in order (rig.check compares
// each stream with the run's events, text included).
func TestSharedWatch(t *testing.T) {
	t.Parallel()
	var cb *countingBus
	var cs *countingStore
	a, b := replicasWith(t, threeTurns(t), replicaConfig{
		tools:  demoTools(t)["a"],
		busB:   func(bb bus.Bus) bus.Bus { cb = &countingBus{Bus: bb}; return cb },
		storeB: func(st agentapi.Store) agentapi.Store { cs = newCountingStore(st, clock.Real()); return cs },
	})
	m := a.model
	s := a.start(t.Context(), "go", "")
	start := s.until(agentapi.TypeRunStart, 0)
	m.gate <- struct{}{}
	td1 := s.until(agentapi.TypeTurnDone, 1)

	// The first viewers start the watch after turn 1.
	type view struct {
		name, last string
		s          *stream
	}
	var views []view
	for range 8 {
		views = append(views, view{"after turn 1", td1.ID, b.follow(start.RunID, td1.ID)})
	}
	watching(t, b, 1, 8)

	// While the run waits for the model, one held-check runs, not one per
	// viewer: at most one read of the run per poll.
	cs.gets.Store(0)
	const polls = 20
	time.Sleep(polls * testPoll)
	if n := cs.gets.Load(); n > polls+2 {
		t.Errorf("8 viewers read the run %d times in %d polls, want one held-check (at most %d)", n, polls, polls+2)
	}

	// Viewers from before where the watch started read the store first.
	views = append(views,
		view{"from the start", "", b.follow(start.RunID, "")},
		view{"after run_start", start.ID, b.follow(start.RunID, start.ID)},
	)
	watching(t, b, 1, 10)
	m.gate <- struct{}{}
	td2 := s.until(agentapi.TypeTurnDone, 2)
	views = append(views, view{"after turn 2", td2.ID, b.follow(start.RunID, td2.ID)})
	watching(t, b, 1, 11)
	m.gate <- struct{}{}

	holder := s.all()
	for _, v := range views {
		checkViewer(t, v.name, v.s.all(), holder, v.last)
	}
	if n := cb.most.Load(); n != 1 {
		t.Errorf("11 viewers held %d bus subscriptions at once, want 1", n)
	}
	watching(t, b, 0, 0)
}

// TestSharedWatchViewersFallBehind: viewers that are dropped from the watch
// whenever they are not waiting for its next event (a buffer of none) go on
// from the log and the watch's memory, and each still gets every stored
// event once, in order, and the turns' text (rig.check).
func TestSharedWatchViewersFallBehind(t *testing.T) {
	t.Parallel()
	a, b := replicasWith(t, threeTurns(t), replicaConfig{
		tools: demoTools(t)["a"],
		apiB:  []agentapi.Option{agentapi.WithWatchBuffer(0)},
	})
	m := a.model
	s := a.start(t.Context(), "go", "")
	start := s.until(agentapi.TypeRunStart, 0)
	var views []*stream
	for range 6 {
		views = append(views, b.follow(start.RunID, start.ID))
	}
	watching(t, b, 1, 6)
	for range 3 {
		m.gate <- struct{}{}
	}
	holder := s.all()
	for i, v := range views {
		checkViewer(t, string(rune('A'+i)), v.all(), holder, start.ID)
	}
}

// TestSharedWatchEndsWithLastViewer: the watch goes on while it has viewers,
// and stops, its subscription and goroutine with it, when the last one
// leaves; goleak (TestMain) checks no goroutine outlives the tests.
func TestSharedWatchEndsWithLastViewer(t *testing.T) {
	t.Parallel()
	var cb *countingBus
	m := echo(t)
	m.gate = make(chan struct{}, 1)
	a, b := replicasWith(t, m, replicaConfig{
		busB: func(bb bus.Bus) bus.Bus { cb = &countingBus{Bus: bb}; return cb },
	})
	s := a.start(t.Context(), "hi", "")
	start := s.until(agentapi.TypeRunStart, 0)
	var views []*stream
	var cancels []context.CancelFunc
	for range 3 {
		ctx, cancel := context.WithCancel(t.Context())
		v := b.followCtx(ctx, start.RunID, "")
		v.until(agentapi.TypeRunStart, 0)
		views, cancels = append(views, v), append(cancels, cancel)
	}
	watching(t, b, 1, 3)
	for i := range 2 {
		views[i].abandon(cancels[i])
	}
	watching(t, b, 1, 1)
	if n := cb.active.Load(); n != 1 {
		t.Errorf("with one viewer left, %d bus subscriptions, want 1", n)
	}
	views[2].abandon(cancels[2])
	watching(t, b, 0, 0)
	testkit.Eventually(t, 10*time.Second, func() bool { return cb.active.Load() == 0 }) // the count drops just after the subscription ends
	m.gate <- struct{}{}
	s.all()
}

// TestSharedWatchCommands: a stop sent through a server that does not hold
// the run waits for its end on the run's shared watch, with the viewers
// there: one bus subscription between them.
func TestSharedWatchCommands(t *testing.T) {
	t.Parallel()
	var cb *countingBus
	m := echo(t)
	m.gate = make(chan struct{}) // never answers: the run ends by the stop
	a, b := replicasWith(t, m, replicaConfig{
		busB: func(bb bus.Bus) bus.Bus { cb = &countingBus{Bus: bb}; return cb },
	})
	s := a.start(t.Context(), "hi", "")
	start := s.until(agentapi.TypeRunStart, 0)
	views := []*stream{b.follow(start.RunID, ""), b.follow(start.RunID, start.ID)}
	for _, v := range views {
		// Not compared with the run as b serves it later: a stopped chat
		// run's run_error is streamed, not stored, so b, which has no hub
		// of the run, ends it from its status, with another ID (T090).
		v.done = true
	}
	watching(t, b, 1, 2)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		b.do("POST", "/v1/runs/"+start.RunID+"/stop", "")
	}()
	holder := s.all()
	<-stopped
	if end := holder[len(holder)-1]; end.Error == nil || end.Error.Code != agentapi.CodeRunInterrupted {
		t.Fatalf("the run ended with %+v, want interrupted", end)
	}
	// The interruption is streamed, not stored: a viewer gets the holder's,
	// or, when b sees the run let go before it, the one its status says.
	for i, v := range views {
		got := v.all()
		want := storedIDs(holder[after(holder, []string{"", start.ID}[i]):])
		end := got[len(got)-1]
		if g := storedIDs(got); !slices.Equal(g, want) || end.Error == nil || end.Error.Code != agentapi.CodeRunInterrupted {
			t.Errorf("viewer %d streamed %v %v, want the stored events %v and run_error interrupted", i, idsOf(got), types(got), want)
		}
	}
	if n := cb.most.Load(); n != 1 {
		t.Errorf("two viewers and a stop held %d bus subscriptions at once, want 1", n)
	}
}

// TestSharedWatchTakeover: when the process holding a run stops without
// letting it go, and another takes it over once its lease is out, the shared
// watch follows the new holder, for every viewer.
func TestSharedWatchTakeover(t *testing.T) {
	t.Parallel()
	m := echo(t)
	m.gate = make(chan struct{}) // a never answers: it hangs, and c answers
	var hs *hangingStore
	var cs *countingStore
	var cb *countingBus
	a, b := replicasWith(t, m, replicaConfig{
		storeA: func(st agentapi.Store) agentapi.Store { hs = &hangingStore{Store: st}; return hs },
		storeB: func(st agentapi.Store) agentapi.Store { cs = newCountingStore(st, nil); return cs },
		busB:   func(bb bus.Bus) bus.Bus { cb = &countingBus{Bus: bb}; return cb },
	})
	cs.clk = a.clock
	ctx, cancel := context.WithCancel(t.Context())
	s := a.start(ctx, "hi", "")
	defer s.abandon(cancel)
	start := s.until(agentapi.TypeRunStart, 0)
	views := []*stream{b.follow(start.RunID, ""), b.follow(start.RunID, start.ID), b.follow(start.RunID, start.ID)}
	watching(t, b, 1, 3)

	// a hangs; its lease runs out, and b's held-check sees the run let go.
	hs.crash.Store(true)
	cs.armed.Store(true)
	a.clock.Advance(31 * time.Second) // past the runner's default lease of 30s
	select {
	case <-cs.letGo:
	case <-time.After(10 * time.Second):
		t.Fatal("b never saw the run let go")
	}
	// c takes it over before b reads the run again; c answers once b
	// follows it, so that b's viewers get c's text live (a watch that
	// starts after events were stored skips the live ones before them).
	cm := echo(t)
	cm.gate = make(chan struct{}, 1)
	c := a.replica(t, cm)
	resumed := c.open(c.postPath(t.Context(), "/v1/runs/"+start.RunID+"/resume", ""))
	close(cs.release)
	testkit.Eventually(t, 10*time.Second, func() bool { return cb.total.Load() == 2 })
	testkit.Eventually(t, 10*time.Second, func() bool { return cb.active.Load() == 1 }) // the old one's count drops just after it ends
	cm.gate <- struct{}{}

	tail := resumed.all()
	if end := tail[len(tail)-1]; end.Type != agentapi.TypeRunDone || end.Answer != "echo: hi" {
		t.Fatalf("c's resumed run ended with %+v, want c's answer", end)
	}
	whole := b.events(start.RunID, "")
	checkViewer(t, "from the start", views[0].all(), whole, "")
	for i, v := range views[1:] {
		checkViewer(t, string(rune('B'+i)), v.all(), whole, start.ID)
	}
	watching(t, b, 0, 0)
}

func idsOf(evs []agentapi.Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.ID
	}
	return out
}

// slowEndStore holds the storing of a run_done event until release: the
// holder has let the run go by then.
type slowEndStore struct {
	agentapi.Store
	reached, release chan struct{}
	once             sync.Once
}

func (s *slowEndStore) AppendEvents(ctx context.Context, runID string, events []store.Event) error {
	for _, e := range events {
		if e.Type == agentapi.TypeRunDone {
			s.once.Do(func() { close(s.reached) })
			select {
			case <-s.release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return s.Store.AppendEvents(ctx, runID, events)
}

// TestSharedWatchStoredEnding: a run's holder lets it go just before it
// stores its last event; the watch waits for that event rather than end
// with one made up from the run's status, with another ID.
func TestSharedWatchStoredEnding(t *testing.T) {
	t.Parallel()
	m := echo(t)
	m.gate = make(chan struct{}, 1)
	se := &slowEndStore{reached: make(chan struct{}), release: make(chan struct{})}
	a, b := replicasWith(t, m, replicaConfig{
		storeA: func(st agentapi.Store) agentapi.Store { se.Store = st; return se },
	})
	s := a.start(t.Context(), "hi", "")
	start := s.until(agentapi.TypeRunStart, 0)
	views := []*stream{b.follow(start.RunID, ""), b.follow(start.RunID, start.ID)}
	watching(t, b, 1, 2)
	m.gate <- struct{}{}
	<-se.reached
	time.Sleep(5 * testPoll) // b's held-check sees the run let go
	close(se.release)
	holder := s.all()
	checkViewer(t, "from the start", views[0].all(), holder, "")
	checkViewer(t, "after run_start", views[1].all(), holder, start.ID)
}
