package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/policy"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// inputs records the input of every call of a tool.
type inputs struct {
	mu  sync.Mutex
	got []string
}

func (in *inputs) add(b json.RawMessage) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.got = append(in.got, string(b))
}

func (in *inputs) list() []string {
	in.mu.Lock()
	defer in.mu.Unlock()
	return slices.Clone(in.got)
}

// rawTool is a tool that records its raw input and answers with f.
type rawTool struct {
	def tool.Definition
	in  *inputs
	f   func() (json.RawMessage, error)
}

func (t rawTool) Definition() tool.Definition { return t.def.Clone() }
func (t rawTool) Invoke(_ context.Context, in json.RawMessage) (json.RawMessage, error) {
	t.in.add(in)
	return t.f()
}

func newRaw(name string, m tool.Metadata, in *inputs, f func() (json.RawMessage, error)) rawTool {
	return rawTool{def: tool.Definition{Name: name, InputSchema: json.RawMessage(`{"type":"object"}`), Metadata: m}, in: in, f: f}
}

// interruptTools are ask (asks "which?" with two choices), write (a
// medium-risk write that approve_writes holds) and lookup (a read), each
// recording its inputs.
type interruptTools struct {
	ask, write, lookup *inputs
	reg                tool.Registry
}

func newInterruptTools(t testing.TB) interruptTools {
	it := interruptTools{ask: &inputs{}, write: &inputs{}, lookup: &inputs{}}
	it.reg = newRegistry(t,
		newRaw("ask", tool.Metadata{Kind: tool.KindRead, Risk: tool.RiskLow, Interrupts: true}, it.ask,
			func() (json.RawMessage, error) { return nil, tool.Interrupt("which?", "red", "blue") }),
		newRaw("write", tool.Metadata{Kind: tool.KindWrite, Risk: tool.RiskMedium}, it.write,
			func() (json.RawMessage, error) { return json.RawMessage(`"written"`), nil }),
		newRaw("lookup", tool.Metadata{Kind: tool.KindRead, Risk: tool.RiskLow}, it.lookup,
			func() (json.RawMessage, error) { return json.RawMessage(`"found"`), nil }),
	)
	return it
}

// saved is p after a save and load, as a store keeps it between runs.
func saved(t testing.TB, p *harness.Pending) *harness.Pending {
	t.Helper()
	var back harness.Pending
	if err := json.Unmarshal(mustMarshal(t, p), &back); err != nil {
		t.Fatal(err)
	}
	return &back
}

func toolNames(req llm.Request) []string {
	var out []string
	for _, d := range req.Tools {
		out = append(out, d.Name)
	}
	return out
}

// An interactive run whose model calls ask_user suspends with the question;
// the answer, in a later run given the saved pending turn, is the call's
// result, the other call of the turn is not run again, and the run goes on.
func TestQuestionSuspendsAndTheAnswerResumes(t *testing.T) {
	leakcheck(t)
	it := newInterruptTools(t)
	fake := llmtest.NewFake(t,
		llmtest.ToolCalls(use("q1", "ask", `{"question":"which?"}`), use("l1", "lookup", `{"k":1}`)),
		llmtest.Text("red it is"),
	)
	h := newHarness(t, fake, it.reg, harness.WithInteraction(harness.Interactive))

	events, res, err := run(t, h, prompt("paint it"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Pending == nil {
		t.Fatalf("the run did not suspend: %+v", res)
	}
	diff(t, "tools offered", []string{"ask", "lookup", "write"}, toolNames(fake.Requests()[0]))
	want := []harness.Interrupt{{ID: "q1", Call: 0, Name: "ask", Kind: harness.InterruptQuestion, Prompt: "which?", Choices: []string{"red", "blue"}}}
	diff(t, "interrupts", want, res.Pending.Interrupts)
	diff(t, "the finished call's result", []llm.ToolResult{{ToolUseID: "l1", Content: `"found"`}}, results(t, res.Pending.Done))
	if n := len(res.Transcript); n != 1 {
		t.Errorf("the transcript has %d messages, want the prompt alone (the pending turn is not in it)", n)
	}
	if last := events[len(events)-1]; last.Kind != harness.EventSuspended {
		t.Errorf("the last event is %v", last.Kind)
	}
	again := newHarness(t, llmtest.NewFake(t, llmtest.ToolCalls(use("q1", "ask", `{}`))), it.reg, harness.WithInteraction(harness.Interactive))
	_, err = harness.Collect(again.Run(t.Context(), prompt("paint it")))
	if se, ok := errors.AsType[*harness.SuspendedError](err); !ok || !errors.Is(err, harness.ErrSuspended) || len(se.Pending.Interrupts) != 1 {
		t.Errorf("Collect: %v, want a *SuspendedError", err)
	}
	fake2 := llmtest.NewFake(t, llmtest.Text("red it is"))
	h2 := newHarness(t, fake2, it.reg, harness.WithInteraction(harness.Interactive))

	_, res2, err := run(t, h2, harness.Input{
		Transcript: res.Transcript, Pending: saved(t, res.Pending),
		Answers: []harness.Answer{{ID: "q1", Text: "red"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Final.Text() != "red it is" {
		t.Errorf("final %q", res2.Final.Text())
	}
	got := results(t, fake2.Requests()[0].Messages[2])
	diff(t, "results the model got", []llm.ToolResult{{ToolUseID: "q1", Content: "red"}, {ToolUseID: "l1", Content: `"found"`}}, got)
	diff(t, "lookup calls (not run again)", []string{`{"k":1}`}, it.lookup.list())
	if rec := res2.Record.Turns[0]; rec.Turn != 0 || rec.Usage != res.Pending.Turn.Usage {
		t.Errorf("turn 0 record %+v", rec)
	}
}

// An approval holds the call; approved, it runs once with exactly its
// original input; denied, its result is the denial, and it never runs.
func TestApprovalRunsTheOriginalInputOrDenies(t *testing.T) {
	leakcheck(t)
	const input = "{ \"path\" : \"a.txt\",\n \"text\": \"<&>é\" }"
	for _, c := range []struct {
		name   string
		answer harness.Answer
		want   llm.ToolResult
		runs   []string
	}{
		{"approved", harness.Answer{ID: "w1", Approved: true, By: "ana"}, llm.ToolResult{ToolUseID: "w1", Content: `"written"`}, []string{input}},
		{"denied", harness.Answer{ID: "w1", Reason: "too risky", By: "ana"}, llm.ToolResult{ToolUseID: "w1", Content: "denied by ana: too risky", IsError: true}, nil},
		{"denied without who or why", harness.Answer{ID: "w1"}, llm.ToolResult{ToolUseID: "w1", Content: "denied by a person: no reason given", IsError: true}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			it := newInterruptTools(t)
			fake := llmtest.NewFake(t, llmtest.ToolCalls(use("w1", "write", input)), llmtest.Text("done"))
			opts := []harness.Option{harness.WithInteraction(harness.Supervised), harness.WithToolPolicy(policy.MustPreset(policy.ApproveWrites), nil)}
			h := newHarness(t, fake, it.reg, opts...)
			events, res, err := run(t, h, prompt("write it"))
			if err != nil || res.Pending == nil {
				t.Fatalf("err %v, pending %v", err, res.Pending)
			}
			in := res.Pending.Interrupts[0]
			if in.Kind != harness.InterruptApproval || in.Decision == nil || in.Decision.Action != policy.Approve || in.Prompt != in.Decision.Reason {
				t.Errorf("interrupt %+v", in)
			}
			if len(decisions(events)["write"]) != 1 {
				t.Errorf("policy events %+v", decisions(events))
			}
			if n := len(it.write.list()); n != 0 {
				t.Fatalf("the held call ran %d times", n)
			}
			h2 := newHarness(t, fake, it.reg, opts...)
			_, res2, err := run(t, h2, harness.Input{Transcript: res.Transcript, Pending: saved(t, res.Pending), Answers: []harness.Answer{c.answer}})
			if err != nil {
				t.Fatal(err)
			}
			diff(t, "the call's result", []llm.ToolResult{c.want}, results(t, res2.Transcript[2]))
			diff(t, "inputs the tool ran with", c.runs, it.write.list())
		})
	}
}

// An autonomous run offers no interrupting tool, gives a call to one anyway
// an error result, and refuses a call the policy holds, saying it cannot
// wait; a run's input can narrow the harness's mode, never widen it.
func TestAutonomousNeverWaits(t *testing.T) {
	leakcheck(t)
	for _, c := range []struct {
		name    string
		harness harness.Interaction
		input   harness.Interaction
	}{
		{"default", "", ""},
		{"autonomous", harness.Autonomous, ""},
		{"narrowed by the input", harness.Interactive, harness.Autonomous},
		{"not widened by the input", harness.Autonomous, harness.Interactive},
	} {
		t.Run(c.name, func(t *testing.T) {
			it := newInterruptTools(t)
			fake := llmtest.NewFake(t,
				llmtest.ToolCalls(use("q1", "ask", `{}`), use("w1", "write", `{}`)),
				llmtest.Text("gave up"),
			)
			opts := []harness.Option{harness.WithToolPolicy(policy.MustPreset(policy.ApproveWrites), nil)}
			if c.harness != "" {
				opts = append(opts, harness.WithInteraction(c.harness))
			}
			h := newHarness(t, fake, it.reg, opts...)
			_, res, err := run(t, h, harness.Input{Message: harness.UserText("go"), Interaction: c.input})
			if err != nil || res.Pending != nil {
				t.Fatalf("err %v, pending %v", err, res.Pending)
			}
			diff(t, "tools offered", []string{"lookup", "write"}, toolNames(fake.Requests()[0]))
			got := results(t, fake.Requests()[1].Messages[2])
			if !got[0].IsError || !got[1].IsError {
				t.Errorf("results %+v, want two errors", got)
			}
			if n := len(it.write.list()); n != 0 {
				t.Errorf("the held call ran %d times", n)
			}
		})
	}
	// The refusal says why, distinctly from a deny.
	err := &harness.PolicyError{Decision: policy.Decision{Action: policy.Approve, Reason: "writes need approval"}, NeedsApproval: true, Interaction: harness.Autonomous}
	deny := &harness.PolicyError{Decision: policy.Decision{Action: policy.Deny, Reason: "writes need approval"}}
	if err.Error() == deny.Error() {
		t.Errorf("an approval refused in an autonomous run reads like a deny: %q", err)
	}
}

// An approved call whose tool then asks a person suspends the run again, on
// the same turn; the second answer finishes it.
func TestApprovedCallThatAsksSuspendsAgain(t *testing.T) {
	leakcheck(t)
	asked := &inputs{}
	reg := newRegistry(t, newRaw("ask", tool.Metadata{Interrupts: true}, asked,
		func() (json.RawMessage, error) { return nil, tool.Interrupt("sure?") }))
	fake := llmtest.NewFake(t, llmtest.ToolCalls(use("a1", "ask", `{}`)), llmtest.Text("ok"))
	opts := []harness.Option{harness.WithInteraction(harness.Interactive), harness.WithToolPolicy(policy.MustPreset(policy.ApproveWrites), nil)}
	h := newHarness(t, fake, reg, opts...)
	_, res, err := run(t, h, prompt("go"))
	if err != nil || res.Pending == nil || res.Pending.Interrupts[0].Kind != harness.InterruptApproval {
		t.Fatalf("err %v, pending %+v", err, res.Pending)
	}
	_, res2, err := run(t, h, harness.Input{Transcript: res.Transcript, Pending: saved(t, res.Pending), Answers: []harness.Answer{{ID: "a1", Approved: true}}})
	if err != nil || res2.Pending == nil || res2.Pending.Interrupts[0].Kind != harness.InterruptQuestion {
		t.Fatalf("err %v, pending %+v", err, res2.Pending)
	}
	if res2.Pending.Turn.Turn != 1 || res2.Turns != 0 {
		t.Errorf("the pending turn is %d, the run made %d model calls", res2.Pending.Turn.Turn, res2.Turns)
	}
	_, res3, err := run(t, h, harness.Input{Transcript: res2.Transcript, Pending: saved(t, res2.Pending), Answers: []harness.Answer{{ID: "a1", Text: "yes"}}})
	if err != nil || res3.Final.Text() != "ok" {
		t.Fatalf("err %v, final %+v", err, res3.Final)
	}
	diff(t, "result", []llm.ToolResult{{ToolUseID: "a1", Content: "yes"}}, results(t, res3.Transcript[2]))
}

// Answers that do not fit the pending turn, and pending turns that do not fit
// the transcript, are invalid input: the run calls nothing.
func TestBadAnswersAreInvalidInput(t *testing.T) {
	leakcheck(t)
	it := newInterruptTools(t)
	fake := llmtest.NewFake(t, llmtest.ToolCalls(use("q1", "ask", `{}`), use("w1", "write", `{}`)))
	opts := []harness.Option{harness.WithInteraction(harness.Interactive), harness.WithToolPolicy(policy.MustPreset(policy.ApproveWrites), nil)}
	h := newHarness(t, fake, it.reg, opts...)
	_, res, err := run(t, h, prompt("go"))
	if err != nil || res.Pending == nil || len(res.Pending.Interrupts) != 2 {
		t.Fatalf("err %v, pending %+v", err, res.Pending)
	}
	good := []harness.Answer{{ID: "q1", Text: "red"}, {ID: "w1", Approved: true}}
	tamper := func(f func(p *harness.Pending)) *harness.Pending {
		p := saved(t, res.Pending)
		f(p)
		return p
	}
	for _, c := range []struct {
		name string
		in   harness.Input
		want error
	}{
		{"one answer missing", harness.Input{Transcript: res.Transcript, Pending: res.Pending, Answers: good[:1]}, harness.ErrInvalidAnswer},
		{"unknown interrupt", harness.Input{Transcript: res.Transcript, Pending: res.Pending, Answers: append(slices.Clone(good), harness.Answer{ID: "x", Text: "a"})}, harness.ErrInvalidAnswer},
		{"answered twice", harness.Input{Transcript: res.Transcript, Pending: res.Pending, Answers: append(slices.Clone(good), good[0])}, harness.ErrInvalidAnswer},
		{"not a choice", harness.Input{Transcript: res.Transcript, Pending: res.Pending, Answers: []harness.Answer{{ID: "q1", Text: "green"}, good[1]}}, harness.ErrInvalidAnswer},
		{"empty answer", harness.Input{Transcript: res.Transcript, Pending: res.Pending, Answers: []harness.Answer{{ID: "q1"}, good[1]}}, harness.ErrInvalidAnswer},
		{"text for an approval", harness.Input{Transcript: res.Transcript, Pending: res.Pending, Answers: []harness.Answer{good[0], {ID: "w1", Text: "yes"}}}, harness.ErrInvalidAnswer},
		{"approval for a question", harness.Input{Transcript: res.Transcript, Pending: res.Pending, Answers: []harness.Answer{{ID: "q1", Text: "red", Approved: true}, good[1]}}, harness.ErrInvalidAnswer},
		{"answers without a pending turn", harness.Input{Transcript: res.Transcript, Answers: good}, harness.ErrInvalidInput},
		{"a message with a pending turn", harness.Input{Message: harness.UserText("hi"), Transcript: res.Transcript, Pending: res.Pending, Answers: good}, harness.ErrInvalidInput},
		{"a transcript the reply does not follow", harness.Input{Transcript: nil, Pending: res.Pending, Answers: good}, harness.ErrInvalidInput},
		{"an unknown interaction", harness.Input{Message: harness.UserText("hi"), Interaction: "sometimes"}, harness.ErrInvalidInput},
		{"an interrupt of a finished call", harness.Input{Transcript: res.Transcript, Pending: tamper(func(p *harness.Pending) {
			p.Done = llm.User(llm.ToolResult{ToolUseID: "q1", Content: "x"})
		}), Answers: good}, harness.ErrInvalidInput},
		{"a call neither done nor waiting", harness.Input{Transcript: res.Transcript, Pending: tamper(func(p *harness.Pending) {
			p.Interrupts = p.Interrupts[:1]
		}), Answers: good[:1]}, harness.ErrInvalidInput},
		{"a reply without calls", harness.Input{Transcript: res.Transcript, Pending: tamper(func(p *harness.Pending) {
			p.Turn.Messages[0].Message = llm.Assistant(llm.Text{Text: "hi"})
		}), Answers: good}, harness.ErrInvalidInput},
		{"an interrupt of another call", harness.Input{Transcript: res.Transcript, Pending: tamper(func(p *harness.Pending) {
			p.Interrupts[1].Call = 0
		}), Answers: good}, harness.ErrInvalidInput},
		{"tool records missing", harness.Input{Transcript: res.Transcript, Pending: tamper(func(p *harness.Pending) {
			p.Turn.Tools = nil
		}), Answers: good}, harness.ErrInvalidInput},
	} {
		t.Run(c.name, func(t *testing.T) {
			before := fake.Remaining()
			_, _, err := run(t, h, c.in)
			if !errors.Is(err, c.want) {
				t.Errorf("err %v, want %v", err, c.want)
			}
			if fake.Remaining() != before || len(it.write.list()) != 0 {
				t.Error("an invalid input called the model or a tool")
			}
		})
	}
	if _, err := harness.New(fake, it.reg, harness.WithInteraction("sometimes")); !errors.Is(err, harness.ErrInvalidConfig) {
		t.Errorf("New with an unknown interaction: %v", err)
	}
}

func TestNarrowInteraction(t *testing.T) {
	t.Parallel()
	modes := harness.Interactions()
	for i, a := range modes {
		for j, b := range modes {
			want := modes[max(i, j)]
			if got := harness.NarrowInteraction(a, b); got != want {
				t.Errorf("NarrowInteraction(%s, %s) = %s, want %s", a, b, got, want)
			}
		}
		if harness.NarrowInteraction(a, "") != a || harness.NarrowInteraction("", a) != a {
			t.Errorf("an empty mode changed %s", a)
		}
		if harness.NarrowInteraction(a, "bogus") != harness.Autonomous {
			t.Errorf("an unknown mode did not count as autonomous")
		}
	}
}
