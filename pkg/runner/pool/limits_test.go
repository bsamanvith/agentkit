package pool_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/runner/pool"
)

// Slots hold at most their limit: TryAcquire refuses when they are all
// taken, Acquire waits for one (or for ctx), and a release frees one.
func TestSlots(t *testing.T) {
	t.Parallel()
	s := pool.NewSlots(2)
	if s.Limit() != 2 || s.InUse() != 0 {
		t.Fatalf("new slots: limit %d, in use %d; want 2, 0", s.Limit(), s.InUse())
	}
	for i := range 2 {
		if !s.TryAcquire() {
			t.Fatalf("slot %d of two could not be taken", i+1)
		}
	}
	if s.TryAcquire() || s.InUse() != 2 {
		t.Fatalf("a third slot of two was taken (in use %d)", s.InUse())
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := s.Acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Acquire with none free = %v, want the context's error", err)
	}
	got := make(chan error, 1)
	go func() { got <- s.Acquire(t.Context()) }()
	s.Release()
	if err := <-got; err != nil || s.InUse() != 2 {
		t.Fatalf("Acquire after a release = %v (in use %d), want the freed slot", err, s.InUse())
	}
	done, cancelDone := context.WithCancel(t.Context())
	cancelDone()
	s.Release()
	if err := s.Acquire(done); !errors.Is(err, context.Canceled) {
		t.Errorf("Acquire with a done context = %v, want context.Canceled even with a slot free", err)
	}
	s.Release()
	defer func() {
		if recover() == nil {
			t.Error("a release of no slot did not panic")
		}
	}()
	s.Release()
}

// WithLimit gives a pool its slots; a pool without one has none (no limit),
// and a limit for an unknown pool, or below 1, is refused.
func TestRouterLimits(t *testing.T) {
	t.Parallel()
	pools := []pool.Pool{fake{name: "chat"}, fake{name: "background"}}
	rt, err := pool.NewRouter(pools, nil, "background", pool.WithLimit("chat", 3))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	if s := rt.Slots("chat"); s == nil || s.Limit() != 3 {
		t.Errorf("chat's slots = %+v, want 3", s)
	}
	if s := rt.Slots("background"); s != nil {
		t.Errorf("background has slots %+v, want none (no limit)", s)
	}
	if rt.DefaultPool().Name() != "background" || len(rt.Pools()) != 2 {
		t.Errorf("DefaultPool %q, Pools %d; want background, 2", rt.DefaultPool().Name(), len(rt.Pools()))
	}
	for name, opt := range map[string]pool.RouterOption{
		"an unknown pool": pool.WithLimit("nope", 1),
		"a limit of 0":    pool.WithLimit("chat", 0),
		"a negative one":  pool.WithLimit("chat", -1),
	} {
		if _, err := pool.NewRouter(pools, nil, "background", opt); !errors.Is(err, pool.ErrInvalidConfig) {
			t.Errorf("NewRouter with %s = %v, want ErrInvalidConfig", name, err)
		}
	}
}

// A run placed already (its stored pool) goes back to that pool, whatever
// the rules say, while the pool exists and is isolated enough; otherwise it
// is routed afresh.
func TestRouterSendsAPlacedRunBack(t *testing.T) {
	t.Parallel()
	rt := router(t, []pool.Pool{fake{name: "chat"}, fake{name: "background"}, fake{name: "vm", iso: pool.VM}},
		[]pool.Rule{{Pool: "chat", When: pool.Chats()}}, "background")
	for _, c := range []struct {
		name string
		req  pool.Request
		want string
	}{
		{"a chat placed in background", pool.Request{Placement: runner.Placement{Interactive: true, Resume: true, Pool: "background"}}, "background"},
		{"a background run placed in chat", pool.Request{Placement: runner.Placement{Pool: "chat"}}, "chat"},
		{"a run of a pool that is gone", pool.Request{Placement: runner.Placement{Interactive: true, Pool: "gone"}}, "chat"},
		{"a run placed in a pool too weak for it", pool.Request{Placement: runner.Placement{Pool: "chat"}, Isolation: pool.VM}, ""},
		{"a run placed in a pool strong enough", pool.Request{Placement: runner.Placement{Pool: "vm"}, Isolation: pool.Sandbox}, "vm"},
	} {
		got, err := placed(t, rt, c.req)
		switch {
		case c.want == "" && !errors.Is(err, pool.ErrNoPool):
			t.Errorf("%s: placed in %q, %v; want ErrNoPool (never a weaker pool)", c.name, got, err)
		case c.want != "" && (err != nil || got != c.want):
			t.Errorf("%s: placed in %q, %v; want %s", c.name, got, err, c.want)
		}
	}
}
