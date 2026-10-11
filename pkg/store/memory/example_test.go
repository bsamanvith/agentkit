package memory_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/memory"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// One owner at a time holds a run's lease; another can claim it once it
// expires. The clock is a fake, so the example does not wait.
func Example() {
	clk := clocktest.NewFake(time.Time{})
	s := memory.New(memory.WithClock(clk))
	acme, _ := tenancy.NewTenant("acme")
	ctx := tenancy.With(context.Background(), acme)

	if _, err := s.Create(ctx, "run-1", "conv-1"); err != nil {
		fmt.Println(err)
		return
	}
	lease, _ := s.Claim(ctx, "run-1", "worker-a", time.Minute)
	fmt.Println("worker-a holds it until", lease.Expires.Format(time.TimeOnly))

	_, err := s.Claim(ctx, "run-1", "worker-b", time.Minute)
	fmt.Println("worker-b is refused:", errors.Is(err, store.ErrLeased))

	clk.Advance(time.Minute)
	lease, _ = s.Claim(ctx, "run-1", "worker-b", time.Minute)
	fmt.Println("after expiry:", lease.Owner)
	// Output:
	// worker-a holds it until 00:01:00
	// worker-b is refused: true
	// after expiry: worker-b
}
