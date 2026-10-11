package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"reflect"
	"runtime"
	"sync"
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
func leakcheck(t testing.TB) {
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

// recorder wraps a model and keeps every request twice: as the harness handed
// it over (sharing memory with the harness) and as a deep copy taken at that
// moment. If the two differ later, the harness changed a request after
// sending it.
type recorder struct {
	inner llm.Model
	mu    sync.Mutex
	sent  []llm.Request
	snap  []llm.Request
}

func (r *recorder) Generate(ctx context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	r.mu.Lock()
	r.sent = append(r.sent, req)
	r.snap = append(r.snap, req.Clone())
	r.mu.Unlock()
	return r.inner.Generate(ctx, req)
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sent)
}

// requests returns the requests made from the index from on.
func (r *recorder) requests(from int) (sent, snap []llm.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]llm.Request(nil), r.sent[from:]...), append([]llm.Request(nil), r.snap[from:]...)
}

// rig is a harness under test. Every run driven through it is checked against
// the invariants in invariants_test.go.
type rig struct {
	*harness.Harness
	rec *recorder
	lim limits
}

func newHarness(tb testing.TB, model llm.Model, reg tool.Registry, opts ...harness.Option) *rig {
	tb.Helper()
	opts = append([]harness.Option{harness.WithNow((&fakeClock{}).now)}, opts...)
	rec := &recorder{inner: model}
	h, err := harness.New(rec, reg, opts...)
	if err != nil {
		tb.Fatal(err)
	}
	return &rig{Harness: h, rec: rec}
}

// fn builds a tool from a function with no input.
func fn[Out any](t testing.TB, name string, f func(ctx context.Context) (Out, error)) tool.Tool {
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

// observation is everything a test can see of one run.
type observation struct {
	in        harness.Input
	events    []harness.Event
	res       harness.Result // the partial result after a failure
	err       error
	finalJSON []byte // the transcript of EventRunDone as received, before the consumer touched it
	complete  bool   // the run ended with EventRunDone or an error
	stopped   bool   // the consumer stopped iterating
	panicked  any    // what the consumer's code panicked with
}

// drive runs to the end and checks the run against the invariants. each sees
// every event as it arrives (it may cancel the run's context) and returns true
// to stop iterating; a panic in it is caught and reported in the observation.
func (r *rig) drive(ctx context.Context, tb testing.TB, in harness.Input, each func(harness.Event) bool) observation {
	tb.Helper()
	o := observation{in: in}
	// The recorder is shared by every run of the rig; this run's requests start here.
	from := r.rec.count()
	func() {
		defer func() {
			if v := recover(); v != nil {
				o.panicked = v
			}
		}()
		for ev, err := range r.Run(ctx, in) {
			if err != nil {
				var re *harness.RunError
				if !errors.As(err, &re) {
					tb.Errorf("run error %T is not a *RunError: %v", err, err)
					return
				}
				o.err, o.res, o.complete = err, partial(re), true
				return
			}
			// The invariants read a private copy: the consumer may scribble on
			// what it is handed.
			o.events = append(o.events, cloneEvent(ev))
			if ev.Kind == harness.EventRunDone {
				o.finalJSON = mustMarshal(tb, ev.Result.Transcript)
				o.res, o.complete = *ev.Result, true
			}
			if each != nil && each(ev) {
				o.stopped = true
				return
			}
		}
	}()
	r.check(tb, o, from)
	return o
}

func mustMarshal(tb testing.TB, v any) []byte {
	tb.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		tb.Errorf("json.Marshal(%T): %v", v, err)
	}
	return b
}

// runCtx runs to the end and returns every event, the result (partial after a
// failure) and the run's error. The run is checked like every other.
func runCtx(ctx context.Context, t *testing.T, h *rig, in harness.Input) ([]harness.Event, harness.Result, error) {
	t.Helper()
	o := h.drive(ctx, t, in, nil)
	if o.panicked != nil || !o.complete {
		t.Fatalf("the run did not end: panic %v, complete %v", o.panicked, o.complete)
	}
	return o.events, o.res, o.err
}

func run(t *testing.T, h *rig, in harness.Input) ([]harness.Event, harness.Result, error) {
	t.Helper()
	return runCtx(t.Context(), t, h, in)
}

func partial(re *harness.RunError) harness.Result {
	return harness.Result{Transcript: re.Transcript(), Usage: re.Usage(), Turns: re.Turns(), Record: re.Record()}
}

func prompt(s string) harness.Input { return harness.Input{Message: harness.UserText(s)} }

// results returns the tool results of a user message.
func results(t testing.TB, m llm.Message) []llm.ToolResult {
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

// equateEmpty makes a nil slice equal an empty one, for the slice types that
// appear in transcripts and records.
var equateEmpty = cmp.Options{
	emptySlice[llm.Message](), emptySlice[llm.Block](), emptySlice[harness.TurnRecord](),
	emptySlice[harness.TimedMessage](), emptySlice[harness.ToolRecord](),
}

func emptySlice[T any]() cmp.Option {
	return cmp.Transformer("emptySlice", func(s []T) []T {
		if s == nil {
			return []T{}
		}
		return s
	})
}

func diff(t testing.TB, what string, want, got any) {
	t.Helper()
	if d := diffString(want, got); d != "" {
		t.Errorf("%s (-want +got):\n%s", what, d)
	}
}

// diffString is cmp.Diff with a fast path: the checks run on every run of every
// test, and almost always find the two values equal.
func diffString(want, got any) string {
	if reflect.DeepEqual(want, got) {
		return ""
	}
	return cmp.Diff(want, got, equateEmpty)
}

// cloneEvent copies the parts of an event that a consumer could write to.
func cloneEvent(ev harness.Event) harness.Event {
	ev.ToolUse.Input = append(json.RawMessage(nil), ev.ToolUse.Input...)
	if ev.Prompt != nil {
		p := harness.TimedMessage{Message: ev.Prompt.Message.Clone(), At: ev.Prompt.At}
		ev.Prompt = &p
	}
	if ev.TurnRecord != nil {
		tr := *ev.TurnRecord
		tr.Messages = make([]harness.TimedMessage, len(tr.Messages))
		for i, m := range ev.TurnRecord.Messages {
			tr.Messages[i] = harness.TimedMessage{Message: m.Message.Clone(), At: m.At}
		}
		tr.Tools = append([]harness.ToolRecord(nil), tr.Tools...)
		ev.TurnRecord = &tr
	}
	return ev
}
