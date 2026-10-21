package runner_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/policy"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// suspendRun starts prompt on conv and checks that it suspends, waiting for
// the interrupts want.
func suspendRun(t *testing.T, p *proc, conv, prompt string, want ...string) store.Run {
	t.Helper()
	evs, err := p.run(conv, prompt)
	if err != nil {
		t.Fatal(err)
	}
	run := lastDone(t, evs).Run
	slices.Sort(want)
	if run.Status != store.StatusSuspended || !slices.Equal(run.Pending, want) {
		t.Fatalf("run %+v, want suspended on %q", run, want)
	}
	if got := runOf(t, p, run.ID); got.Status != store.StatusSuspended {
		t.Fatalf("stored run %+v, want suspended", got)
	}
	return run
}

// policyApproving holds every call of the tool for an approval.
func policyApproving(name string) policy.Policy {
	return policy.Policy{Rules: []policy.Rule{
		{Name: "hold", Match: policy.Match{Tools: []string{name}}, Action: policy.Approve, Reason: "hold " + name},
	}, Default: policy.Allow}
}

func runOf(t *testing.T, p *proc, id string) store.Run {
	t.Helper()
	run, err := p.w.mem.Get(p.w.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

// answer answers and drains the execution.
func (p *proc) answer(runID string, answers ...harness.Answer) ([]runner.Event, error) {
	p.w.t.Helper()
	x, err := p.r.Answer(p.w.ctx, runID, answers...)
	if err != nil {
		return nil, err
	}
	return p.drain(x, nil)
}

// A question suspends the run; its answer, given to another process after
// the first one is gone, resumes it, and the answer is the call's result.
func TestQuestionSuspendsAcrossARestart(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.interactions()
	prompt := w.model.onCalls("q", []string{"echo", "ask"})
	id := prompt + "#0.1"
	first := w.process()
	run := suspendRun(t, first, "c", prompt, id)

	// Nothing of the pending turn is in the context yet.
	if tr := w.transcript("c"); len(tr) != 1 {
		t.Fatalf("context while suspended: %s", js(tr))
	}
	ins, answers, err := first.r.Interrupts(w.ctx, run.ID)
	if err != nil || len(ins) != 1 || len(answers) != 0 {
		t.Fatalf("Interrupts = %+v, %+v, %v", ins, answers, err)
	}
	if in := ins[0]; in.ID != id || in.Kind != harness.InterruptQuestion || in.Prompt != "yes or no?" || !slices.Equal(in.Choices, []string{"yes", "no"}) {
		t.Fatalf("interrupt %+v", in)
	}

	// A new process, after a restart: it reads the pending turn from the log.
	second := w.process()
	evs, err := second.answer(run.ID, harness.Answer{ID: id, Text: "yes"})
	if err != nil {
		t.Fatal(err)
	}
	done := lastDone(t, evs)
	if done.Run.Status != store.StatusCompleted {
		t.Fatalf("run %+v, want completed", done.Run)
	}
	res := toolResults(w, prompt)
	if len(res) != 2 || res[0].Content != `"echo `+prompt+`#0.0"` || res[1].ToolUseID != id || res[1].Content != "yes" || res[1].IsError {
		t.Fatalf("results sent to the model: %s", js(res))
	}
	if n := len(w.model.requestsFor(prompt)); n != 2 {
		t.Fatalf("%d model calls, want 2: the pending turn's is not made again", n)
	}
	if err := w.transcript("c").Validate(); err != nil {
		t.Fatal(err)
	}

	// A second answer comes too late.
	if _, err := second.r.Answer(w.ctx, run.ID, harness.Answer{ID: id, Text: "yes"}); !errors.Is(err, store.ErrFinal) {
		t.Fatalf("answer after completion: %v, want ErrFinal", err)
	}
	if _, _, err := second.r.Interrupts(w.ctx, run.ID); !errors.Is(err, store.ErrFinal) {
		t.Fatalf("Interrupts after completion: %v", err)
	}
}

// An approved call runs once, with exactly its original input; a denied one
// never runs, and its result is the denial.
func TestApprovalRunsTheOriginalInputOrDenies(t *testing.T) {
	t.Parallel()
	for _, approved := range []bool{true, false} {
		t.Run(map[bool]string{true: "approved", false: "denied"}[approved], func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			w.interactions()
			prompt := w.model.onCalls("w", []string{"write"})
			id := prompt + "#0.0"
			p := w.process()
			run := suspendRun(t, p, "c", prompt, id)
			ins, _, err := p.r.Interrupts(w.ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			in := ins[0]
			if in.Kind != harness.InterruptApproval || in.Decision == nil || in.Decision.Reason != "writes need a yes" || in.Input != `{"s":"`+id+`"}` {
				t.Fatalf("interrupt %+v", in)
			}
			if w.writes(id) != 0 {
				t.Fatal("the call ran before its approval")
			}
			a := harness.Answer{ID: id, Approved: approved, By: "ann"}
			if !approved {
				a.Reason = "not today"
			}
			evs, err := p.answer(run.ID, a)
			if err != nil {
				t.Fatal(err)
			}
			if run := lastDone(t, evs).Run; run.Status != store.StatusCompleted {
				t.Fatalf("run %+v", run)
			}
			res := toolResults(w, prompt)
			if approved {
				if w.writes(id) != 1 || len(res) != 1 || res[0].Content != `"wrote `+id+`"` || res[0].IsError {
					t.Fatalf("wrote %d times, results %s", w.writes(id), js(res))
				}
			} else if w.writes(id) != 0 || len(res) != 1 || !res[0].IsError || res[0].Content != "denied by ann: not today" {
				t.Fatalf("wrote %d times, results %s", w.writes(id), js(res))
			}
			// A second, different, answer is refused; the run is final.
			if _, err := p.r.Answer(w.ctx, run.ID, harness.Answer{ID: id, Approved: !approved}); !errors.Is(err, store.ErrFinal) {
				t.Fatalf("second answer: %v", err)
			}
		})
	}
}

// With two interrupts, the run resumes only once both have answers; the same
// answer twice is a no-op, a different one is ErrAnswered.
func TestAnswersComeOneByOne(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.interactions()
	prompt := w.model.onCalls("two", []string{"ask", "write"}, []string{"echo"})
	q, wr := prompt+"#0.0", prompt+"#0.1"
	p := w.process()
	run := suspendRun(t, p, "c", prompt, q, wr)

	evs, err := p.answer(run.ID, harness.Answer{ID: q, Text: "no"})
	if err != nil {
		t.Fatal(err)
	}
	if got := lastDone(t, evs).Run; got.Status != store.StatusSuspended || len(evs) != 1 {
		t.Fatalf("after one answer: %s", js(evs))
	}
	if _, err := p.answer(run.ID, harness.Answer{ID: q, Text: "no"}); err != nil {
		t.Fatalf("the same answer again: %v", err)
	}
	if _, err := p.r.Answer(w.ctx, run.ID, harness.Answer{ID: q, Text: "yes"}); !errors.Is(err, runner.ErrAnswered) || !errors.Is(err, runner.ErrNotPending) {
		t.Fatalf("a different answer: %v, want ErrAnswered", err)
	}
	if _, err := p.r.Answer(w.ctx, run.ID, harness.Answer{ID: "nope", Text: "yes"}); !errors.Is(err, runner.ErrNotPending) || errors.Is(err, runner.ErrAnswered) {
		t.Fatalf("an unknown interrupt: %v, want ErrNotPending", err)
	}
	for _, bad := range []harness.Answer{
		{ID: wr, Text: "yes"},    // text for an approval
		{ID: wr, Approved: true}, // fine alone, but twice in one request
	} {
		answers := []harness.Answer{bad}
		if bad.Text == "" {
			answers = append(answers, bad)
		}
		if _, err := p.r.Answer(w.ctx, run.ID, answers...); !errors.Is(err, runner.ErrInvalidInput) || !errors.Is(err, harness.ErrInvalidAnswer) {
			t.Fatalf("answers %+v: %v, want an invalid answer", answers, err)
		}
	}
	if _, answers, _ := p.r.Interrupts(w.ctx, run.ID); len(answers) != 1 {
		t.Fatalf("answers stored: %+v", answers)
	}
	if n := len(w.log("c")); n != 3 {
		t.Fatalf("log of %d entries, want prompt and two interrupt entries: the repeat stores nothing", n)
	}

	evs, err = p.answer(run.ID, harness.Answer{ID: wr, Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := lastDone(t, evs).Run; got.Status != store.StatusCompleted {
		t.Fatalf("run %+v", got)
	}
	if w.writes(wr) != 1 {
		t.Fatalf("wrote %d times", w.writes(wr))
	}
	// The pending turn's events are turn 0: they finish the turn before.
	var turns []int
	for _, ev := range evs {
		if ev.Kind == runner.EventHarness && ev.Harness.Kind == harness.EventTurnDone {
			turns = append(turns, ev.Harness.Turn)
		}
	}
	if !slices.Equal(turns, []int{0, 1, 2}) {
		t.Fatalf("turns done %v, want the pending turn as 0, then 1 and 2", turns)
	}
}

// An approved call that asks suspends the run again, at the same turn.
func TestApprovedCallThatAsksSuspendsTheRunAgain(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.interactions()
	w.hopts[1] = harness.WithToolPolicy(policyApproving("ask"), nil)
	prompt := w.model.onCalls("aa", []string{"ask"})
	id := prompt + "#0.0"
	p := w.process()
	run := suspendRun(t, p, "c", prompt, id)
	evs, err := p.answer(run.ID, harness.Answer{ID: id, Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := lastDone(t, evs).Run; got.Status != store.StatusSuspended || !slices.Equal(got.Pending, []string{id}) {
		t.Fatalf("run %+v, want suspended again", got)
	}
	ins, answers, err := p.r.Interrupts(w.ctx, run.ID)
	if err != nil || len(ins) != 1 || ins[0].Kind != harness.InterruptQuestion || len(answers) != 0 {
		t.Fatalf("Interrupts = %+v %+v %v, want the question", ins, answers, err)
	}
	evs, err = p.answer(run.ID, harness.Answer{ID: id, Text: "no"})
	if err != nil {
		t.Fatal(err)
	}
	if got := lastDone(t, evs).Run; got.Status != store.StatusCompleted {
		t.Fatalf("run %+v", got)
	}
	if res := toolResults(w, prompt); len(res) != 1 || res[0].Content != "no" {
		t.Fatalf("results %s", js(res))
	}
}

// A crash at any write of an answer leaves a run that the same answer, sent
// again to another process, completes, running an approved call once.
func TestAnswerSurvivesCrashes(t *testing.T) {
	t.Parallel()
	for _, k := range []struct {
		op     string
		n      int
		landed bool
	}{
		{"Append", 1, false}, // the answers
		{"Append", 1, true},
		{"Claim", 1, false}, // the resume
		{"Claim", 1, true},
		{"Transition", 1, false},
		{"Transition", 1, true},
		{"Append", 2, false}, // the finished turn
		{"Append", 2, true},
	} {
		t.Run(k.op+map[bool]string{true: "Landed", false: ""}[k.landed]+string(rune('0'+k.n)), func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			w.interactions()
			prompt := w.model.onCalls("w", []string{"write"})
			id := prompt + "#0.0"
			run := suspendRun(t, w.process(), "c", prompt, id)
			dead := w.process()
			dead.store.crashAt(crashAtNth(k.op, k.n, k.landed))
			a := harness.Answer{ID: id, Approved: true}
			if x, err := dead.r.Answer(w.ctx, run.ID, a); err == nil {
				if _, err := dead.drain(x, nil); !errors.Is(err, errCrashed) {
					t.Fatalf("crashed execution ended with %v", err)
				}
			} else if !errors.Is(err, errCrashed) {
				t.Fatal(err)
			}
			w.expire()
			p := w.process()
			var evs []runner.Event
			var err error
			switch got := runOf(t, p, run.ID); got.Status {
			case store.StatusSuspended:
				evs, err = p.answer(run.ID, a)
			case store.StatusRunning:
				// It moved to running: recovery's job.
				evs, err = p.resume(run.ID)
			default:
				t.Fatalf("run %+v after the crash", got)
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := lastDone(t, evs).Run; got.Status != store.StatusCompleted {
				t.Fatalf("run %+v", got)
			}
			// A crash before the finished turn is stored runs it again, as
			// it would any call whose turn was lost.
			want := 1
			if k.op == "Append" && k.n == 2 && !k.landed {
				want = 2
			}
			if w.writes(id) != want {
				t.Fatalf("wrote %d times", w.writes(id))
			}
		})
	}
}

// A crash while the run suspends leaves it running, for recovery: the
// resume suspends it again from the pending turn the log holds, or runs the
// turn again when the log does not hold it.
func TestSuspendSurvivesCrashes(t *testing.T) {
	t.Parallel()
	for _, landed := range []bool{false, true} {
		t.Run(map[bool]string{true: "landed", false: "lost"}[landed], func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			w.interactions()
			prompt := w.model.onCalls("w", []string{"write"})
			id := prompt + "#0.0"
			dead := w.process()
			dead.store.crashAt(crashAtNth("Append", 2, landed)) // the interrupt entry
			if _, err := dead.run("c", prompt); !errors.Is(err, errCrashed) {
				t.Fatalf("crashed run ended with %v", err)
			}
			w.expire()
			p := w.process()
			page, err := w.mem.List(w.ctx, "c", store.Page{})
			if err != nil || len(page.Runs) != 1 {
				t.Fatalf("runs %+v, %v", page.Runs, err)
			}
			run := page.Runs[0]
			evs, err := p.resume(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got := lastDone(t, evs).Run; got.Status != store.StatusSuspended || !slices.Equal(got.Pending, []string{id}) {
				t.Fatalf("run %+v", got)
			}
			want := 1
			if !landed {
				want = 2
			}
			if n := len(w.model.requestsFor(prompt)); n != want {
				t.Fatalf("%d model calls, want %d", n, want)
			}
			if _, err := p.answer(run.ID, harness.Answer{ID: id, Approved: true}); err != nil {
				t.Fatal(err)
			}
			if w.writes(id) != 1 {
				t.Fatalf("wrote %d times", w.writes(id))
			}
		})
	}
}

// An autonomous run is not offered ask, and a call held for approval is
// denied with a reason that says why; the mode is kept on resume.
func TestAutonomousRunsNeverWait(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.interactions()
	prompt := w.model.onCalls("auto", []string{"write"}, []string{"echo"})
	dead := w.process()
	dead.store.crashAt(crashAtNth("Append", 2, false)) // after its prompt and first turn
	x, err := dead.r.Run(w.ctx, "c", harness.UserText(prompt), runner.Interaction(harness.Autonomous))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dead.drain(x, nil); !errors.Is(err, errCrashed) {
		t.Fatalf("crashed run ended with %v", err)
	}
	w.expire()
	p := w.process()
	evs, err := p.resume(x.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := lastDone(t, evs).Run; got.Status != store.StatusCompleted {
		t.Fatalf("run %+v", got)
	}
	for i, req := range w.model.requestsFor(prompt) {
		for _, d := range req.Tools {
			if d.Name == "ask" {
				t.Fatalf("request %d offered ask to an autonomous run", i)
			}
		}
	}
	res := toolResults(w, prompt)
	if len(res) != 2 || !res[0].IsError || !strings.Contains(res[0].Content, "cannot wait") || !strings.Contains(res[0].Content, "autonomous") {
		t.Fatalf("results %s", js(res))
	}
	if w.writes(prompt+"#0.0") != 0 {
		t.Fatal("the held write ran")
	}
	rec, err := runner.DecodeRecord(w.log("c")[0])
	if err != nil || rec.Prompt.Interaction != harness.Autonomous {
		t.Fatalf("prompt record %+v, %v", rec.Prompt, err)
	}
	if _, err := p.r.Run(w.ctx, "c", harness.UserText(prompt), runner.Interaction("chatty")); !errors.Is(err, runner.ErrInvalidConfig) {
		t.Fatalf("a bad mode: %v", err)
	}

	// The interactive agent offers it.
	other := w.model.on("i", 0)
	if _, err := p.run("c2", other); err != nil {
		t.Fatal(err)
	}
	if reqs := w.model.requestsFor(other); !slices.ContainsFunc(reqs[0].Tools, func(d tool.Definition) bool { return d.Name == "ask" }) {
		t.Fatal("an interactive run was not offered ask")
	}
}

// A suspended chat run is answered, not resumed or decided; a flow run is
// decided, not answered. Cancel ends a suspended run for good.
func TestSuspendedChatRunGuards(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.interactions()
	prompt := w.model.onCalls("g", []string{"ask"})
	id := prompt + "#0.0"
	p := w.process()
	run := suspendRun(t, p, "c", prompt, id)
	if _, err := p.r.Resume(w.ctx, run.ID); !errors.Is(err, runner.ErrAwaitingDecision) {
		t.Fatalf("Resume: %v", err)
	}
	if _, err := p.r.Decide(w.ctx, run.ID, id, flow.Decision{Approved: true}); !errors.Is(err, runner.ErrNotPending) {
		t.Fatalf("Decide: %v", err)
	}
	if _, err := p.r.Answer(w.ctx, "nope", harness.Answer{ID: id, Text: "yes"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Answer of no run: %v", err)
	}
	// A running run waits for no answers.
	other := w.model.on("o", 0)
	x, err := p.r.Run(w.ctx, "c2", harness.UserText(other))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.r.Answer(w.ctx, x.Run.ID, harness.Answer{ID: id, Text: "yes"}); !errors.Is(err, runner.ErrNotPending) {
		t.Fatalf("Answer of a running run: %v", err)
	}
	if _, err := p.drain(x, nil); err != nil {
		t.Fatal(err)
	}

	got, err := p.r.Cancel(w.ctx, run.ID)
	if err != nil || got.Status != store.StatusCanceled {
		t.Fatalf("Cancel = %+v, %v", got, err)
	}
	if _, err := p.r.Answer(w.ctx, run.ID, harness.Answer{ID: id, Text: "yes"}); !errors.Is(err, store.ErrFinal) {
		t.Fatalf("Answer of a canceled run: %v", err)
	}
	if err := w.transcript("c").Validate(); err != nil {
		t.Fatal(err)
	}
	// The conversation goes on.
	next := w.model.on("next", 0)
	if _, err := p.run("c", next); err != nil {
		t.Fatal(err)
	}
}
