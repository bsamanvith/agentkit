package runnerrpc_test

import (
	"context"
	"errors"
	"iter"
	"reflect"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/policy"
	"github.com/bsamanvith/agentkit/pkg/rpc"
	"github.com/bsamanvith/agentkit/pkg/runner/runnerrpc"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/tool/tooltest"
)

// holding returns an interactive agent on the fixture tools whose echo calls
// wait for an approval.
func holding(t *testing.T, replies ...llmtest.Reply) *harness.Harness {
	t.Helper()
	tools, err := tool.NewRegistry(tooltest.Fixtures())
	if err != nil {
		t.Fatal(err)
	}
	h, err := harness.New(llmtest.NewFake(t, replies...), tools, harness.WithClock(clocktest.NewFake(now)),
		harness.WithInteraction(harness.Interactive),
		harness.WithToolPolicy(policy.Policy{Rules: []policy.Rule{
			{Name: "hold-echo", Match: policy.Match{Tools: []string{"echo"}}, Action: policy.Approve, Reason: "echo needs a yes"},
		}, Default: policy.Allow}, nil))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func sameEvents(t *testing.T, got, want []harness.Event) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d", len(got), len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("event %d (%s):\n got %#v\nwant %#v", i, want[i].Kind, got[i], want[i])
			if got[i].Result != nil && want[i].Result != nil {
				t.Logf("results:\n got %#v\nwant %#v", *got[i].Result, *want[i].Result)
			}
		}
	}
}

// A remote run that waits for people suspends as the local one does: the
// interrupt events and the pending turn cross the wire; the resume with the
// answers finishes the turn the same way.
func TestRemoteRunSuspendsAndResumesLikeLocal(t *testing.T) {
	t.Parallel()
	in := harness.Input{Message: llm.User(llm.Text{Text: "echo hi twice"}), At: now, Interaction: harness.Interactive}
	first := []llmtest.Reply{llmtest.ToolCalls(echoCall("t1")).WithUsage(llm.Usage{InputTokens: 10, OutputTokens: 5})}
	want, err := collect(holding(t, first...), in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := collect(remote(t, holding(t, first...)), in)
	if err != nil {
		t.Fatal(err)
	}
	sameEvents(t, got, want)
	last := got[len(got)-1]
	if last.Kind != harness.EventSuspended || last.Result == nil || last.Result.Pending == nil {
		t.Fatalf("last event %+v, want the run suspended with its pending turn", last)
	}
	sawInterrupt := false
	for _, ev := range got {
		if ev.Kind == harness.EventInterrupt && ev.Interrupt != nil && ev.Interrupt.ID == "t1" && ev.Interrupt.Kind == harness.InterruptApproval {
			sawInterrupt = true
		}
	}
	if !sawInterrupt {
		t.Fatal("no interrupt event for t1")
	}

	// The resume: the transcript so far, the pending turn and its answer.
	resume := harness.Input{
		Transcript:  last.Result.Transcript,
		Interaction: harness.Interactive,
		Pending:     last.Result.Pending,
		Answers:     []harness.Answer{{ID: "t1", Approved: true, By: "ann"}},
	}
	second := []llmtest.Reply{llmtest.Text("done: hihi")}
	want, err = collect(holding(t, second...), resume)
	if err != nil {
		t.Fatal(err)
	}
	got, err = collect(remote(t, holding(t, second...)), resume)
	if err != nil {
		t.Fatal(err)
	}
	sameEvents(t, got, want)
	if end := got[len(got)-1]; end.Kind != harness.EventRunDone || end.Result.Final.Text() != "done: hihi" {
		t.Fatalf("resume ended with %+v", end)
	}

	// A bad answer is the same error as locally.
	resume.Answers = []harness.Answer{{ID: "t1", Text: "yes"}}
	if _, err := collect(remote(t, holding(t)), resume); !errors.Is(err, harness.ErrInvalidInput) {
		t.Fatalf("bad answer: %v, want ErrInvalidInput", err)
	}
}

// A runner older than interrupts must not run a run that may wait for
// people, nor finish a pending turn without its answers: it fails, not
// retried. An autonomous run still goes to it.
func TestOldRunnerRefusesARunThatMayWait(t *testing.T) {
	t.Parallel()
	type oldReq struct {
		Agent string `json:"agent"`
	}
	ran := 0
	s := rpc.NewServer()
	handle := func(context.Context, oldReq) iter.Seq2[struct{}, error] {
		return func(func(struct{}, error) bool) { ran++ }
	}
	rpc.Stream[oldReq, struct{}]{Name: runnerrpc.Service + "/Run"}.Handle(s, handle)
	rpc.Stream[oldReq, struct{}]{Name: runnerrpc.Service + "/RunWithPolicy"}.Handle(s, handle)
	a := runnerrpc.NewClient(rpc.Pipe(s)).Agent("default", harness.ContextWindow{})
	msg := llm.User(llm.Text{Text: "x"})
	for _, in := range []harness.Input{
		{Message: msg, Interaction: harness.Interactive},
		{Message: msg, Interaction: harness.Supervised, ToolPolicy: &policy.Policy{Default: policy.Allow}},
		{Transcript: harness.Transcript{msg}, Pending: &harness.Pending{Done: llm.User()}, Answers: []harness.Answer{{ID: "t1", Text: "yes"}}},
	} {
		_, err := collect(a, in)
		if !errors.Is(err, rpc.ErrUnknownMethod) || llm.IsRetryable(err) {
			t.Fatalf("input %+v: err = %v, want a non-retryable rpc.ErrUnknownMethod", in, err)
		}
	}
	if ran != 0 {
		t.Fatal("the old runner ran a run it cannot suspend")
	}
	if _, err := collect(a, harness.Input{Message: msg, Interaction: harness.Autonomous}); err != nil || ran != 1 {
		t.Fatalf("autonomous run: err = %v, ran = %d", err, ran)
	}
}
