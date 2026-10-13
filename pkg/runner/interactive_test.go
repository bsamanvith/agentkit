package runner_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store"
)

// retrying is a stop function for drain that reads to the end and lets every
// inline retry go ahead: on each EventRetrying it advances the clock by the
// event's delay once the execution waits (its lease renewal and its retry
// timer are armed).
func (w *world) retrying() func(runner.Event) bool {
	return func(ev runner.Event) bool {
		if ev.Kind == runner.EventRetrying {
			w.advanceWhenWaiting(ev.Delay)
		}
		return false
	}
}

func (w *world) advanceWhenWaiting(d time.Duration) {
	go func() {
		if err := w.clk.WaitForTimers(w.t.Context(), 2); err == nil {
			w.clk.Advance(d)
		}
	}()
}

// holdAt makes the model wait, the first time it is asked turn k of prompt,
// until the run's context ends. The channel is closed when it starts waiting.
func (w *world) holdAt(prompt string, k int) <-chan struct{} {
	held := make(chan struct{})
	var fired atomic.Bool
	w.model.gate = func(ctx context.Context, p string, turn int) {
		if p == prompt && turn == k && fired.CompareAndSwap(false, true) {
			close(held)
			<-ctx.Done()
		}
	}
	return held
}

// interrupted starts an interactive run of prompt in conv on p, interrupts it
// while the model works on turn k, and returns its ID.
func (w *world) interrupted(p *proc, conv, prompt string, k int) string {
	w.t.Helper()
	held := w.holdAt(prompt, k)
	x, err := p.r.Run(w.ctx, conv, harness.UserText(prompt), runner.Interactive())
	if err != nil {
		w.t.Fatal(err)
	}
	done := p.drainInBackground(x)
	<-held
	if _, err := p.r.Interrupt(w.ctx, x.Run.ID); err != nil {
		w.t.Fatalf("Interrupt: %v", err)
	}
	if err := <-done; !errors.Is(err, runner.ErrInterrupted) {
		w.t.Fatalf("interrupted execution ended with %v, want ErrInterrupted", err)
	}
	return x.Run.ID
}

// An interactive run whose process died anywhere is left alone by recovery,
// which calls no model for it: it is interrupted once its lease expires, and
// its user can still continue it.
func TestRecoveryLeavesInteractiveRunsAlone(t *testing.T) {
	t.Parallel()
	readOnly := map[string]bool{}
	for _, k := range readOnlyKillPoints {
		readOnly[k.name] = true
	}
	for _, k := range slices.Concat(killPoints, readOnlyKillPoints) {
		t.Run(k.name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			if readOnly[k.name] {
				w.readOnly()
			}
			prompt := w.model.on("q", 2)
			id := w.crashRun(w.ctx, "c", prompt, k, runner.Interactive())
			before, log, requests := w.get(id), w.log("c"), len(w.model.Requests())
			if !before.Interactive {
				t.Fatalf("run %+v is not interactive", before)
			}

			w.expire()
			p := w.process()
			rep, err := w.recovery(p).Recover(w.ctx)
			if err != nil || rep != (runner.Report{}) {
				t.Fatalf("Recover = %+v, %v; want nothing done", rep, err)
			}
			after := w.get(id)
			if after.Status != before.Status || after.Attempts != before.Attempts || after.Lease != before.Lease ||
				!sameJSON(w.log("c"), log) || len(w.model.Requests()) != requests {
				t.Fatalf("recovery touched the run: %+v → %+v, log %d → %d entries, %d → %d requests",
					before, after, len(log), len(w.log("c")), requests, len(w.model.Requests()))
			}
			if after.Status.Final() {
				return // it had completed
			}
			if !after.Interrupted(w.clk.Now()) {
				t.Fatalf("run %+v is not interrupted once its lease expired", after)
			}
			evs, err := p.resume(id)
			switch {
			case !k.started && !errors.Is(err, runner.ErrNotStarted):
				t.Errorf("continuing a run that never stored its prompt = %v, want ErrNotStarted", err)
			case k.started && (err != nil || lastDone(t, evs).Run.Status != store.StatusCompleted):
				t.Errorf("continuing the run = %v, want it completed", err)
			}
		})
	}
}

// Interrupt stops a live run: the turns it kept are stored, its lease is
// released, and the run stays running, interrupted. Interrupting it again,
// once free, does nothing; another process holding it refuses.
func TestInterruptStoresTurnsAndLeavesTheRunInterrupted(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.readOnly() // the two turns are kept, not yet stored, when it stops
	prompt := w.model.on("q", 3)
	p := w.process()
	held := w.holdAt(prompt, 2)
	x, err := p.r.Run(w.ctx, "c", harness.UserText(prompt), runner.Interactive())
	if err != nil {
		t.Fatal(err)
	}
	done := p.drainInBackground(x)
	<-held
	if got := len(w.log("c")); got != 1 {
		t.Fatalf("log has %d entries before the interrupt, want the prompt alone", got)
	}
	if _, err := w.process().r.Interrupt(w.ctx, x.Run.ID); !errors.Is(err, store.ErrLeased) {
		t.Fatalf("Interrupt by a process that does not hold the run = %v, want ErrLeased", err)
	}

	run, err := p.r.Interrupt(w.ctx, x.Run.ID)
	if err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if err := <-done; !errors.Is(err, runner.ErrInterrupted) {
		t.Fatalf("execution ended with %v, want ErrInterrupted", err)
	}
	now := w.clk.Now()
	if run.Status != store.StatusRunning || run.Lease != (store.Lease{}) || !run.Interrupted(now) {
		t.Fatalf("Interrupt returned %+v, want running, free and interrupted", run)
	}
	if got := len(w.log("c")); got != 3 {
		t.Errorf("log has %d entries after two turns, want 3", got)
	}
	if again, err := p.r.Interrupt(w.ctx, x.Run.ID); err != nil || !again.Interrupted(now) {
		t.Errorf("second Interrupt = %+v, %v; want the interrupted run, untouched", again, err)
	}
	if got := p.store.writes()["Transition"]; got != 0 {
		t.Errorf("%d transitions, want none: an interrupted run stays running", got)
	}
}

// An interactive run whose consumer stops reading (a closed tab) ends as an
// Interrupt does: its turns stored, its lease released, interrupted.
func TestInteractiveRunWhoseConsumerStopsIsInterrupted(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.readOnly()
	prompt := w.model.on("q", 3)
	p := w.process()
	x, err := p.r.Run(w.ctx, "c", harness.UserText(prompt), runner.Interactive())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.drain(x, func(ev runner.Event) bool {
		return ev.Harness.Kind == harness.EventTurnDone && ev.Harness.TurnRecord.Turn == 2
	}); err != nil {
		t.Fatal(err)
	}
	run := w.get(x.Run.ID)
	if run.Status != store.StatusRunning || run.Lease != (store.Lease{}) || !run.Interrupted(w.clk.Now()) {
		t.Fatalf("run = %+v, want running, free and interrupted", run)
	}
	if got := len(w.log("c")); got != 3 {
		t.Errorf("log has %d entries after two turns, want 3", got)
	}
}

// Continue is Resume: an interactive run interrupted while the model works
// on turn k sends, when resumed by any process, exactly the requests an
// uninterrupted run sends from turn k on, and ends like one.
func TestContinueSendsTheUnansweredRequests(t *testing.T) {
	t.Parallel()
	const tools = 3
	ref := newWorld(t)
	prompt := ref.model.on("q", tools)
	if _, err := ref.process().run("c", prompt); err != nil {
		t.Fatal(err)
	}
	want := ref.model.requestsFor(prompt)

	for k := 0; k <= tools; k++ {
		t.Run(fmt.Sprint("at turn ", k), func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			w.model.on("q", tools)
			id := w.interrupted(w.process(), "c", prompt, k)
			if got := w.model.requestsFor(prompt); !sameJSON(got, want[:k+1]) {
				t.Fatalf("requests before the interrupt %s, want %s", js(got), js(want[:k+1]))
			}
			evs, err := w.process().resume(id)
			if err != nil {
				t.Fatal(err)
			}
			if done := lastDone(t, evs); done.Run.Status != store.StatusCompleted || !done.Run.Interactive {
				t.Fatalf("continued run = %+v, want an interactive run completed", done.Run)
			}
			if got := w.model.requestsFor(prompt)[k+1:]; !sameJSON(got, want[k:]) {
				t.Errorf("requests of the continued run:\n%s\nuninterrupted run from turn %d:\n%s", js(got), k, js(want[k:]))
			}
			if !sameJSON(w.transcript("c"), ref.transcript("c")) {
				t.Errorf("transcript %s, uninterrupted %s", js(w.transcript("c")), js(ref.transcript("c")))
			}
		})
	}
}

// A new message on a conversation whose interactive run nobody runs
// (interrupted by the user, by a dead process, or failed) cancels that run
// and starts the new one in the same call. The model sees a valid
// conversation: the old run's marker reply, then the new message.
func TestNewMessageReplacesAnInterruptedRun(t *testing.T) {
	t.Parallel()
	for _, how := range []string{"before the answer", "after a tool turn", "process died", "failed"} {
		t.Run(how, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			p := w.process()
			prompt := w.model.on("old", 2)
			var old string
			switch how {
			case "before the answer":
				old = w.interrupted(p, "c", prompt, 0)
			case "after a tool turn":
				old = w.interrupted(p, "c", prompt, 1)
			case "process died":
				old = w.crashRun(w.ctx, "c", prompt, killPoints[2], runner.Interactive()) // after the first turn
				w.expire()
			case "failed":
				w.model.failing("old", 1, 10, llm.ErrOverloaded)
				x, err := p.r.Run(w.ctx, "c", harness.UserText(prompt), runner.Interactive())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := p.drain(x, w.retrying()); err == nil {
					t.Fatal("run against a failing model succeeded")
				}
				old = x.Run.ID
			}
			stored := len(w.log("c"))
			next := w.model.on("next", 0)
			x, err := p.r.Run(w.ctx, "c", harness.UserText(next), runner.Interactive())
			if err != nil {
				t.Fatalf("new message after the run stopped (%s) = %v", how, err)
			}
			evs, err := p.drain(x, nil)
			if err != nil || lastDone(t, evs).Run.Status != store.StatusCompleted {
				t.Fatalf("new run ended with %v", err)
			}
			if run := w.get(old); run.Status != store.StatusCanceled {
				t.Errorf("old run is %s, want canceled", run.Status)
			}
			log := w.log("c")
			if len(log) != stored+3 || log[stored].RunID != old || log[stored].Messages[0].Text() != runner.CanceledText {
				t.Fatalf("log after the new message = %s; want the old run's %d entries, its marker, the new prompt and answer", js(log), stored)
			}
			reqs := w.model.requestsFor(next)
			if len(reqs) != 1 || !sameJSON(reqs[0].Messages, messagesOf(log[:stored+2])) {
				t.Errorf("the new run sent %s, want the stored conversation with the marker and the new message", js(reqs))
			}
		})
	}
}

// A new message never replaces a run someone is running, nor a background
// run: it gets ErrRunUnfinished, and the run is untouched.
func TestNewMessageOnALiveOrBackgroundRunIsRefused(t *testing.T) {
	t.Parallel()
	t.Run("live", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		p := w.process()
		prompt := w.model.on("q", 1)
		held := w.holdAt(prompt, 0)
		x, err := p.r.Run(w.ctx, "c", harness.UserText(prompt), runner.Interactive())
		if err != nil {
			t.Fatal(err)
		}
		done := p.drainInBackground(x)
		<-held
		for _, q := range []*proc{p, w.process()} {
			_, err := q.r.Run(w.ctx, "c", harness.UserText(w.model.on("next", 0)), runner.Interactive())
			var u *runner.UnfinishedError
			if !errors.As(err, &u) || u.RunID != x.Run.ID {
				t.Errorf("new message while the run is live = %v, want ErrRunUnfinished for %q", err, x.Run.ID)
			}
		}
		if _, err := p.r.Interrupt(w.ctx, x.Run.ID); err != nil {
			t.Fatal(err)
		}
		<-done
		if run := w.get(x.Run.ID); run.Status != store.StatusRunning {
			t.Errorf("run is %s, want running", run.Status)
		}
	})
	t.Run("not yet iterated", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		p := w.process()
		x, err := p.r.Run(w.ctx, "c", harness.UserText(w.model.on("q", 0)), runner.Interactive())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.process().r.Run(w.ctx, "c", harness.UserText(w.model.on("next", 0))); !errors.Is(err, runner.ErrRunUnfinished) {
			t.Errorf("new message while the run's execution holds its lease = %v, want ErrRunUnfinished", err)
		}
		if _, err := p.drain(x, nil); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("background", func(t *testing.T) {
		t.Parallel()
		w := newWorld(t)
		p := w.process()
		x, err := p.r.Run(w.ctx, "c", harness.UserText(w.model.on("q", 1)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.drain(x, func(ev runner.Event) bool { return ev.Kind == runner.EventSaved }); err != nil {
			t.Fatal(err)
		}
		if _, err := p.r.Run(w.ctx, "c", harness.UserText(w.model.on("next", 0)), runner.Interactive()); !errors.Is(err, runner.ErrRunUnfinished) {
			t.Errorf("new message on a stopped background run = %v, want ErrRunUnfinished", err)
		}
		if run := w.get(x.Run.ID); run.Status != store.StatusRunning {
			t.Errorf("background run is %s, want running", run.Status)
		}
	})
}

// Two new messages racing to replace an interrupted run start exactly one
// run, wherever the second one lands in the first one's calls.
func TestRacingNewMessagesStartOneRun(t *testing.T) {
	t.Parallel()
	points := []struct {
		op string
		n  int
	}{
		{"Unfinished", 1}, // before the first cancels
		{"Claim", 1},      // as it claims the old run
		{"Transition", 1}, // as it cancels it
		{"Append", 1},     // as it appends the marker
		{"Create", 2},     // as it creates its own run
		{"Claim", 2},      // as it claims its own run
	}
	for _, at := range points {
		t.Run(fmt.Sprint(at.op, " ", at.n), func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			old := w.interrupted(w.process(), "c", w.model.on("old", 1), 0)
			first, second := w.process(), w.process()
			msg := func(name string) llm.Message { return harness.UserText(w.model.on(name, 0)) }

			// The second message is sent while the first one's call is
			// at the point: before its at.n-th call of at.op.
			var x2 *runner.Execution
			var err2 error
			var once sync.Once
			first.store.before = func(op string, n int) {
				if op == at.op && n == at.n {
					once.Do(func() { x2, err2 = second.r.Run(w.ctx, "c", msg("b"), runner.Interactive()) })
				}
			}
			x1, err1 := first.r.Run(w.ctx, "c", msg("a"), runner.Interactive())
			if x2 == nil && err2 == nil {
				t.Fatalf("the first message never reached %s %d: the test proves nothing", at.op, at.n)
			}

			started := 0
			for _, x := range []*runner.Execution{x1, x2} {
				if x == nil {
					continue
				}
				started++
				evs, err := first.drain(x, nil)
				if err != nil || lastDone(t, evs).Run.Status != store.StatusCompleted {
					t.Errorf("started run ended with %v", err)
				}
			}
			for _, err := range []error{err1, err2} {
				if err != nil && !errors.Is(err, runner.ErrRunUnfinished) {
					t.Errorf("the message that lost the race got %v, want ErrRunUnfinished", err)
				}
			}
			if started != 1 {
				t.Fatalf("%d runs started (errors %v, %v), want exactly one", started, err1, err2)
			}
			if run := w.get(old); run.Status != store.StatusCanceled {
				t.Errorf("old run is %s, want canceled", run.Status)
			}
		})
	}
}

// A retryable model error in an interactive run is retried inline, under the
// same execution: the run completes with an EventRetrying per retry, is never
// stored failed in between, and sends the failed request again as it was.
func TestRetryableErrorIsRetriedInline(t *testing.T) {
	t.Parallel()
	want := reference(t, "q", 2)
	w := newWorld(t)
	p := w.process()
	prompt := w.model.on("q", 2)
	w.model.failing("q", 1, 2, fmt.Errorf("%w: scripted", llm.ErrOverloaded))
	x, err := p.r.Run(w.ctx, "c", harness.UserText(prompt), runner.Interactive())
	if err != nil {
		t.Fatal(err)
	}
	evs, err := p.drain(x, w.retrying())
	if err != nil {
		t.Fatalf("run ended with %v, want it completed after the retries", err)
	}
	run := lastDone(t, evs).Run
	if run.Status != store.StatusCompleted || run.Attempts != 0 {
		t.Errorf("run = %+v, want completed with no failure stored", run)
	}
	var retries []int
	policy := runner.DefaultInteractiveRetryPolicy()
	for _, ev := range evs {
		if ev.Kind != runner.EventRetrying {
			continue
		}
		retries = append(retries, ev.Attempt)
		if !llm.IsRetryable(ev.Err) || ev.Delay <= 0 || ev.Delay > policy.MaxDelay {
			t.Errorf("EventRetrying %+v: want the model's error and a delay within the policy", ev)
		}
	}
	if !slices.Equal(retries, []int{2, 3}) {
		t.Errorf("EventRetrying attempts %v, want [2 3]", retries)
	}
	if got := p.store.writes(); got["Transition"] != 1 || got["Append"] != 4 {
		t.Errorf("writes %v, want one transition (completed) and the prompt and three turns appended", got)
	}
	if got, want := w.model.requestsFor(prompt), slices.Concat(want[:2], want[1:2], want[1:]); !sameJSON(got, want) {
		t.Errorf("requests %s, want the failed one sent again as it was: %s", js(got), js(want))
	}
}

// At the attempt limit the run is stored failed, not retryable, and recovery
// never resumes it.
func TestInlineRetryStopsAtMaxAttempts(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	p := w.process(runner.WithInteractiveRetry(runner.RetryPolicy{MaxAttempts: 2, BaseDelay: time.Second, MaxDelay: time.Second}))
	prompt := w.model.on("q", 1)
	w.model.failing("q", 0, 10, fmt.Errorf("%w: scripted", llm.ErrRateLimited))
	x, err := p.r.Run(w.ctx, "c", harness.UserText(prompt), runner.Interactive())
	if err != nil {
		t.Fatal(err)
	}
	evs, err := p.drain(x, w.retrying())
	var re *runner.RunError
	if !errors.As(err, &re) || !errors.Is(err, llm.ErrRateLimited) {
		t.Fatalf("run ended with %v, want a RunError with the model's error", err)
	}
	run := w.get(x.Run.ID)
	if run.Status != store.StatusFailed || run.Retryable || run.Attempts != 1 || re.Run.Retryable {
		t.Errorf("run = %+v, want failed for good, once", run)
	}
	if n := len(slices.DeleteFunc(evs, func(ev runner.Event) bool { return ev.Kind != runner.EventRetrying })); n != 1 {
		t.Errorf("%d EventRetrying, want 1", n)
	}
	if got := p.store.writes()["Transition"]; got != 1 {
		t.Errorf("%d transitions, want one (failed)", got)
	}
	w.clk.Advance(time.Hour)
	if rep, err := w.recovery(w.process()).Recover(w.ctx); err != nil || rep != (runner.Report{}) {
		t.Errorf("Recover = %+v, %v; want nothing done", rep, err)
	}
	if got := len(w.model.requestsFor(prompt)); got != 2 {
		t.Errorf("%d requests, want 2", got)
	}
}

// An Interrupt while an inline retry waits ends the run at once, interrupted,
// without the clock moving.
func TestInterruptDuringTheRetryWaitEndsTheRunAtOnce(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	p := w.process()
	prompt := w.model.on("q", 1)
	w.model.failing("q", 0, 10, fmt.Errorf("%w: scripted", llm.ErrOverloaded))
	x, err := p.r.Run(w.ctx, "c", harness.UserText(prompt), runner.Interactive())
	if err != nil {
		t.Fatal(err)
	}
	start := w.clk.Now()
	interrupted := make(chan error, 1)
	_, err = p.drain(x, func(ev runner.Event) bool {
		if ev.Kind == runner.EventRetrying {
			go func() {
				if err := w.clk.WaitForTimers(w.t.Context(), 2); err != nil {
					interrupted <- err
					return
				}
				_, err := p.r.Interrupt(w.ctx, x.Run.ID)
				interrupted <- err
			}()
		}
		return false
	})
	if !errors.Is(err, runner.ErrInterrupted) {
		t.Fatalf("run ended with %v, want ErrInterrupted", err)
	}
	if err := <-interrupted; err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	run := w.get(x.Run.ID)
	if !w.clk.Now().Equal(start) || !run.Interrupted(w.clk.Now()) || run.Attempts != 0 {
		t.Errorf("run = %+v at %v, want interrupted at once (%v) with no failure stored", run, w.clk.Now(), start)
	}
	if got := p.store.writes()["Transition"]; got != 0 {
		t.Errorf("%d transitions, want none", got)
	}
}

// An error that is not retryable ends an interactive run at once: failed for
// good, after one request.
func TestNonRetryableErrorFailsAnInteractiveRunAtOnce(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	p := w.process()
	prompt := w.model.on("q", 1)
	w.model.failing("q", 0, 10, fmt.Errorf("%w: scripted", llm.ErrInvalidRequest))
	x, err := p.r.Run(w.ctx, "c", harness.UserText(prompt), runner.Interactive())
	if err != nil {
		t.Fatal(err)
	}
	evs, err := p.drain(x, w.retrying())
	if !errors.Is(err, llm.ErrInvalidRequest) {
		t.Fatalf("run ended with %v, want the model's error", err)
	}
	if run := w.get(x.Run.ID); run.Status != store.StatusFailed || run.Retryable {
		t.Errorf("run = %+v, want failed for good", run)
	}
	if got := len(w.model.requestsFor(prompt)); got != 1 || slices.ContainsFunc(evs, func(ev runner.Event) bool { return ev.Kind == runner.EventRetrying }) {
		t.Errorf("%d requests, events %s; want one request and no retry", got, js(evs))
	}
}
