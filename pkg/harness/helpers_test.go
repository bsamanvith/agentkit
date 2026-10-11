package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// None of the tests in this package call t.Parallel: each one checks, when it
// ends, that every goroutine the run started has gone (leakcheck), and the
// goroutine count of a test running next to others means nothing.

// leakcheck fails the test if goroutines started during it are still running
// when it ends. Call it first, so that it runs after every other cleanup.
func leakcheck(t *testing.T) {
	t.Helper()
	baseline := runtime.NumGoroutine()
	t.Cleanup(func() {
		testkit.Eventually(t, 5*time.Second, func() bool { return runtime.NumGoroutine() <= baseline })
	})
}

// fakeClock returns times that advance by a millisecond on every call, so
// timestamps are deterministic and strictly increasing, from any goroutine.
type fakeClock struct{ n atomic.Int64 }

var clockStart = time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)

func (c *fakeClock) now() time.Time {
	return clockStart.Add(time.Duration(c.n.Add(1)) * time.Millisecond)
}

func newRegistry(tb testing.TB, tools ...tool.Tool) tool.Registry {
	tb.Helper()
	reg, err := tool.NewRegistry(tools)
	if err != nil {
		tb.Fatal(err)
	}
	return reg
}

func newHarness(t *testing.T, model llm.Model, reg tool.Registry, opts ...harness.Option) *harness.Harness {
	t.Helper()
	opts = append([]harness.Option{harness.WithNow((&fakeClock{}).now)}, opts...)
	h, err := harness.New(model, reg, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// fn builds a tool from a function with no input.
func fn[Out any](t *testing.T, name string, f func(ctx context.Context) (Out, error)) tool.Tool {
	t.Helper()
	tl, err := tool.Func(name, "test tool "+name, func(ctx context.Context, _ struct{}) (Out, error) { return f(ctx) })
	if err != nil {
		t.Fatal(err)
	}
	return tl
}

func use(id, name, input string) llm.ToolUse {
	return llm.ToolUse{ID: id, Name: name, Input: json.RawMessage(input)}
}

// reply scripts a model reply with arbitrary blocks, a stop kind and usage.
func reply(stop llm.StopKind, usage llm.Usage, blocks ...llm.Block) llmtest.Reply {
	return llmtest.Reply{Response: llm.Response{
		Message: llm.Assistant(blocks...),
		Stop:    llm.StopReason{Kind: stop},
		Usage:   usage,
	}}
}

// runCtx runs to the end and returns every event, the result (partial after a
// failure) and the run's error.
func runCtx(ctx context.Context, t *testing.T, h *harness.Harness, in harness.Input) ([]harness.Event, harness.Result, error) {
	t.Helper()
	var events []harness.Event
	for ev, err := range h.Run(ctx, in) {
		if err != nil {
			var re *harness.RunError
			if !errors.As(err, &re) {
				t.Fatalf("run error %T is not a *RunError: %v", err, err)
			}
			return events, partial(re), err
		}
		events = append(events, ev)
		if ev.Kind == harness.EventRunDone {
			return events, *ev.Result, nil
		}
	}
	t.Fatal("the run ended without a final event or an error")
	return nil, harness.Result{}, nil
}

func run(t *testing.T, h *harness.Harness, in harness.Input) ([]harness.Event, harness.Result, error) {
	t.Helper()
	return runCtx(t.Context(), t, h, in)
}

func partial(re *harness.RunError) harness.Result {
	return harness.Result{Transcript: re.Transcript(), Usage: re.Usage(), Turns: re.Turns(), Record: re.Record()}
}

func prompt(s string) harness.Input { return harness.Input{Message: harness.UserText(s)} }

// results returns the tool results of a user message.
func results(t *testing.T, m llm.Message) []llm.ToolResult {
	t.Helper()
	if m.Role != llm.RoleUser {
		t.Fatalf("message has role %q, want a user message of tool results", m.Role)
	}
	var out []llm.ToolResult
	for _, b := range m.Content {
		r, ok := b.(llm.ToolResult)
		if !ok {
			t.Fatalf("block %T in a tool-results message", b)
		}
		out = append(out, r)
	}
	return out
}

func diff(t *testing.T, what string, want, got any) {
	t.Helper()
	if d := cmp.Diff(want, got); d != "" {
		t.Errorf("%s (-want +got):\n%s", what, d)
	}
}
