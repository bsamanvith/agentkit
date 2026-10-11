package harness_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/tool/tooltest"
)

// wait blocks until ch is closed or ctx is done.
func wait(ctx context.Context, ch <-chan struct{}) error {
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// The tests below control tools from the consumer's side. Events are
// delivered on the goroutine that iterates Run, so what the consumer does when
// it sees an event happens at a fixed point of the run, and no test depends on
// which goroutine happens to be scheduled first.

func TestParallelToolsRunConcurrentlyResultsInCallOrder(t *testing.T) {
	leakcheck(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	// Both tools wait for a barrier that the consumer opens once it has seen
	// both start: they get past it only if they run at the same time. Tool "a"
	// (the first call) then also waits until the consumer has seen "b" finish,
	// so the calls finish in the opposite order to the calls.
	barrier := make(chan struct{})
	bFinished := make(chan struct{})
	a := fn(t, "a", func(ctx context.Context) (string, error) {
		if err := wait(ctx, barrier); err != nil {
			return "", err
		}
		return "A", wait(ctx, bFinished)
	})
	b := fn(t, "b", func(ctx context.Context) (string, error) { return "B", wait(ctx, barrier) })
	fake := llmtest.NewFake(t, llmtest.ToolCalls(use("c1", "a", `{}`), use("c2", "b", `{}`)), llmtest.Text("done"))
	h := newHarness(t, fake, newRegistry(t, a, b))

	var starts, dones []int
	o := h.drive(ctx, t, prompt("go"), func(ev harness.Event) bool {
		switch ev.Kind {
		case harness.EventToolStart:
			if starts = append(starts, ev.Call); len(starts) == 2 {
				close(barrier)
			}
		case harness.EventToolDone:
			if dones = append(dones, ev.Call); ev.Call == 1 {
				close(bFinished)
			}
		}
		return false
	})
	if o.err != nil {
		t.Fatal(o.err)
	}

	// One message, in the order of the calls, although b finished first.
	reqs := fake.Requests()
	if len(reqs[1].Messages) != 3 {
		t.Fatalf("second request has %d messages, want prompt, reply and one results message", len(reqs[1].Messages))
	}
	diff(t, "results", []llm.ToolResult{
		{ToolUseID: "c1", Content: `"A"`},
		{ToolUseID: "c2", Content: `"B"`},
	}, results(t, reqs[1].Messages[2]))
	// Events come in finish order and say which call they are for.
	diff(t, "order in which calls finished", []int{1, 0}, dones)
}

// gateInput numbers a call of the gate tool.
type gateInput struct {
	N int `json:"n"`
}

func TestMaxParallelToolsBoundsConcurrency(t *testing.T) {
	leakcheck(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	const calls, limit = 4, 2
	// Each call announces it has begun and then holds its slot until its gate
	// opens. The consumer opens the gates only after it has seen the first two
	// begin, and then looks for a third that began as well.
	entered := make(chan int, calls)
	gates := make([]chan struct{}, calls)
	var uses []llm.ToolUse
	for i := range gates {
		gates[i] = make(chan struct{})
		uses = append(uses, use("c"+string(rune('1'+i)), "gate", `{"n":`+string(rune('0'+i))+`}`))
	}
	var running, peak atomic.Int32
	gate, err := tool.Func("gate", "Hold a slot.", func(ctx context.Context, in gateInput) (int, error) {
		n := running.Add(1)
		defer running.Add(-1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		entered <- in.N
		return in.N, wait(ctx, gates[in.N])
	})
	if err != nil {
		t.Fatal(err)
	}
	fake := llmtest.NewFake(t, llmtest.ToolCalls(uses...), llmtest.Text("done"))
	h := newHarness(t, fake, newRegistry(t, gate), harness.WithMaxParallelTools(limit))

	startEvents := 0
	o := h.drive(ctx, t, prompt("go"), func(ev harness.Event) bool {
		if ev.Kind != harness.EventToolStart {
			return false
		}
		if startEvents++; startEvents != limit {
			return false
		}
		for range limit { // both are inside the tool, holding their slots
			select {
			case <-entered:
			case <-ctx.Done():
				t.Error("the first calls never began")
			}
		}
		// Nothing more may begin while the slots are held. This is a wait for
		// something that must not happen: it can only fail by letting a
		// broken harness pass, never by failing a correct one.
		select {
		case n := <-entered:
			t.Errorf("call %d began while %d calls held all the slots", n, limit)
		case <-time.After(200 * time.Millisecond):
		}
		for _, g := range gates {
			close(g)
		}
		return false
	})
	if o.err != nil {
		t.Fatal(o.err)
	}
	if p := peak.Load(); p > limit {
		t.Errorf("%d calls ran at once, the limit is %d", p, limit)
	}
	var contents []string
	for _, r := range results(t, fake.Requests()[1].Messages[2]) {
		contents = append(contents, r.Content)
	}
	diff(t, "results in the order of the calls", []string{"0", "1", "2", "3"}, contents)
}

// A consumer that stops iterating, or whose code panics, cancels the model call
// and the running tools, and Run returns only after they have returned.
func TestConsumerStopCancelsEverything(t *testing.T) {
	hang := func(t *testing.T, running *atomic.Int32) tool.Tool {
		return fn(t, "hang", func(ctx context.Context) (string, error) {
			running.Add(1)
			defer running.Add(-1)
			<-ctx.Done()
			// Slow cleanup: the only way to see whether Run waited for the tool
			// is a tool that takes a moment to stop. It can make a broken
			// harness pass by luck, never fail a correct one.
			<-time.After(30 * time.Millisecond)
			return "", ctx.Err()
		})
	}
	toolsScript := func() []llmtest.Reply {
		return []llmtest.Reply{
			llmtest.ToolCalls(use("c1", "hang", `{}`), use("c2", "hang", `{}`), use("c3", "hang", `{}`)),
			llmtest.Text("never asked"),
		}
	}
	for _, how := range []string{"stops", "panics"} {
		t.Run("consumer "+how+" during tools", func(t *testing.T) {
			leakcheck(t)
			var running atomic.Int32
			fake := llmtest.NewFake(t, toolsScript()...)
			h := newHarness(t, fake, newRegistry(t, hang(t, &running)))

			o := h.drive(t.Context(), t, prompt("go"), func(ev harness.Event) bool {
				if ev.Kind == harness.EventToolStart && how == "panics" {
					panic("consumer bug")
				}
				return ev.Kind == harness.EventToolStart
			})
			if how == "stops" && !o.stopped || how == "panics" && o.panicked == nil {
				t.Fatalf("the consumer did not %s: %+v", how, o)
			}
			// Run has returned: the tools must have stopped already, not eventually.
			if n := running.Load(); n != 0 {
				t.Errorf("%d tool calls still running after Run returned", n)
			}
			if len(fake.Requests()) != 1 {
				t.Errorf("%d model calls, want 1", len(fake.Requests()))
			}
		})
	}
	t.Run("consumer stops during the model stream", func(t *testing.T) {
		leakcheck(t)
		// The model goes quiet after its first event and answers again only when
		// its context is cancelled. A harness that kept reading after the
		// consumer stopped would wait here until the deadline.
		slow := llmtest.Text("a long enough answer to arrive in several deltas")
		slow.Stall = true
		fake := llmtest.NewFake(t, slow)
		h := newHarness(t, fake, newRegistry(t, tooltest.Fixtures()...))
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		o := h.drive(ctx, t, prompt("go"), func(ev harness.Event) bool { return ev.Kind == harness.EventTextDelta })
		if !o.stopped {
			t.Fatal("the consumer did not stop")
		}
		if ctx.Err() != nil {
			t.Error("Run only returned when the model's context expired: it did not stop the model stream")
		}
	})
}

// A request the harness sends carries the options it was given, and only
// those, on every turn.
func TestRequestOptionsReachEveryRequest(t *testing.T) {
	leakcheck(t)
	provider := map[string]string{"thinking": "on"}
	fake := llmtest.NewFake(t,
		llmtest.ToolCalls(use("c1", "echo", `{"text":"x"}`)),
		llmtest.Text("done"),
	)
	h := newHarness(t, fake, newRegistry(t, tooltest.Fixtures()...),
		harness.WithSystem("sys"),
		harness.WithRequest(harness.RequestOptions{MaxTokens: 321, Provider: provider}))
	if _, _, err := run(t, h, prompt("go")); err != nil {
		t.Fatal(err)
	}
	for i, req := range fake.Requests() {
		if req.MaxTokens != 321 || req.System != "sys" || len(req.Tools) != 3 {
			t.Errorf("request %d: MaxTokens %d, System %q, %d tools", i, req.MaxTokens, req.System, len(req.Tools))
		}
		diff(t, "provider options", any(provider), req.Provider)
	}
	if _, err := harness.New(fake, newRegistry(t), harness.WithRequest(harness.RequestOptions{MaxTokens: -1})); err == nil {
		t.Error("a negative output limit was accepted")
	}
}

// With one slot the calls run one after the other, in the order of the calls.
func TestOneSlotRunsCallsInOrder(t *testing.T) {
	leakcheck(t)
	var uses []llm.ToolUse
	for _, id := range []string{"c1", "c2", "c3", "c4", "c5"} {
		uses = append(uses, use(id, "echo", `{"text":"`+id+`"}`))
	}
	fake := llmtest.NewFake(t, llmtest.ToolCalls(uses...), llmtest.Text("done"))
	h := newHarness(t, fake, newRegistry(t, tooltest.Fixtures()...), harness.WithMaxParallelTools(1))

	var order []int
	h.drive(t.Context(), t, prompt("go"), func(ev harness.Event) bool {
		if ev.Kind == harness.EventToolStart {
			order = append(order, ev.Call)
		}
		return false
	})
	diff(t, "order in which calls began", []int{0, 1, 2, 3, 4}, order)
}
