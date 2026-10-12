package memory_test

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/bus/bustest"
	"github.com/bsamanvith/agentkit/pkg/bus/memory"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// TestMain fails the package if any goroutine outlives its tests: every
// subscription the suite cancels or closes must stop.
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestConformance(t *testing.T) {
	t.Parallel()
	bustest.Run(t, func(_ *testing.T, trim func(bus.Topic) bus.Trim) bustest.Backend {
		clk := clocktest.NewFake(time.Time{})
		return bustest.Backend{
			Bus: memory.New(memory.WithClock(clk), memory.WithTrim(trim)),
			// The memory bus loses a topic only when it expires: let it.
			Lose: func(_ *testing.T, topic bus.Topic) { clk.Advance(trim(topic).MaxAge) },
		}
	})
}

// A failed publish on a topic with no message yet outlives the minute sweep,
// which drops the topics with nothing to remember.
func TestFailedFirstPublishOutlivesTheSweep(t *testing.T) {
	clk := clocktest.NewFake(time.Time{})
	m := memory.New(memory.WithClock(clk))
	defer m.Close()
	acme, _ := tenancy.NewTenant("acme")
	topic, _ := bus.RunTopic(acme, "first")
	other, _ := bus.RunTopic(acme, "other")
	done, cancel := context.WithCancel(t.Context())
	cancel()
	if err := m.Publish(done, topic, bus.Message{ID: bus.ID{Seq: 1}}); err == nil {
		t.Fatal("publish with a done context succeeded")
	}
	clk.Advance(2 * time.Minute)
	for _, p := range []struct {
		topic bus.Topic
		seq   uint64
	}{{other, 1}, {topic, 2}} { // the first publish sweeps
		if err := m.Publish(t.Context(), p.topic, bus.Message{ID: bus.ID{Seq: p.seq}}); err != nil {
			t.Fatal(err)
		}
	}
	s, err := m.Subscribe(t.Context(), topic, bus.ID{})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case m, ok := <-s.C():
		if ok {
			t.Fatalf("got %s as if it were the first message", m.ID)
		}
		if !errors.Is(s.Err(), bus.ErrGap) {
			t.Errorf("Err = %v, want ErrGap", s.Err())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("subscription neither delivered nor ended in 10s")
	}
}

// BenchmarkPublishFanOut is the cost of one message published and delivered
// to each of 8 subscribers. The publisher waits for them every 1000
// messages, well within the history kept, so none falls behind into ErrGap
// and every message is delivered, which the benchmark checks.
func BenchmarkPublishFanOut(b *testing.B) {
	const subscribers, batch = 8, 1000
	acme, _ := tenancy.NewTenant("acme")
	topic, _ := bus.RunTopic(acme, "bench")
	m := memory.New()
	defer m.Close()
	ctx, cancel := context.WithCancel(b.Context())
	defer cancel()
	var wg sync.WaitGroup
	got := make([]atomic.Uint64, subscribers)
	subs := make([]bus.Subscription, subscribers)
	for i := range subscribers {
		s, err := m.Subscribe(ctx, topic, bus.ID{})
		if err != nil {
			b.Fatal(err)
		}
		subs[i] = s
		wg.Go(func() {
			for msg := range s.C() {
				got[i].Store(msg.ID.Seq)
			}
		})
	}
	caughtUp := func(seq uint64) {
		for i := range got {
			for got[i].Load() < seq {
				if err := subs[i].Err(); err != nil {
					b.Fatalf("subscriber %d ended at %d of %d: %v", i, got[i].Load(), seq, err)
				}
				runtime.Gosched()
			}
		}
	}
	data := make([]byte, 256)
	b.ReportAllocs()
	var seq uint64
	for b.Loop() {
		seq++
		if err := m.Publish(ctx, topic, bus.Message{ID: bus.ID{Seq: seq}, Data: data}); err != nil {
			b.Fatal(err)
		}
		if seq%batch == 0 {
			caughtUp(seq)
		}
	}
	caughtUp(seq)
	cancel()
	wg.Wait()
}
