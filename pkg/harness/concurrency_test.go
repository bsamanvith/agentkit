package harness_test

import (
	"context"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
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

func TestParallelToolsRunConcurrentlyResultsInCallOrder(t *testing.T) {
	leakcheck(t)
	// Both tools announce they started, then wait for a barrier that opens only
	// once both have started: they pass only if they run at the same time.
	// Tool "a" (the first call) then waits for "b" to finish, so the calls
	// finish in the opposite order to the calls.
	started := make(chan string, 2)
	barrier := make(chan struct{})
	bDone := make(chan struct{})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	a := fn(t, "a", func(ctx context.Context) (string, error) {
		started <- "a"
		if err := wait(ctx, barrier); err != nil {
			return "", err
		}
		return "A", wait(ctx, bDone)
	})
	b := fn(t, "b", func(ctx context.Context) (string, error) {
		started <- "b"
		if err := wait(ctx, barrier); err != nil {
			return "", err
		}
		defer close(bDone)
		return "B", nil
	})
	go func() {
		for range 2 {
			select {
			case <-started:
			case <-ctx.Done():
				return
			}
		}
		close(barrier)
	}()

	fake := llmtest.NewFake(t, llmtest.ToolCalls(use("c1", "a", `{}`), use("c2", "b", `{}`)), llmtest.Text("done"))
	h := newHarness(t, fake, newRegistry(t, a, b))

	events, _, err := runCtx(ctx, t, h, prompt("go"))
	if err != nil {
		t.Fatal(err)
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
	var starts, dones []int
	for _, ev := range events {
		switch ev.Kind {
		case harness.EventToolStart:
			starts = append(starts, ev.Call)
			if len(dones) > 0 {
				t.Error("a call finished before both had started")
			}
		case harness.EventToolDone:
			dones = append(dones, ev.Call)
		}
	}
	if len(starts) != 2 {
		t.Errorf("start events for calls %v", starts)
	}
	diff(t, "order in which calls finished", []int{1, 0}, dones)
}

func TestMaxParallelToolsBoundsConcurrency(t *testing.T) {
	leakcheck(t)
	var running, peak atomic.Int32
	slow := fn(t, "slow", func(context.Context) (string, error) {
		n := running.Add(1)
		for { // record the highest number of calls seen at once
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		runtime.Gosched()
		running.Add(-1)
		return "ok", nil
	})
	var calls []llm.ToolUse
	for _, id := range []string{"c1", "c2", "c3", "c4", "c5"} {
		calls = append(calls, use(id, "slow", `{}`))
	}
	fake := llmtest.NewFake(t, llmtest.ToolCalls(calls...), llmtest.Text("done"))
	h := newHarness(t, fake, newRegistry(t, slow), harness.WithMaxParallelTools(2))

	if _, _, err := run(t, h, prompt("go")); err != nil {
		t.Fatal(err)
	}
	if p := peak.Load(); p > 2 {
		t.Errorf("%d calls ran at once, the limit is 2", p)
	}
	if got := len(results(t, fake.Requests()[1].Messages[2])); got != 5 {
		t.Errorf("%d results, want 5", got)
	}
}

// A consumer that stops iterating cancels the model call and the running
// tools, and Run returns only after they have returned.
func TestConsumerStopCancelsEverything(t *testing.T) {
	t.Run("during tools", func(t *testing.T) {
		leakcheck(t)
		var running atomic.Int32
		hang := fn(t, "hang", func(ctx context.Context) (string, error) {
			running.Add(1)
			defer running.Add(-1)
			<-ctx.Done()
			return "", ctx.Err()
		})
		fake := llmtest.NewFake(t,
			llmtest.ToolCalls(use("c1", "hang", `{}`), use("c2", "hang", `{}`), use("c3", "hang", `{}`)),
			llmtest.Text("never asked"),
		)
		h := newHarness(t, fake, newRegistry(t, hang))

		for ev, err := range h.Run(t.Context(), prompt("go")) {
			if err != nil {
				t.Fatal(err)
			}
			if ev.Kind == harness.EventToolStart {
				break
			}
		}
		// Run has returned: the tools must have stopped already, not eventually.
		if n := running.Load(); n != 0 {
			t.Errorf("%d tool calls still running after Run returned", n)
		}
		if len(fake.Requests()) != 1 {
			t.Errorf("%d model calls, want 1", len(fake.Requests()))
		}
	})
	t.Run("during the model stream", func(t *testing.T) {
		leakcheck(t)
		fake := llmtest.NewFake(t, llmtest.Text("a long enough answer to arrive in several deltas"))
		h := newHarness(t, fake, newRegistry(t, tooltest.Fixtures()...))
		for ev, err := range h.Run(t.Context(), prompt("go")) {
			if err != nil {
				t.Fatal(err)
			}
			if ev.Kind == harness.EventTextDelta {
				break
			}
		}
	})
}
