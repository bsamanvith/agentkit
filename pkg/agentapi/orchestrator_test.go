package agentapi_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store"
)

// countingRuns is an orchestrator of the test's own: it hands each call to a
// runner.Runner and rebuilds its executions with runner.NewExecution, as one
// that runs them elsewhere would.
type countingRuns struct {
	rn      *runner.Runner
	started atomic.Int64
	stopped atomic.Bool
}

func (c *countingRuns) Start(ctx context.Context, conv string, msg llm.Message) (*runner.Execution, error) {
	c.started.Add(1)
	return rewrap(c.rn.Run(ctx, conv, msg, runner.Interactive()))
}

func (c *countingRuns) Resume(ctx context.Context, id string) (*runner.Execution, error) {
	return rewrap(c.rn.Resume(ctx, id))
}

func (c *countingRuns) Cancel(ctx context.Context, id string) (store.Run, error) {
	return c.rn.Cancel(ctx, id)
}

func (c *countingRuns) Interrupt(ctx context.Context, id string) (store.Run, error) {
	return c.rn.Interrupt(ctx, id)
}

func (c *countingRuns) Shutdown(ctx context.Context) error {
	c.stopped.Store(true)
	return c.rn.Shutdown(ctx)
}

func (c *countingRuns) Decide(context.Context, string, string, flow.Decision) (*runner.Execution, error) {
	return nil, errors.ErrUnsupported
}

func rewrap(x *runner.Execution, err error) (*runner.Execution, error) {
	if err != nil {
		return nil, err
	}
	return runner.NewExecution(x.Run, x.Events()), nil
}

// TestWithRuns: the server runs its runs with the orchestrator WithRuns
// gives, over the server's own store, and stops it at shutdown; there is no
// local runner to recover with.
func TestWithRuns(t *testing.T) {
	t.Parallel()
	runs := &countingRuns{}
	opt := agentapi.WithRuns(func(st runner.Store, agents runner.AgentFactory) (agentapi.Runs, error) {
		rn, err := runner.New(st, agents)
		runs.rn = rn
		return runs, err
	})
	r := newRig(t, script(t, llmtest.Text("one"), llmtest.Text("two")), rigConfig{api: []agentapi.Option{opt}})
	if r.srv.Runner() != nil {
		t.Error("Runner() is not nil with WithRuns")
	}
	first := r.run("hi", "")
	if end := first.last(); end.Type != agentapi.TypeRunDone || end.Answer != "one" {
		t.Fatalf("first run ended %+v", end)
	}
	if end := r.run("again", first.conversationID()).last(); end.Answer != "two" {
		t.Fatalf("second run ended %+v", end)
	}
	if n := runs.started.Load(); n != 2 {
		t.Errorf("Start called %d times, want 2", n)
	}
	if err := r.srv.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !runs.stopped.Load() {
		t.Error("Shutdown did not stop the orchestrator")
	}
}
