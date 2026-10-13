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
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

func (w *world) recovery(p *proc, opts ...runner.RecoveryOption) *runner.Recovery {
	w.t.Helper()
	rc, err := runner.NewRecovery(p.r, memory.Admin(w.mem), opts...)
	if err != nil {
		w.t.Fatal(err)
	}
	return rc
}

// reference returns the requests an uninterrupted chat run of the scripted
// prompt name makes.
func reference(t *testing.T, name string, tools int, setup ...func(*world)) []llm.Request {
	t.Helper()
	w := newWorld(t)
	for _, f := range setup {
		f(w)
	}
	prompt := w.model.on(name, tools)
	if _, err := w.process().run("c", prompt); err != nil {
		t.Fatal(err)
	}
	return w.model.requestsFor(prompt)
}

// killPoint is where a process dies during a chat run of two tool calls,
// whose log is a prompt, two tool turns and the answer.
type killPoint struct {
	name    string
	op      string
	n       int
	landed  bool
	started bool // the prompt is stored, so the run continues
}

var killPoints = []killPoint{
	{"before the prompt is stored", "Append", 1, false, false},
	{"after the prompt", "Append", 1, true, true},
	{"after the first turn", "Append", 2, true, true},
	{"losing the second turn", "Append", 3, false, true},
	{"after the answer", "Append", 4, true, true},
	{"before completing", "Transition", 1, false, true},
	{"after completing", "Transition", 1, true, true},
	{"releasing the lease", "Release", 1, false, true},
}

// readOnlyKillPoints are where a process dies during the same run when its
// tool is read-only: the run keeps its turns and stores them in one write.
var readOnlyKillPoints = []killPoint{
	{"losing the kept turns", "AppendBatch", 1, false, true},
	{"after the kept turns", "AppendBatch", 1, true, true},
}

// crashRun runs prompt in conv on a process that dies at k.
func (w *world) crashRun(ctx context.Context, conv, prompt string, k killPoint) string {
	w.t.Helper()
	dead := w.process()
	dead.store.crashAt(crashAtNth(k.op, k.n, k.landed))
	x, err := dead.r.Run(ctx, conv, harness.UserText(prompt))
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := x.Wait(); k.op != "Release" && !errors.Is(err, errCrashed) {
		w.t.Fatalf("run dying %s ended with %v", k.name, err)
	}
	return x.Run.ID
}

// storedTurns counts a conversation's turn entries.
func storedTurns(ctx context.Context, t *testing.T, cs store.ConversationStore, conv string) int {
	t.Helper()
	page, err := cs.History(ctx, conv, store.Page{Order: store.Ascending, Limit: store.MaxPageLimit})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range page.Entries {
		if e.Kind == store.KindTurn {
			n++
		}
	}
	return n
}

// A run whose process died anywhere is resumed by recovery, once, and makes
// exactly the requests an uninterrupted run would have made after what was
// stored; a run that died before its prompt was stored is canceled as not
// started.
func TestRecoveryResumesARunKilledAnywhere(t *testing.T) {
	t.Parallel()
	refWrite, refRead := reference(t, "q", 2), reference(t, "q", 2, (*world).readOnly)
	readOnly := map[string]bool{}
	for _, k := range readOnlyKillPoints {
		readOnly[k.name] = true
	}
	for _, k := range slices.Concat(killPoints, readOnlyKillPoints) {
		t.Run(k.name, func(t *testing.T) {
			t.Parallel()
			w, ref := newWorld(t), refWrite
			if readOnly[k.name] {
				w.readOnly()
				ref = refRead
			}
			prompt := w.model.on("q", 2)
			id := w.crashRun(w.ctx, "c", prompt, k)
			stored := storedTurns(w.ctx, t, w.mem, "c")
			finished := w.get(id).Status == store.StatusCompleted
			before := len(w.model.Requests())

			p := w.process()
			rc := w.recovery(p)
			// Within its lease the dead process's run is left alone.
			if rep, err := rc.Recover(w.ctx); err != nil || rep.Resumed != 0 {
				t.Fatalf("Recover within the lease = %+v, %v; want nothing resumed", rep, err)
			}
			w.expire()
			rep, err := rc.Recover(w.ctx)
			if err != nil {
				t.Fatal(err)
			}
			run := w.get(id)
			got := w.model.Requests()[before:]
			switch {
			case !k.started:
				if rep.Resumed != 1 || run.Status != store.StatusCanceled || run.Error != runner.ErrNotStarted.Error() || len(w.log("c")) != 0 || len(got) != 0 {
					t.Fatalf("report %+v, run %+v, log %s, requests %d; want it canceled as not started", rep, run, js(w.log("c")), len(got))
				}
				return
			case finished:
				if rep.Resumed != 0 || run.Status != store.StatusCompleted || len(got) != 0 {
					t.Fatalf("report %+v, run %+v; want the completed run left alone", rep, run)
				}
			default:
				if rep.Resumed != 1 || run.Status != store.StatusCompleted {
					t.Fatalf("report %+v, run %+v; want it resumed to completion", rep, run)
				}
			}
			if !sameJSON(got, ref[min(stored, len(ref)):]) {
				t.Errorf("with %d turns stored, recovery made %s; the uninterrupted run's rest is %s", stored, js(got), js(ref[min(stored, len(ref)):]))
			}
			if rep, err := rc.Recover(w.ctx); err != nil || rep != (runner.Report{}) {
				t.Errorf("a second pass = %+v, %v; want nothing to do", rep, err)
			}
			w.checkCache(p, "c")
		})
	}
}

// Two replicas recovering at once resume every run exactly once, each in its
// own tenant.
func TestTwoReplicasRecoverEachRunOnce(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	globex, err := tenancy.NewTenant("globex")
	if err != nil {
		t.Fatal(err)
	}
	gctx := tenancy.With(context.Background(), globex)
	type crashed struct {
		ctx    context.Context
		name   string
		id     string
		stored int
		ref    []llm.Request
	}
	var runs []crashed
	for i, k := range killPoints {
		if !k.started {
			continue
		}
		ctx := w.ctx
		if i%2 == 1 {
			ctx = gctx
		}
		name := fmt.Sprint("r", i)
		id := w.crashRun(ctx, "c"+name, w.model.on(name, 2), k)
		if run, err := w.mem.Get(ctx, id); err != nil || run.Status == store.StatusCompleted {
			continue
		}
		runs = append(runs, crashed{ctx, name, id, storedTurns(ctx, t, w.mem, "c"+name), reference(t, name, 2)})
	}
	w.expire()
	before := len(w.model.Requests())

	reps := make([]runner.Report, 2)
	var wg sync.WaitGroup
	for i := range reps {
		rc := w.recovery(w.process(), runner.WithWorkers(2))
		wg.Go(func() {
			var err error
			if reps[i], err = rc.Recover(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if total := reps[0].Resumed + reps[1].Resumed; total != len(runs) || reps[0].Errors+reps[1].Errors != 0 {
		t.Fatalf("reports %+v, want %d runs resumed between them", reps, len(runs))
	}
	all := w.model.Requests()[before:]
	for _, c := range runs {
		run, err := w.mem.Get(c.ctx, c.id)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != store.StatusCompleted {
			t.Errorf("run %q is %s", c.id, run.Status)
		}
		got := []llm.Request{}
		for _, r := range all {
			if p, _ := promptOf(r.Messages); p == "q:"+c.name {
				got = append(got, r)
			}
		}
		if want := c.ref[c.stored:]; !sameJSON(got, want) {
			t.Errorf("run %q with %d turns stored: recovery made %d requests, want %d (%s)", c.id, c.stored, len(got), len(want), js(got))
		}
	}
}

// A retryable failure is tried again by recovery once its delay is up, with
// delays that grow, until it succeeds.
func TestRetryableFailureIsRetriedWithGrowingDelays(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	p := w.process()
	rc := w.recovery(p)
	prompt := w.model.on("q", 1)
	w.model.failing("q", 1, 2, fmt.Errorf("%w: scripted", llm.ErrOverloaded))
	_, err := p.run("c", prompt)
	var re *runner.RunError
	if !errors.As(err, &re) || !errors.Is(err, llm.ErrOverloaded) {
		t.Fatalf("first attempt = %v, want a RunError for the overload", err)
	}
	id := re.Run.ID
	policy := runner.DefaultRetryPolicy()
	var last time.Duration
	for n := 1; n <= 2; n++ {
		run := w.get(id)
		if run.Status != store.StatusFailed || !run.Retryable || run.Attempts != n {
			t.Fatalf("after failure %d: %+v, want failed, retryable, %d attempts", n, run, n)
		}
		d := run.NextAttemptAt.Sub(w.clk.Now())
		full := min(policy.BaseDelay<<(n-1), policy.MaxDelay)
		if d < full/2 || d > full || d < last {
			t.Errorf("delay after failure %d = %v, want in [%v, %v] and at least %v", n, d, full/2, full, last)
		}
		last = d
		w.clk.Advance(d - time.Nanosecond)
		if rep, err := rc.Recover(w.ctx); err != nil || rep.Resumed != 0 {
			t.Fatalf("Recover before the retry is due = %+v, %v", rep, err)
		}
		w.clk.Advance(time.Nanosecond)
		if rep, err := rc.Recover(w.ctx); err != nil || rep.Resumed != 1 {
			t.Fatalf("Recover when the retry is due = %+v, %v", rep, err)
		}
	}
	if run := w.get(id); run.Status != store.StatusCompleted || run.Attempts != 2 {
		t.Fatalf("after two retries: %+v, want completed", run)
	}
	// The stored turn was not repeated: one request for the first turn, three
	// for the second.
	if got := len(w.model.requestsFor(prompt)); got != 4 {
		t.Errorf("%d requests, want 4", got)
	}
	if !sameJSON(w.transcript("c"), referenceTranscript(t, "q", 1)) {
		t.Errorf("retried transcript differs from an uninterrupted run's")
	}
}

func referenceTranscript(t *testing.T, name string, tools int) harness.Transcript {
	t.Helper()
	w := newWorld(t)
	if _, err := w.process().run("c", w.model.on(name, tools)); err != nil {
		t.Fatal(err)
	}
	return w.transcript("c")
}

func TestRetryStopsAtMaxAttempts(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	p := w.process(runner.WithRetry(runner.RetryPolicy{MaxAttempts: 3, BaseDelay: time.Second, MaxDelay: time.Minute}))
	rc := w.recovery(p)
	prompt := w.model.on("q", 0)
	w.model.failing("q", 0, 10, fmt.Errorf("%w: scripted", llm.ErrRateLimited))
	if _, err := p.run("c", prompt); err == nil {
		t.Fatal("failing run succeeded")
	}
	for range 5 {
		w.clk.Advance(time.Hour)
		if _, err := rc.Recover(w.ctx); err != nil {
			t.Fatal(err)
		}
	}
	run := w.get("run1")
	if run.Status != store.StatusFailed || run.Retryable || run.Attempts != 3 {
		t.Errorf("run = %+v, want failed for good after 3 attempts", run)
	}
	if got := len(w.model.requestsFor(prompt)); got != 3 {
		t.Errorf("%d requests, want 3", got)
	}
	// An operator may still resume it.
	if _, err := p.resume(run.ID); err == nil {
		t.Errorf("operator's resume succeeded against a model that still fails")
	}
	if run := w.get(run.ID); run.Attempts != 4 || run.Retryable {
		t.Errorf("after the operator's resume: %+v", run)
	}
}

func TestNonRetryableErrorsAreNeverRetried(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name     string
		err      error
		requests int
	}{
		{"invalid request", llm.ErrInvalidRequest, 2},
		{"context too long", llm.ErrContextTooLong, 2},
		{"auth", llm.ErrAuth, 2},
		{"budget", harness.ErrBudgetExceeded, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			prompt := w.model.on("q", 2)
			if c.name == "budget" {
				w.agent = func(string) runner.Agent {
					h, err := harness.New(w.model, w.tools, harness.WithNow(w.clk.Now), harness.WithMaxTurns(1))
					if err != nil {
						t.Fatal(err)
					}
					return h
				}
			} else {
				w.model.failing("q", 1, 1, fmt.Errorf("%w: scripted", c.err))
			}
			p := w.process()
			_, err := p.run("c", prompt)
			if !errors.Is(err, c.err) {
				t.Fatalf("run = %v, want %v", err, c.err)
			}
			w.clk.Advance(24 * time.Hour)
			if rep, err := w.recovery(p).Recover(w.ctx); err != nil || rep.Resumed != 0 {
				t.Errorf("Recover = %+v, %v; want nothing resumed", rep, err)
			}
			if run := w.get("run1"); run.Status != store.StatusFailed || run.Retryable || run.Attempts != 1 {
				t.Errorf("run = %+v, want failed, not retryable", run)
			}
			if got := len(w.model.requestsFor(prompt)); got != c.requests {
				t.Errorf("%d requests, want %d", got, c.requests)
			}
		})
	}
}

// The loop makes a pass at once and then one per interval of the runner's
// clock, and returns when its context ends.
func TestRecoveryLoopRunsEveryInterval(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	p := w.process()
	prompt := w.model.on("q", 0)
	w.model.failing("q", 0, 1, fmt.Errorf("%w: scripted", llm.ErrRateLimited))
	if _, err := p.run("c", prompt); err == nil {
		t.Fatal("failing run succeeded")
	}
	ctx, cancel := context.WithCancel(w.ctx)
	reports := make(chan runner.Report)
	done := make(chan error, 1)
	rc := w.recovery(p, runner.WithInterval(time.Hour))
	go func() {
		done <- rc.Run(ctx, func(rep runner.Report, err error) {
			if err != nil {
				t.Error(err)
			}
			reports <- rep
		})
	}()
	if rep := <-reports; rep.Resumed != 0 {
		t.Fatalf("first pass = %+v, before the retry is due", rep)
	}
	if err := w.clk.WaitForTimers(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	w.clk.Advance(time.Hour)
	if rep := <-reports; rep.Resumed != 1 {
		t.Fatalf("second pass = %+v, want the due retry resumed", rep)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v after its context ended", err)
	}
	if run := w.get("run1"); run.Status != store.StatusCompleted {
		t.Errorf("run = %+v", run)
	}
}

// Recovery leaves a new run, between its creation and its creator's claim,
// to its creator.
func TestRecoveryLeavesANewRunToItsCreator(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	p := w.process()
	rc := w.recovery(w.process())
	var rep runner.Report
	p.store.before = func(op string, n int) {
		if op == "Claim" && n == 1 {
			var err error
			if rep, err = rc.Recover(w.ctx); err != nil {
				t.Error(err)
			}
		}
	}
	evs, err := p.run("c", w.model.on("q", 1))
	if err != nil {
		t.Fatalf("creator's run = %v", err)
	}
	if lastDone(t, evs).Run.Status != store.StatusCompleted || rep.Resumed != 0 || rep.Skipped != 1 {
		t.Errorf("creator's run %+v; recovery %+v, want the run skipped", lastDone(t, evs).Run, rep)
	}
}

// A caller may resume a dead process's run itself instead of waiting for
// recovery: Resume is refused while the dead lease holds, and afterwards
// makes exactly the requests an uninterrupted run makes after the stored
// turns.
func TestDeadProcessRunIsResumedByHandAfterItsLease(t *testing.T) {
	t.Parallel()
	const tools = 3
	ref := newWorld(t)
	prompt := ref.model.on("q", tools)
	if _, err := ref.process().run("c", prompt); err != nil {
		t.Fatal(err)
	}
	want := ref.model.requestsFor(prompt)
	for k := 2; k <= tools+1; k++ { // dies at append k: the prompt and k-2 turns are stored
		t.Run(fmt.Sprint(k-2, " turns stored"), func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			w.model.on("q", tools)
			dead := w.process()
			dead.store.crashAt(crashAtNth("Append", k, false))
			x, err := dead.r.Run(w.ctx, "c", harness.UserText(prompt))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := x.Wait(); !errors.Is(err, errCrashed) {
				t.Fatalf("crashed run ended with %v", err)
			}
			p := w.process()
			if _, err := p.r.Resume(w.ctx, x.Run.ID); !errors.Is(err, store.ErrLeased) {
				t.Fatalf("Resume while the dead process's lease holds = %v, want ErrLeased", err)
			}
			w.expire()
			before := len(w.model.requestsFor(prompt))
			evs, err := p.resume(x.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if run := lastDone(t, evs).Run; run.Status != store.StatusCompleted {
				t.Fatalf("resumed run = %+v, want completed", run)
			}
			if got := w.model.requestsFor(prompt)[before:]; !sameJSON(got, want[k-2:]) {
				t.Errorf("requests of the resumed run:\n%s\nuninterrupted run after %d turns:\n%s", js(got), k-2, js(want[k-2:]))
			}
		})
	}
}
