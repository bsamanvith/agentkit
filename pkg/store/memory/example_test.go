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

// The recovery loop starts with no tenant. Its Admin view finds the runs that
// should continue in every tenant, each with its tenant, which the loop puts
// back in ctx to resume the run.
func ExampleStore_Admin() {
	clk := clocktest.NewFake(time.Time{})
	s := memory.New(memory.WithClock(clk))
	for _, id := range []string{"acme", "globex"} {
		tenant, _ := tenancy.NewTenant(id)
		ctx := tenancy.With(context.Background(), tenant)
		if _, err := s.Create(ctx, "run-1", "conv-1"); err != nil {
			fmt.Println(err)
			return
		}
		if _, err := s.Claim(ctx, "run-1", "crashed-worker", time.Minute); err != nil {
			fmt.Println(err)
			return
		}
	}
	clk.Advance(time.Minute) // the worker's leases expire

	runs, _ := s.Admin().ListResumableAll(context.Background(), clk.Now(), 0)
	for _, r := range runs {
		ctx := tenancy.With(context.Background(), r.Tenant)
		_, err := s.Claim(ctx, r.Run.ID, "recovery", time.Minute)
		fmt.Println(r.Tenant.ID(), r.Run.ID, "claimed:", err == nil)
	}
	// Output:
	// acme run-1 claimed: true
	// globex run-1 claimed: true
}
