package runner_test

import (
	"context"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store"
)

// consumeHook is a command log that calls after once, after the first
// consumption that takes a pause: what happens just as a run acts on one.
type consumeHook struct {
	store.CommandLog
	once  sync.Once
	after func(runID string)
}

func (h *consumeHook) ConsumeCommands(ctx context.Context, runID, owner string, through int64) error {
	cmds, _ := h.PendingCommands(ctx, runID, 0)
	if err := h.CommandLog.ConsumeCommands(ctx, runID, owner, through); err != nil {
		return err
	}
	for _, c := range cmds {
		if c.Seq <= through && c.Type == runner.CommandPause {
			h.once.Do(func() { h.after(runID) })
		}
	}
	return nil
}

// A resume sent as the run consumes its pause, while the run still shows
// running (its sender sees it so, and leaves it to the run), withdraws the
// pause: the run goes on to its answer, not paused with the resume unread.
func TestAResumeSentAsThePauseIsTakenWithdrawsIt(t *testing.T) {
	t.Parallel()
	w := newSteerWorld(t)
	prompt := w.model.on("race", 1)
	w.gateAt(prompt+"/0", func(id string) { w.send(id, runner.PauseCommand("p")) })
	hook := &consumeHook{CommandLog: w.mem, after: func(id string) { w.send(id, runner.ResumeCommand("r")) }}
	p := w.process(runner.WithCommands(hook))
	x := w.start(p, "c", prompt)
	evs, err := p.drain(x, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done := lastDone(t, evs); done.Run.Status != store.StatusCompleted {
		t.Fatalf("run %s, want completed: the resume came before it paused", done.Run.Status)
	}
	if left, err := w.mem.PendingCommands(w.ctx, x.Run.ID, 0); err != nil || len(left) != 0 {
		t.Errorf("pending commands after the run = %v, %v; want none", left, err)
	}
}

// A run whose execution is stopped just after it consumed its pause is
// stored paused all the same: the pause is not lost, and recovery does not
// take the run up as if it was never asked.
func TestAPauseTakenAsTheExecutionStopsIsKept(t *testing.T) {
	t.Parallel()
	w := newSteerWorld(t)
	prompt := w.model.on("stop", 2)
	w.gateAt(prompt+"/0", func(id string) { w.send(id, runner.PauseCommand("p")) })
	ctx, cancel := context.WithCancel(w.ctx)
	defer cancel()
	hook := &consumeHook{CommandLog: w.mem, after: func(string) { cancel() }}
	p := w.process(runner.WithCommands(hook))
	x, err := p.r.Run(ctx, "c", harness.UserText(prompt))
	if err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	w.runID = x.Run.ID
	w.mu.Unlock()
	_, _ = p.drain(x, nil)
	if run := w.get(x.Run.ID); run.Status != store.StatusPaused {
		t.Fatalf("stored run %s after its pause was consumed, want paused", run.Status)
	}
	w.expire()
	p2 := w.process()
	if rep, err := w.recovery(p2).Recover(w.ctx); err != nil || rep.Resumed != 0 {
		t.Fatalf("recovery = %+v, %v; want nothing resumed", rep, err)
	}
	evs, err := p2.resume(x.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done := lastDone(t, evs); done.Run.Status != store.StatusCompleted {
		t.Fatalf("resumed run %s, want completed", done.Run.Status)
	}
}
