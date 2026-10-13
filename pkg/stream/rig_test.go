package stream_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"
	"go.uber.org/goleak"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/bus"
	busmemory "github.com/bsamanvith/agentkit/pkg/bus/memory"
	redisbus "github.com/bsamanvith/agentkit/pkg/bus/redis"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/stream"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// TestMain fails the package if a goroutine outlives its tests: every watch
// and inbox a test ends must stop.
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

// wait bounds every wait for something that must happen.
const wait = 10 * time.Second

// poll is the log check interval in tests: short, so a watch that relies on
// it ends quickly.
const poll = 20 * time.Millisecond

// backend is one bus under test, with a way to lose a topic's history.
type backend struct {
	name string
	bus  bus.Bus
	// lose drops a topic's history, as when it expires or Redis restarts
	// without its data.
	lose func(t *testing.T, topic bus.Topic)
}

// quiet drops the logs of publishes the tests make fail.
var quiet = stream.WithLogger(slog.New(slog.DiscardHandler))

func roomy(bus.Topic) bus.Trim { return bus.Trim{MaxLen: 100000, MaxAge: time.Hour} }

// forEachBus runs f against the memory bus and, when AGENTKIT_REDIS_URL is
// set, the Redis bus, each trimming by trim.
func forEachBus(t *testing.T, trim func(bus.Topic) bus.Trim, f func(t *testing.T, b backend)) {
	t.Helper()
	t.Run("memory", func(t *testing.T) {
		t.Parallel()
		f(t, memoryBackend(t, trim))
	})
	t.Run("redis", func(t *testing.T) {
		t.Parallel()
		f(t, redisBackend(t, trim))
	})
}

func memoryBackend(t *testing.T, trim func(bus.Topic) bus.Trim) backend {
	clk := clocktest.NewFake(time.Time{})
	b := busmemory.New(busmemory.WithClock(clk), busmemory.WithTrim(trim))
	t.Cleanup(func() { _ = b.Close() })
	return backend{
		name: "memory",
		bus:  b,
		// The memory bus loses a topic only when it expires: let it.
		lose: func(_ *testing.T, topic bus.Topic) { clk.Advance(trim(topic).MaxAge) },
	}
}

func redisBackend(t *testing.T, trim func(bus.Topic) bus.Trim) backend {
	url := testkit.IntegrationEnv(t, "AGENTKIT_REDIS_URL")
	opt, err := goredis.ParseURL(url)
	if err != nil {
		t.Fatalf("AGENTKIT_REDIS_URL: %v", err)
	}
	opt.MaintNotificationsConfig = &maintnotifications.Config{Mode: maintnotifications.ModeDisabled}
	c := goredis.NewClient(opt)
	prefix := "agentkit-test:" + rand.Text() + ":"
	b, err := redisbus.New(c, redisbus.WithPrefix(prefix), redisbus.WithTrim(trim),
		redisbus.WithBackoff(10*time.Millisecond, 200*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = b.Close()
		ctx := context.Background()
		if keys, err := c.Keys(ctx, prefix+"*").Result(); err == nil && len(keys) > 0 {
			_ = c.Del(ctx, keys...).Err()
		}
		_ = c.Close()
	})
	return backend{
		name: "redis",
		bus:  b,
		// As when Redis restarts without its data. The bus still remembers
		// what it published, which only helps.
		lose: func(t *testing.T, topic bus.Topic) {
			if err := c.Del(t.Context(), b.Key(topic)).Err(); err != nil {
				t.Fatal(err)
			}
		},
	}
}

func tenantCtx(t *testing.T, id string) context.Context {
	t.Helper()
	tn, err := tenancy.NewTenant(id)
	if err != nil {
		t.Fatal(err)
	}
	return tenancy.With(t.Context(), tn)
}

func runTopic(t *testing.T, ctx context.Context, runID string) bus.Topic {
	t.Helper()
	topic, err := bus.RunTopic(tenancy.MustFrom(ctx), runID)
	if err != nil {
		t.Fatal(err)
	}
	return topic
}

// faulty wraps a bus with publishes that fail or vanish on demand.
type faulty struct {
	bus.Bus
	mu   sync.Mutex
	fail int // fail the next fail publishes, as on a timeout
	drop int // drop the next drop publishes, as when the process dies first; before fail
}

var errInjected = errors.New("injected publish failure")

func (f *faulty) Publish(ctx context.Context, topic bus.Topic, msg bus.Message) error {
	f.mu.Lock()
	drop := f.drop > 0
	fail := !drop && f.fail > 0
	if drop {
		f.drop--
	} else if fail {
		f.fail--
	}
	f.mu.Unlock()
	switch {
	case fail:
		// The bus sees a publish that may or may not have landed.
		done, cancel := context.WithCancel(ctx)
		cancel()
		_ = f.Bus.Publish(done, topic, msg)
		return errInjected
	case drop:
		return nil
	}
	return f.Bus.Publish(ctx, topic, msg)
}

func (f *faulty) failNext(n int) { f.mu.Lock(); f.fail += n; f.mu.Unlock() }
func (f *faulty) dropNext(n int) { f.mu.Lock(); f.drop += n; f.mu.Unlock() }

// run is a run's events as written, to check what watchers get against.
type run struct {
	t      *testing.T
	ctx    context.Context
	id     string
	log    store.EventLog
	s      *stream.RunStream
	mu     sync.Mutex
	events []stream.Event // in ID order
	live   map[bus.ID]bool
}

func newRun(t *testing.T, ctx context.Context, p *stream.Publisher, log store.EventLog, id string) *run {
	t.Helper()
	r := &run{t: t, ctx: ctx, id: id, log: log, live: map[bus.ID]bool{}}
	r.reopen(p)
	return r
}

// reopen opens the run's stream on p, as the process taking the run over.
func (r *run) reopen(p *stream.Publisher) {
	r.t.Helper()
	s, err := p.Open(r.ctx, r.id)
	if err != nil {
		r.t.Fatalf("Open(%q): %v", r.id, err)
	}
	r.s = s
}

func data(typ string, i int) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"what":%q,"i":%d}`, typ, i))
}

func (r *run) durable(typ string) stream.Event {
	r.t.Helper()
	r.mu.Lock()
	d := data(typ, len(r.events))
	r.mu.Unlock()
	id, err := r.s.Durable(r.ctx, typ, d)
	if err != nil {
		r.t.Fatalf("Durable(%q): %v", typ, err)
	}
	return r.add(stream.Event{ID: id, Type: typ, Data: d}, false)
}

func (r *run) delta() stream.Event {
	r.t.Helper()
	r.mu.Lock()
	d := data("text", len(r.events))
	r.mu.Unlock()
	id, err := r.s.Live(r.ctx, "text_delta", d)
	if err != nil {
		r.t.Fatalf("Live: %v", err)
	}
	return r.add(stream.Event{ID: id, Type: "text_delta", Data: d}, true)
}

func (r *run) add(e stream.Event, live bool) stream.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n := len(r.events); n > 0 && e.ID.Compare(r.events[n-1].ID) <= 0 {
		r.t.Fatalf("event %s is not after the last one, %s", e.ID, r.events[n-1].ID)
	}
	r.events = append(r.events, e)
	if live {
		r.live[e.ID] = true
	}
	return e
}

func isDone(typ string) bool { return typ == "run_done" }

// check says what is wrong with what a watch from after got: an event that
// was not written or not as written, one out of order or repeated, a durable
// event missing before the last one it got, or, when ended says the watch
// ended by itself, an end other than the run's first run_done after after.
func (r *run) check(after bus.ID, got []stream.Event, ended bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	byID := map[bus.ID]stream.Event{}
	for _, e := range r.events {
		byID[e.ID] = e
	}
	prev := after
	for i, e := range got {
		w, ok := byID[e.ID]
		if !ok {
			return fmt.Errorf("event %d (%s %s) was never written", i, e.ID, e.Type)
		}
		if e.Type != w.Type || string(e.Data) != string(w.Data) {
			return fmt.Errorf("event %s = %s %s, want %s %s", e.ID, e.Type, e.Data, w.Type, w.Data)
		}
		if e.ID.Compare(prev) <= 0 {
			return fmt.Errorf("event %d (%s) is not after %s", i, e.ID, prev)
		}
		prev = e.ID
	}
	seen := map[bus.ID]bool{}
	for _, e := range got {
		seen[e.ID] = true
	}
	for _, e := range r.events {
		if e.ID.Compare(after) <= 0 {
			continue
		}
		if ended && isDone(e.Type) && prev != e.ID {
			return fmt.Errorf("the watch ended at %s, not at the end %s; got %v", prev, e.ID, ids(got))
		}
		if r.live[e.ID] || e.ID.Compare(prev) > 0 {
			continue
		}
		if !seen[e.ID] {
			return fmt.Errorf("durable event %s (%s) is missing; got %v", e.ID, e.Type, ids(got))
		}
	}
	return nil
}

func ids(es []stream.Event) []string {
	var s []string
	for _, e := range es {
		s = append(s, e.ID.String())
	}
	return s
}

// watch is one stream.Watch running in the background.
type watch struct {
	t      *testing.T
	after  bus.ID
	events chan stream.Event
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

func startWatch(t *testing.T, ctx context.Context, log store.EventLog, b bus.Bus, runID string, after bus.ID) *watch {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	w := &watch{t: t, after: after, events: make(chan stream.Event, 100000), cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(w.done)
		for e, err := range stream.Watch(ctx, log, b, runID, after, isDone, stream.WithPoll(poll)) {
			if err != nil {
				w.err = err
				return
			}
			w.events <- e
		}
	}()
	t.Cleanup(w.stop)
	return w
}

func (w *watch) stop() {
	w.cancel()
	<-w.done
}

// next returns the watch's next event.
func (w *watch) next() stream.Event {
	w.t.Helper()
	select {
	case e := <-w.events:
		return e
	case <-w.done:
		select {
		case e := <-w.events:
			return e
		default:
		}
		w.t.Fatalf("watch after %s ended (err %v) where an event was due", w.after, w.err)
	case <-time.After(wait):
		w.t.Fatalf("watch after %s: no event in %v", w.after, wait)
	}
	return stream.Event{}
}

// end waits for the watch to end by itself and returns all it got and its
// error.
func (w *watch) end() ([]stream.Event, error) {
	w.t.Helper()
	select {
	case <-w.done:
	case <-time.After(wait):
		w.t.Fatalf("watch after %s did not end in %v", w.after, wait)
	}
	var got []stream.Event
	for {
		select {
		case e := <-w.events:
			got = append(got, e)
		default:
			return got, w.err
		}
	}
}
