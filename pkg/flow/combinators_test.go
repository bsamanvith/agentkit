package flow_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/flow"
)

func TestSequenceRunsInOrderAndStopsAtFirstError(t *testing.T) {
	t.Parallel()
	var c calls
	fail := flow.Func("b", func(context.Context, *flow.State) error { c.add("b"); return errBoom })

	ok := flow.Sequence("seq", trail(&c, "a"), trail(&c, "b"), trail(&c, "c"))
	st, err := run(t, flow.NewMemory(), "ok", ok)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	equalStrings(t, "trail", mustGet[[]string](t, st, "trail"), []string{"a", "b", "c"})

	c = calls{}
	_, err = run(t, flow.NewMemory(), "fail", flow.Sequence("seq", trail(&c, "a"), fail, trail(&c, "c")))
	if !errors.Is(err, errBoom) {
		t.Fatalf("Run error = %v, want errBoom", err)
	}
	var se *flow.StepError
	if !errors.As(err, &se) || se.Path != "seq/b" {
		t.Errorf("error = %v, want a StepError at seq/b", err)
	}
	equalStrings(t, "steps run", c.all(), []string{"a", "b"})
}

func TestParallelMergesDisjointKeys(t *testing.T) {
	t.Parallel()
	setter := func(key string, v int) flow.Step {
		return flow.Func("set-"+key, func(_ context.Context, st *flow.State) error {
			// Each branch also sees the state as it was before Parallel.
			if got, _ := flow.Get[string](st, "base"); got != "kept" {
				return fmt.Errorf("branch %s saw base = %q", key, got)
			}
			return flow.Set(st, key, v)
		})
	}
	seed := flow.NewState()
	mustSet(t, seed, "base", "kept")
	del := flow.Func("del", func(_ context.Context, st *flow.State) error { st.Delete("gone"); return nil })
	mustSet(t, seed, "gone", true)

	st, err := run(t, flow.NewMemory(), "r", flow.Parallel("par", setter("a", 1), setter("b", 2), del), flow.WithState(seed))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if mustGet[int](t, st, "a") != 1 || mustGet[int](t, st, "b") != 2 || mustGet[string](t, st, "base") != "kept" {
		t.Errorf("merged state = %v", st.Keys())
	}
	if st.Has("gone") {
		t.Error("a key deleted by one branch is still present")
	}
}

func TestParallelMergeIsIndependentOfFinishOrder(t *testing.T) {
	t.Parallel()
	results := map[string]string{}
	for name, order := range map[string][]int{"forward": {0, 1, 2}, "reverse": {2, 1, 0}} {
		branches := finishInOrder(order, func(i int, st *flow.State) error {
			return flow.Set(st, fmt.Sprintf("k%d", i), i)
		})
		st, err := run(t, flow.NewMemory(), "r", flow.Parallel("par", branches...))
		if err != nil {
			t.Fatalf("%s: Run: %v", name, err)
		}
		out, _ := json.Marshal(st)
		results[name] = string(out)
	}
	if results["forward"] != results["reverse"] || results["forward"] != `{"k0":0,"k1":1,"k2":2}` {
		t.Errorf("merged states differ by finish order: %v", results)
	}
}

// finishInOrder builds branches b0..bN-1 named "b<i>" that run fn and then
// return in the given order: the first in order returns first, each of the
// others waits for the one before it. Gates, not sleeps, so the order is
// exact.
func finishInOrder(order []int, fn func(i int, st *flow.State) error) []flow.Step {
	done := make([]chan struct{}, len(order))
	for i := range done {
		done[i] = make(chan struct{})
	}
	prev := map[int]int{}
	for pos := 1; pos < len(order); pos++ {
		prev[order[pos]] = order[pos-1]
	}
	branches := make([]flow.Step, len(order))
	for i := range order {
		branches[i] = flow.Func(fmt.Sprintf("b%d", i), func(ctx context.Context, st *flow.State) error {
			if p, wait := prev[i]; wait {
				select {
				case <-done[p]:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			err := fn(i, st)
			close(done[i])
			return err
		})
	}
	return branches
}

func TestParallelKeyConflictIsError(t *testing.T) {
	t.Parallel()
	set := func(name string, v int) flow.Step {
		return flow.Func(name, func(_ context.Context, st *flow.State) error {
			if err := flow.Set(st, "shared", v); err != nil {
				return err
			}
			return flow.Set(st, "own-"+name, v)
		})
	}
	// Run the Parallel directly so the state it leaves behind can be checked.
	st := flow.NewState()
	mustSet(t, st, "before", 1)
	err := flow.Parallel("par", set("x", 1), set("y", 1)).Run(t.Context(), st) // equal values still conflict
	if !errors.Is(err, flow.ErrConflict) {
		t.Fatalf("Run error = %v, want ErrConflict", err)
	}
	var ce *flow.ConflictError
	if !errors.As(err, &ce) || len(ce.Keys) != 1 || ce.Keys[0] != "shared" {
		t.Errorf("ConflictError = %+v, want key [shared]", ce)
	}
	if keys := st.Keys(); len(keys) != 1 || keys[0] != "before" {
		t.Errorf("state after a conflict has keys %v, want it unchanged", keys)
	}

	// Deleting a key another branch sets is a conflict too.
	seed := flow.NewState()
	mustSet(t, seed, "shared", 0)
	del := flow.Func("del", func(_ context.Context, st *flow.State) error { st.Delete("shared"); return nil })
	if err := flow.Parallel("par", set("x", 1), del).Run(t.Context(), seed); !errors.Is(err, flow.ErrConflict) {
		t.Errorf("delete vs set: error = %v, want ErrConflict", err)
	}

	// Through Run, the error reaches the caller too.
	if _, err := run(t, flow.NewMemory(), "r", flow.Parallel("par", set("x", 1), set("y", 2))); !errors.Is(err, flow.ErrConflict) {
		t.Errorf("Run error = %v, want ErrConflict", err)
	}
}

func TestParallelFailureCancelsSiblings(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	sawCancel := make(chan struct{})
	errNotCancelled := errors.New("sibling was not cancelled")

	waiter := flow.Func("waiter", func(ctx context.Context, _ *flow.State) error {
		close(started)
		select {
		case <-ctx.Done():
			close(sawCancel)
			return ctx.Err()
		case <-time.After(5 * time.Second): // only reached if the bug is present
			return errNotCancelled
		}
	})
	failer := flow.Func("failer", func(ctx context.Context, _ *flow.State) error {
		select {
		case <-started: // fail only once the sibling is running
			return errBoom
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	_, err := run(t, flow.NewMemory(), "r", flow.Parallel("par", waiter, failer))
	if !errors.Is(err, errBoom) {
		t.Fatalf("Run error = %v, want errBoom (not the sibling's cancellation)", err)
	}
	select {
	case <-sawCancel:
	default:
		t.Error("the sibling's context was never cancelled")
	}
}

func TestLoopStopsWhenConditionHolds(t *testing.T) {
	t.Parallel()
	var c calls
	inc := flow.Func("inc", func(_ context.Context, st *flow.State) error {
		c.add("inc")
		n, _ := flow.Get[int](st, "n")
		return flow.Set(st, "n", n+1)
	})
	until := func(st *flow.State) (bool, error) {
		n, err := flow.Get[int](st, "n")
		return n >= 3, err
	}
	var rec []string
	st, err := run(t, flow.NewMemory(), "r", flow.Loop("count", inc, until, 10),
		observer(func(e flow.Event) {
			if e.Kind == flow.EventStarted {
				rec = append(rec, e.Path)
			}
		}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := mustGet[int](t, st, "n"); got != 3 || c.count("inc") != 3 {
		t.Errorf("n = %d after %d runs, want 3 after 3", got, c.count("inc"))
	}
	equalStrings(t, "started", rec, []string{"count", "count[0]/inc", "count[1]/inc", "count[2]/inc"})
}

func TestLoopErrorsAtMaximum(t *testing.T) {
	t.Parallel()
	var c calls
	never := func(*flow.State) (bool, error) { return false, nil }
	errRunaway := errors.New("loop ran past any sane limit")
	body := flow.Func("body", func(context.Context, *flow.State) error {
		c.add("body")
		if c.count("body") > 100 { // a loop that ignores its maximum stops here, not never
			return errRunaway
		}
		return nil
	})
	_, err := run(t, flow.NewMemory(), "r", flow.Loop("l", body, never, 3))
	if !errors.Is(err, flow.ErrMaxIterations) {
		t.Fatalf("Run error = %v, want ErrMaxIterations", err)
	}
	if c.count("body") != 3 {
		t.Errorf("body ran %d times, want exactly the maximum, 3", c.count("body"))
	}
}

func TestRouteRunsChosenStep(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"fast", "slow"} {
		var c calls
		r := flow.Route("pick",
			func(st *flow.State) (string, error) { return flow.Get[string](st, "kind") },
			map[string]flow.Step{"fast": trail(&c, "fast"), "slow": trail(&c, "slow")})
		seed := flow.NewState()
		mustSet(t, seed, "kind", kind)
		if _, err := run(t, flow.NewMemory(), "r", r, flow.WithState(seed)); err != nil {
			t.Fatalf("%s: Run: %v", kind, err)
		}
		equalStrings(t, kind+": steps run", c.all(), []string{kind})
	}
}

func TestRouteUnknownKeyIsError(t *testing.T) {
	t.Parallel()
	var c calls
	r := flow.Route("pick",
		func(*flow.State) (string, error) { return "mystery", nil },
		map[string]flow.Step{"fast": trail(&c, "fast"), "slow": trail(&c, "slow")})
	_, err := run(t, flow.NewMemory(), "r", r)
	if !errors.Is(err, flow.ErrUnknownRoute) {
		t.Fatalf("Run error = %v, want ErrUnknownRoute", err)
	}
	if got := c.all(); len(got) != 0 {
		t.Errorf("steps ran for an unknown key: %v", got)
	}
}
