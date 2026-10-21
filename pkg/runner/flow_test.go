package runner_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store"
)

// steps makes flow steps that count their runs and set a key of their name.
type steps struct {
	mu sync.Mutex
	n  map[string]int
}

func (c *steps) step(name string) flow.Step {
	return flow.Func(name, func(_ context.Context, st *flow.State) error {
		c.mu.Lock()
		if c.n == nil {
			c.n = map[string]int{}
		}
		c.n[name]++
		c.mu.Unlock()
		return flow.Set(st, name, true)
	})
}

func (c *steps) counts() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return maps.Clone(c.n)
}

// approvalFlow is draft, an approval "f/ok", send; version 2 adds a step.
func (w *world) approvalFlow(c *steps, version *atomic.Int32) {
	w.flows = func(*proc, string) (flow.Step, error) {
		tree := []flow.Step{c.step("draft"), flow.Approve("ok"), c.step("send")}
		if version.Load() == 2 {
			tree = append(tree, c.step("log"))
		}
		return flow.Sequence("f", tree...), nil
	}
}

func (p *proc) runFlow(conv string) ([]runner.Event, error) {
	p.w.t.Helper()
	x, err := p.r.RunFlow(p.w.ctx, conv, nil)
	if err != nil {
		return nil, err
	}
	return p.drain(x, nil)
}

func (p *proc) decide(runID, path string, d flow.Decision, opts ...runner.ResumeOption) ([]runner.Event, error) {
	p.w.t.Helper()
	x, err := p.r.Decide(p.w.ctx, runID, path, d, opts...)
	if err != nil {
		return nil, err
	}
	return p.drain(x, nil)
}

func (w *world) suspended() []string {
	w.t.Helper()
	page, err := w.mem.ListSuspended(w.ctx, store.Page{Limit: store.MaxPageLimit})
	if err != nil {
		w.t.Fatal(err)
	}
	var ids []string
	for _, r := range page.Runs {
		ids = append(ids, r.ID)
	}
	return ids
}

func TestApprovedRunCompletes(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	var c steps
	var v atomic.Int32
	w.approvalFlow(&c, &v)
	p := w.process()
	evs, err := p.runFlow("c")
	if err != nil {
		t.Fatal(err)
	}
	run := lastDone(t, evs).Run
	if run.Status != store.StatusSuspended || !slices.Equal(run.Pending, []string{"f/ok"}) {
		t.Fatalf("run = %+v, want suspended on f/ok", run)
	}
	if got := w.suspended(); !slices.Equal(got, []string{run.ID}) {
		t.Errorf("ListSuspended = %v", got)
	}
	if rep, err := w.recovery(p).Recover(w.ctx); err != nil || rep != (runner.Report{}) {
		t.Errorf("Recover = %+v, %v; a suspended run waits for its decision", rep, err)
	}
	if _, err := p.resume(run.ID); !errors.Is(err, runner.ErrAwaitingDecision) {
		t.Errorf("Resume without a decision = %v, want ErrAwaitingDecision", err)
	}
	if _, err := p.decide(run.ID, "f/other", flow.Decision{Approved: true}); !errors.Is(err, runner.ErrNotPending) {
		t.Errorf("Decide on another path = %v, want ErrNotPending", err)
	}
	evs, err = w.process().decide(run.ID, "f/ok", flow.Decision{Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	done := lastDone(t, evs)
	if done.Run.Status != store.StatusCompleted || done.State == nil {
		t.Fatalf("after approval: %+v", done)
	}
	if sent, err := flow.Get[bool](done.State, "send"); err != nil || !sent {
		t.Errorf("final state has send = %v, %v", sent, err)
	}
	if got := c.counts(); got["draft"] != 1 || got["send"] != 1 {
		t.Errorf("steps ran %v times, want each once", got)
	}
	if got := w.suspended(); len(got) != 0 {
		t.Errorf("ListSuspended after approval = %v", got)
	}
	// A completed run is returned from its checkpoint by Resume, but a
	// decision for it is refused.
	if _, err := p.decide(run.ID, "f/ok", flow.Decision{Approved: true}); !errors.Is(err, store.ErrFinal) {
		t.Errorf("a second Decide = %v, want ErrFinal", err)
	}
}

func TestRejectedRunIsFinal(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	var c steps
	var v atomic.Int32
	w.approvalFlow(&c, &v)
	p := w.process()
	evs, err := p.runFlow("c")
	if err != nil {
		t.Fatal(err)
	}
	id := lastDone(t, evs).Run.ID
	evs, err = p.decide(id, "f/ok", flow.Decision{Reason: "the quote is too high"})
	if err != nil {
		t.Fatal(err)
	}
	run := lastDone(t, evs).Run
	if run.Status != store.StatusRejected || !strings.Contains(run.Error, "the quote is too high") {
		t.Fatalf("after rejection: %+v", run)
	}
	w.clk.Advance(1000 * ttl)
	if rep, err := w.recovery(p).Recover(w.ctx); err != nil || rep != (runner.Report{}) {
		t.Errorf("Recover = %+v, %v", rep, err)
	}
	if _, err := p.resume(id); !errors.Is(err, store.ErrFinal) {
		t.Errorf("Resume of a rejected run = %v, want ErrFinal", err)
	}
	if _, err := p.decide(id, "f/ok", flow.Decision{Approved: true}); !errors.Is(err, store.ErrFinal) {
		t.Errorf("Decide on a rejected run = %v, want ErrFinal", err)
	}
	if got := c.counts(); got["send"] != 0 {
		t.Errorf("a step after the rejected approval ran: %v", got)
	}
	if _, err := p.runFlow("c"); err != nil {
		t.Errorf("a rejected run holds the conversation: %v", err)
	}
}

// A run resumed with a different step tree fails with ErrShapeChanged and
// writes nothing; the original tree, or the operator's option, continues it.
func TestChangedStepTreeWritesNothing(t *testing.T) {
	t.Parallel()
	for _, how := range []string{"suspended", "interrupted"} {
		t.Run(how, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			var c steps
			var v atomic.Int32
			w.approvalFlow(&c, &v)
			p := w.process()
			var id string
			resume := func(opts ...runner.ResumeOption) ([]runner.Event, error) {
				q := w.process()
				if how == "suspended" {
					return q.decide(id, "f/ok", flow.Decision{Approved: true}, opts...)
				}
				return q.resume(id, opts...)
			}
			if how == "suspended" {
				evs, err := p.runFlow("c")
				if err != nil {
					t.Fatal(err)
				}
				id = lastDone(t, evs).Run.ID
			} else {
				dead := w.process()
				dead.store.crashAt(crashAtNth("Save", 2, true)) // after draft
				x, err := dead.r.RunFlow(w.ctx, "c", nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := x.Wait(); !errors.Is(err, errCrashed) {
					t.Fatal(err)
				}
				id = x.Run.ID
				w.expire()
			}
			run, ckpt := w.get(id), w.checkpoint(id)

			v.Store(2)
			_, err := resume()
			var sc *flow.ShapeChangedError
			var re *runner.RunError
			if !errors.As(err, &sc) || !errors.Is(err, flow.ErrShapeChanged) || errors.As(err, &re) {
				t.Fatalf("resume with a changed tree = %v, want a ShapeChangedError and no run failure", err)
			}
			if got := w.get(id); !sameJSON(got, run) {
				t.Errorf("run changed: %+v, was %+v", got, run)
			}
			if got := w.checkpoint(id); !sameJSON(got, ckpt) {
				t.Errorf("checkpoint changed: %s, was %s", js(got), js(ckpt))
			}
			if rep, err := w.recovery(p).Recover(w.ctx); how == "interrupted" && (rep.Resumed != 0 || !errors.Is(err, flow.ErrShapeChanged)) {
				// Recovery reports it on every pass and never fails the run.
				t.Errorf("Recover = %+v, %v; want the shape change reported", rep, err)
			}
			if got := w.get(id); got.Attempts != run.Attempts || got.Status != run.Status {
				t.Errorf("recovery changed the run: %+v", got)
			}
			if how == "interrupted" {
				// The dispatcher keeps the lease it claimed the run with
				// for one lease time, so that it does not claim it again at
				// once (T099).
				w.expire()
			}

			v.Store(1)
			evs, err := resume()
			if err != nil {
				t.Fatal(err)
			}
			if how == "interrupted" {
				// It goes on to the approval.
				if got := lastDone(t, evs).Run; got.Status != store.StatusSuspended {
					t.Fatalf("resume with the original tree: %+v", got)
				}
				if evs, err = p.decide(id, "f/ok", flow.Decision{Approved: true}); err != nil {
					t.Fatal(err)
				}
			}
			if got := lastDone(t, evs).Run; got.Status != store.StatusCompleted {
				t.Fatalf("resume with the original tree: %+v", got)
			}
			if got := c.counts(); got["draft"] != 1 || got["send"] != 1 {
				t.Errorf("steps ran %v", got)
			}
		})
	}
}

func TestOperatorMayResumeWithAChangedTree(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	var c steps
	var v atomic.Int32
	w.approvalFlow(&c, &v)
	p := w.process()
	evs, err := p.runFlow("c")
	if err != nil {
		t.Fatal(err)
	}
	id := lastDone(t, evs).Run.ID
	v.Store(2)
	evs, err = p.decide(id, "f/ok", flow.Decision{Approved: true}, runner.AllowShapeChange())
	if err != nil {
		t.Fatal(err)
	}
	if lastDone(t, evs).Run.Status != store.StatusCompleted {
		t.Fatalf("run did not complete")
	}
	if got := c.counts(); got["draft"] != 1 || got["send"] != 1 || got["log"] != 1 {
		t.Errorf("steps ran %v", got)
	}
}

// A completed run is returned from its checkpoint: nothing runs, so even a
// tree that changed since cannot fail it.
func TestCompletedRunIsReturnedFromItsCheckpoint(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	var c steps
	var v atomic.Int32
	w.approvalFlow(&c, &v)
	p := w.process()
	evs, err := p.runFlow("c")
	if err != nil {
		t.Fatal(err)
	}
	id := lastDone(t, evs).Run.ID
	evs, err = p.decide(id, "f/ok", flow.Decision{Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	want := lastDone(t, evs)
	before := c.counts()
	v.Store(2)
	for _, q := range []*proc{p, w.process()} {
		evs, err := q.resume(id)
		if err != nil {
			t.Fatal(err)
		}
		got := lastDone(t, evs)
		if len(evs) != 1 || !sameJSON(got.State, want.State) || !sameJSON(got.Run, w.get(id)) {
			t.Errorf("Resume of a completed run = %s, want only its final state %s", js(evs), js(want.State))
		}
	}
	if got := c.counts(); !sameJSON(got, before) {
		t.Errorf("steps ran again: %v, were %v", got, before)
	}
}

func (w *world) checkpoint(id string) flow.Checkpoint {
	w.t.Helper()
	ck, err := w.mem.Load(w.ctx, id)
	if err != nil {
		w.t.Fatal(err)
	}
	return ck
}

// A flow run killed between or inside its steps is resumed by recovery: the
// steps its checkpoint records do not run again, the others run once.
func TestRecoveryResumesAFlowMidway(t *testing.T) {
	t.Parallel()
	for _, k := range []killPoint{
		{"before the first checkpoint", "Save", 1, false, false},
		{"losing the first step", "Save", 2, false, true},
		{"after the first step", "Save", 2, true, true},
		{"after the second step", "Save", 3, true, true},
		{"before completing", "Transition", 1, false, true},
	} {
		t.Run(k.name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			var c steps
			w.flows = func(*proc, string) (flow.Step, error) {
				return flow.Sequence("f", c.step("a"), c.step("b"), c.step("c")), nil
			}
			dead := w.process()
			dead.store.crashAt(crashAtNth(k.op, k.n, k.landed))
			x, err := dead.r.RunFlow(w.ctx, "c", nil)
			var id string
			if !k.started {
				if !errors.Is(err, errCrashed) {
					t.Fatalf("RunFlow = %v, want the crash", err)
				}
				page, err := w.mem.List(w.ctx, "c", store.Page{})
				if err != nil || len(page.Runs) != 1 {
					t.Fatal(page, err)
				}
				id = page.Runs[0].ID
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := x.Wait(); !errors.Is(err, errCrashed) {
					t.Fatal(err)
				}
				id = x.Run.ID
			}
			recorded := map[string]bool{}
			if ck, err := w.mem.Load(w.ctx, id); err == nil {
				for path := range ck.Steps {
					recorded[path] = true
				}
				if ck.Status == flow.StatusCompleted {
					recorded["f/a"], recorded["f/b"], recorded["f/c"] = true, true, true
				}
			}
			before := c.counts()
			w.expire()
			rep, err := w.recovery(w.process()).Recover(w.ctx)
			if err != nil || rep.Resumed != 1 {
				t.Fatalf("Recover = %+v, %v", rep, err)
			}
			run := w.get(id)
			if !k.started {
				if run.Status != store.StatusCanceled {
					t.Fatalf("run = %+v, want canceled as not started", run)
				}
				return
			}
			if run.Status != store.StatusCompleted {
				t.Fatalf("run = %+v", run)
			}
			after := c.counts()
			for _, s := range []string{"a", "b", "c"} {
				want := 1
				if recorded["f/"+s] {
					want = 0
				}
				if got := after[s] - before[s]; got != want {
					t.Errorf("step %s ran %d times on resume, want %d (recorded %v)", s, got, want, recorded)
				}
			}
		})
	}
}

// A checkpoint from before fingerprints resumes with a warning on the stream.
func TestWarningReachesTheStream(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	var c steps
	var v atomic.Int32
	w.approvalFlow(&c, &v)
	p := w.process()
	evs, err := p.runFlow("c")
	if err != nil {
		t.Fatal(err)
	}
	id := lastDone(t, evs).Run.ID
	ck := w.checkpoint(id)
	ck.Shape = ""
	if err := w.mem.Save(w.ctx, id, ck); err != nil {
		t.Fatal(err)
	}
	evs, err = p.decide(id, "f/ok", flow.Decision{Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(evs, func(ev runner.Event) bool {
		return ev.Kind == runner.EventFlow && ev.Flow.Kind == flow.EventWarning && ev.RunID == id
	}) {
		t.Errorf("no warning in %s", js(evs))
	}
	if lastDone(t, evs).Run.Status != store.StatusCompleted {
		t.Errorf("run did not complete")
	}
}

// An interrupted flow run, whether its consumer stopped or its process died,
// is resumed by hand and completes without running a step its checkpoint
// recorded.
func TestInterruptedFlowIsResumedByHand(t *testing.T) {
	t.Parallel()
	for _, how := range []string{"stopped", "died"} {
		t.Run(how, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			var c steps
			w.flows = func(*proc, string) (flow.Step, error) {
				return flow.Sequence("f", c.step("a"), c.step("b"), c.step("c")), nil
			}
			first := w.process()
			if how == "died" {
				first.store.crashAt(crashAtNth("Save", 3, true)) // a and b are saved
			}
			x, err := first.r.RunFlow(w.ctx, "c", nil)
			if err != nil {
				t.Fatal(err)
			}
			if how == "stopped" {
				_, err = first.drain(x, func(ev runner.Event) bool {
					return ev.Kind == runner.EventFlow && ev.Flow.Kind == flow.EventFinished && ev.Flow.Path == "f/a"
				})
			} else if _, err = x.Wait(); errors.Is(err, errCrashed) {
				err = nil
			}
			if err != nil {
				t.Fatal(err)
			}
			if run := w.get(x.Run.ID); run.Status != store.StatusRunning {
				t.Fatalf("interrupted run = %+v, want running", run)
			}
			ck, err := w.mem.Load(w.ctx, x.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			before := c.counts()
			if how == "died" {
				w.expire()
			}
			evs, err := w.process().resume(x.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if run := lastDone(t, evs).Run; run.Status != store.StatusCompleted {
				t.Fatalf("resumed run = %+v, want completed", run)
			}
			after := c.counts()
			for _, s := range []string{"a", "b", "c"} {
				if _, saved := ck.Steps["f/"+s]; saved && after[s] != before[s] {
					t.Errorf("step %s was saved before the interruption and ran again", s)
				}
				if after[s] == 0 {
					t.Errorf("step %s never ran", s)
				}
			}
		})
	}
}

// A flow step that fails retryably fails the run as retryable; recovery
// retries it from its checkpoint, so the steps before it do not run again.
func TestFailedFlowStepIsRetriedFromItsCheckpoint(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	var c steps
	var failed atomic.Bool
	w.flows = func(*proc, string) (flow.Step, error) {
		flaky := flow.Func("b", func(ctx context.Context, st *flow.State) error {
			if failed.CompareAndSwap(false, true) {
				return fmt.Errorf("drafting: %w", llm.ErrOverloaded)
			}
			return c.step("b").Run(ctx, st)
		})
		return flow.Sequence("f", c.step("a"), flaky, c.step("c")), nil
	}
	p := w.process()
	_, err := p.runFlow("c")
	var re *runner.RunError
	if !errors.As(err, &re) || !errors.Is(err, llm.ErrOverloaded) {
		t.Fatalf("flow with a failing step ended with %v, want a RunError for ErrOverloaded", err)
	}
	if run := w.get(re.Run.ID); run.Status != store.StatusFailed || !run.Retryable || run.Attempts != 1 {
		t.Fatalf("failed flow run = %+v, want failed, retryable, 1 attempt", run)
	}
	w.clk.Advance(w.get(re.Run.ID).NextAttemptAt.Sub(w.clk.Now()))
	if rep, err := w.recovery(p).Recover(w.ctx); err != nil || rep.Resumed != 1 {
		t.Fatalf("Recover = %+v, %v", rep, err)
	}
	if run := w.get(re.Run.ID); run.Status != store.StatusCompleted {
		t.Fatalf("retried flow run = %+v, want completed", run)
	}
	if got, want := c.counts(), map[string]int{"a": 1, "b": 1, "c": 1}; !maps.Equal(got, want) {
		t.Errorf("successful step runs = %v, want %v: a must not rerun", got, want)
	}
}

// Canceling a run that waits for an approval makes it final: it leaves the
// suspended list, and a later decision is refused without running anything.
func TestCanceledSuspendedRunIsFinal(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	var c steps
	var v atomic.Int32
	w.approvalFlow(&c, &v)
	p := w.process()
	evs, err := p.runFlow("c")
	if err != nil {
		t.Fatal(err)
	}
	id := lastDone(t, evs).Run.ID
	run, err := p.r.Cancel(w.ctx, id)
	if err != nil || run.Status != store.StatusCanceled {
		t.Fatalf("Cancel = %+v, %v; want canceled", run, err)
	}
	if got := w.suspended(); len(got) != 0 {
		t.Errorf("ListSuspended after cancel = %v", got)
	}
	if _, err := p.decide(id, "f/ok", flow.Decision{Approved: true}); !errors.Is(err, store.ErrFinal) {
		t.Errorf("Decide on a canceled run = %v, want ErrFinal", err)
	}
	if _, err := p.r.Cancel(w.ctx, id); !errors.Is(err, store.ErrFinal) {
		t.Errorf("a second Cancel = %v, want ErrFinal", err)
	}
	if got := c.counts(); got["send"] != 0 {
		t.Errorf("a step after the canceled approval ran: %v", got)
	}
}

// Two replicas deciding the same approval at once: one decision runs the
// rest of the flow, exactly once, and the other is refused.
func TestConcurrentDecisionsRunTheRestOnce(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	var c steps
	// The winner's send step waits until the other decision has returned,
	// so the two overlap.
	oneReturned := make(chan struct{})
	w.flows = func(*proc, string) (flow.Step, error) {
		send := flow.Func("send", func(ctx context.Context, st *flow.State) error {
			select {
			case <-oneReturned:
			case <-ctx.Done():
			case <-time.After(30 * time.Second): // only a runner that lets both decide gets here
			}
			return c.step("send").Run(ctx, st)
		})
		return flow.Sequence("f", c.step("draft"), flow.Approve("ok"), send), nil
	}
	evs, err := w.process().runFlow("c")
	if err != nil {
		t.Fatal(err)
	}
	id := lastDone(t, evs).Run.ID
	var once sync.Once
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		p := w.process()
		wg.Go(func() {
			x, err := p.r.Decide(w.ctx, id, "f/ok", flow.Decision{Approved: true})
			if err == nil {
				_, err = x.Wait()
			}
			errs[i] = err
			if err != nil {
				once.Do(func() { close(oneReturned) })
			}
		})
	}
	wg.Wait()
	once.Do(func() { close(oneReturned) })
	wins := 0
	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, store.ErrLeased), errors.Is(err, store.ErrFinal), errors.Is(err, runner.ErrNotPending):
		default:
			t.Errorf("losing Decide = %v, want ErrLeased, ErrFinal or ErrNotPending", err)
		}
	}
	if wins != 1 || c.counts()["send"] != 1 || w.get(id).Status != store.StatusCompleted {
		t.Errorf("%d decisions won, send ran %d times, run %s; want 1, 1, completed", wins, c.counts()["send"], w.get(id).Status)
	}
}
