package runner_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store"
)

// simulate plays a random history, chosen by seed, against one store: chat
// messages on a few conversations, whose executions run normally, are
// abandoned by their consumer at a random event, die at a random store call
// (the call landing or not), or are stopped and canceled, against a model
// that fails retryably or for good now and then. Between messages, recovery
// replicas (one or two at once, and themselves liable to die) and the clock
// bring every run to an end, canceling runs that failed for good.
//
// Half the messages start interactive runs, which are also interrupted while
// the model works, retried inline, and left interrupted: the user then
// continues them (Resume), or a later message replaces them. Every recovery
// pass is checked to leave every interactive run alone and to call no model
// for it.
//
// Besides the invariants every test checks, the final log of every
// conversation is judged against a restatement of the scripted model: each
// run's entries are contiguous, start with its prompt, continue with a prefix
// of the turns the script gives that prompt, and are whole if the run
// completed; a canceled run that stopped short ends with the marker.
func simulate(t *testing.T, seed uint64) map[string]int {
	rng := rand.New(rand.NewPCG(seed, 0x5eed))
	w := newWorld(t)
	mix := map[string]int{}
	if rng.IntN(2) == 0 {
		// Turns are kept and stored in batches.
		mix["read-only tools"]++
		w.readOnly()
	}
	scripts := map[string]int{} // prompt → tools
	convs := make([]string, 1+rng.IntN(3))
	for i := range convs {
		convs[i] = fmt.Sprint("c", i)
	}

	// recover runs a recovery pass on p, and checks that it leaves every
	// unfinished interactive run alone: its status, its failures and its
	// conversation's log are unchanged, and no request is made for its
	// prompt.
	recover := func(p *proc) {
		type state struct {
			run      store.Run
			entries  int
			requests int
		}
		snapshot := func() map[string]state {
			out := map[string]state{}
			for _, conv := range convs {
				page, err := w.mem.List(w.ctx, conv, store.Page{Limit: store.MaxPageLimit})
				if err != nil {
					t.Fatal(err)
				}
				log := w.log(conv)
				for _, r := range page.Runs {
					if !r.Interactive || r.Status.Final() {
						continue
					}
					st := state{run: r, entries: len(log)}
					for _, e := range log {
						if e.RunID == r.ID && e.Kind == store.KindPrompt {
							st.requests = len(w.model.requestsFor(e.Messages[0].Text()))
						}
					}
					out[r.ID] = st
				}
			}
			return out
		}
		before := snapshot()
		_, _ = w.recovery(p).Recover(w.ctx)
		after := snapshot()
		for id, b := range before {
			a, ok := after[id]
			if !ok || a.run.Status != b.run.Status || a.run.Attempts != b.run.Attempts || a.entries != b.entries || a.requests != b.requests {
				t.Errorf("seed %d: recovery touched interactive run %q: %+v → %+v", seed, id, b, a)
			}
		}
	}

	// drain reads x like a client, letting inline retries go ahead unless
	// stop says to stop at that event.
	drain := func(p *proc, x *runner.Execution, stop func(runner.Event) bool) error {
		_, err := p.drain(x, func(ev runner.Event) bool {
			if stop != nil && stop(ev) {
				return true
			}
			if ev.Kind == runner.EventRetrying {
				mix["inline retry"]++
				w.advanceWhenWaiting(ev.Delay)
			}
			return false
		})
		return err
	}

	// settle brings the conversation's unfinished run, if any, to an end;
	// unless final, it may leave an interrupted interactive run for the
	// next message to replace.
	settle := func(conv string, final bool) {
		for range 50 {
			run, err := w.mem.Unfinished(w.ctx, conv)
			if errors.Is(err, store.ErrNotFound) {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if run.Interactive {
				w.expire() // a dead process's lease
				recover(w.process())
				if !w.get(run.ID).Interrupted(w.clk.Now()) && run.Status == store.StatusRunning {
					t.Fatalf("seed %d: interactive run %+v is not interrupted", seed, w.get(run.ID))
				}
				if !final && rng.IntN(2) == 0 {
					mix["left for a new message"]++
					return
				}
				mix["continue"]++
				if run.Status == store.StatusFailed {
					mix["continue a failed run"]++
				}
				p := w.process()
				x, err := p.r.Resume(w.ctx, run.ID)
				if err != nil {
					t.Fatalf("seed %d: continue %q: %v", seed, run.ID, err)
				}
				if err := drain(p, x, nil); err != nil && !errors.Is(err, runner.ErrNotStarted) {
					var re *runner.RunError
					if !errors.As(err, &re) {
						t.Fatalf("seed %d: continued run %q ended with %v", seed, run.ID, err)
					}
				}
				continue
			}
			switch {
			case run.Status == store.StatusFailed && !run.Retryable:
				mix["fatal failure canceled"]++
				w.expire()
				if _, err := w.process().r.Cancel(w.ctx, run.ID); err != nil {
					t.Fatalf("seed %d: cancel %q: %v", seed, run.ID, err)
				}
				continue
			case run.Status == store.StatusFailed:
				mix["retry"]++
				// When it is due and a dead process's lease has expired.
				due := run.NextAttemptAt
				if run.Lease.Expires.After(due) {
					due = run.Lease.Expires.Add(time.Second)
				}
				w.clk.Advance(max(0, due.Sub(w.clk.Now())))
			default:
				w.expire()
			}
			replicas := 1 + rng.IntN(2)
			if replicas == 2 {
				mix["two replicas"]++
			}
			var wg sync.WaitGroup
			for range replicas {
				p := w.process()
				if rng.IntN(4) == 0 {
					mix["recovery dies"]++
					n, landed := 1+rng.IntN(8), rng.IntN(2) == 0
					p.store.crashAt(func(_ string, _, total int) (bool, bool) { return total == n, landed })
				}
				wg.Go(func() { recover(p) })
			}
			wg.Wait()
		}
		run, _ := w.mem.Unfinished(w.ctx, conv)
		t.Fatalf("seed %d: run %+v of %q never ended", seed, run, conv)
	}

	n := 2 + rng.IntN(5)
	for i := range n {
		conv := convs[rng.IntN(len(convs))]
		settle(conv, false)
		replaces := ""
		if u, err := w.mem.Unfinished(w.ctx, conv); err == nil {
			replaces = u.ID
		}
		name := fmt.Sprintf("%s-%d", conv, i)
		tools := rng.IntN(4)
		prompt := w.model.on(name, tools)
		scripts[prompt] = tools
		switch rng.IntN(6) {
		case 0:
			mix["retryable failure"]++
			w.model.failing(name, rng.IntN(tools+1), 1+rng.IntN(2), llm.ErrOverloaded)
		case 1:
			mix["fatal failure"]++
			w.model.failing(name, rng.IntN(tools+1), 1, llm.ErrInvalidRequest)
		}
		var opts []runner.RunOption
		if rng.IntN(2) == 0 {
			mix["flush every set"]++
			opts = append(opts, runner.FlushEvery(1+rng.IntN(3)))
		}
		interactive := rng.IntN(2) == 0
		if interactive {
			mix["interactive"]++
			opts = append(opts, runner.Interactive())
			if rng.IntN(6) == 0 {
				mix["interactive failed for good"]++
				w.model.failing(name, rng.IntN(tools+1), 3, llm.ErrOverloaded)
			}
		}
		p := w.process()
		mode := rng.IntN(5)
		if mode == 4 && !interactive {
			mode = 0
		}
		if mode == 2 {
			at, landed := 1+rng.IntN(10), rng.IntN(2) == 0
			mix[fmt.Sprint("dies, landed ", landed)]++
			p.store.crashAt(func(op string, _, total int) (bool, bool) {
				if total == at && op == "AppendBatch" {
					mix["dies writing a batch"]++
				}
				return total == at, landed
			})
		}
		x, err := p.r.Run(w.ctx, conv, harness.UserText(prompt), opts...)
		if err != nil {
			if !errors.Is(err, errCrashed) {
				t.Fatalf("seed %d: Run: %v", seed, err)
			}
			mix["dies before running"]++
			continue
		}
		if replaces != "" {
			mix["new message replaces"]++
			if r := w.get(replaces); r.Status != store.StatusCanceled {
				t.Fatalf("seed %d: new message started %q, but run %+v was not canceled", seed, x.Run.ID, r)
			}
		}
		if interactive && mode == 2 {
			mix["interactive dies"]++
		}
		var stop func(runner.Event) bool
		if mode == 1 || mode == 3 {
			k := rng.IntN(2 * (tools + 2))
			stop = func(runner.Event) bool { k--; return k < 0 }
		}
		if mode == 4 {
			// The user presses stop while the model works on turn k.
			held := w.holdAt(prompt, rng.IntN(tools+1))
			done := make(chan error, 1)
			go func() { done <- drain(p, x, nil) }()
			select {
			case <-held:
				mix["interrupt"]++
				if _, err := p.r.Interrupt(w.ctx, x.Run.ID); err != nil {
					t.Fatalf("seed %d: Interrupt: %v", seed, err)
				}
				if err := <-done; !errors.Is(err, runner.ErrInterrupted) {
					t.Fatalf("seed %d: interrupted run ended with %v", seed, err)
				}
			case err := <-done:
				_ = err // ended before that turn (failed for good, say)
			}
			w.model.gate = nil
			continue
		}
		err = drain(p, x, stop)
		switch {
		case err == nil && w.get(x.Run.ID).Status == store.StatusCompleted:
			mix["completed at once"]++
		case mode == 3:
			mix["canceled"]++
			if _, err := p.r.Cancel(w.ctx, x.Run.ID); err != nil && !errors.Is(err, store.ErrFinal) {
				t.Fatalf("seed %d: Cancel: %v", seed, err)
			}
		}
	}
	for _, conv := range convs {
		settle(conv, true)
		// A process that died replacing a run may have canceled it without
		// its marker: the next message appends it.
		if tr := w.transcript(conv); len(tr) > 0 && tr[len(tr)-1].Role == llm.RoleUser {
			mix["marker by the next run"]++
			prompt := w.model.on(conv+"-last", 0)
			scripts[prompt] = 0
			if _, err := w.process().run(conv, prompt); err != nil {
				t.Fatalf("seed %d: last message: %v", seed, err)
			}
		}
	}

	// The restatement: what the script answers, turn by turn.
	turnsOf := func(prompt string) [][]llm.Message {
		var out [][]llm.Message
		for j := range scripts[prompt] {
			id := fmt.Sprintf("%s#%d", prompt, j)
			s := fmt.Sprintf("%s/%d", prompt, j)
			in, _ := json.Marshal(map[string]string{"s": s})
			res, _ := json.Marshal("echo " + s)
			out = append(out, []llm.Message{
				llm.Assistant(llm.ToolUse{ID: id, Name: "echo", Input: in}),
				llm.User(llm.ToolResult{ToolUseID: id, Content: string(res)}),
			})
		}
		return append(out, []llm.Message{llm.Assistant(llm.Text{Text: "answer to " + prompt})})
	}
	for _, conv := range convs {
		page, err := w.mem.List(w.ctx, conv, store.Page{Limit: store.MaxPageLimit})
		if err != nil {
			t.Fatal(err)
		}
		runs := map[string]store.Run{}
		for _, r := range page.Runs {
			if r.Status != store.StatusCompleted && r.Status != store.StatusCanceled {
				t.Errorf("seed %d: run %+v is not over", seed, r)
			}
			runs[r.ID] = r
		}
		log := w.log(conv)
		var order []string
		segs := map[string][]store.Entry{}
		for _, e := range log {
			if len(order) == 0 || order[len(order)-1] != e.RunID {
				if _, ok := segs[e.RunID]; ok {
					t.Errorf("seed %d: entries of run %q in %q are not contiguous", seed, e.RunID, conv)
				}
				order = append(order, e.RunID)
			}
			segs[e.RunID] = append(segs[e.RunID], e)
		}
		for id, seg := range segs {
			run, ok := runs[id]
			if !ok || seg[0].Kind != store.KindPrompt {
				t.Errorf("seed %d: entries of %q in %q do not start with a prompt of one of its runs: %s", seed, id, conv, js(seg))
				continue
			}
			prompt := seg[0].Messages[0].Text()
			want := turnsOf(prompt)
			rest := seg[1:]
			marker := len(rest) > 0 && rest[len(rest)-1].Messages[0].Text() == runner.CanceledText
			if marker {
				rest = rest[:len(rest)-1]
				mix["marker"]++
			}
			if len(rest) > len(want) {
				t.Errorf("seed %d: run %q has %d turns, the script %d", seed, id, len(rest), len(want))
				continue
			}
			for j, e := range rest {
				if !sameJSON(e.Messages, want[j]) {
					t.Errorf("seed %d: turn %d of run %q = %s, script %s", seed, j+1, id, js(e.Messages), js(want[j]))
				}
			}
			whole := len(rest) == len(want)
			switch {
			case run.Status == store.StatusCompleted && (!whole || marker):
				t.Errorf("seed %d: completed run %q has %d of %d turns, marker %v", seed, id, len(rest), len(want), marker)
			case run.Status == store.StatusCanceled && whole == marker:
				t.Errorf("seed %d: canceled run %q has %d of %d turns, marker %v", seed, id, len(rest), len(want), marker)
			}
		}
		for id, r := range runs {
			if _, ok := segs[id]; !ok && r.Status == store.StatusCompleted {
				t.Errorf("seed %d: completed run %q has no entries", seed, id)
			}
			if _, ok := segs[id]; !ok {
				mix["not started"]++
			}
		}
		if tr := w.transcript(conv); len(tr) > 0 && tr[len(tr)-1].Role != llm.RoleAssistant {
			t.Errorf("seed %d: %q ends on a user message with every run over", seed, conv)
		}
	}
	return mix
}

func TestSimulation(t *testing.T) {
	t.Parallel()
	const seeds = 300
	var mu sync.Mutex
	mix := map[string]int{}
	t.Run("seeds", func(t *testing.T) {
		for seed := range uint64(seeds) {
			t.Run(fmt.Sprint(seed), func(t *testing.T) {
				t.Parallel()
				m := simulate(t, seed)
				mu.Lock()
				defer mu.Unlock()
				for k, v := range m {
					mix[k] += v
				}
			})
		}
	})
	// The generator reaches every kind of case: a check is only as good as
	// the inputs it sees.
	var missing []string
	for _, k := range []string{
		"completed at once", "canceled", "marker", "not started", "dies before running",
		"dies, landed true", "dies, landed false", "retry", "retryable failure",
		"fatal failure", "fatal failure canceled", "two replicas", "recovery dies",
		"read-only tools", "flush every set", "dies writing a batch",
		"interactive", "interactive dies", "interrupt", "continue", "continue a failed run",
		"left for a new message", "new message replaces", "inline retry", "interactive failed for good",
		"marker by the next run",
	} {
		if mix[k] == 0 {
			missing = append(missing, k)
		}
	}
	t.Logf("case mix over %d seeds: %v", seeds, mix)
	if !t.Failed() && len(missing) > 0 {
		slices.Sort(missing)
		t.Errorf("the simulation never produced %v (mix %v)", missing, mix)
	}
}

func FuzzSimulation(f *testing.F) {
	for seed := range uint64(8) {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed uint64) { simulate(t, seed) })
}
