package agentclient_test

import (
	"errors"
	"io"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/policy"
)

// A run whose call waits for an approval streams the interrupt and ends
// suspended; Answer approves it, the run goes on, and Events follows it to
// its answer. A different answer then is a run-state error.
func TestAnswer(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	m := &model{replies: []llmtest.Reply{
		llmtest.ToolCalls(call("toolu_1", "echo", `{"text":"x"}`)), llmtest.Text("Echoed."),
	}}
	r := newRigHarness(t, m, []harness.Option{
		harness.WithInteraction(harness.Interactive),
		harness.WithToolPolicy(policy.Policy{Rules: []policy.Rule{
			{Name: "hold", Match: policy.Match{Tools: []string{"echo"}}, Action: policy.Approve, Reason: "echo needs a yes"},
		}, Default: policy.Allow}, nil),
	})
	s, err := r.client.Stream(ctx, agentclient.RunRequest{Message: "echo x", Interaction: "interactive"})
	if err != nil {
		t.Fatal(err)
	}
	in := until(t, s, wire.TypeInterrupt)
	if in.Interrupt == nil || in.Interrupt.ID != "toolu_1" || in.Interrupt.Kind != wire.InterruptApproval || in.Interrupt.Prompt != "echo needs a yes" || string(in.Interrupt.Input) != `{"text":"x"}` {
		t.Fatalf("interrupt event %s", jsonOf(in))
	}
	end := until(t, s, wire.TypeRunError)
	if end.Error.Code != wire.CodeSuspended {
		t.Fatalf("the run ended %s", jsonOf(end))
	}
	run, err := r.client.GetRun(ctx, s.RunID())
	if err != nil || run.Status != wire.StatusSuspended || len(run.Interrupts) != 1 || run.Interrupts[0].Answer != nil {
		t.Fatalf("GetRun = %s, %v", jsonOf(run), err)
	}

	run, err = r.client.Answer(ctx, s.RunID(), wire.Answer{ID: "toolu_1", Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status == wire.StatusSuspended {
		t.Fatalf("the answered run is %s", jsonOf(run))
	}
	rest, err := r.client.Events(ctx, s.RunID(), end.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := readAll(rest)
	if !errors.Is(got.err, io.EOF) || len(got.events) == 0 || got.events[len(got.events)-1].Answer != "Echoed." {
		t.Fatalf("events after the answer = %s, %v", jsonOf(got.events), got.err)
	}
	if _, err := r.client.Answer(ctx, s.RunID(), wire.Answer{ID: "toolu_1", Approved: false}); !errors.Is(err, agentclient.ErrRunState) {
		t.Fatalf("an answer to a done run: %v, want ErrRunState", err)
	}
}
