package agentapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/harness"
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

func (c *countingRuns) Start(ctx context.Context, conv string, msg llm.Message, opts agentapi.StartOptions) (*runner.Execution, error) {
	c.started.Add(1)
	return rewrap(c.rn.Run(ctx, conv, msg, opts.RunOptions()...))
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

func (c *countingRuns) Answer(context.Context, string, ...harness.Answer) (*runner.Execution, error) {
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

// TestNewExecutionWithoutEvents: an execution made with no events ends at
// once with ErrNoEvents instead of panicking in whoever iterates it.
func TestNewExecutionWithoutEvents(t *testing.T) {
	t.Parallel()
	if _, err := runner.NewExecution(store.Run{}, nil).Wait(); !errors.Is(err, runner.ErrNoEvents) {
		t.Errorf("Wait = %v, want ErrNoEvents", err)
	}
}

// gatedRuns holds each execution back from the runner until the
// execution's context is done (or the test ends): the moment between the
// server launching a run and the runner starting it, when the run is leased
// (by this server) but the runner knows no execution of it to stop.
type gatedRuns struct {
	countingRuns
	gate chan struct{}
	open func()
}

func (g *gatedRuns) Start(ctx context.Context, conv string, msg llm.Message, opts agentapi.StartOptions) (*runner.Execution, error) {
	x, err := g.rn.Run(ctx, conv, msg, opts.RunOptions()...)
	if err != nil {
		return nil, err
	}
	return runner.NewExecution(x.Run, func(yield func(runner.Event, error) bool) {
		select {
		case <-ctx.Done():
		case <-g.gate:
		}
		for ev, err := range x.Events() {
			if !yield(ev, err) {
				return
			}
		}
	}), nil
}

// gated returns gatedRuns as the orchestrator of a server whose store is on
// clk; the test ends with its gate open, before the rig's cleanup, whatever
// failed (register it after the rig).
func gated(clk *clocktest.Fake) (*gatedRuns, agentapi.Option) {
	gate := make(chan struct{})
	runs := &gatedRuns{gate: gate, open: sync.OnceFunc(func() { close(gate) })}
	return runs, agentapi.WithRuns(func(st runner.Store, agents runner.AgentFactory) (agentapi.Runs, error) {
		rn, err := runner.New(st, agents, runner.WithClock(clk)) // leases are on the store's clock
		runs.rn = rn
		return runs, err
	})
}

// TestCommandBeforeExecutionStarts: a cancel or stop that reaches the server
// as soon as its run's stream opens, before the runner has started the
// execution, stops the run as it would a moment later, instead of answering
// run_leased for the server's own run.
func TestCommandBeforeExecutionStarts(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ verb, status string }{
		{"cancel", agentapi.StatusCanceled},
		{"stop", agentapi.StatusInterrupted},
	} {
		t.Run(c.verb, func(t *testing.T) {
			t.Parallel()
			clk := clocktest.NewFake(time.Time{})
			runs, opt := gated(clk)
			r := newRig(t, script(t), rigConfig{clock: clk, api: []agentapi.Option{opt}})
			t.Cleanup(runs.open)
			s := r.start(t.Context(), "hi", "")
			id := s.resp.Header.Get("Agentkit-Run-Id")
			resp, body := r.do(http.MethodPost, "/v1/runs/"+id+"/"+c.verb, "")
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("POST %s: %d %s", c.verb, resp.StatusCode, body)
			}
			var run agentapi.Run
			if err := json.Unmarshal(body, &run); err != nil {
				t.Fatal(err)
			}
			if run.Status != c.status {
				t.Errorf("the run is %s after %s, want %s", run.Status, c.verb, c.status)
			}
			s.all()
		})
	}
}

// TestCommandBeforeExecutionStartsAcrossReplicas: a cancel or stop sent
// through another server reaches the run's server as a command in the same
// moment, before its runner has started the execution, and stops the run
// there: the command is not consumed and lost.
func TestCommandBeforeExecutionStartsAcrossReplicas(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ verb, status string }{
		{"cancel", agentapi.StatusCanceled},
		{"stop", agentapi.StatusInterrupted},
	} {
		t.Run(c.verb, func(t *testing.T) {
			t.Parallel()
			var runs *gatedRuns
			a, b := replicasWith(t, script(t), replicaConfig{apiA: func(clk *clocktest.Fake) []agentapi.Option {
				var opt agentapi.Option
				runs, opt = gated(clk)
				return []agentapi.Option{opt}
			}})
			t.Cleanup(runs.open)
			s := a.start(t.Context(), "hi", "")
			id := s.resp.Header.Get("Agentkit-Run-Id")
			type answer struct {
				status int
				body   []byte
			}
			answered := make(chan answer, 1)
			go func() {
				resp, body := b.do(http.MethodPost, "/v1/runs/"+id+"/"+c.verb, "")
				answered <- answer{resp.StatusCode, body}
			}()
			// A canceled run shows interrupted a moment (let go, then
			// canceled): wait for the status the command ends it with.
			testkit.Eventually(t, 30*time.Second, func() bool { return b.getRun(id).Status == c.status })
			runs.open() // a lost command would have left the run waiting for this
			got := <-answered
			var run agentapi.Run
			if got.status != http.StatusOK || json.Unmarshal(got.body, &run) != nil || run.Status != c.status {
				t.Errorf("POST %s through the other server: %d %s, want 200 and the run %s", c.verb, got.status, got.body, c.status)
			}
			s.all()
		})
	}
}

// TestGraceBeforeExecutionStarts: a stream closed before the runner has
// started its run's execution leaves the run to the reconnect grace, which
// interrupts it as it would a moment later: it does not stay leased.
func TestGraceBeforeExecutionStarts(t *testing.T) {
	t.Parallel()
	clk := clocktest.NewFake(time.Time{})
	runs, opt := gated(clk)
	r, armed := graceRig(t, script(t), rigConfig{clock: clk, api: []agentapi.Option{opt}})
	t.Cleanup(runs.open)
	ctx, cancel := context.WithCancel(t.Context())
	s := r.start(ctx, "hi", "")
	id := s.resp.Header.Get("Agentkit-Run-Id")
	cancel()
	s.resp.Body.Close()
	<-armed
	r.clock.Advance(agentapi.DefaultReconnectGrace)
	if run := r.waitEnded(id); run.Status != agentapi.StatusInterrupted {
		t.Errorf("the run is %s past the grace, want %s", run.Status, agentapi.StatusInterrupted)
	}
}
