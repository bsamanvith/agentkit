package harness_test

import (
	"context"
	"iter"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
)

// slowModel stands for a model that takes time: it advances the fake clock by
// first before its first event and by rest before its last one. Nothing in the
// harness waits on a timer, so the time moves inside the call, on the run's
// own goroutine, and every reading is deterministic.
type slowModel struct {
	inner       llm.Model
	clk         *clocktest.Fake
	first, rest []time.Duration // per call
	calls       int
}

func (m *slowModel) Generate(ctx context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	i := m.calls
	m.calls++
	return func(yield func(llm.Event, error) bool) {
		n := 0
		for ev, err := range m.inner.Generate(ctx, req) {
			switch {
			case n == 0:
				m.clk.Advance(m.first[i])
			case err == nil && ev.Kind == llm.EventMessageDone:
				m.clk.Advance(m.rest[i])
			}
			n++
			if !yield(ev, err) {
				return
			}
		}
	}
}

// TestRecordTimingsComeFromTheClock: with a fake clock that the model and the
// tool advance by fixed amounts, the record holds exactly those times and
// durations, and the events carry the same clock.
func TestRecordTimingsComeFromTheClock(t *testing.T) {
	leakcheck(t)
	start := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	clk := clocktest.NewFake(start)
	const toolTook = 50 * time.Millisecond
	work := fn(t, "work", func(context.Context) (string, error) {
		clk.Advance(toolTook)
		return "done", nil
	})
	model := &slowModel{
		inner: llmtest.NewFake(t, llmtest.ToolCalls(use("w1", "work", `{}`)), llmtest.Text("finished")),
		clk:   clk,
		first: []time.Duration{100 * time.Millisecond, 10 * time.Millisecond},
		rest:  []time.Duration{200 * time.Millisecond, 20 * time.Millisecond},
	}
	h := newHarness(t, model, newRegistry(t, work), harness.WithClock(clk))

	evs, res, err := run(t, h, prompt("go"))
	if err != nil {
		t.Fatal(err)
	}
	at := func(ms int) time.Time { return start.Add(time.Duration(ms) * time.Millisecond) }
	rec := res.Record
	type turnTimes struct{ Requested, First, Done time.Time }
	var got []turnTimes
	for _, tr := range rec.Turns {
		got = append(got, turnTimes{tr.RequestedAt, tr.FirstEventAt, tr.DoneAt})
	}
	diff(t, "turn times", []turnTimes{
		{at(0), at(100), at(300)},
		{at(350), at(360), at(380)},
	}, got)
	diff(t, "run bracket", []time.Time{at(0), at(380)}, []time.Time{rec.StartedAt, rec.FinishedAt})
	if rec.Prompt == nil || !rec.Prompt.At.Equal(at(0)) {
		t.Errorf("prompt stamped %+v, want %v", rec.Prompt, at(0))
	}
	var msgAt []time.Time
	for _, tr := range rec.Turns {
		for _, m := range tr.Messages {
			msgAt = append(msgAt, m.At)
		}
	}
	// The reply when it was complete, the results when the tool had finished.
	diff(t, "message times", []time.Time{at(300), at(350), at(380)}, msgAt)
	if len(rec.Turns) != 2 || len(rec.Turns[0].Tools) != 1 {
		t.Fatalf("record = %+v", rec)
	}
	tl := rec.Turns[0].Tools[0]
	if !tl.StartedAt.Equal(at(300)) || tl.Duration != toolTook {
		t.Errorf("tool started %v and took %v, want %v and %v", tl.StartedAt, tl.Duration, at(300), toolTook)
	}
	for _, ev := range evs {
		if ev.At.Before(start) || ev.At.After(at(380)) {
			t.Errorf("event %v at %v, outside the fake clock's run", ev.Kind, ev.At)
		}
	}
}
