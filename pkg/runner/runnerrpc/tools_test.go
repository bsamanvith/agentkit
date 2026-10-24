package runnerrpc_test

import (
	"context"
	"errors"
	"iter"
	"reflect"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/rpc"
	"github.com/bsamanvith/agentkit/pkg/runner/runnerrpc"
)

// inputAgent records the input of each run and ends it at once.
type inputAgent struct{ got []harness.Input }

func (a *inputAgent) Run(_ context.Context, in harness.Input) iter.Seq2[harness.Event, error] {
	a.got = append(a.got, in)
	return func(func(harness.Event, error) bool) {}
}

// A run's choice of tools reaches the remote runner.
func TestToolChoiceCrossesRPC(t *testing.T) {
	t.Parallel()
	rec := &inputAgent{}
	a := remote(t, rec)
	choice := &harness.ToolChoice{Toolsets: []string{"gmail"}, Search: harness.SearchDeferred}
	if _, err := collect(a, harness.Input{Message: llm.User(llm.Text{Text: "x"}), Tools: choice}); err != nil {
		t.Fatal(err)
	}
	if len(rec.got) != 1 || !reflect.DeepEqual(rec.got[0].Tools, choice) {
		t.Fatalf("remote input tools = %+v, want %+v", rec.got, choice)
	}
}

// A runner older than tool choices fails a run that has one, rather than
// offer the toolsets it left out.
func TestOldRunnerRefusesARunWithAToolChoice(t *testing.T) {
	t.Parallel()
	type oldReq struct {
		Agent string `json:"agent"`
	}
	ran := false
	s := rpc.NewServer()
	h := func(context.Context, oldReq) iter.Seq2[struct{}, error] {
		return func(func(struct{}, error) bool) { ran = true }
	}
	for _, m := range []string{"/Run", "/RunWithPolicy", "/RunWithInterrupts"} {
		rpc.Stream[oldReq, struct{}]{Name: runnerrpc.Service + m}.Handle(s, h)
	}
	a := runnerrpc.NewClient(rpc.Pipe(s)).Agent("default", harness.ContextWindow{})
	in := harness.Input{Message: llm.User(llm.Text{Text: "x"}), Tools: &harness.ToolChoice{Toolsets: []string{}}}
	if _, err := collect(a, in); !errors.Is(err, rpc.ErrUnknownMethod) || llm.IsRetryable(err) {
		t.Fatalf("err = %v, want a non-retryable rpc.ErrUnknownMethod", err)
	}
	if ran {
		t.Fatal("the old runner ran a run whose tool choice it cannot honour")
	}
}
