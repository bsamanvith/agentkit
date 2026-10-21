package runner_test

import (
	"context"
	"errors"
	"slices"

	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store"
)

// checkInterrupts checks invariant 7, on the interrupt entries of a
// conversation's log:
//
//   - every one is a pending turn the runner can read, and the run's next
//     ones repeat it with the answers so far, each keeping every answer of
//     the one before (a turn whose answers were all given may be followed by
//     a new pending turn: an approved tool that asked);
//   - after the last one comes nothing (the run is not completed), the
//     turn that finishes it, or the marker of a canceled run. The finishing
//     turn holds the same reply, the results of the calls that had finished
//     unchanged, and for each interrupt the result its answer gives, every
//     interrupt being answered;
//   - a suspended chat run waits for exactly the interrupts of its last
//     pending turn, which ends the log.
//
// Called with w.mu held.
func (w *world) checkInterrupts(ctx context.Context, conv string) {
	t := w.t
	log := w.logIn(ctx, conv)
	var prev *store.Entry
	var prevTurn harness.Pending
	var prevAnswers []harness.Answer
	for i := range log {
		e := log[i]
		if e.Kind == store.KindInterrupt {
			p, answers, err := runner.PendingTurn(e)
			if err != nil {
				t.Errorf("invariant 7: %v", err)
				prev = nil
				continue
			}
			if prev != nil {
				switch {
				case prev.RunID != e.RunID:
					t.Errorf("invariant 7: run %q's pending turn at %d is followed by run %q's at %d", prev.RunID, prev.Seq, e.RunID, e.Seq)
				case sameJSON(prevTurn, p):
					if len(answers) < len(prevAnswers) || len(prevAnswers) > 0 && !sameJSON(answers[:len(prevAnswers)], prevAnswers) {
						t.Errorf("invariant 7: answers at %d of %q %s drop or change those at %d %s", e.Seq, conv, js(answers), prev.Seq, js(prevAnswers))
					}
				case !complete(prevTurn, prevAnswers):
					t.Errorf("invariant 7: pending turn at %d of %q replaced at %d before all its answers came", prev.Seq, conv, e.Seq)
				}
			}
			prev, prevTurn, prevAnswers = &log[i], p, answers
			continue
		}
		if prev == nil {
			continue
		}
		rec, _ := runner.DecodeRecord(e)
		switch {
		case e.RunID == prev.RunID && rec.Canceled != nil:
		case e.RunID == prev.RunID && e.Kind == store.KindTurn && rec.Turn != nil:
			w.checkFinished(conv, prevTurn, prevAnswers, e)
		default:
			t.Errorf("invariant 7: the pending turn at %d of %q is followed by %s entry %d of run %q", prev.Seq, conv, e.Kind, e.Seq, e.RunID)
		}
		prev = nil
	}
	if prev != nil {
		if run, err := w.mem.Get(ctx, prev.RunID); err == nil && run.Status == store.StatusCompleted {
			t.Errorf("invariant 7: run %q completed with its pending turn at %d of %q unfinished", run.ID, prev.Seq, conv)
		}
	}
	page, err := w.mem.List(ctx, conv, store.Page{Limit: store.MaxPageLimit})
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range page.Runs {
		if run.Status != store.StatusSuspended {
			continue
		}
		if _, err := w.mem.Load(ctx, run.ID); !errors.Is(err, flow.ErrNotFound) {
			continue // a flow run, waiting for decisions
		}
		if prev == nil || prev.RunID != run.ID {
			t.Errorf("invariant 7: suspended chat run %q does not end the log of %q with its pending turn", run.ID, conv)
			continue
		}
		var ids []string
		for _, in := range prevTurn.Interrupts {
			ids = append(ids, in.ID)
		}
		slices.Sort(ids)
		if !slices.Equal(ids, run.Pending) {
			t.Errorf("invariant 7: suspended run %q waits for %q, its pending turn for %q", run.ID, run.Pending, ids)
		}
	}
}

func complete(p harness.Pending, answers []harness.Answer) bool {
	for _, in := range p.Interrupts {
		if !slices.ContainsFunc(answers, func(a harness.Answer) bool { return a.ID == in.ID }) {
			return false
		}
	}
	return true
}

// checkFinished checks the turn entry e that finishes the pending turn p.
func (w *world) checkFinished(conv string, p harness.Pending, answers []harness.Answer, e store.Entry) {
	t := w.t
	if len(e.Messages) != 2 || !sameJSON(e.Messages[0], p.Reply()) {
		t.Errorf("invariant 7: turn %d of %q does not finish the pending reply %s: %s", e.Seq, conv, js(p.Reply()), js(e.Messages))
		return
	}
	got := map[string]llm.ToolResult{}
	for _, b := range e.Messages[1].Content {
		if r, ok := b.(llm.ToolResult); ok {
			got[r.ToolUseID] = r
		}
	}
	for _, b := range p.Done.Content {
		r := b.(llm.ToolResult)
		if !sameJSON(got[r.ToolUseID], r) {
			t.Errorf("invariant 7: turn %d of %q changed a finished call's result %s to %s", e.Seq, conv, js(r), js(got[r.ToolUseID]))
		}
	}
	for _, in := range p.Interrupts {
		i := slices.IndexFunc(answers, func(a harness.Answer) bool { return a.ID == in.ID })
		if i < 0 {
			t.Errorf("invariant 7: turn %d of %q finishes interrupt %q, which has no answer", e.Seq, conv, in.ID)
			continue
		}
		a, r := answers[i], got[in.ID]
		switch {
		case in.Kind == harness.InterruptQuestion && (r.Content != a.Text || r.IsError):
			t.Errorf("invariant 7: question %q answered %q has the result %s", in.ID, a.Text, js(r))
		case in.Kind == harness.InterruptApproval && !a.Approved && (!r.IsError || r.Content != denial(a)):
			t.Errorf("invariant 7: denied call %q has the result %s", in.ID, js(r))
		case in.Kind == harness.InterruptApproval && a.Approved && r.Content == denial(a):
			t.Errorf("invariant 7: approved call %q has a denial for its result", in.ID)
		}
	}
}

// denial is a denied call's result.
func denial(a harness.Answer) string {
	by, reason := a.By, a.Reason
	if by == "" {
		by = "a person"
	}
	if reason == "" {
		reason = "no reason given"
	}
	return "denied by " + by + ": " + reason
}
