package runnerrpc_test

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/rpc"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/runner/runnerrpc"
)

// models is a served agent that runs other models: each its own harness.
type models struct {
	*harness.Harness
	on map[string]*harness.Harness
}

func (m models) ForModel(_ context.Context, model string) (runner.Agent, error) {
	h, ok := m.on[model]
	if !ok {
		return nil, fmt.Errorf("not %q", model)
	}
	return h, nil
}

// A run that names a model runs on what the agent's ForModel returns; one
// that names none on the agent; a model the agent refuses, or an agent with
// one model, fails with ErrNoModel.
func TestRemoteRunOnAModel(t *testing.T) {
	t.Parallel()
	s := rpc.NewServer()
	served := models{
		Harness: newHarness(t, llmtest.Text("default")),
		on:      map[string]*harness.Harness{"anthropic/claude-opus-5-5": newHarness(t, llmtest.Text("opus"))},
	}
	if err := runnerrpc.Register(s, map[string]runner.Agent{"multi": served, "one": newHarness(t)}); err != nil {
		t.Fatal(err)
	}
	c := runnerrpc.NewClient(rpc.Pipe(s))
	in := harness.Input{Message: llm.User(llm.Text{Text: "x"})}
	answer := func(a runner.Agent) (string, error) {
		evs, err := collect(a, in)
		if err != nil {
			return "", err
		}
		r := evs[len(evs)-1].Result
		if r == nil {
			return "", errors.New("no result")
		}
		return r.Final.Text(), nil
	}
	if got, err := answer(c.ModelAgent("multi", "anthropic/claude-opus-5-5", harness.ContextWindow{})); err != nil || got != "opus" {
		t.Errorf("run on opus = %q, %v; want opus's answer", got, err)
	}
	if got, err := answer(c.Agent("multi", harness.ContextWindow{})); err != nil || got != "default" {
		t.Errorf("run on the default = %q, %v; want the default's answer", got, err)
	}
	for _, a := range []runner.Agent{
		c.ModelAgent("multi", "anthropic/claude-fable-5-1", harness.ContextWindow{}),
		c.ModelAgent("one", "anthropic/claude-opus-5-5", harness.ContextWindow{}),
	} {
		if _, err := answer(a); !errors.Is(err, runnerrpc.ErrNoModel) || llm.IsRetryable(err) {
			t.Errorf("err = %v, want a non-retryable ErrNoModel", err)
		}
	}
}

// A runner older than models serves Run, which would run the default: a
// run on another model fails instead, without running.
func TestOldRunnerRefusesARunOnAModel(t *testing.T) {
	t.Parallel()
	type oldReq struct {
		Agent string `json:"agent"`
	}
	ran := false
	s := rpc.NewServer()
	// A runner of the tool choice, before models: a run on a model that
	// also chooses its tools must not go as RunWithTools.
	for _, m := range []string{"/Run", "/RunWithTools"} {
		rpc.Stream[oldReq, struct{}]{Name: runnerrpc.Service + m}.Handle(s, func(context.Context, oldReq) iter.Seq2[struct{}, error] {
			return func(func(struct{}, error) bool) { ran = true }
		})
	}
	a := runnerrpc.NewClient(rpc.Pipe(s)).ModelAgent("default", "anthropic/claude-opus-5-5", harness.ContextWindow{})
	for _, in := range []harness.Input{
		{Message: llm.User(llm.Text{Text: "x"})},
		{Message: llm.User(llm.Text{Text: "x"}), Tools: &harness.ToolChoice{}},
	} {
		if _, err := collect(a, in); !errors.Is(err, rpc.ErrUnknownMethod) || llm.IsRetryable(err) {
			t.Fatalf("err = %v, want a non-retryable rpc.ErrUnknownMethod", err)
		}
	}
	if ran {
		t.Fatal("the old runner ran a run on a model it does not know")
	}
}
