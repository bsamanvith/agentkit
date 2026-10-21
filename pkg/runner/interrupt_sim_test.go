package runner_test

import (
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// answerFor is the answer people give interrupt in: always the same for its
// ID, so that an answer sent again after a crash is the same answer.
func answerFor(in harness.Interrupt) harness.Answer {
	h := fnv.New32a()
	h.Write([]byte(in.ID))
	yes := h.Sum32()%3 != 0
	if in.Kind == harness.InterruptQuestion {
		return harness.Answer{ID: in.ID, Text: map[bool]string{true: "yes", false: "no"}[yes]}
	}
	a := harness.Answer{ID: in.ID, Approved: yes, By: "ann"}
	if !yes {
		a.Reason = "not that"
	}
	return a
}

// simulateInterrupts plays a random history, chosen by seed, of chat runs
// whose tools ask people (ask) and whose writes wait for approval (write),
// against one store. Runs are interactive or autonomous; their processes
// die at a random store call while running, suspending or answering; people
// answer the interrupts one by one or all at once, send an answer twice,
// send a different answer to an interrupt answered already, or cancel the
// run. Recovery and the clock bring every run that is not suspended to an
// end.
//
// Besides the invariants every test checks (invariant 7 among them), a write
// runs only once approved, and at least once if its run completed with it;
// an autonomous run never suspends, and is never offered ask.
func simulateInterrupts(t *testing.T, seed uint64) map[string]int {
	rng := rand.New(rand.NewPCG(seed, 0x1a7e))
	w := newWorld(t)
	w.interactions()
	mix := map[string]int{}
	var mu sync.Mutex // mix, from recovery goroutines
	count := func(k string) { mu.Lock(); mix[k]++; mu.Unlock() }
	convs := []string{"c0", "c1"}[:1+rng.IntN(2)]
	approved := map[string]bool{} // write call ID → its answer approved it
	autonomous := map[string]bool{}
	autoPrompt := map[string]bool{}

	// dying returns a process that may die at its n-th store call.
	dying := func(odds int, what string) *proc {
		p := w.process()
		if rng.IntN(odds) == 0 {
			n, landed := 1+rng.IntN(6), rng.IntN(2) == 0
			p.store.crashAt(func(_ string, _, total int) (bool, bool) {
				if total == n {
					count(fmt.Sprintf("dies %s, landed %v", what, landed))
				}
				return total == n, landed
			})
		}
		return p
	}
	ended := func(err error) bool {
		var re *runner.RunError
		return err == nil || errors.Is(err, errCrashed) || errors.As(err, &re)
	}

	// answer answers the run's open interrupts, as people would.
	answer := func(run store.Run) {
		p := dying(4, "answering")
		ins, answers, err := p.r.Interrupts(w.ctx, run.ID)
		if errors.Is(err, errCrashed) {
			return
		}
		if err != nil {
			t.Fatalf("seed %d: Interrupts of %q: %v", seed, run.ID, err)
		}
		var open []harness.Answer
		for _, in := range ins {
			a := answerFor(in)
			if in.Kind == harness.InterruptApproval {
				approved[in.ID] = a.Approved
			}
			if !slices.ContainsFunc(answers, func(b harness.Answer) bool { return b.ID == in.ID }) {
				open = append(open, a)
			}
		}
		if len(answers) > 0 && rng.IntN(3) == 0 {
			// The same answer again, and a different one.
			had := answers[rng.IntN(len(answers))]
			if _, err := p.answer(run.ID, had); err != nil && !errors.Is(err, errCrashed) {
				t.Fatalf("seed %d: the same answer again: %v", seed, err)
			}
			count("answer again")
			other := had
			other.Text, other.Approved = map[string]string{"yes": "no", "no": "yes", "": ""}[had.Text], !had.Approved && had.Text == ""
			// The same answer may have resumed the run, if a crash had kept
			// it from resuming: then the run waits for no answer.
			if _, err := p.r.Answer(w.ctx, run.ID, other); err != nil && !errors.Is(err, runner.ErrNotPending) && !errors.Is(err, store.ErrFinal) && !errors.Is(err, errCrashed) {
				t.Fatalf("seed %d: a different answer: %v", seed, err)
			} else if errors.Is(err, runner.ErrAnswered) {
				count("different answer refused")
			}
			return
		}
		if len(open) == 0 {
			// Every answer is stored, but the run did not resume: a crash.
			count("all answered, not resumed")
			open = answers[:1]
		}
		if rng.IntN(2) == 0 {
			open = open[:1]
		}
		if len(ins) > 1 {
			count("two interrupts")
		}
		count("answer")
		if _, err := p.answer(run.ID, open...); !ended(err) {
			t.Fatalf("seed %d: Answer %+v of %q: %v", seed, open, run.ID, err)
		}
	}

	// settle brings the conversation's runs to an end.
	settle := func(conv string) {
		for range 100 {
			run, err := w.mem.Unfinished(w.ctx, conv)
			if errors.Is(err, store.ErrNotFound) {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case run.Status == store.StatusSuspended && run.Lease.Expires.After(w.clk.Now()):
				// A process died answering: people try again later.
				count("suspended run leased")
				w.expire()
			case run.Status == store.StatusSuspended:
				if autonomous[run.ID] {
					t.Fatalf("seed %d: autonomous run %+v suspended", seed, run)
				}
				count("suspended")
				if rng.IntN(10) == 0 {
					count("canceled while suspended")
					if _, err := w.process().r.Cancel(w.ctx, run.ID); err != nil {
						t.Fatalf("seed %d: Cancel: %v", seed, err)
					}
					continue
				}
				answer(run)
			case run.Status == store.StatusFailed && !run.Retryable:
				count("fatal failure canceled")
				w.expire()
				if _, err := w.process().r.Cancel(w.ctx, run.ID); err != nil {
					t.Fatalf("seed %d: Cancel: %v", seed, err)
				}
			default:
				due := run.NextAttemptAt
				if run.Lease.Expires.After(due) {
					due = run.Lease.Expires.Add(time.Second)
				}
				w.clk.Advance(max(ttl+time.Second, due.Sub(w.clk.Now())))
				count("recover")
				_, _ = w.recovery(dying(4, "recovering")).Recover(w.ctx)
			}
		}
		run, _ := w.mem.Unfinished(w.ctx, conv)
		t.Fatalf("seed %d: run %+v of %q never ended", seed, run, conv)
	}

	tools := []string{"echo", "ask", "write"}
	for i := range 2 + rng.IntN(4) {
		conv := convs[rng.IntN(len(convs))]
		settle(conv)
		var turns [][]string
		for range 1 + rng.IntN(3) {
			var calls []string
			for range 1 + rng.IntN(2) {
				calls = append(calls, tools[rng.IntN(len(tools))])
			}
			turns = append(turns, calls)
		}
		prompt := w.model.onCalls(fmt.Sprintf("%s-%d", conv, i), turns...)
		var opts []runner.RunOption
		auto := rng.IntN(5) == 0
		if auto {
			count("autonomous")
			opts = append(opts, runner.Interaction(harness.Autonomous))
		}
		p := dying(3, "running")
		x, err := p.r.Run(w.ctx, conv, harness.UserText(prompt), opts...)
		if errors.Is(err, errCrashed) {
			continue
		}
		if err != nil {
			t.Fatalf("seed %d: Run: %v", seed, err)
		}
		autonomous[x.Run.ID], autoPrompt[prompt] = auto, auto
		if _, err := p.drain(x, nil); !ended(err) {
			t.Fatalf("seed %d: run %q ended with %v", seed, x.Run.ID, err)
		}
	}
	for _, conv := range convs {
		settle(conv)
	}

	// Writes run only once approved, and every approved write whose run
	// completed ran.
	completed := map[string]bool{}
	for _, conv := range convs {
		page, err := w.mem.List(w.ctx, conv, store.Page{Limit: store.MaxPageLimit})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range page.Runs {
			if r.Status == store.StatusCompleted {
				completed[r.ID] = true
				count("completed")
			}
		}
		for _, e := range w.log(conv) {
			if e.Kind != store.KindTurn || !completed[e.RunID] {
				continue
			}
			rec, err := runner.DecodeRecord(e)
			if err != nil || rec.Turn == nil {
				continue
			}
			for _, tr := range rec.Turn.Tools {
				if tr.Name == "write" && approved[tr.ToolUseID] && w.writes(tr.ToolUseID) == 0 {
					t.Errorf("seed %d: approved write %q of completed run %q never ran", seed, tr.ToolUseID, e.RunID)
				}
			}
		}
	}
	w.mu.Lock()
	for id, n := range w.written {
		if !approved[id] {
			t.Errorf("seed %d: write %q ran %d times without an approval", seed, id, n)
		}
		if n > 1 {
			mix["approved write ran again after a crash"]++
		}
	}
	w.mu.Unlock()
	for prompt, auto := range autoPrompt {
		for _, req := range w.model.requestsFor(prompt) {
			offered := slices.ContainsFunc(req.Tools, func(d tool.Definition) bool { return d.Name == "ask" })
			if offered == auto {
				t.Errorf("seed %d: a request for %q offered ask %v, autonomous %v", seed, prompt, offered, auto)
			}
		}
	}
	return mix
}

func TestInterruptSimulation(t *testing.T) {
	t.Parallel()
	seeds := uint64(200)
	if testing.Short() {
		seeds = 60
	}
	var mu sync.Mutex
	mix := map[string]int{}
	t.Run("seeds", func(t *testing.T) {
		for seed := range seeds {
			t.Run(fmt.Sprint(seed), func(t *testing.T) {
				t.Parallel()
				m := simulateInterrupts(t, seed)
				mu.Lock()
				defer mu.Unlock()
				for k, v := range m {
					mix[k] += v
				}
			})
		}
	})
	var missing []string
	for _, k := range []string{
		"suspended", "answer", "answer again", "different answer refused", "two interrupts",
		"canceled while suspended", "autonomous", "completed", "recover",
		"dies running, landed true", "dies running, landed false",
		"dies answering, landed true", "dies answering, landed false",
		"all answered, not resumed",
	} {
		if mix[k] == 0 {
			missing = append(missing, k)
		}
	}
	t.Logf("case mix over %d seeds: %v", seeds, mix)
	if !t.Failed() && len(missing) > 0 {
		t.Errorf("the simulation never produced %v (mix %v)", missing, mix)
	}
}

func FuzzInterruptSimulation(f *testing.F) {
	for seed := range uint64(4) {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed uint64) { simulateInterrupts(t, seed) })
}
