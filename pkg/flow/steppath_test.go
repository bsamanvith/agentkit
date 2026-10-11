package flow_test

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/flow"
)

func TestStepPathNamesTheRunningStep(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var got []string
	probe := func(name string) flow.Step {
		return flow.Func(name, func(ctx context.Context, _ *flow.State) error {
			id, path, ok := flow.StepPath(ctx)
			if !ok {
				t.Errorf("%s: StepPath not ok inside Run", name)
			}
			mu.Lock()
			defer mu.Unlock()
			got = append(got, id+" "+path)
			return nil
		})
	}
	n := 0
	step := flow.Sequence("root",
		probe("a"),
		flow.Parallel("par", probe("b"), probe("b")),
		flow.Loop("loop", probe("c"), func(*flow.State) (bool, error) { n++; return n == 2, nil }, 3),
	)
	if _, err := flow.Run(t.Context(), "run-1", step, flow.WithCheckpointer(flow.NewMemory())); err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	want := []string{
		"run-1 root/a",
		"run-1 root/loop[0]/c",
		"run-1 root/loop[1]/c",
		"run-1 root/par[0]/b",
		"run-1 root/par[1]/b",
	}
	if !slices.Equal(got, want) {
		t.Errorf("paths = %q, want %q", got, want)
	}

	// Outside Run (a step or combinator run directly) there is no run.
	detached := flow.Sequence("root", flow.Func("x", func(ctx context.Context, _ *flow.State) error {
		if id, path, ok := flow.StepPath(ctx); ok {
			t.Errorf("StepPath outside Run = %q, %q, true", id, path)
		}
		return nil
	}))
	if err := detached.Run(t.Context(), flow.NewState()); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := flow.StepPath(t.Context()); ok {
		t.Error("StepPath of a plain context is ok")
	}
}
