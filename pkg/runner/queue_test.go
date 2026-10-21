package runner_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/memory"
)

// prompts counts a conversation's prompt entries.
func prompts(log []store.Entry) int {
	n := 0
	for _, e := range log {
		if e.Kind == store.KindPrompt {
			n++
		}
	}
	return n
}

// A run enqueued for later is started by the dispatcher's loop at the first
// pass from its start time, not before; its prompt is stored once, when it
// is enqueued, and the run makes exactly the requests a run started at once
// makes.
func TestEnqueuedRunStartsAtItsTimeThroughTheDispatcher(t *testing.T) {
	t.Parallel()
	want := reference(t, "q", 2)
	w := newWorld(t)
	p := w.process()
	prompt := w.model.on("q", 2)
	t0 := w.clk.Now()

	run, err := p.r.Enqueue(w.ctx, "c", harness.UserText(prompt), runner.StartAt(t0.Add(time.Minute)))
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if run.Status != store.StatusRunning || run.Lease != (store.Lease{}) || !run.StartAt.Equal(t0.Add(time.Minute)) {
		t.Errorf("Enqueue = %+v, want a running run, unclaimed, starting in a minute", run)
	}
	if stored := w.get(run.ID); !sameJSON(stored, run) {
		t.Errorf("Enqueue returned\n%+v\nthe store holds\n%+v", run, stored)
	}
	if log := w.log("c"); len(log) != 1 || log[0].Kind != store.KindPrompt || log[0].RunID != run.ID || log[0].Messages[0].Text() != prompt {
		t.Fatalf("log after Enqueue = %s, want the run's prompt alone", js(log))
	}

	ctx, cancel := context.WithCancel(w.ctx)
	reports := make(chan runner.Report)
	done := make(chan error, 1)
	rc := w.recovery(p, runner.WithInterval(30*time.Second))
	go func() {
		done <- rc.Run(ctx, func(rep runner.Report, err error) {
			if err != nil {
				t.Error(err)
			}
			reports <- rep
		})
	}()
	for _, at := range []time.Duration{0, 30 * time.Second} {
		if rep := <-reports; rep != (runner.Report{}) {
			t.Fatalf("pass at t0+%v = %+v, before the run's start", at, rep)
		}
		if err := w.clk.WaitForTimers(t.Context(), 1); err != nil {
			t.Fatal(err)
		}
		w.clk.Advance(30 * time.Second)
	}
	if rep := <-reports; rep.Resumed != 1 {
		t.Fatalf("pass at t0+1m = %+v, want the run started", rep)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}

	if got := w.get(run.ID); got.Status != store.StatusCompleted {
		t.Errorf("run = %+v, want completed", got)
	}
	if n := prompts(w.log("c")); n != 1 {
		t.Errorf("the log holds %d prompts, want the one stored by Enqueue", n)
	}
	if got := w.model.requestsFor(prompt); !sameJSON(got, want) {
		t.Errorf("the dispatched run made\n%s\na run started at once makes\n%s", js(got), js(want))
	}
	if !sameJSON(w.transcript("c"), referenceTranscript(t, "q", 2)) {
		t.Errorf("the dispatched run's transcript differs from a run started at once")
	}
}

// A dispatcher runs the due runs of its own lanes only, highest priority
// first, then oldest; another lane waits for its own dispatcher.
func TestDispatcherTakesItsLanesByPriority(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	p := w.process()
	enqueue := func(name, lane string, priority int) string {
		t.Helper()
		run, err := p.r.Enqueue(w.ctx, "c-"+name, harness.UserText(w.model.on(name, 0)), runner.Lane(lane), runner.Priority(priority))
		if err != nil {
			t.Fatalf("Enqueue %s: %v", name, err)
		}
		w.clk.Advance(time.Millisecond)
		return run.ID
	}
	ids := map[string]string{}
	for _, r := range []struct {
		name, lane string
		priority   int
	}{
		{"low", "a", 0}, {"high", "a", 2}, {"mid", "a", 1}, {"high-later", "a", 2}, {"other-lane", "b", 9},
	} {
		ids[enqueue(r.name, r.lane, r.priority)] = r.name
	}

	var mu sync.Mutex
	var order []string
	execute := func(_ context.Context, x *runner.Execution) {
		mu.Lock()
		order = append(order, ids[x.Run.ID])
		mu.Unlock()
		if _, err := x.Wait(); err != nil {
			t.Error(err)
		}
	}
	a := w.recovery(p, runner.WithLanes("a"), runner.WithWorkers(1), runner.WithExecute(execute))
	if rep, err := a.Recover(w.ctx); err != nil || rep.Resumed != 4 {
		t.Fatalf("lane a's pass = %+v, %v; want its four runs", rep, err)
	}
	if want := []string{"high", "high-later", "mid", "low"}; !slices.Equal(order, want) {
		t.Errorf("lane a ran %v, want %v", order, want)
	}
	for id, name := range ids {
		if want := map[bool]store.Status{true: store.StatusRunning, false: store.StatusCompleted}[name == "other-lane"]; w.get(id).Status != want {
			t.Errorf("run %s is %s, want %s", name, w.get(id).Status, want)
		}
	}
	b := w.recovery(p, runner.WithLanes("b", "c"), runner.WithExecute(execute))
	if rep, err := b.Recover(w.ctx); err != nil || rep.Resumed != 1 || order[len(order)-1] != "other-lane" {
		t.Errorf("lane b's pass = %+v, %v, ran %v; want the other lane's run", rep, err, order)
	}
}

// Enqueue takes Run's options but Interactive, and refuses what Run refuses;
// Run refuses StartAt; a dispatcher needs a lane.
func TestEnqueueRefusals(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	p := w.process()
	msg := harness.UserText(w.model.on("q", 0))
	for name, c := range map[string]struct {
		msg  llm.Message
		opts []runner.RunOption
		want error
	}{
		"interactive":        {msg, []runner.RunOption{runner.Interactive()}, runner.ErrInvalidConfig},
		"flush below 1":      {msg, []runner.RunOption{runner.FlushEvery(0)}, runner.ErrInvalidConfig},
		"an assistant reply": {llm.Assistant(llm.Text{Text: "hi"}), nil, runner.ErrInvalidInput},
		"a lane with a NUL":  {msg, []runner.RunOption{runner.Lane("a\x00")}, store.ErrInvalid},
		"a priority too big": {msg, []runner.RunOption{runner.Priority(store.MaxPriority + 1)}, store.ErrInvalid},
	} {
		if run, err := p.r.Enqueue(w.ctx, "c-"+name, c.msg, c.opts...); !errors.Is(err, c.want) {
			t.Errorf("Enqueue with %s = %+v, %v; want %v", name, run, err, c.want)
		}
	}
	if _, err := p.r.Enqueue(context.Background(), "c", msg); err == nil {
		t.Error("Enqueue with no tenant succeeded")
	}
	if _, err := p.r.Run(w.ctx, "c", msg, runner.StartAt(w.clk.Now().Add(time.Hour))); !errors.Is(err, runner.ErrInvalidConfig) {
		t.Errorf("Run with StartAt = %v, want ErrInvalidConfig", err)
	}

	first, err := p.r.Enqueue(w.ctx, "c", msg)
	if err != nil {
		t.Fatal(err)
	}
	var ue *runner.UnfinishedError
	if _, err := p.r.Enqueue(w.ctx, "c", harness.UserText(w.model.on("again", 0))); !errors.As(err, &ue) || ue.RunID != first.ID {
		t.Errorf("Enqueue on a conversation with an enqueued run = %v, want an UnfinishedError naming %s", err, first.ID)
	}
	if n := prompts(w.log("c")); n != 1 {
		t.Errorf("the log holds %d prompts, want the first run's alone", n)
	}
	if _, err := runner.NewRecovery(p.r, memory.Admin(w.mem), runner.WithLanes()); !errors.Is(err, runner.ErrInvalidConfig) {
		t.Errorf("WithLanes() = %v, want ErrInvalidConfig", err)
	}
	if rep, err := w.recovery(p).Recover(w.ctx); err != nil || rep.Resumed != 1 {
		t.Errorf("Recover = %+v, %v; want the enqueued run run", rep, err)
	}
}

// A run whose prompt could not be stored is canceled, so that its
// conversation is free; the dispatcher never sees it.
func TestEnqueueCancelsARunWhosePromptIsNotStored(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	p := w.process()
	// Every attempt of the prompt's append fails (the runner tries three).
	p.store.fail = func(op string, n int) (bool, bool) { return op == "Append" && n <= 3, false }
	if _, err := p.r.Enqueue(w.ctx, "c", harness.UserText(w.model.on("q", 0))); !errors.Is(err, errFlaky) {
		t.Fatalf("Enqueue with a failing Append = %v, want the store's error", err)
	}
	if run := w.get("run1"); run.Status != store.StatusCanceled || run.Lease != (store.Lease{}) {
		t.Errorf("run = %+v, want it canceled and released", run)
	}
	if rep, err := w.recovery(p).Recover(w.ctx); err != nil || rep != (runner.Report{}) {
		t.Errorf("Recover = %+v, %v; want nothing to run", rep, err)
	}
	if _, err := p.run("c", w.model.on("next", 0)); err != nil {
		t.Errorf("a run after the canceled one: %v", err)
	}
}

// A run the dispatcher claimed but could not resume keeps the claim's lease
// until it expires: the next pass does not take it again at once, and other
// runs of the lane are not held up behind it.
func TestDispatcherBacksOffARunItCannotResume(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	p := w.process()
	boom := errors.New("no agent for you")
	broken, err := runner.New(w.mem, func(_ context.Context, pl runner.Placement) (runner.Agent, error) {
		if pl.ConversationID == "c-broken" {
			return nil, boom
		}
		return harness.New(w.model, w.tools, harness.WithClock(w.clk))
	}, runner.WithClock(w.clk), runner.WithLease(ttl), runner.WithOwner("broken"), runner.WithIDs(w.newID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.r.Enqueue(w.ctx, "c-broken", harness.UserText(w.model.on("x", 0)), runner.Priority(1)); err != nil {
		t.Fatal(err)
	}
	ok, err := p.r.Enqueue(w.ctx, "c-ok", harness.UserText(w.model.on("y", 0)))
	if err != nil {
		t.Fatal(err)
	}
	rc, err := runner.NewRecovery(broken, memory.Admin(w.mem))
	if err != nil {
		t.Fatal(err)
	}
	rep, err := rc.Recover(w.ctx)
	if !errors.Is(err, boom) || rep.Errors != 1 || rep.Resumed != 1 || w.get(ok.ID).Status != store.StatusCompleted {
		t.Fatalf("first pass = %+v, %v; want the broken run's error and the other run run", rep, err)
	}
	if rep, err := rc.Recover(w.ctx); err != nil || rep != (runner.Report{}) {
		t.Errorf("a pass within the lease = %+v, %v; want the broken run left alone", rep, err)
	}
	w.expire()
	if rep, err := rc.Recover(w.ctx); !errors.Is(err, boom) || rep.Errors != 1 {
		t.Errorf("a pass after the lease = %+v, %v; want the broken run tried again", rep, err)
	}
	w.expire() // the invariants want no lease held at the end
}

// A run whose execution ends at once and leaves it due (here, an execute
// that stops reading at the first event) is run once per pass: claimed
// again, it keeps that claim's lease as a backoff and the pass goes on to
// the runs behind it, which it would otherwise hold up pass after pass.
func TestDispatcherRunsARunOncePerPass(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	p := w.process()
	for i := range 3 {
		if _, err := p.r.Enqueue(w.ctx, fmt.Sprint("c", i), harness.UserText(w.model.on(fmt.Sprint("q", i), 1))); err != nil {
			t.Fatal(err)
		}
	}
	var (
		mu  sync.Mutex
		ran []string
	)
	rc := w.recovery(p, runner.WithWorkers(1), runner.WithExecute(func(_ context.Context, x *runner.Execution) {
		mu.Lock()
		ran = append(ran, x.Run.ID)
		mu.Unlock()
		for range x.Events() {
			break
		}
	}))
	rep, err := rc.Recover(w.ctx)
	if err != nil || rep.Resumed != 3 || rep.Skipped != 3 || len(ran) != 3 {
		t.Errorf("Recover = %+v, %v, ran %v; want each run run once, then skipped once", rep, err, ran)
	}
	if run := w.get("run1"); run.Status != store.StatusRunning || run.Lease == (store.Lease{}) {
		t.Errorf("run1 = %+v, want it running and leased until its backoff ends", run)
	}
	if rep, err := rc.Recover(w.ctx); err != nil || rep != (runner.Report{}) {
		t.Errorf("a pass within the backoff = %+v, %v; want nothing claimed", rep, err)
	}
	w.expire()
	if rep, err := rc.Recover(w.ctx); err != nil || rep.Resumed != 3 {
		t.Errorf("a pass after the backoff = %+v, %v; want the runs run again", rep, err)
	}
	w.expire() // the invariants want no lease held at the end
}
