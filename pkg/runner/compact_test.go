package runner_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store"
)

// noCompact hides the Compactor methods of its agent.
type noCompact struct{ runner.Agent }

// failingCompact is a harness whose summaries fail with err.
type failingCompact struct {
	*harness.Harness
	err error
}

func (f failingCompact) Compact(context.Context, harness.Transcript) (harness.Summary, error) {
	return harness.Summary{}, f.err
}

func windowHarness(t *testing.T, w *world, cw harness.ContextWindow) *harness.Harness {
	t.Helper()
	h, err := harness.New(w.model, w.tools, harness.WithClock(w.clk), harness.WithContextWindow(cw))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// compactions returns the compaction entries of a log.
func compactions(log []store.Entry) []store.Entry {
	var out []store.Entry
	for _, e := range log {
		if e.Kind == store.KindCompaction {
			out = append(out, e)
		}
	}
	return out
}

// summaryRequests returns the model's requests that asked for a summary.
func (m *scriptModel) summaryRequests() []llm.Request {
	var out []llm.Request
	for _, r := range m.Requests() {
		if r.System == harness.CompactionPrompt {
			out = append(out, r)
		}
	}
	return out
}

func isSummary(m llm.Message) bool {
	return m.Role == llm.RoleUser && strings.HasPrefix(m.Text(), runner.SummaryPrefix)
}

// A run whose request does not fit is compacted and runs again from the
// compacted context: the next request starts with the summary and keeps
// verbatim what must follow it.
func TestContextTooLongIsCompactedAndResumed(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		tools int
		// tail is how many messages before the failing request the
		// compaction keeps, and ack whether the fixed reply precedes them.
		tail int
		ack  bool
		// kept is in the tail, so not in the summary's request.
		kept string
	}{
		{"on the prompt", 0, 1, true, "q:b"},
		{"on tool results", 2, 2, false, "q:b/1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			p := w.process()
			if _, err := p.run("c", w.model.on("a", 1)); err != nil {
				t.Fatal(err)
			}
			prompt := w.model.on("b", c.tools)
			w.model.failing("b", c.tools, 1, fmt.Errorf("%w: scripted", llm.ErrContextTooLong))
			before := w.log("c")
			evs, err := p.run("c", prompt)
			if err != nil {
				t.Fatal(err)
			}
			done := lastDone(t, evs)
			if done.Run.Status != store.StatusCompleted || done.Run.Attempts != 0 {
				t.Errorf("run = %+v, want completed at the first attempt", done.Run)
			}
			log := w.log("c")
			cs := compactions(log)
			if len(cs) != 1 {
				t.Fatalf("%d compactions, want 1: %s", len(cs), js(log))
			}
			e := cs[0]
			if e.Covers != e.Seq-1 || e.RunID != done.RunID {
				t.Errorf("compaction at %d covers %d, run %q; want %d, %q", e.Seq, e.Covers, e.RunID, e.Seq-1, done.RunID)
			}
			rec, err := runner.DecodeRecord(e)
			if err != nil || rec.Compaction == nil || rec.Compaction.Reason != runner.ReasonContextTooLong {
				t.Errorf("record %s, %v", e.Record, err)
			}
			// The summary, the fixed reply if needed, then the tail as stored.
			pre := messagesOf(log[:e.Seq-1])
			want := 1 + c.tail
			if c.ack {
				want++
			}
			if len(e.Messages) != want || !isSummary(e.Messages[0]) ||
				!sameJSON(e.Messages[len(e.Messages)-c.tail:], pre[len(pre)-c.tail:]) ||
				c.ack && e.Messages[1].Text() != runner.CompactedText {
				t.Errorf("compaction messages %s, context before %s", js(e.Messages), js(pre))
			}
			// The summary was asked for everything before the tail.
			sr := w.model.summaryRequests()
			if len(sr) != 1 || !strings.Contains(sr[0].Messages[0].Text(), "q:a") || strings.Contains(sr[0].Messages[0].Text(), c.kept) || len(sr[0].Tools) != 0 {
				t.Errorf("summary requests %s", js(sr))
			}
			// The request after the compaction starts with it.
			reqs := w.model.Requests()
			last := reqs[len(reqs)-1]
			if !sameJSON(last.Messages[:len(e.Messages)], e.Messages) {
				t.Errorf("last request %s, want it to start with %s", js(last.Messages), js(e.Messages))
			}
			// Entries before it are untouched.
			if !sameJSON(log[:len(before)], before) {
				t.Errorf("history changed:\n%s\n%s", js(before), js(log[:len(before)]))
			}
			// Announced once stored, and followed by EventSaved.
			if i := slices.IndexFunc(evs, func(ev runner.Event) bool { return ev.Kind == runner.EventCompacted }); i < 0 || evs[i].Seq != e.Seq ||
				evs[i+1].Kind != runner.EventSaved || evs[i+1].Seq != e.Seq {
				t.Errorf("want EventCompacted then EventSaved at %d: %s", e.Seq, js(evs))
			}
		})
	}
}

// A run is compacted once per execution: a second failure for want of room
// is the run's.
func TestContextTooLongTwiceFails(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	p := w.process()
	if _, err := p.run("c", w.model.on("a", 0)); err != nil {
		t.Fatal(err)
	}
	prompt := w.model.on("b", 0)
	w.model.failing("b", 0, 2, fmt.Errorf("%w: scripted", llm.ErrContextTooLong))
	_, err := p.run("c", prompt)
	if !errors.Is(err, llm.ErrContextTooLong) {
		t.Fatalf("run = %v, want context too long", err)
	}
	if n := len(compactions(w.log("c"))); n != 1 {
		t.Errorf("%d compactions, want 1", n)
	}
	if run := w.get("run2"); run.Status != store.StatusFailed || run.Retryable {
		t.Errorf("run = %+v, want failed, not retryable", run)
	}
}

// A compaction that fails, or has nothing to summarize, leaves the log as it
// was, and the run fails with its own error.
func TestFailedCompactionLeavesTheLog(t *testing.T) {
	t.Parallel()
	errSummary := errors.New("summary failed")
	for _, c := range []struct {
		name  string
		first bool // the failing run is the conversation's first
		want  string
	}{
		{"model error", false, errSummary.Error()},
		{"nothing to compact", true, "nothing to compact"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			w.agent = func(string) runner.Agent { return failingCompact{mustHarness(t, w), errSummary} }
			p := w.process()
			if !c.first {
				if _, err := p.run("c", w.model.on("a", 0)); err != nil {
					t.Fatal(err)
				}
			}
			if c.first {
				w.agent = nil // the harness itself: nothing before the prompt
			}
			prompt := w.model.on("b", 1)
			w.model.failing("b", 0, 1, fmt.Errorf("%w: scripted", llm.ErrContextTooLong))
			_, err := p.run("c", prompt)
			if !errors.Is(err, llm.ErrContextTooLong) || errors.Is(err, errSummary) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("run = %v, want context too long, mentioning %q", err, c.want)
			}
			log := w.log("c")
			if n := len(compactions(log)); n != 0 {
				t.Errorf("%d compactions, want none", n)
			}
			if last := log[len(log)-1]; last.Kind != store.KindPrompt {
				t.Errorf("log ends on %s, want the run's prompt", last.Kind)
			}
		})
	}
}

// A conversation whose last request filled its share of the window is
// compacted before the next run's message is stored, and not below it.
func TestFullWindowIsCompactedBeforeTheNextRun(t *testing.T) {
	t.Parallel()
	// The scripted model's first request of a run uses 10 input tokens.
	for _, c := range []struct {
		share float64
		want  int
	}{
		{0.1, 1},  // 10 of 100: full
		{0.11, 0}, // 10 of 100 is below 11
	} {
		t.Run(fmt.Sprint(c.share), func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			w.agent = func(string) runner.Agent {
				return windowHarness(t, w, harness.ContextWindow{Tokens: 100, Share: c.share})
			}
			p := w.process()
			if _, err := p.run("c", w.model.on("a", 0)); err != nil {
				t.Fatal(err)
			}
			prompt := w.model.on("b", 0)
			evs, err := p.run("c", prompt)
			if err != nil {
				t.Fatal(err)
			}
			log := w.log("c")
			cs := compactions(log)
			if len(cs) != c.want {
				t.Fatalf("%d compactions, want %d: %s", len(cs), c.want, js(log))
			}
			if c.want == 0 {
				return
			}
			e := cs[0]
			rec, _ := runner.DecodeRecord(e)
			if e.Seq != 3 || e.RunID != "run1" || rec.Compaction == nil || rec.Compaction.Reason != runner.ReasonWindowFull {
				t.Errorf("compaction %s, want at 3, labelled run1, window_full", js(e))
			}
			if !isSummary(e.Messages[0]) || len(e.Messages) != 2 || log[3].Kind != store.KindPrompt || log[3].RunID != "run2" {
				t.Errorf("log %s", js(log))
			}
			reqs := w.model.requestsFor(prompt)
			if len(reqs) != 1 || !sameJSON(reqs[0].Messages[:2], e.Messages) {
				t.Errorf("requests of the run %s, want them to start with %s", js(reqs), js(e.Messages))
			}
			if i := slices.IndexFunc(evs, func(ev runner.Event) bool { return ev.Kind == runner.EventCompacted }); i < 0 || evs[i].Seq != 3 {
				t.Errorf("events %s, want EventCompacted at 3", js(evs))
			}
		})
	}
}

// A run that stops after compacting ahead of its message, before the message
// is stored, has not started: its resume cancels it.
func TestCompactionBeforeThePromptIsNotAStart(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.agent = func(string) runner.Agent { return windowHarness(t, w, harness.ContextWindow{Tokens: 10}) }
	p := w.process()
	if _, err := p.run("c", w.model.on("a", 0)); err != nil {
		t.Fatal(err)
	}
	// Appends: run1's prompt and turn, then run2's compaction and prompt.
	p.store.crashAt(crashAtNth("Append", 4, false))
	if _, err := p.run("c", w.model.on("b", 0)); err == nil {
		t.Fatal("run survived its crash")
	}
	if n := len(compactions(w.log("c"))); n != 1 {
		t.Fatalf("%d compactions, want 1", n)
	}
	w.expire()
	if _, err := w.process().resume("run2"); !errors.Is(err, runner.ErrNotStarted) {
		t.Errorf("resume = %v, want %v", err, runner.ErrNotStarted)
	}
}

// History keeps every entry byte for byte through any number of
// compactions, and the context starts at the latest one.
func TestHistorySurvivesCompactions(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.agent = func(string) runner.Agent { return windowHarness(t, w, harness.ContextWindow{Tokens: 1}) }
	p := w.process()
	kept := []store.Entry{}
	for i := range 4 {
		if _, err := p.run("c", w.model.on(fmt.Sprint(i), 1)); err != nil {
			t.Fatal(err)
		}
		log := w.log("c")
		if !sameJSON(log[:len(kept)], kept) {
			t.Fatalf("after run %d, history changed:\n%s\n%s", i, js(kept), js(log[:len(kept)]))
		}
		kept = log
	}
	cs := compactions(kept)
	if len(cs) != 3 {
		t.Fatalf("%d compactions, want 3", len(cs))
	}
	cv, err := w.mem.Context(w.ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	if cv.Entries[0].Seq != cs[2].Seq {
		t.Errorf("context starts at %d, want the latest compaction at %d", cv.Entries[0].Seq, cs[2].Seq)
	}
	// Each summary was asked of the context the previous one started.
	sr := w.model.summaryRequests()
	if len(sr) != 3 || !strings.Contains(sr[2].Messages[0].Text(), runner.SummaryPrefix) {
		t.Errorf("summary requests %s", js(sr))
	}
}

// A compaction ahead of a run that fails leaves the log as it was, and the
// run goes ahead.
func TestFailedCompactionBeforeARunIsSkipped(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.agent = func(string) runner.Agent {
		return failingCompact{windowHarness(t, w, harness.ContextWindow{Tokens: 1}), errors.New("summary failed")}
	}
	p := w.process()
	for _, q := range []string{"a", "b"} {
		evs, err := p.run("c", w.model.on(q, 0))
		if err != nil {
			t.Fatal(err)
		}
		lastDone(t, evs)
	}
	if n := len(compactions(w.log("c"))); n != 0 {
		t.Errorf("%d compactions, want none", n)
	}
}

// A compaction empties the window: a run that stores no turn after one does
// not lead to another.
func TestCompactionEmptiesTheWindow(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.agent = func(string) runner.Agent { return windowHarness(t, w, harness.ContextWindow{Tokens: 10}) }
	p := w.process()
	if _, err := p.run("c", w.model.on("a", 0)); err != nil {
		t.Fatal(err)
	}
	w.model.on("b", 0)
	w.model.failing("b", 0, 1, fmt.Errorf("%w: scripted", llm.ErrAuth))
	if _, err := p.run("c", "q:b"); !errors.Is(err, llm.ErrAuth) {
		t.Fatalf("run = %v, want %v", err, llm.ErrAuth)
	}
	if _, err := p.r.Cancel(w.ctx, "run2"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.run("c", w.model.on("c", 0)); err != nil {
		t.Fatal(err)
	}
	if n := len(compactions(w.log("c"))); n != 1 {
		t.Errorf("%d compactions, want 1", n)
	}
}

// Compacting a run is not one of its interactive retries.
func TestCompactionIsNotARetry(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	p := w.process(runner.WithInteractiveRetry(runner.RetryPolicy{MaxAttempts: 2, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}))
	if _, err := p.run("c", w.model.on("a", 0)); err != nil {
		t.Fatal(err)
	}
	prompt := w.model.on("b", 0)
	w.model.failing("b", 0, 1, fmt.Errorf("%w: scripted", llm.ErrContextTooLong))
	var once sync.Once
	w.model.gate = func(_ context.Context, p string, _ int) {
		if p == prompt {
			// The request after the compaction is overloaded, once.
			once.Do(func() { w.model.failing("b", 0, 1, llm.ErrOverloaded) })
		}
	}
	x, err := p.r.Run(w.ctx, "c", harness.UserText(prompt), runner.Interactive())
	if err != nil {
		t.Fatal(err)
	}
	evs, err := p.drain(x, w.retrying())
	if err != nil {
		t.Fatalf("run = %v, want it compacted, then retried once", err)
	}
	if lastDone(t, evs).Run.Status != store.StatusCompleted {
		t.Errorf("events %s", js(evs))
	}
}
