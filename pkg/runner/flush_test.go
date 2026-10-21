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

func diff(t *testing.T, what string, want, got any) {
	t.Helper()
	if !sameJSON(want, got) {
		t.Errorf("%s = %s, want %s", what, js(got), js(want))
	}
}

// saves returns the positions of the EventSaved events of evs.
func saves(evs []runner.Event) []int64 {
	var out []int64
	for _, ev := range evs {
		if ev.Kind == runner.EventSaved {
			out = append(out, ev.Seq)
		}
	}
	return out
}

// A run whose tools only read keeps its turns and stores them in one write
// when it ends; FlushEvery(1) stores each as it completes. Either way the
// claim is not renewed at once.
func TestReadOnlyTurnsAreStoredTogether(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		opts   []runner.RunOption
		saves  []int64
		writes map[string]int
	}{
		// The run is created leased: no Claim (T099).
		{
			"default", nil,
			[]int64{1, 5},
			map[string]int{"Create": 1, "Append": 1, "AppendBatch": 1, "Transition": 1, "Release": 1},
		},
		{
			"every turn",
			[]runner.RunOption{runner.FlushEvery(1)},
			[]int64{1, 2, 3, 4, 5},
			map[string]int{"Create": 1, "Append": 5, "Transition": 1, "Release": 1},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			w.readOnly()
			p := w.process()
			x, err := p.r.Run(w.ctx, "c", harness.UserText(w.model.on("q", 3)), tc.opts...)
			if err != nil {
				t.Fatal(err)
			}
			evs, err := p.drain(x, nil)
			if err != nil {
				t.Fatal(err)
			}
			lastDone(t, evs)
			diff(t, "saved positions", tc.saves, saves(evs))
			diff(t, "writes", tc.writes, p.store.writes())
		})
	}
}

// Turns are stored when the run keeps FlushEvery of them, and the rest when
// it ends.
func TestTurnsAreStoredEveryN(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.readOnly()
	p := w.process(runner.WithFlushEvery(2))
	evs, err := p.run("c", w.model.on("q", 4)) // a prompt and 5 turns
	if err != nil {
		t.Fatal(err)
	}
	lastDone(t, evs)
	diff(t, "saved positions", []int64{1, 3, 5, 6}, saves(evs))
}

// A turn that called a tool which may have changed the world is stored
// before the model is called again, so a resume never calls that tool
// again; read-only turns wait.
func TestTurnsThatMayChangeTheWorldAreStoredBeforeTheNextModelCall(t *testing.T) {
	t.Parallel()
	for _, readOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "write", true: "read"}[readOnly], func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			if readOnly {
				w.readOnly()
			}
			p := w.process()
			var logs []int
			w.model.gate = func(context.Context, string, int) { logs = append(logs, len(w.log("c"))) }
			if _, err := p.run("c", w.model.on("q", 2)); err != nil {
				t.Fatal(err)
			}
			want := []int{1, 2, 3} // the prompt, and every turn so far
			if readOnly {
				want = []int{1, 1, 1}
			}
			diff(t, "entries stored at each model call", want, logs)
		})
	}
}

// A crash loses the turns not stored yet, and only those: the resumed run
// redoes them, and ends as an uninterrupted run would. A run started with
// FlushEvery keeps it when another process resumes it.
func TestCrashRedoesOnlyUnstoredTurns(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		opts []runner.RunOption
		// The crash, at a write that does not land.
		op string
		n  int
		// Model calls before the crash and after, and the resuming
		// process's writes of the log.
		before, after int
		writes        map[string]int
	}{
		// All 4 turns are kept until the end, and lost.
		{"default", nil, "AppendBatch", 1, 4, 4, map[string]int{"AppendBatch": 1}},
		// The prompt and the first turn are stored; the second is lost.
		{"every turn", []runner.RunOption{runner.FlushEvery(1)}, "Append", 3, 2, 3, map[string]int{"Append": 3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			w.readOnly()
			prompt := w.model.on("q", 3)
			dead := w.process()
			dead.store.crashAt(crashAtNth(tc.op, tc.n, false))
			x, err := dead.r.Run(w.ctx, "c", harness.UserText(prompt), tc.opts...)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := dead.drain(x, nil); !errors.Is(err, errCrashed) {
				t.Fatalf("crashed execution ended with %v", err)
			}
			if got := len(w.model.requestsFor(prompt)); got != tc.before {
				t.Fatalf("%d model calls before the crash, want %d", got, tc.before)
			}
			w.expire()
			p := w.process()
			evs, err := p.resume(x.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			done := lastDone(t, evs)
			if got := len(w.model.requestsFor(prompt)) - tc.before; got != tc.after {
				t.Errorf("%d model calls after the crash, want %d", got, tc.after)
			}
			log := p.store.writes()
			delete(log, "Claim")
			delete(log, "Renew")
			delete(log, "Transition")
			delete(log, "Release")
			diff(t, "writes of the log on resume", tc.writes, log)

			fresh := newWorld(t)
			fresh.readOnly()
			fresh.model.on("q", 3)
			want, err := fresh.process().run("c", prompt)
			if err != nil {
				t.Fatal(err)
			}
			if !sameJSON(done.Result.Transcript, lastDone(t, want).Result.Transcript) {
				t.Errorf("resumed transcript %s, uninterrupted %s", js(done.Result.Transcript), js(lastDone(t, want).Result.Transcript))
			}
		})
	}
}

// A consumer that stops keeps the turns done so far: they are stored, the
// run stays running, and its resume does only what is left.
func TestInterruptedRunStoresItsTurns(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.readOnly()
	prompt := w.model.on("q", 3)
	p := w.process()
	x, err := p.r.Run(w.ctx, "c", harness.UserText(prompt))
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.drain(x, func(ev runner.Event) bool {
		return ev.Harness.Kind == harness.EventTurnDone && ev.Harness.TurnRecord.Turn == 2
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(w.log("c")); got != 3 {
		t.Fatalf("log has %d entries after two turns, want 3", got)
	}
	if run := w.get(x.Run.ID); run.Status != store.StatusRunning {
		t.Fatalf("interrupted run is %s, want running", run.Status)
	}
	evs, err := p.resume(x.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	lastDone(t, evs)
	if got := len(w.model.requestsFor(prompt)); got != 4 {
		t.Errorf("%d model calls in all, want 4: the resume redid stored turns", got)
	}
}

// Shutdown stops the runner's executions, which store their turns and
// release their leases, and refuses new ones; another process continues
// the runs.
func TestShutdownStoresTurnsAndLeavesRunsToOthers(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.readOnly()
	prompt := w.model.on("q", 3)
	p := w.process()
	waiting := make(chan struct{})
	w.model.gate = func(ctx context.Context, _ string, turn int) {
		if turn == 2 {
			close(waiting)
			<-ctx.Done()
		}
	}
	x, err := p.r.Run(w.ctx, "c", harness.UserText(prompt))
	if err != nil {
		t.Fatal(err)
	}
	done := p.drainInBackground(x)
	<-waiting
	if err := p.r.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, runner.ErrShutdown) {
		t.Fatalf("execution ended with %v, want ErrShutdown", err)
	}
	if got := len(w.log("c")); got != 3 {
		t.Errorf("log has %d entries after two turns, want 3", got)
	}
	run := w.get(x.Run.ID)
	if run.Status != store.StatusRunning || run.Lease.Owner != "" {
		t.Errorf("run after shutdown = %+v, want running and free", run)
	}
	if _, err := p.r.Run(w.ctx, "d", harness.UserText(prompt)); !errors.Is(err, runner.ErrShutdown) {
		t.Errorf("Run after Shutdown: %v, want ErrShutdown", err)
	}
	if _, err := p.r.Resume(w.ctx, x.Run.ID); !errors.Is(err, runner.ErrShutdown) {
		t.Errorf("Resume after Shutdown: %v, want ErrShutdown", err)
	}
	w.model.gate = nil
	evs, err := w.process().resume(x.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	lastDone(t, evs)
}

func TestFlushEveryBelowOneIsRefused(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	if _, err := runner.New(w.mem, nil, runner.WithFlushEvery(0)); !errors.Is(err, runner.ErrInvalidConfig) {
		t.Errorf("WithFlushEvery(0): %v, want ErrInvalidConfig", err)
	}
	p := w.process()
	if _, err := p.r.Run(w.ctx, "c", harness.UserText("hi"), runner.FlushEvery(0)); !errors.Is(err, runner.ErrInvalidConfig) {
		t.Errorf("FlushEvery(0): %v, want ErrInvalidConfig", err)
	}
}

// An execution iterated late in its claim renews the lease before it runs,
// so nobody can take the run while it does; a fresh claim is not renewed.
func TestLateExecutionRenewsItsClaim(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	prompt := w.model.on("q", 0)
	p := w.process()
	x, err := p.r.Run(w.ctx, "c", harness.UserText(prompt))
	if err != nil {
		t.Fatal(err)
	}
	w.clk.Advance(ttl - time.Second)
	w.model.gate = func(context.Context, string, int) {
		// Past the claim, before the first renewal of the run.
		w.clk.Advance(2 * time.Second)
		if _, err := w.mem.Claim(w.ctx, x.Run.ID, "intruder", ttl); !errors.Is(err, store.ErrLeased) {
			t.Errorf("claim of a running run: %v, want ErrLeased", err)
		}
	}
	evs, err := p.drain(x, nil)
	if err != nil {
		t.Fatal(err)
	}
	lastDone(t, evs)
	if got := p.store.writes()["Renew"]; got != 1 {
		t.Errorf("%d renewals, want 1 at the start", got)
	}
}
