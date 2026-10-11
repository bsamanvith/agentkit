package flow_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/flow/flowtest"
)

func TestResumeRunsOnlyRemainingSteps(t *testing.T) {
	t.Parallel()
	var c calls
	cp := flow.NewMemory()
	seq := flow.Sequence("seq", trail(&c, "a"), trail(&c, "b"), failOnce(&c, "c"), trail(&c, "d"))
	seed := flow.NewState()
	mustSet(t, seed, "seed", "s")

	if _, err := run(t, cp, "r", seq, flow.WithState(seed)); !errors.Is(err, errBoom) {
		t.Fatalf("Run error = %v, want errBoom", err)
	}
	equalStrings(t, "first run", c.all(), []string{"a", "b", "c"})
	if got := load(t, cp, "r").Status; got != flow.StatusFailed {
		t.Errorf("status = %s, want failed", got)
	}

	st, err := resume(t, cp, "r", seq)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	// Only steps 3 and 4 ran again, and step 3 started from the state as it
	// was after step 2: its failed attempt's "c-partial" write is gone.
	equalStrings(t, "all runs", c.all(), []string{"a", "b", "c", "c", "d"})
	equalStrings(t, "trail", mustGet[[]string](t, st, "trail"), []string{"a", "b", "c", "d"})
	if mustGet[string](t, st, "seed") != "s" {
		t.Error("the initial state was lost on resume")
	}
	if got := load(t, cp, "r").Status; got != flow.StatusCompleted {
		t.Errorf("status = %s, want completed", got)
	}

	// A completed run resumes to its final state without running anything.
	again, err := resume(t, cp, "r", seq)
	if err != nil {
		t.Fatalf("second Resume: %v", err)
	}
	if len(c.all()) != 5 || !slices.Equal(mustGet[[]string](t, again, "trail"), []string{"a", "b", "c", "d"}) {
		t.Errorf("resuming a completed run ran steps %v", c.all())
	}
}

func TestResumeAfterCancellation(t *testing.T) {
	t.Parallel()
	var c calls
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cancelling := flow.Func("b", func(ctx context.Context, st *flow.State) error {
		c.add("b")
		if c.count("b") == 1 {
			cancel() // the process is told to stop in the middle of step b
			return ctx.Err()
		}
		return appendTrail(st, "b")
	})
	seq := flow.Sequence("seq", trail(&c, "a"), cancelling, trail(&c, "c"))
	cp := flow.NewMemory()

	_, err := flow.Run(ctx, "r", seq, flow.WithCheckpointer(cp))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	st, err := resume(t, cp, "r", seq)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	equalStrings(t, "all runs", c.all(), []string{"a", "b", "b", "c"})
	equalStrings(t, "trail", mustGet[[]string](t, st, "trail"), []string{"a", "b", "c"})
}

func TestApproveSuspendsThenResumeApproves(t *testing.T) {
	t.Parallel()
	var c calls
	cp := flow.NewMemory()
	f := flow.Sequence("flow", trail(&c, "before"), flow.Approve("ok"), trail(&c, "after"))

	_, err := run(t, cp, "r", f)
	if !errors.Is(err, flow.ErrSuspended) {
		t.Fatalf("Run error = %v, want ErrSuspended", err)
	}
	var susp *flow.SuspendedError
	if !errors.As(err, &susp) || !slices.Equal(susp.Paths, []string{"flow/ok"}) {
		t.Errorf("SuspendedError = %+v, want paths [flow/ok]", susp)
	}
	saved := load(t, cp, "r")
	if saved.Status != flow.StatusSuspended || !slices.Equal(saved.Pending, []string{"flow/ok"}) {
		t.Errorf("checkpoint = status %s pending %v, want suspended on flow/ok", saved.Status, saved.Pending)
	}

	// Resuming with no decision waits again; nothing runs.
	if _, err := resume(t, cp, "r", f); !errors.Is(err, flow.ErrSuspended) {
		t.Fatalf("Resume without a decision: error = %v, want ErrSuspended", err)
	}
	equalStrings(t, "steps run so far", c.all(), []string{"before"})

	st, err := resume(t, cp, "r", f, flow.WithDecision("flow/ok", flow.Decision{Approved: true}))
	if err != nil {
		t.Fatalf("Resume with approval: %v", err)
	}
	equalStrings(t, "all runs", c.all(), []string{"before", "after"})
	equalStrings(t, "trail", mustGet[[]string](t, st, "trail"), []string{"before", "after"})
}

func TestApproveRejectionEndsRun(t *testing.T) {
	t.Parallel()
	var c calls
	cp := flow.NewMemory()
	f := flow.Sequence("flow", trail(&c, "before"), flow.Approve("ok"), trail(&c, "after"))
	if _, err := run(t, cp, "r", f); !errors.Is(err, flow.ErrSuspended) {
		t.Fatalf("Run error = %v, want ErrSuspended", err)
	}

	_, err := resume(t, cp, "r", f, flow.WithDecision("flow/ok", flow.Decision{Reason: "too risky"}))
	if !errors.Is(err, flow.ErrRejected) {
		t.Fatalf("Resume error = %v, want ErrRejected", err)
	}
	var rej *flow.RejectedError
	if !errors.As(err, &rej) || rej.Path != "flow/ok" || rej.Reason != "too risky" {
		t.Errorf("RejectedError = %+v", rej)
	}
	if got := load(t, cp, "r").Status; got != flow.StatusRejected {
		t.Errorf("status = %s, want rejected", got)
	}

	// The rejection is final: resuming again, even with an approval, does not
	// ask again and does not run the rest.
	_, err = resume(t, cp, "r", f, flow.WithDecision("flow/ok", flow.Decision{Approved: true}))
	if !errors.Is(err, flow.ErrRejected) {
		t.Errorf("second Resume error = %v, want ErrRejected", err)
	}
	equalStrings(t, "steps run", c.all(), []string{"before"})
}

func TestParallelBranchSuspendsWithoutCancellingSiblings(t *testing.T) {
	t.Parallel()
	var c calls
	cp := flow.NewMemory()
	f := flow.Parallel("par", flow.Approve("ask"), trail(&c, "work"))

	_, err := run(t, cp, "r", f)
	var susp *flow.SuspendedError
	if !errors.As(err, &susp) || !slices.Equal(susp.Paths, []string{"par[0]/ask"}) {
		t.Fatalf("Run error = %v, want suspended on par[0]/ask", err)
	}
	if c.count("work") != 1 {
		t.Errorf("the sibling ran %d times, want it to finish", c.count("work"))
	}

	st, err := resume(t, cp, "r", f, flow.WithDecision("par[0]/ask", flow.Decision{Approved: true}))
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if c.count("work") != 1 {
		t.Errorf("the finished sibling ran again on resume: %v", c.all())
	}
	equalStrings(t, "trail", mustGet[[]string](t, st, "trail"), []string{"work"})
}

// TestNestedCombinatorsCheckpointAndResume runs a sequence inside a parallel
// inside a loop, fails in the second iteration, and checks that resuming runs
// only what was unfinished and ends where an uninterrupted run ends.
func TestNestedCombinatorsCheckpointAndResume(t *testing.T) {
	t.Parallel()
	sideSaved := make(chan struct{})
	build := func(c *calls, failB bool) flow.Step {
		a := flow.Func("a", func(_ context.Context, st *flow.State) error {
			c.add("a")
			n, _ := flow.Get[int](st, "a-runs")
			return flow.Set(st, "a-runs", n+1)
		})
		b := flow.Func("b", func(_ context.Context, st *flow.State) error {
			c.add("b")
			n, _ := flow.Get[int](st, "n")
			return flow.Set(st, "n", n+1)
		})
		if failB {
			failing := false
			inner := b
			b = flow.Func("b", func(ctx context.Context, st *flow.State) error {
				n, _ := flow.Get[int](st, "n")
				if n == 1 && !failing { // second iteration, first attempt
					failing = true
					c.add("b")
					select { // let the sibling finish and checkpoint first, so what ran is exact
					case <-sideSaved:
					case <-time.After(10 * time.Second):
						return errors.New("the sibling never finished")
					}
					return errBoom
				}
				return inner.Run(ctx, st)
			})
		}
		side := flow.Func("side", func(_ context.Context, st *flow.State) error {
			c.add("side")
			n, _ := flow.Get[int](st, "n")
			sides, _ := flow.Get[[]int](st, "sides")
			return flow.Set(st, "sides", append(sides, n))
		})
		fan := flow.Parallel("fan", flow.Sequence("pipe", a, b), side)
		return flow.Loop("outer", fan, func(st *flow.State) (bool, error) {
			n, _ := flow.Get[int](st, "n")
			return n >= 3, nil
		}, 10)
	}

	var clean calls
	want, err := run(t, flow.NewMemory(), "clean", build(&clean, false))
	if err != nil {
		t.Fatalf("uninterrupted Run: %v", err)
	}

	var c calls
	cp := flow.NewMemory()
	flowTree := build(&c, true)
	observeSide := func(e flow.Event) {
		if e.Kind == flow.EventFinished && e.Path == "outer[1]/fan[1]/side" {
			close(sideSaved)
		}
	}
	if _, err := run(t, cp, "r", flowTree, flow.WithObserver(observeSide)); !errors.Is(err, errBoom) {
		t.Fatalf("Run error = %v, want errBoom", err)
	}
	// Branches run concurrently, so compare how often each step ran.
	counts := func(c *calls) [3]int { return [3]int{c.count("a"), c.count("b"), c.count("side")} }
	if got, want := counts(&c), [3]int{2, 2, 2}; got != want {
		t.Fatalf("before the failure (a, b, side) ran %v times, want %v", got, want)
	}

	got, err := resume(t, cp, "r", flowTree)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	// Iteration 0 is skipped whole; in iteration 1 "a" and "side" are skipped
	// (they had completed) and only "b" runs again; iteration 2 runs fresh.
	if gotN, want := counts(&c), [3]int{3, 4, 3}; gotN != want {
		t.Errorf("after Resume (a, b, side) ran %v times, want %v", gotN, want)
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("resumed state = %s, want the uninterrupted run's %s", gotJSON, wantJSON)
	}
}

func TestCompletedStepsDropTheirChildRecords(t *testing.T) {
	t.Parallel()
	var c calls
	cp := flow.NewMemory()
	var midRun flow.Checkpoint
	probe := flow.Func("probe", func(ctx context.Context, _ *flow.State) error {
		var err error
		midRun, err = cp.Load(ctx, "r")
		return err
	})
	f := flow.Sequence("outer",
		flow.Sequence("inner", trail(&c, "a"), trail(&c, "b")),
		probe)
	if _, err := run(t, cp, "r", f); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// While probe runs, inner has completed: its own record stands in for a and b.
	keys := stepKeys(midRun)
	if want := []string{"outer/inner"}; !slices.Equal(keys, want) {
		t.Errorf("records mid-run = %v, want %v", keys, want)
	}
	if keys := stepKeys(load(t, cp, "r")); !slices.Equal(keys, []string{"outer"}) {
		t.Errorf("records after the run = %v, want only the root", keys)
	}
}

func stepKeys(c flow.Checkpoint) []string {
	var keys []string
	for k := range c.Steps {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func TestEventsReportPathsAndOutcomes(t *testing.T) {
	t.Parallel()
	var c calls
	var rec flowtest.Recorder
	cp := flow.NewMemory()
	fail := flow.Func("b", func(context.Context, *flow.State) error { return errBoom })

	_, err := run(t, cp, "ok", flow.Sequence("main", trail(&c, "a"), trail(&c, "b")), flow.WithObserver(rec.Observe))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	equalStrings(t, "events of a successful run", rec.Summary(), []string{
		"started main", "started main/a", "finished main/a", "started main/b", "finished main/b", "finished main",
	})

	rec = flowtest.Recorder{}
	if _, err := run(t, cp, "fail", flow.Sequence("main", trail(&c, "a"), fail, trail(&c, "c")), flow.WithObserver(rec.Observe)); err == nil {
		t.Fatal("Run succeeded")
	}
	equalStrings(t, "events of a failing run", rec.Summary(), []string{
		"started main", "started main/a", "finished main/a", "started main/b", "failed main/b", "failed main",
	})
	if last := rec.Events()[4]; !errors.Is(last.Err, errBoom) {
		t.Errorf("failed event carries error %v, want errBoom", last.Err)
	}

	rec = flowtest.Recorder{}
	if _, err := run(t, cp, "ask", flow.Sequence("main", flow.Approve("ok")), flow.WithObserver(rec.Observe)); !errors.Is(err, flow.ErrSuspended) {
		t.Fatalf("Run error = %v, want ErrSuspended", err)
	}
	equalStrings(t, "events of a suspended run", rec.Summary(), []string{
		"started main", "started main/ok", "suspended main/ok", "suspended main",
	})

	// Parallel branches interleave, but each path's events stay in order.
	rec = flowtest.Recorder{}
	noop := func(name string) flow.Step {
		return flow.Func(name, func(context.Context, *flow.State) error { return nil })
	}
	par := flow.Sequence("main", flow.Parallel("par", noop("x"), noop("y")))
	if _, err := run(t, cp, "par", par, flow.WithObserver(rec.Observe)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, path := range []string{"main", "main/par", "main/par[0]/x", "main/par[1]/y"} {
		var kinds []string
		for _, e := range rec.Events() {
			if e.Path == path {
				kinds = append(kinds, string(e.Kind))
			}
		}
		equalStrings(t, "events of "+path, kinds, []string{"started", "finished"})
	}
	if n := len(rec.Events()); n != 8 {
		t.Errorf("got %d events, want 8: %v", n, rec.Summary())
	}
}

func TestPathsAreDeterministicUnderScheduling(t *testing.T) {
	t.Parallel()
	var want []string
	for _, order := range [][]int{{0, 1, 2}, {2, 1, 0}, {1, 2, 0}} {
		branches := finishInOrder(order, func(int, *flow.State) error { return nil })
		var paths []string
		var rec flowtest.Recorder
		f := flow.Parallel("par", branches...)
		if _, err := run(t, flow.NewMemory(), "r", f, flow.WithObserver(rec.Observe)); err != nil {
			t.Fatalf("order %v: Run: %v", order, err)
		}
		for _, e := range rec.Events() {
			if e.Kind == flow.EventStarted {
				paths = append(paths, e.Path)
			}
		}
		slices.Sort(paths)
		if want == nil {
			want = paths
		}
		equalStrings(t, fmt.Sprintf("paths for finish order %v", order), paths, want)
	}
	equalStrings(t, "paths", want, []string{"par", "par[0]/b0", "par[1]/b1", "par[2]/b2"})
}

func TestCheckpointIsSavedAfterTheStepHasFinishedChangingState(t *testing.T) {
	t.Parallel()
	cp := flow.NewMemory()
	var atFinish []string
	var failedStepHadRecord bool
	obs := func(e flow.Event) {
		switch {
		case e.Kind == flow.EventFinished && e.Path == "main/a":
			// By the time a step is reported finished, its final state is
			// already durable.
			st := flow.NewState()
			if err := json.Unmarshal(load(t, cp, "r").Steps["main/a"], st); err == nil {
				atFinish, _ = flow.Get[[]string](st, "trail")
			}
		case e.Kind == flow.EventFailed && e.Path == "main/b":
			_, failedStepHadRecord = load(t, cp, "r").Steps["main/b"]
		}
	}
	var c calls
	f := flow.Sequence("main", trail(&c, "a"), failOnce(&c, "b"))
	if _, err := run(t, cp, "r", f, flow.WithObserver(obs)); !errors.Is(err, errBoom) {
		t.Fatalf("Run error = %v, want errBoom", err)
	}
	equalStrings(t, "state saved for main/a", atFinish, []string{"a"})
	if failedStepHadRecord {
		t.Error("a step that failed has a checkpoint record")
	}
	if rec := load(t, cp, "r").Steps["main/b"]; rec != nil {
		t.Errorf("the failed step's partial state was checkpointed: %s", rec)
	}
}
