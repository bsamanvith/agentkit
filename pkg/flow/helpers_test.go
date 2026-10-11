package flow_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/flow"
)

var errBoom = errors.New("boom")

// calls records which steps actually ran, across the whole test.
type calls struct {
	mu  sync.Mutex
	got []string
}

func (c *calls) add(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, name)
}

func (c *calls) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.got)
}

func (c *calls) count(name string) int {
	n := 0
	for _, g := range c.all() {
		if g == name {
			n++
		}
	}
	return n
}

// trail is a step that records its run in c and appends its name to the
// "trail" list in the state.
func trail(c *calls, name string) flow.Step {
	return flow.Func(name, func(_ context.Context, st *flow.State) error {
		c.add(name)
		return appendTrail(st, name)
	})
}

func appendTrail(st *flow.State, name string) error {
	t, err := get[[]string](st, "trail")
	if err != nil && !errors.Is(err, flow.ErrNotFound) {
		return err
	}
	return flow.Set(st, "trail", append(t, name))
}

func get[T any](st *flow.State, key string) (T, error) { return flow.Get[T](st, key) }

func mustGet[T any](t *testing.T, st *flow.State, key string) T {
	t.Helper()
	v, err := flow.Get[T](st, key)
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}
	return v
}

func mustSet[T any](t *testing.T, st *flow.State, key string, v T) {
	t.Helper()
	if err := flow.Set(st, key, v); err != nil {
		t.Fatalf("Set(%q): %v", key, err)
	}
}

// failOnce is a step that fails the first time it runs and succeeds after.
// Before failing it writes to the state, to prove that a failed step's partial
// changes are not checkpointed.
func failOnce(c *calls, name string) flow.Step {
	var mu sync.Mutex
	failed := false
	return flow.Func(name, func(_ context.Context, st *flow.State) error {
		c.add(name)
		mu.Lock()
		first := !failed
		failed = true
		mu.Unlock()
		if first {
			if err := appendTrail(st, name+"-partial"); err != nil {
				return err
			}
			return errBoom
		}
		return appendTrail(st, name)
	})
}

// shapeChecked is a Checkpointer that fails the test when a checkpoint is saved
// without the fingerprint of the tree that is running. run and resume wrap
// their checkpointer in it, so every test in the package checks it.
type shapeChecked struct {
	flow.Checkpointer
	t    testing.TB
	want string
}

func (c shapeChecked) Save(ctx context.Context, id string, ck flow.Checkpoint) error {
	if ck.Shape != c.want {
		c.t.Errorf("saved a checkpoint (status %s, %d steps) with shape %q, want the running tree's %q", ck.Status, len(ck.Steps), ck.Shape, c.want)
	}
	return c.Checkpointer.Save(ctx, id, ck)
}

// withShapeCheck wraps cp for a run of step. A tree that is not valid has no
// fingerprint, and flow reports that itself.
func withShapeCheck(t testing.TB, cp flow.Checkpointer, step flow.Step) flow.Checkpointer {
	want, err := flow.Fingerprint(step)
	if err != nil {
		return cp
	}
	return shapeChecked{Checkpointer: cp, t: t, want: want}
}

func run(t *testing.T, cp flow.Checkpointer, id string, step flow.Step, opts ...flow.Option) (*flow.State, error) {
	t.Helper()
	cp = withShapeCheck(t, cp, step)
	return flow.Run(t.Context(), id, step, append([]flow.Option{flow.WithCheckpointer(cp)}, opts...)...)
}

func resume(t *testing.T, cp flow.Checkpointer, id string, step flow.Step, opts ...flow.Option) (*flow.State, error) {
	t.Helper()
	cp = withShapeCheck(t, cp, step)
	return flow.Resume(t.Context(), id, step, append([]flow.Option{flow.WithCheckpointer(cp)}, opts...)...)
}

func load(t *testing.T, cp flow.Checkpointer, id string) flow.Checkpoint {
	t.Helper()
	c, err := cp.Load(t.Context(), id)
	if err != nil {
		t.Fatalf("Load(%q): %v", id, err)
	}
	return c
}

func equalStrings(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}
