package memory_test

import (
	"context"
	"sync"
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

// BenchmarkPublishFanOut publishes to a topic with 8 subscribers reading
// along: the per-message cost of the in-process bus.
func BenchmarkPublishFanOut(b *testing.B) {
	acme, _ := tenancy.NewTenant("acme")
	topic, _ := bus.RunTopic(acme, "bench")
	m := memory.New()
	defer m.Close()
	ctx, cancel := context.WithCancel(b.Context())
	defer cancel()
	var wg sync.WaitGroup
	for range 8 {
		s, err := m.Subscribe(ctx, topic, bus.ID{})
		if err != nil {
			b.Fatal(err)
		}
		wg.Go(func() {
			for range s.C() {
			}
		})
	}
	data := make([]byte, 256)
	b.ReportAllocs()
	var seq uint64
	for b.Loop() {
		seq++
		if err := m.Publish(ctx, topic, bus.Message{ID: bus.ID{Seq: seq}, Data: data}); err != nil {
			b.Fatal(err)
		}
	}
	cancel()
	wg.Wait()
}
