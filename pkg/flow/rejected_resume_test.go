package flow_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/flow"
)

// A rejection is final (T023), also when it cancelled a sibling branch that
// had not finished: resuming the rejected run must report the rejection and
// run nothing, not run the sibling again and report its failure instead.
// Found by the T013 dry run on pkg/flow.
//
// Whether the work step starts before the recorded rejection cancels the
// Parallel depends on scheduling, so the checks on its calls and on the
// returned error catch the original symptom only when it does. The checks
// that a rejected run emits no events and saves nothing fail on every run
// without the fix.
func TestResumeOfARejectedRunIsRejectedEvenIfASiblingFails(t *testing.T) {
	t.Parallel()
	errWork := errors.New("work failed")
	var workCalls atomic.Int32
	work := flow.Func("work", func(ctx context.Context, _ *flow.State) error {
		switch workCalls.Add(1) {
		case 1: // run 1: fails, so the run can be resumed with a decision
			return errWork
		case 2: // resume 1: still working when the rejection cancels it
			<-ctx.Done()
			return ctx.Err()
		default: // any later attempt fails at once, beating the approval in Parallel
			return errWork
		}
	})
	step := flow.Parallel("par", work, flow.Approve("ok"))
	cp := flow.NewMemory()
	const id = "rejected-resume"

	if _, err := run(t, cp, id, step); !errors.Is(err, errWork) {
		t.Fatalf("run 1: err = %v, want the work failure", err)
	}
	_, err := resume(t, cp, id, step, flow.WithDecision("par[1]/ok", flow.Decision{Reason: "no"}))
	var rej *flow.RejectedError
	if !errors.As(err, &rej) || rej.Path != "par[1]/ok" || rej.Reason != "no" {
		t.Fatalf("resume 1: err = %v, want the rejection of par[1]/ok", err)
	}
	if got := load(t, cp, id).Status; got != flow.StatusRejected {
		t.Fatalf("after the rejection: status %q, want %q", got, flow.StatusRejected)
	}
	callsBefore := workCalls.Load()

	var events atomic.Int32
	counting := &countingSaves{Checkpointer: cp}
	_, err = resume(t, counting, id, step, flow.WithObserver(func(flow.Event) { events.Add(1) }))
	if !errors.As(err, &rej) || rej.Path != "par[1]/ok" || rej.Reason != "no" {
		t.Errorf("resume 2: err = %v, want the recorded rejection of par[1]/ok", err)
	}
	if got := workCalls.Load(); got != callsBefore {
		t.Errorf("resume 2 ran the work step again (%d calls, want %d)", got, callsBefore)
	}
	if n := events.Load(); n != 0 {
		t.Errorf("resume 2 emitted %d events, want none: a rejected run does not run", n)
	}
	if n := counting.saves.Load(); n != 0 {
		t.Errorf("resume 2 saved %d checkpoints, want none: a rejected run is final", n)
	}
	if got := load(t, cp, id).Status; got != flow.StatusRejected {
		t.Errorf("after resume 2: status %q, want %q (a rejection is final)", got, flow.StatusRejected)
	}
}

// countingSaves counts the checkpoints saved through it.
type countingSaves struct {
	flow.Checkpointer
	saves atomic.Int32
}

func (c *countingSaves) Save(ctx context.Context, id string, ck flow.Checkpoint) error {
	c.saves.Add(1)
	return c.Checkpointer.Save(ctx, id, ck)
}
