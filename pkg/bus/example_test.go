package bus_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/bus/memory"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// A watcher that reconnects resumes after the last ID it saw. When the bus no
// longer has what came next, it says so instead of skipping ahead.
func Example() {
	ctx := context.Background()
	acme, _ := tenancy.NewTenant("acme")
	topic, _ := bus.RunTopic(acme, "run-1")
	b := memory.New()
	defer b.Close()

	for _, id := range []string{"1", "1.1", "1.2", "2"} {
		seq, _ := bus.ParseID(id)
		_ = b.Publish(ctx, topic, bus.Message{ID: seq, Data: []byte("event " + id)})
	}

	lastSeen, _ := bus.ParseID("1.1") // from Last-Event-ID
	sub, _ := b.Subscribe(ctx, topic, lastSeen)
	for range 2 {
		m := <-sub.C()
		fmt.Printf("%s: %s\n", m.ID, m.Data)
	}

	err := b.Publish(ctx, topic, bus.Message{ID: bus.ID{Seq: 2}})
	fmt.Println("publish 2 again is stale:", errors.Is(err, bus.ErrStale))
	// Output:
	// 1.2: event 1.2
	// 2: event 2
	// publish 2 again is stale: true
}
