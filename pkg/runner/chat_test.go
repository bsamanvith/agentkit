package runner_test

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

func TestRunStoresThePromptAndEveryTurn(t *testing.T) {
	t.Parallel()
	for _, tools := range []int{0, 1, 4} {
		t.Run(fmt.Sprint(tools, "tools"), func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			p := w.process()
			first := w.model.on("first", tools)
			evs, err := p.run("c", first)
			if err != nil {
				t.Fatal(err)
			}
			done := lastDone(t, evs)
			log := w.log("c")
			if len(log) != 1+tools+1 || log[0].Kind != store.KindPrompt {
				t.Fatalf("log has %d entries (first %s), want a prompt and %d turns", len(log), log[0].Kind, tools+1)
			}
			for _, e := range log[1:] {
				if e.Kind != store.KindTurn || e.RunID != done.RunID {
					t.Errorf("entry %d: kind %s run %q, want a turn of %q", e.Seq, e.Kind, e.RunID, done.RunID)
				}
			}
			if !sameJSON(w.transcript("c"), done.Result.Transcript) {
				t.Errorf("stored context %s, Result transcript %s", js(w.transcript("c")), js(done.Result.Transcript))
			}
			if done.Run.Status != store.StatusCompleted || done.Run.Usage != done.Result.Usage {
				t.Errorf("run = %+v, want completed with usage %+v", done.Run, done.Result.Usage)
			}

			// A follow-up continues the same log.
			evs, err = p.run("c", w.model.on("second", 1))
			if err != nil {
				t.Fatal(err)
			}
			if got := lastDone(t, evs).Result.Transcript; !sameJSON(got, w.transcript("c")) || len(w.log("c")) != len(log)+3 {
				t.Errorf("after a follow-up, log has %d entries and context %s; Result %s", len(w.log("c")), js(w.transcript("c")), js(got))
			}
		})
	}
}

// interrupt runs a chat whose consumer stops at the k-th turn event (k = 0:
// at the run start) and resumes it in a new process. The resumed run must
// make exactly the requests an uninterrupted run makes after the first k.
func TestStoppedRunResumesWithTheSameRequests(t *testing.T) {
	t.Parallel()
	const tools = 3
	ref := newWorld(t)
	prompt := ref.model.on("q", tools)
	if _, err := ref.process().run("c", prompt); err != nil {
		t.Fatal(err)
	}
	want := ref.model.requestsFor(prompt)

	for k := 0; k <= tools; k++ {
		t.Run(fmt.Sprint("after turn ", k), func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			w.model.on("q", tools)
			p := w.process()
			x, err := p.r.Run(w.ctx, "c", harness.UserText(prompt))
			if err != nil {
				t.Fatal(err)
			}
			turns := 0
			_, err = p.drain(x, func(ev runner.Event) bool {
				if ev.Kind == runner.EventHarness && ev.Harness.Kind == harness.EventTurnDone {
					turns++
				}
				return k == 0 && ev.Harness.Kind == harness.EventRunStart || k > 0 && turns == k
			})
			if err != nil {
				t.Fatalf("a consumer that stops ends without an error, got %v", err)
			}
			if got := len(w.log("c")); got != 1+k {
				t.Fatalf("after stopping at turn %d the log has %d entries, want %d", k, got, 1+k)
			}
			if run := w.get(x.Run.ID); run.Status != store.StatusRunning || run.Lease != (store.Lease{}) {
				t.Fatalf("stopped run = %+v, want running with no lease", run)
			}
			evs, err := w.process().resume(x.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if lastDone(t, evs).Run.Status != store.StatusCompleted {
				t.Fatalf("resumed run did not complete")
			}
			if got := w.model.requestsFor(prompt); !sameJSON(got, want) {
				t.Errorf("requests of the stopped and resumed run:\n%s\nuninterrupted:\n%s", js(got), js(want))
			}
			if !sameJSON(w.transcript("c"), ref.transcript("c")) {
				t.Errorf("transcript %s, uninterrupted %s", js(w.transcript("c")), js(ref.transcript("c")))
			}
		})
	}
}

func TestOneRunnerHoldsTheLease(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	a, b := w.process(), w.process()
	prompt := w.model.on("q", 1)
	x, err := a.r.Run(w.ctx, "c", harness.UserText(prompt))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.r.Resume(w.ctx, x.Run.ID); !errors.Is(err, store.ErrLeased) {
		t.Fatalf("second runner's Resume = %v, want ErrLeased", err)
	}
	if _, err := b.r.Run(w.ctx, "c", harness.UserText(prompt)); !errors.Is(err, runner.ErrRunUnfinished) {
		t.Fatalf("second runner's Run = %v, want ErrRunUnfinished", err)
	}
	if _, err := a.drain(x, nil); err != nil {
		t.Fatal(err)
	}
}

// The lease is renewed while a run waits on the model longer than its time
// to live: another runner cannot take it over meanwhile.
func TestLeaseIsRenewedDuringALongTurn(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	a, b := w.process(), w.process()
	prompt := w.model.on("q", 0)
	waiting, release := make(chan struct{}), make(chan struct{})
	w.model.gate = func(ctx context.Context, _ string, _ int) {
		close(waiting)
		<-release
	}
	x, err := a.r.Run(w.ctx, "c", harness.UserText(prompt))
	if err != nil {
		t.Fatal(err)
	}
	done := a.drainInBackground(x)
	<-waiting
	// The renewer re-arms its timer after each renewal, so a pending timer
	// after an advance means the renewal is stored.
	for i := range 7 { // two lease times, one renewal at a time
		if err := w.clk.WaitForTimers(t.Context(), 1); err != nil {
			t.Fatal(err)
		}
		if i > 0 && !w.get(x.Run.ID).Lease.Expires.After(w.clk.Now().Add(ttl/2)) {
			t.Fatalf("lease not renewed after %d renewal times: %+v at %v", i, w.get(x.Run.ID).Lease, w.clk.Now())
		}
		if i < 6 {
			w.clk.Advance(ttl / 3)
		}
	}
	if _, err := b.r.Resume(w.ctx, x.Run.ID); !errors.Is(err, store.ErrLeased) {
		t.Errorf("after two lease times, another runner's Resume = %v, want ErrLeased", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// A writer whose view of the log is stale (another writer appended behind its
// back) stops at the conflict, writes nothing over the other entry, and
// reports it.
func TestStaleWriterStopsWithoutOverwriting(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	p := w.process()
	prompt := w.model.on("q", 2)
	waiting, release := make(chan struct{}), make(chan struct{})
	w.model.gate = func(_ context.Context, _ string, turn int) {
		if turn == 1 {
			close(waiting)
			<-release
		}
	}
	x, err := p.r.Run(w.ctx, "c", harness.UserText(prompt))
	if err != nil {
		t.Fatal(err)
	}
	done := p.drainInBackground(x)
	<-waiting
	other := store.Entry{Seq: 3, Kind: store.KindTurn, Messages: []llm.Message{llm.Assistant(llm.Text{Text: "the other writer"})}, RunID: "other"}
	if err := w.mem.Append(w.ctx, "c", other); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale writer's run ended with %v, want ErrConflict", err)
	}
	log := w.log("c")
	if len(log) != 3 || !sameJSON(log[2].Messages, other.Messages) {
		t.Errorf("log after the conflict = %s, want the other writer's entry last", js(log))
	}
	if run := w.get(x.Run.ID); run.Status != store.StatusRunning {
		t.Errorf("stale writer moved the run to %s; it belongs to whoever wrote", run.Status)
	}
}

func TestCacheNeverCrossesTenantsOrConversations(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	p := w.process(runner.WithCacheSize(2))
	globex, err := tenancy.NewTenant("globex")
	if err != nil {
		t.Fatal(err)
	}
	other := tenancy.With(context.Background(), globex)
	// The same conversation id in two tenants, and a third conversation, so
	// that the cache of two evicts.
	for i, c := range []struct {
		ctx  context.Context
		conv string
	}{{w.ctx, "c"}, {other, "c"}, {w.ctx, "d"}, {w.ctx, "c"}, {other, "c"}} {
		x, err := p.r.Run(c.ctx, c.conv, harness.UserText(w.model.on(fmt.Sprint(i), i%2)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := x.Wait(); err != nil {
			t.Fatal(err)
		}
		for _, d := range []struct {
			ctx  context.Context
			conv string
		}{{w.ctx, "c"}, {other, "c"}, {w.ctx, "d"}} {
			got, seq, err := p.r.Context(d.ctx, d.conv)
			if err != nil {
				t.Fatal(err)
			}
			cv, err := w.mem.Context(d.ctx, d.conv)
			if err != nil {
				t.Fatal(err)
			}
			if seq != cv.Seq || !sameJSON(got, messagesOf(cv.Entries)) {
				t.Errorf("after run %d, cached %q = %s at %d; cold %s at %d", i, d.conv, js(got), seq, js(messagesOf(cv.Entries)), cv.Seq)
			}
		}
	}
	// Another writer appends behind the cache's back: the next read sees it.
	cv, err := w.mem.Context(w.ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	e := store.Entry{Seq: cv.Seq + 1, Kind: store.KindPrompt, Messages: []llm.Message{harness.UserText("more")}}
	if err := w.mem.Append(w.ctx, "c", e); err != nil {
		t.Fatal(err)
	}
	w.checkCache(p, "c")
	if _, _, err := p.r.Context(context.Background(), "c"); !errors.Is(err, tenancy.ErrNoTenant) {
		t.Errorf("Context with no tenant = %v, want ErrNoTenant", err)
	}
}

func TestNewMessageWaitsForTheUnfinishedRun(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	p := w.process()
	bad := w.model.on("bad", 0)
	w.model.failing("bad", 0, 1, fmt.Errorf("%w: scripted", llm.ErrInvalidRequest))
	if _, err := p.run("c", bad); err == nil {
		t.Fatal("run with a failing model succeeded")
	}
	run := w.get("run1")
	_, err := p.r.Run(w.ctx, "c", harness.UserText(w.model.on("next", 0)))
	var u *runner.UnfinishedError
	if !errors.As(err, &u) || !errors.Is(err, runner.ErrRunUnfinished) || u.RunID != run.ID {
		t.Fatalf("Run on a conversation with a failed run = %v, want UnfinishedError for %q", err, run.ID)
	}
	if _, err := p.r.Cancel(w.ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := p.run("c", "q:next"); err != nil {
		t.Fatalf("after Cancel, Run = %v", err)
	}
}

// A run canceled before the model answered leaves the conversation on a user
// message: Cancel appends a marker reply, so the next run's first request
// alternates, and nothing stored is changed.
func TestCancelBeforeTheAnswerKeepsTheConversationValid(t *testing.T) {
	t.Parallel()
	for _, how := range []string{"stopped", "live", "crashed"} {
		t.Run(how, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			p := w.process()
			prompt := w.model.on("q", 2)
			var (
				x      *runner.Execution
				before []store.Entry
			)
			switch how {
			case "stopped": // the consumer went away after the first turn
				var err error
				if x, err = p.r.Run(w.ctx, "c", harness.UserText(prompt)); err != nil {
					t.Fatal(err)
				}
				if _, err := p.drain(x, func(ev runner.Event) bool { return ev.Harness.Kind == harness.EventTurnDone }); err != nil {
					t.Fatal(err)
				}
			case "live": // canceled while waiting on the model, in this process
				waiting, release := make(chan struct{}), make(chan struct{})
				w.model.gate = func(ctx context.Context, _ string, turn int) {
					if turn == 1 {
						close(waiting)
						select {
						case <-release:
						case <-ctx.Done():
						}
					}
				}
				defer close(release)
				var err error
				if x, err = p.r.Run(w.ctx, "c", harness.UserText(prompt)); err != nil {
					t.Fatal(err)
				}
				done := p.drainInBackground(x)
				<-waiting
				before = w.log("c")
				if _, err := p.r.Cancel(w.ctx, x.Run.ID); err != nil {
					t.Fatal(err)
				}
				if err := <-done; !errors.Is(err, runner.ErrCanceled) {
					t.Fatalf("canceled execution ended with %v, want ErrCanceled", err)
				}
			case "crashed": // the process died after its first turn
				dead := w.process()
				dead.store.crashAt(crashAtNth("Append", 3, false))
				var err error
				if x, err = dead.r.Run(w.ctx, "c", harness.UserText(prompt)); err != nil {
					t.Fatal(err)
				}
				if _, err := dead.drain(x, nil); !errors.Is(err, errCrashed) {
					t.Fatalf("crashed run ended with %v", err)
				}
				w.expire()
			}
			if how != "live" {
				before = w.log("c")
				if _, err := p.r.Cancel(w.ctx, x.Run.ID); err != nil {
					t.Fatal(err)
				}
			}
			if run := w.get(x.Run.ID); run.Status != store.StatusCanceled {
				t.Fatalf("run is %s after Cancel", run.Status)
			}
			after := w.log("c")
			if len(after) != len(before)+1 || !sameJSON(after[:len(before)], before) {
				t.Fatalf("Cancel changed the log: before %s, after %s", js(before), js(after))
			}
			if marker := after[len(after)-1]; marker.Messages[0].Text() != runner.CanceledText || marker.RunID != x.Run.ID {
				t.Fatalf("last entry after Cancel = %s, want the marker of %q", js(marker), x.Run.ID)
			}
			next := w.model.on("next", 0)
			if _, err := p.run("c", next); err != nil {
				t.Fatal(err)
			}
			first := w.model.requestsFor(next)[0]
			if err := harness.Transcript(first.Messages).Validate(); err != nil {
				t.Errorf("first request after Cancel is not a valid conversation: %v", err)
			}
			if stored := messagesOf(after); len(first.Messages) <= len(stored) || !sameJSON(first.Messages[:len(stored)], stored) {
				t.Errorf("first request after Cancel does not start with the stored conversation")
			}
		})
	}
}

// When Cancel could not store its marker (the process died between the
// transition and the append), the next run appends it before its prompt.
func TestNextRunAppendsAMissingMarker(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	p := w.process()
	x, err := p.r.Run(w.ctx, "c", harness.UserText(w.model.on("q", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.drain(x, func(ev runner.Event) bool { return ev.Harness.Kind == harness.EventRunStart }); err != nil {
		t.Fatal(err)
	}
	canceler := w.process()
	canceler.store.crashAt(crashAtNth("Append", 1, false))
	if _, err := canceler.r.Cancel(w.ctx, x.Run.ID); !errors.Is(err, errCrashed) {
		t.Fatalf("Cancel = %v, want the crash", err)
	}
	if run := w.get(x.Run.ID); run.Status != store.StatusCanceled {
		t.Fatalf("run is %s", run.Status)
	}
	w.expire()
	if _, err := p.run("c", w.model.on("next", 0)); err != nil {
		t.Fatal(err)
	}
	log := w.log("c")
	if len(log) != 4 || log[1].Messages[0].Text() != runner.CanceledText || log[1].RunID != x.Run.ID {
		t.Errorf("log = %s, want prompt, marker, prompt, answer", js(log))
	}
}

func TestRunRefusesAnInvalidMessage(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	p := w.process()
	for _, msg := range []llm.Message{
		llm.Assistant(llm.Text{Text: "hi"}),
		{Role: llm.RoleUser},
		llm.User(llm.ToolResult{ToolUseID: "x", Content: "orphan"}),
	} {
		if _, err := p.r.Run(w.ctx, "c", msg); !errors.Is(err, runner.ErrInvalidInput) || !errors.Is(err, harness.ErrInvalidInput) {
			t.Errorf("Run(%s) = %v, want ErrInvalidInput", js(msg), err)
		}
	}
	if _, err := p.r.Run(context.Background(), "c", harness.UserText("hi")); !errors.Is(err, tenancy.ErrNoTenant) {
		t.Errorf("Run with no tenant = %v, want ErrNoTenant", err)
	}
	if page, _ := w.mem.List(w.ctx, "c", store.Page{}); len(page.Runs) != 0 {
		t.Errorf("refused messages created runs: %+v", page.Runs)
	}
}

// An agent that emits an event kind the runner does not know: the runner
// passes it on and the run completes.
func TestUnknownHarnessEventsArePassedOn(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.agent = func(string) runner.Agent { return unknownKindAgent{h: mustHarness(t, w)} }
	evs, err := w.process().run("c", w.model.on("q", 1))
	if err != nil {
		t.Fatal(err)
	}
	lastDone(t, evs)
	if !slices.ContainsFunc(evs, func(ev runner.Event) bool { return ev.Harness.Kind == 200 }) {
		t.Errorf("the unknown event was not passed on: %s", js(evs))
	}
}

type unknownKindAgent struct{ h *harness.Harness }

func (a unknownKindAgent) Run(ctx context.Context, in harness.Input) iter.Seq2[harness.Event, error] {
	return func(yield func(harness.Event, error) bool) {
		for ev, err := range a.h.Run(ctx, in) {
			if !yield(ev, err) {
				return
			}
			if ev.Kind == harness.EventTurnDone && !yield(harness.Event{Kind: 200, Turn: ev.Turn}, nil) {
				return
			}
		}
	}
}

func mustHarness(t *testing.T, w *world) *harness.Harness {
	t.Helper()
	h, err := harness.New(w.model, w.tools, harness.WithNow(w.clk.Now))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// A store call that times out is retried; an append that had landed is not
// stored twice, and a transition that had landed is not reported as failed.
func TestStoreTimeoutsAreRetriedWithoutDuplicates(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		op     string
		n      int
		landed bool
		// reader reads the conversation through the runner before the
		// retry, so the cache already has the entry that landed.
		reader bool
	}{
		{"Append", 2, true, false},
		{"Append", 2, false, false},
		{"Append", 1, true, false},
		{"Append", 2, true, true},
		{"Transition", 1, true, false},
	} {
		t.Run(fmt.Sprint(c.op, c.n, c.landed, c.reader), func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			p := w.process()
			p.store.fail = func(op string, n int) (bool, bool) { return op == c.op && n == c.n, c.landed }
			if c.reader {
				p.store.before = func(op string, n int) {
					if op == c.op && n == c.n+1 {
						if _, _, err := p.r.Context(w.ctx, "c"); err != nil {
							t.Error(err)
						}
					}
				}
			}
			evs, err := p.run("c", w.model.on("q", 2))
			if err != nil {
				t.Fatal(err)
			}
			if run := lastDone(t, evs).Run; run.Status != store.StatusCompleted {
				t.Errorf("run = %+v", run)
			}
			if !sameJSON(w.transcript("c"), referenceTranscript(t, "q", 2)) {
				t.Errorf("transcript %s differs from an undisturbed run's", js(w.transcript("c")))
			}
		})
	}
}

// A writer whose lease was taken over (it stalled past its time to live)
// stops before writing anything more.
func TestLostLeaseStopsTheWriter(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	p := w.process()
	prompt := w.model.on("q", 2)
	waiting, gate := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	w.model.gate = func(ctx context.Context, _ string, turn int) {
		if turn == 1 {
			close(waiting)
			select {
			case <-gate:
			case <-ctx.Done():
			}
		}
	}
	defer release()
	x, err := p.r.Run(w.ctx, "c", harness.UserText(prompt))
	if err != nil {
		t.Fatal(err)
	}
	// Before the stalled writer renews, another process takes the expired
	// lease.
	p.store.before = func(op string, n int) {
		if op == "Renew" && n == 2 { // the first renewal is at the start
			if _, err := w.mem.Claim(w.ctx, x.Run.ID, "intruder", ttl); err != nil {
				t.Error(err)
			}
		}
	}
	done := p.drainInBackground(x)
	<-waiting
	before := w.log("c")
	if err := w.clk.WaitForTimers(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	w.clk.Advance(ttl + time.Second)
	select {
	case err = <-done:
	case <-time.After(30 * time.Second): // only a writer that never stops gets here
		release() // and goes on to write, which fails below
		err = <-done
	}
	if !errors.Is(err, store.ErrLeaseLost) && !errors.Is(err, store.ErrLeased) {
		t.Fatalf("writer that lost its lease ended with %v, want ErrLeaseLost", err)
	}
	if got := w.log("c"); !sameJSON(got, before) {
		t.Errorf("writer wrote after losing its lease: %s", js(got[len(before):]))
	}
	if run := w.get(x.Run.ID); run.Status != store.StatusRunning || run.Lease.Owner != "intruder" {
		t.Errorf("run = %+v, want running, leased to the intruder", run)
	}
	if err := w.mem.Release(w.ctx, x.Run.ID, "intruder"); err != nil {
		t.Fatal(err)
	}
}

// Cancel acts only on runs of its own process or nobody's: a run another
// live process is executing is refused (the API needs a signal to that
// process), and a run that has ended is final.
func TestCancelOfARunHeldElsewhereIsRefused(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	prompt := w.model.on("q", 1)
	waiting, gate := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	defer release()
	w.model.gate = func(ctx context.Context, _ string, turn int) {
		if turn == 0 {
			close(waiting)
			select {
			case <-gate:
			case <-ctx.Done():
			}
		}
	}
	a, b := w.process(), w.process()
	x, err := a.r.Run(w.ctx, "c", harness.UserText(prompt))
	if err != nil {
		t.Fatal(err)
	}
	done := a.drainInBackground(x)
	<-waiting
	before := w.log("c")
	if _, err := b.r.Cancel(w.ctx, x.Run.ID); !errors.Is(err, store.ErrLeased) {
		t.Errorf("Cancel of a run another process executes = %v, want ErrLeased", err)
	}
	if got := w.log("c"); !sameJSON(got, before) {
		t.Errorf("a refused Cancel wrote %s", js(got[len(before):]))
	}
	release()
	if err := <-done; err != nil {
		t.Fatalf("the run interrupted by a refused Cancel ended with %v", err)
	}
	if run := w.get(x.Run.ID); run.Status != store.StatusCompleted {
		t.Fatalf("run = %+v, want completed", run)
	}
	if _, err := b.r.Cancel(w.ctx, x.Run.ID); !errors.Is(err, store.ErrFinal) {
		t.Errorf("Cancel of a completed run = %v, want ErrFinal", err)
	}
}
