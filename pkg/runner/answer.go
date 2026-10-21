package runner

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// ErrAnswered means [Runner.Answer] was given an answer to an interrupt that
// has a different one already. It matches [ErrNotPending] too: the
// interrupt waits for nothing more.
var ErrAnswered = fmt.Errorf("%w: interrupt answered already", ErrNotPending)

// Interrupts returns the interrupts a suspended chat run waits for, and the
// answers given so far. A run that does not wait for answers (it is not
// suspended, or is a flow run waiting for decisions) gives [ErrNotPending],
// and a final one [store.ErrFinal].
func (r *Runner) Interrupts(ctx context.Context, runID string) ([]harness.Interrupt, []harness.Answer, error) {
	if _, err := tenancy.Require(ctx); err != nil {
		return nil, nil, err
	}
	run, err := r.store.Get(ctx, runID)
	if err != nil {
		return nil, nil, fmt.Errorf("run %q: %w", runID, err)
	}
	_, p, err := r.pendingOf(ctx, run)
	if err != nil {
		return nil, nil, err
	}
	return p.turn.Clone().Interrupts, slices.Clone(p.answers), nil
}

// pendingOf returns the conversation's context and the pending turn a
// suspended chat run waits on.
func (r *Runner) pendingOf(ctx context.Context, run store.Run) (view, *pendingTurn, error) {
	if run.Status.Final() {
		return view{}, nil, fmt.Errorf("run %q is %s: %w", run.ID, run.Status, store.ErrFinal)
	}
	if run.Status != store.StatusSuspended {
		return view{}, nil, fmt.Errorf("%w: run %q is %s", ErrNotPending, run.ID, run.Status)
	}
	v, err := r.cache.load(ctx, r.store, run.ConversationID)
	if err != nil {
		return view{}, nil, fmt.Errorf("run %q: %w", run.ID, err)
	}
	p := v.pending
	if p == nil || p.run != run.ID {
		return view{}, nil, fmt.Errorf("%w: run %q waits for decisions, not answers", ErrNotPending, run.ID)
	}
	if p.err != nil {
		return view{}, nil, fmt.Errorf("run %q: reading its pending turn: %w", run.ID, p.err)
	}
	return v, p, nil
}

// mergeAnswers adds answers to those p has. It reports whether any is new.
func mergeAnswers(p *pendingTurn, answers []harness.Answer) ([]harness.Answer, bool, error) {
	merged := slices.Clone(p.answers)
	changed := false
	for _, a := range answers {
		in, ok := p.turn.Interrupt(a.ID)
		if !ok {
			return nil, false, fmt.Errorf("%w: the run waits for no interrupt %q", ErrNotPending, a.ID)
		}
		if had, ok := p.answered(a.ID); ok {
			if had != a {
				return nil, false, fmt.Errorf("%w: interrupt %q", ErrAnswered, a.ID)
			}
			continue
		}
		if err := in.Check(a); err != nil {
			return nil, false, fmt.Errorf("%w: %w", ErrInvalidInput, err)
		}
		merged = append(merged, a)
		changed = true
	}
	return merged, changed, nil
}

// Answer answers interrupts of a suspended chat run: a question's text, or
// an approval's approved, reason and who. The answers are stored before
// anything runs. Once every interrupt of the run's pending turn has its
// answer, the run resumes as [Runner.Resume] does: Answer claims it and
// moves it to running before it returns, and the execution finishes the
// pending turn (an answer is its question's result, an approved call runs
// with its original input, a denied one's result is the denial) and goes
// on. Until then, the execution yields one [EventDone] with the run, still
// suspended, and nothing runs.
//
// The same answer again is a no-op, so a retry is safe; it also resumes a run
// whose every answer is stored but which a crash kept from resuming. A
// different answer to an interrupt that has one is [ErrAnswered]; an answer
// to an interrupt the run does not wait for, or to a run that is not
// suspended on interrupts (it resumed already, or is a flow run waiting for
// decisions), is [ErrNotPending]; a final run is [store.ErrFinal]. An answer
// that does not fit its interrupt (text that is not one of its choices, text
// for an approval) matches [harness.ErrInvalidAnswer] and [ErrInvalidInput].
func (r *Runner) Answer(ctx context.Context, runID string, answers ...harness.Answer) (*Execution, error) {
	if _, err := tenancy.Require(ctx); err != nil {
		return nil, err
	}
	if r.isClosed() {
		return nil, ErrShutdown
	}
	ids := map[string]bool{}
	for _, a := range answers {
		if ids[a.ID] {
			return nil, fmt.Errorf("%w: %w: interrupt %q is answered twice", ErrInvalidInput, harness.ErrInvalidAnswer, a.ID)
		}
		ids[a.ID] = true
	}
	// Two answers at once race for the log's next position: the loser reads
	// the log again, with the winner's answers.
	for try := 1; ; try++ {
		run, err := r.store.Get(ctx, runID)
		if err != nil {
			return nil, fmt.Errorf("run %q: %w", runID, err)
		}
		v, p, err := r.pendingOf(ctx, run)
		if err != nil {
			return nil, err
		}
		merged, changed, err := mergeAnswers(p, answers)
		if err != nil {
			return nil, fmt.Errorf("run %q: %w", runID, err)
		}
		if changed {
			reply := p.turn.Reply()
			_, err := r.append(ctx, run.ConversationID, &v, store.Entry{
				Kind:     store.KindInterrupt,
				Messages: []llm.Message{reply},
				RunID:    runID,
				Record:   mustJSON(Record{Interrupt: interruptRecord(p.turn, merged)}),
			})
			if errors.Is(err, store.ErrConflict) && try < 5 {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("run %q: storing the answers: %w", runID, err)
			}
			p.answers = merged
		}
		if !p.complete() {
			done := Event{Kind: EventDone, RunID: runID, Run: run}
			return &Execution{Run: run, events: func(yield func(Event, error) bool) { yield(done, nil) }}, nil
		}
		return r.resume(ctx, runID, resumeConfig{answered: true})
	}
}
