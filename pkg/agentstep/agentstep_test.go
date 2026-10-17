package agentstep_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/bsamanvith/agentkit/pkg/agentstep"
	"github.com/bsamanvith/agentkit/pkg/agentstep/agentsteptest"
	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
)

var research = agentstep.Keys{Input: "topic", Output: "research"}

func TestMemorySaver(t *testing.T) {
	agentsteptest.RunSaver(t, func(*testing.T) agentstep.TranscriptSaver { return agentstep.NewMemorySaver() })
}

func TestStepWritesAnswerAndTranscript(t *testing.T) {
	t.Parallel()
	r := newRig(t, "research", "topic")
	model := llmtest.NewFake(t, threeTurns()...)
	st, err := r.run(t.Context(), "run", flow.Sequence("flow", r.agent("agent", model, research)))
	if err != nil {
		t.Fatal(err)
	}
	out, err := flow.Get[agentstep.Output](st, "research")
	if err != nil {
		t.Fatal(err)
	}
	if out.Answer != "Go is simple." {
		t.Errorf("answer = %q", out.Answer)
	}
	want := `user:go|assistant:call c1|user:result "notes on syntax"|assistant:call c2|user:result "notes on runtime"|assistant:Go is simple.|`
	if got := texts(out.Transcript); got != want {
		t.Errorf("transcript = %s\nwant         %s", got, want)
	}
	if n := model.Remaining(); n != 0 {
		t.Errorf("%d replies unused", n)
	}
}

func TestResumeDoesNotRerunFinishedAgentStep(t *testing.T) {
	t.Parallel()
	r := newRig(t, "research", "topic")
	tree := func(model llm.Model) flow.Step {
		return flow.Sequence("flow", r.agent("agent", model, research), flow.Approve("approve"))
	}
	first := llmtest.NewFake(t, threeTurns()...)
	if _, err := r.run(t.Context(), "run", tree(first)); !errors.Is(err, flow.ErrSuspended) {
		t.Fatalf("Run = %v, want ErrSuspended", err)
	}
	// A model with no replies fails the test if it is called.
	second := llmtest.NewFake(t)
	st, err := r.resume(t.Context(), "run", tree(second), flow.WithDecision("flow/approve", flow.Decision{Approved: true}))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(second.Requests()); n != 0 {
		t.Errorf("resume made %d model calls, want 0", n)
	}
	if out, err := flow.Get[agentstep.Output](st, "research"); err != nil || out.Answer != "Go is simple." {
		t.Errorf("research = %+v, %v", out, err)
	}
}

func TestInterruptedStepResumesWithOneMoreCall(t *testing.T) {
	t.Parallel()

	// The uninterrupted run, for the requests it makes.
	whole := llmtest.NewFake(t, threeTurns()...)
	ur := newRig(t, "research", "topic")
	if _, err := ur.run(t.Context(), "run", flow.Sequence("flow", ur.agent("agent", whole, research))); err != nil {
		t.Fatal(err)
	}

	// The same run, stopped right after turn 2 was saved (appends: the prompt,
	// turn 1, turn 2), as if the process had been killed.
	r := newRig(t, "research", "topic")
	ctx, kill := context.WithCancel(t.Context())
	r.saver.after, r.saver.afterN = kill, 3
	before := llmtest.NewFake(t, threeTurns()[:2]...)
	if _, err := r.run(ctx, "run", flow.Sequence("flow", r.agent("agent", before, research))); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted Run = %v, want context.Canceled", err)
	}
	after := llmtest.NewFake(t, threeTurns()[2])
	st, err := r.resume(t.Context(), "run", flow.Sequence("flow", r.agent("agent", after, research)))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(after.Requests()); n != 1 {
		t.Errorf("resume made %d model calls, want 1", n)
	}
	got := append(before.Requests(), after.Requests()...)
	if diff := cmp.Diff(whole.Requests(), got); diff != "" {
		t.Errorf("requests differ from an uninterrupted run (-want +got):\n%s", diff)
	}
	if out, err := flow.Get[agentstep.Output](st, "research"); err != nil || out.Answer != "Go is simple." {
		t.Errorf("research = %+v, %v", out, err)
	}
}

func TestParallelAgentsNeedDifferentKeys(t *testing.T) {
	t.Parallel()
	two := func(r *rig, a, b agentstep.Keys) flow.Step {
		return flow.Sequence("flow", flow.Parallel("par",
			r.agent("agent", llmtest.NewFake(t, llmtest.Text("from a")), a),
			r.agent("agent", llmtest.NewFake(t, llmtest.Text("from b")), b),
		))
	}

	t.Run("different keys merge", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, "a", "b", "topic")
		st, err := r.run(t.Context(), "run", two(r, agentstep.Keys{Input: "topic", Output: "a"}, agentstep.Keys{Input: "topic", Output: "b"}))
		if err != nil {
			t.Fatal(err)
		}
		for k, want := range map[string]string{"a": "from a", "b": "from b"} {
			if out, err := flow.Get[agentstep.Output](st, k); err != nil || out.Answer != want {
				t.Errorf("%s = %+v, %v; want answer %q", k, out, err, want)
			}
		}
	})

	t.Run("same key conflicts", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		_, err := r.run(t.Context(), "run", two(r, research, research))
		var ce *flow.ConflictError
		if !errors.Is(err, flow.ErrConflict) || !errors.As(err, &ce) || len(ce.Keys) != 1 || ce.Keys[0] != "research" {
			t.Errorf("Run = %v, want a conflict on research", err)
		}
	})
}

func TestRerunAfterAnswerMakesNoModelCall(t *testing.T) {
	t.Parallel()
	r := newRig(t, "research", "topic")
	// The agent answers, but its checkpoint is lost: flow runs it again.
	r.cp.failPath = "flow/agent"
	first := llmtest.NewFake(t, threeTurns()...)
	if _, err := r.run(t.Context(), "run", flow.Sequence("flow", r.agent("agent", first, research))); !errors.Is(err, errCheckpoint) {
		t.Fatalf("Run = %v, want the checkpoint failure", err)
	}
	second := llmtest.NewFake(t)
	st, err := r.resume(t.Context(), "run", flow.Sequence("flow", r.agent("agent", second, research)))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(second.Requests()); n != 0 {
		t.Errorf("re-run made %d model calls, want 0", n)
	}
	if out, err := flow.Get[agentstep.Output](st, "research"); err != nil || out.Answer != "Go is simple." || len(out.Transcript) != 6 {
		t.Errorf("research = %+v, %v", out, err)
	}
}

func TestFailedFinalReplyIsAskedAgain(t *testing.T) {
	t.Parallel()
	r := newRig(t, "research", "topic")
	cut := llmtest.Text("Go is sim")
	cut.Response.Stop = llm.StopReason{Kind: llm.StopMaxTokens}
	first := llmtest.NewFake(t, threeTurns()[0], cut)
	if _, err := r.run(t.Context(), "run", flow.Sequence("flow", r.agent("agent", first, research))); !errors.Is(err, harness.ErrMaxTokens) {
		t.Fatalf("Run = %v, want ErrMaxTokens", err)
	}
	second := llmtest.NewFake(t, llmtest.Text("Go is simple."))
	st, err := r.resume(t.Context(), "run", flow.Sequence("flow", r.agent("agent", second, research)))
	if err != nil {
		t.Fatal(err)
	}
	// The question the cut-off reply answered is asked again, unchanged.
	if diff := cmp.Diff(first.Requests()[1], second.Requests()[0]); diff != "" {
		t.Errorf("retried request differs (-failed +retried):\n%s", diff)
	}
	if out, err := flow.Get[agentstep.Output](st, "research"); err != nil || out.Answer != "Go is simple." || len(out.Transcript) != 4 {
		t.Errorf("research = %+v, %v", out, err)
	}
}

func TestSaveFailureStopsTheRun(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.saver.failAt = 2 // turn 1
	model := llmtest.NewFake(t, threeTurns()...)
	_, err := r.run(t.Context(), "run", flow.Sequence("flow", r.agent("agent", model, research)))
	if !errors.Is(err, errSave) {
		t.Fatalf("Run = %v, want the save error", err)
	}
	if n := len(model.Requests()); n != 1 {
		t.Errorf("%d model calls after the save failed, want 1", n)
	}
}

func TestStepNeedsAFlowRunAndItsInput(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	step := r.agent("agent", llmtest.NewFake(t), research)
	if err := step.Run(t.Context(), flow.NewState()); !errors.Is(err, agentstep.ErrNoRun) {
		t.Errorf("Run outside flow = %v, want ErrNoRun", err)
	}
	_, err := flow.Run(t.Context(), "run", flow.Sequence("flow", step), flow.WithCheckpointer(flow.NewMemory()))
	if !errors.Is(err, flow.ErrNotFound) {
		t.Errorf("Run without the input key = %v, want ErrNotFound", err)
	}
}

func TestNewRejectsBadConfig(t *testing.T) {
	t.Parallel()
	h, err := harness.New(llmtest.NewFake(t), newRig(t).tools)
	if err != nil {
		t.Fatal(err)
	}
	saver := agentstep.NewMemorySaver()
	for name, try := range map[string]func() (*agentstep.Step, error){
		"no name":    func() (*agentstep.Step, error) { return agentstep.New("", h, saver, research) },
		"no harness": func() (*agentstep.Step, error) { return agentstep.New("a", nil, saver, research) },
		"nil harness": func() (*agentstep.Step, error) {
			return agentstep.New("a", (*harness.Harness)(nil), saver, research)
		},
		"nil agent": func() (*agentstep.Step, error) { return agentstep.New("a", (*otherAgent)(nil), saver, research) },
		"no saver":  func() (*agentstep.Step, error) { return agentstep.New("a", h, nil, research) },
		"no input":  func() (*agentstep.Step, error) { return agentstep.New("a", h, saver, agentstep.Keys{Output: "o"}) },
		"no output": func() (*agentstep.Step, error) { return agentstep.New("a", h, saver, agentstep.Keys{Input: "i"}) },
		"same key": func() (*agentstep.Step, error) {
			return agentstep.New("a", h, saver, agentstep.Keys{Input: "k", Output: "k"})
		},
	} {
		if _, err := try(); !errors.Is(err, agentstep.ErrInvalidConfig) {
			t.Errorf("%s: New = %v, want ErrInvalidConfig", name, err)
		}
	}
}

// otherAgent is an agent that is not a harness, as a remote runner is.
type otherAgent struct{ *harness.Harness }
