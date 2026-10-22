package harness_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
)

// TestPrepareAddsBlocksToTheUserTurn: the hooks' blocks go before the new
// message's own, in order, into the request, the transcript and the record;
// the system prompt is the same; each hook sees the message as given.
func TestPrepareAddsBlocksToTheUserTurn(t *testing.T) {
	leakcheck(t)
	var seen []llm.Message
	hook := func(text string) harness.PrepareFunc {
		return func(_ context.Context, m llm.Message) ([]llm.Block, error) {
			seen = append(seen, m.Clone())
			m.Content[0] = llm.Text{Text: "scribbled"} // a copy: the run must not see it
			return []llm.Block{llm.Text{Text: text}}, nil
		}
	}
	fake := llmtest.NewFake(t, llmtest.Text("ok"))
	h, err := harness.New(fake, newRegistry(t), harness.WithSystem("sys"),
		harness.WithPrepare(hook("A")), harness.WithPrepare(hook("B")))
	if err != nil {
		t.Fatal(err)
	}
	var start *harness.TimedMessage
	var res harness.Result
	for ev, err := range h.Run(t.Context(), prompt("q")) {
		if err != nil {
			t.Fatal(err)
		}
		switch ev.Kind {
		case harness.EventRunStart:
			start = ev.Prompt
		case harness.EventRunDone:
			res = *ev.Result
		}
	}
	want := llm.User(llm.Text{Text: "A"}, llm.Text{Text: "B"}, llm.Text{Text: "q"})
	reqs := fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests", len(reqs))
	}
	diff(t, "the request's user turn", want, reqs[0].Messages[0])
	if reqs[0].System != "sys" {
		t.Errorf("system = %q", reqs[0].System)
	}
	diff(t, "the transcript's user turn", want, res.Transcript[0])
	diff(t, "the record's prompt", want, res.Record.Prompt.Message)
	if start == nil {
		t.Fatal("no prompt on EventRunStart")
	}
	diff(t, "EventRunStart's prompt", want, start.Message)
	diff(t, "what the hooks saw", []llm.Message{harness.UserText("q"), harness.UserText("q")}, seen)

	// A follow-up adds to the new message only; the earlier turns stay.
	fake2 := llmtest.NewFake(t, llmtest.Text("ok again"))
	h2, err := harness.New(fake2, newRegistry(t), harness.WithPrepare(hook("C")))
	if err != nil {
		t.Fatal(err)
	}
	res2, err := harness.Collect(h2.Run(t.Context(), harness.Input{Transcript: res.Transcript, Message: harness.UserText("q2")}))
	if err != nil {
		t.Fatal(err)
	}
	diff(t, "the follow-up's earlier turns", []llm.Message(res.Transcript), []llm.Message(res2.Transcript[:2]))
	diff(t, "the follow-up's user turn", llm.User(llm.Text{Text: "C"}, llm.Text{Text: "q2"}), res2.Transcript[2])
}

// TestPrepareRunsOnlyWithANewMessage: a resumed run has no new message, and
// a hook with nothing to add leaves the message as it is.
func TestPrepareRunsOnlyWithANewMessage(t *testing.T) {
	leakcheck(t)
	calls := 0
	hook := harness.WithPrepare(func(context.Context, llm.Message) ([]llm.Block, error) { calls++; return nil, nil })
	fake := llmtest.NewFake(t, llmtest.Text("one"), llmtest.Text("two"))
	h, err := harness.New(fake, newRegistry(t), hook)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.Collect(h.Run(t.Context(), harness.Input{Transcript: harness.Transcript{harness.UserText("left over")}})); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Errorf("a resumed run called the hook %d times", calls)
	}
	res, err := harness.Collect(h.Run(t.Context(), prompt("q")))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("hook called %d times, want 1", calls)
	}
	diff(t, "the message", harness.UserText("q"), res.Transcript[0])
}

// TestPrepareErrorFailsTheRun: before any model call, with a *RunError.
func TestPrepareErrorFailsTheRun(t *testing.T) {
	leakcheck(t)
	boom := errors.New("index down")
	fake := llmtest.NewFake(t)
	h, err := harness.New(fake, newRegistry(t), harness.WithPrepare(func(context.Context, llm.Message) ([]llm.Block, error) {
		return nil, boom
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = harness.Collect(h.Run(t.Context(), prompt("q")))
	var re *harness.RunError
	if !errors.Is(err, boom) || !errors.As(err, &re) {
		t.Errorf("err = %v, want a *RunError wrapping the hook's", err)
	}
	if n := len(fake.Requests()); n != 0 {
		t.Errorf("%d model calls after a failed hook", n)
	}
	if _, err := harness.New(fake, newRegistry(t), harness.WithPrepare(nil)); !errors.Is(err, harness.ErrInvalidConfig) {
		t.Errorf("a nil hook: %v, want ErrInvalidConfig", err)
	}
}

// TestPrepareSkipsAnsweredPendingTurn: a run that answers a pending turn has
// no new message, so the hook is not called again, and the message the
// first run prepared reaches the model unchanged.
func TestPrepareSkipsAnsweredPendingTurn(t *testing.T) {
	leakcheck(t)
	calls := 0
	hook := harness.WithPrepare(func(context.Context, llm.Message) ([]llm.Block, error) {
		calls++
		return []llm.Block{llm.Text{Text: "recalled"}}, nil
	})
	// last is the result of the run's last event (done or suspended).
	last := func(h *harness.Harness, in harness.Input) harness.Result {
		t.Helper()
		var res *harness.Result
		for ev, err := range h.Run(t.Context(), in) {
			if err != nil {
				t.Fatal(err)
			}
			if ev.Result != nil {
				res = ev.Result
			}
		}
		if res == nil {
			t.Fatal("no result")
		}
		return *res
	}
	it := newInterruptTools(t)
	fake := llmtest.NewFake(t, llmtest.ToolCalls(use("q1", "ask", `{"question":"which?"}`)))
	h, err := harness.New(fake, it.reg, harness.WithInteraction(harness.Interactive), hook)
	if err != nil {
		t.Fatal(err)
	}
	res := last(h, prompt("paint it"))
	if res.Pending == nil {
		t.Fatalf("the run did not suspend: %+v", res)
	}
	want := llm.User(llm.Text{Text: "recalled"}, llm.Text{Text: "paint it"})
	diff(t, "the suspended run's user turn", want, res.Transcript[0])

	fake2 := llmtest.NewFake(t, llmtest.Text("red it is"))
	h2, err := harness.New(fake2, it.reg, harness.WithInteraction(harness.Interactive), hook)
	if err != nil {
		t.Fatal(err)
	}
	res2 := last(h2, harness.Input{
		Transcript: res.Transcript, Pending: saved(t, res.Pending),
		Answers: []harness.Answer{{ID: "q1", Text: "red"}},
	})
	if res2.Final.Text() != "red it is" {
		t.Errorf("final %q", res2.Final.Text())
	}
	if calls != 1 {
		t.Errorf("hook called %d times across the run and its answer, want 1", calls)
	}
	diff(t, "the answered run's user turn", want, fake2.Requests()[0].Messages[0])
}
