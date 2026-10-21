package runner_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store"
)

// A background chat run still going on at its wall-clock limit (T071) is
// stopped mid-turn, keeps the turns it did, and is failed for good with
// ErrTimeLimit; a resume after the limit fails at once, without the model.
func TestTimeLimitFailsABackgroundRun(t *testing.T) {
	t.Parallel()
	const limit = ttl / 2 // advanced at once, it leaves the lease held
	w := newWorld(t)
	p := w.process(runner.WithTimeLimit(limit))
	q := w.model.on("long", 3)
	w.model.gate = func(ctx context.Context, prompt string, turn int) {
		if turn == 1 { // the second model call: past the limit, then wait
			w.clk.Advance(limit)
			<-ctx.Done()
		}
	}
	_, err := p.run("c", q)
	var re *runner.RunError
	if !errors.As(err, &re) || !errors.Is(err, runner.ErrTimeLimit) {
		t.Fatalf("the run ended with %v, want a RunError of ErrTimeLimit", err)
	}
	run := w.get(re.Run.ID)
	if run.Status != store.StatusFailed || run.Retryable || run.Error != runner.ErrTimeLimit.Error() {
		t.Errorf("run = %+v, want failed for good with the limit's error", run)
	}
	if got := w.log("c"); len(got) != 2 || got[1].Kind != store.KindTurn {
		t.Errorf("the log has %d entries, want the prompt and the first turn kept", len(got))
	}

	w.model.gate = nil
	calls := len(w.model.Requests())
	if _, err := p.resume(run.ID); !errors.Is(err, runner.ErrTimeLimit) {
		t.Errorf("a resume past the limit gave %v, want ErrTimeLimit", err)
	}
	if n := len(w.model.Requests()); n != calls {
		t.Errorf("the resume called the model %d times, want none", n-calls)
	}
	if run := w.get(run.ID); run.Status != store.StatusFailed || run.Retryable {
		t.Errorf("after the resume the run is %+v, want failed for good", run)
	}
}

// Interactive runs, which stop with their user, have no wall-clock limit.
func TestTimeLimitLeavesInteractiveRuns(t *testing.T) {
	t.Parallel()
	const limit = ttl / 4
	w := newWorld(t)
	p := w.process(runner.WithTimeLimit(limit))
	q := w.model.on("chat", 2)
	w.model.gate = func(ctx context.Context, prompt string, turn int) {
		if turn == 1 {
			w.clk.Advance(2 * limit)
		}
	}
	x, err := p.r.Run(w.ctx, "c", harness.UserText(q), runner.Interactive())
	if err != nil {
		t.Fatal(err)
	}
	evs, err := p.drain(x, nil)
	if err != nil {
		t.Fatalf("the interactive run ended with %v, want it done", err)
	}
	if end := lastDone(t, evs); end.Run.Status != store.StatusCompleted {
		t.Errorf("the interactive run is %s, want completed", end.Run.Status)
	}
}

func TestTimeLimitMustNotBeNegative(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	if _, err := runner.New(w.mem, func(context.Context, runner.Placement) (runner.Agent, error) { return nil, nil },
		runner.WithTimeLimit(-time.Second)); !errors.Is(err, runner.ErrInvalidConfig) {
		t.Errorf("New with a negative limit: %v, want ErrInvalidConfig", err)
	}
}
