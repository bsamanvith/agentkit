package pool_test

import (
	"context"
	"errors"
	"iter"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/runner/pool"
)

// spawner is a pool that makes an agent of its own for every request, as a
// sandbox or VM pool will, and keeps them in a pool.Owned.
type spawner struct {
	name  string
	owned pool.Owned

	mu   sync.Mutex
	made []*spawned
}

func (s *spawner) Name() string              { return s.name }
func (s *spawner) Isolation() pool.Isolation { return pool.Sandbox }
func (s *spawner) Close() error              { return s.owned.Close() }

func (s *spawner) Agent(context.Context, pool.Request) (runner.Agent, error) {
	a := &spawned{}
	release, err := s.owned.Add(a)
	if err != nil {
		return nil, err
	}
	a.release = release
	s.mu.Lock()
	s.made = append(s.made, a)
	s.mu.Unlock()
	return a, nil
}

// spawned is an agent with something to close; it counts its closes.
type spawned struct {
	mu      sync.Mutex
	closes  int
	fail    error
	release func()
}

func (*spawned) Run(context.Context, harness.Input) iter.Seq2[harness.Event, error] {
	return func(func(harness.Event, error) bool) {}
}

func (a *spawned) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closes++
	return a.fail
}

func (a *spawned) closed() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.closes
}

// Closing the router closes every pool and the agents they made that are
// still open, once; an agent that closed itself and was released is left
// alone; and nothing is handed out afterwards (review item D).
func TestRouterCloseClosesPoolAgents(t *testing.T) {
	t.Parallel()
	sp := &spawner{name: "sandbox"}
	rt := router(t, []pool.Pool{pool.NewProcess("default"), sp}, []pool.Rule{{Pool: "sandbox", When: pool.Needs(pool.Sandbox)}}, "default")
	r := chat("a")
	r.Isolation = pool.Sandbox
	for range 3 {
		if _, err := rt.Agent(t.Context(), r); err != nil {
			t.Fatalf("Agent: %v", err)
		}
	}
	// The first agent ends its run, closes itself and lets the pool forget it.
	done := sp.made[0]
	if err := done.Close(); err != nil {
		t.Fatal(err)
	}
	done.release()

	sp.made[2].fail = errors.New("vm would not stop")
	err := rt.Close()
	if err == nil || !errors.Is(err, sp.made[2].fail) {
		t.Errorf("Close = %v, want the agent's close error", err)
	}
	for i, a := range sp.made {
		if got := a.closed(); got != 1 {
			t.Errorf("agent %d closed %d times, want once", i, got)
		}
	}
	if again := rt.Close(); !errors.Is(again, sp.made[2].fail) {
		t.Errorf("second Close = %v, want the first's result", again)
	}
	for i, a := range sp.made {
		if got := a.closed(); got != 1 {
			t.Errorf("after a second Close, agent %d closed %d times, want once", i, got)
		}
	}

	if _, err := rt.Agent(t.Context(), r); !errors.Is(err, pool.ErrClosed) || !errors.Is(err, runner.ErrShutdown) {
		t.Errorf("Agent after Close = %v, want ErrClosed matching runner.ErrShutdown", err)
	}
	if len(sp.made) != 3 {
		t.Errorf("the pool made %d agents, want no more after Close", len(sp.made))
	}

	// A closed router gives nothing, even from a pool that would.
	lax := router(t, []pool.Pool{fake{"default", pool.Process}}, nil, "default")
	_ = lax.Close()
	if a, err := lax.Agent(t.Context(), chat("a")); !errors.Is(err, pool.ErrClosed) {
		t.Errorf("a closed router's Agent = %v, %v; want ErrClosed", a, err)
	}
}

// A pool closed while it was making an agent closes that agent at once.
func TestOwnedClosesLateAdds(t *testing.T) {
	t.Parallel()
	var o pool.Owned
	if err := o.Close(); err != nil {
		t.Fatalf("closing an empty Owned: %v", err)
	}
	late := &spawned{}
	if _, err := o.Add(late); !errors.Is(err, pool.ErrClosed) {
		t.Errorf("Add after Close = %v, want ErrClosed", err)
	}
	if late.closed() != 1 {
		t.Errorf("an agent added after Close was closed %d times, want once", late.closed())
	}
}

// The process pool closes nothing (its agents are the server's) and gives
// nothing once closed.
func TestProcessClose(t *testing.T) {
	t.Parallel()
	built := &spawned{}
	rt := pool.Default()
	r := chat("a")
	r.Built = built
	if _, err := rt.Agent(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	if built.closed() != 0 {
		t.Error("closing the process pool closed the server's agent")
	}
	p := pool.NewProcess("p")
	_ = p.Close()
	if _, err := p.Agent(t.Context(), r); !errors.Is(err, pool.ErrClosed) {
		t.Errorf("a closed process pool's Agent = %v, want ErrClosed", err)
	}
}
