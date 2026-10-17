package runnerrpc_test

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/rpc"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/runner/runnerrpc"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/tool/tooltest"
)

var now = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

// newHarness returns an agent on the fixture tools that plays replies.
func newHarness(t *testing.T, replies ...llmtest.Reply) *harness.Harness {
	t.Helper()
	tools, err := tool.NewRegistry(tooltest.Fixtures())
	if err != nil {
		t.Fatal(err)
	}
	h, err := harness.New(llmtest.NewFake(t, replies...), tools, harness.WithNow(func() time.Time { return now }),
		harness.WithMaxParallelTools(1)) // tool events in one order, to compare runs
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// remote serves a as agent "default" over HTTP and returns the client's agent.
func remote(t *testing.T, a runner.Agent) runner.Agent {
	t.Helper()
	s := rpc.NewServer()
	if err := runnerrpc.Register(s, map[string]runner.Agent{"default": a}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	conn, err := rpc.Dial(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return runnerrpc.NewClient(conn).Agent("default", harness.ContextWindow{})
}

func collect(a runner.Agent, in harness.Input) ([]harness.Event, error) {
	var evs []harness.Event
	for ev, err := range a.Run(context.Background(), in) {
		if err != nil {
			return evs, err
		}
		evs = append(evs, ev)
	}
	return evs, nil
}

func echoCall(id string) llm.ToolUse {
	return llm.ToolUse{ID: id, Name: "echo", Input: []byte(`{"text":"hi","times":2}`)}
}

// script is a run with a tool call, a failing tool call and an answer.
func script() []llmtest.Reply {
	return []llmtest.Reply{
		llmtest.ToolCalls(echoCall("t1"), llm.ToolUse{ID: "t2", Name: "fail"}).WithUsage(llm.Usage{InputTokens: 10, OutputTokens: 5}),
		llmtest.Text("done: hihi").WithUsage(llm.Usage{InputTokens: 20, OutputTokens: 3}),
	}
}

// A remote agent streams exactly the events of the same agent run locally.
func TestRemoteRunMatchesLocal(t *testing.T) {
	t.Parallel()
	history := harness.Transcript{llm.User(llm.Text{Text: "earlier"}), llm.Assistant(llm.Text{Text: "ok"})}
	in := harness.Input{Message: llm.User(llm.Text{Text: "echo hi twice"}), Transcript: history, At: now}

	want, err := collect(newHarness(t, script()...), in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := collect(remote(t, newHarness(t, script()...)), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d", len(got), len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("event %d (%s):\n got %+v\nwant %+v", i, want[i].Kind, got[i], want[i])
		}
	}
	kinds := func(evs []harness.Event) (ks []harness.EventKind) {
		for _, e := range evs {
			ks = append(ks, e.Kind)
		}
		return ks
	}
	for _, k := range []harness.EventKind{harness.EventToolStart, harness.EventToolDone, harness.EventTurnDone, harness.EventRunDone} {
		if !slices.Contains(kinds(got), k) {
			t.Errorf("no %s event: the comparison did not cover it", k)
		}
	}
}

// A resume (no new message) works the same way.
func TestRemoteResumesWithoutAMessage(t *testing.T) {
	t.Parallel()
	in := harness.Input{Transcript: harness.Transcript{llm.User(llm.Text{Text: "hello"})}}
	evs, err := collect(remote(t, newHarness(t, llmtest.Text("hi"))), in)
	if err != nil {
		t.Fatal(err)
	}
	last := evs[len(evs)-1]
	if evs[0].Prompt != nil || last.Result == nil || last.Result.Final.Text() != "hi" {
		t.Errorf("events = %+v, want a run with no prompt that answers hi", evs)
	}
}

// A failed remote run is the *harness.RunError of the local one: the same
// cause, the work it did, and still retryable.
func TestRemoteFailureIsARunError(t *testing.T) {
	t.Parallel()
	replies := []llmtest.Reply{
		llmtest.ToolCalls(echoCall("t1")).WithUsage(llm.Usage{InputTokens: 7, OutputTokens: 2}),
		llmtest.Fail(fmt.Errorf("%w: slow down", llm.ErrRateLimited)),
	}
	in := harness.Input{Message: llm.User(llm.Text{Text: "go"}), At: now}
	_, local := collect(newHarness(t, replies...), in)
	_, err := collect(remote(t, newHarness(t, replies...)), in)

	re, ok := errors.AsType[*harness.RunError](err)
	if !ok {
		t.Fatalf("err = %v (%T), want a *harness.RunError", err, err)
	}
	if !errors.Is(err, llm.ErrRateLimited) || !llm.IsRetryable(err) {
		t.Errorf("err = %v, want ErrRateLimited and retryable", err)
	}
	lre, _ := errors.AsType[*harness.RunError](local)
	if re.Turns() != lre.Turns() || re.Usage() != lre.Usage() || !reflect.DeepEqual(re.Transcript(), lre.Transcript()) {
		t.Errorf("remote partial = %d turns, %+v, %v\n local partial = %d turns, %+v, %v",
			re.Turns(), re.Usage(), re.Transcript(), lre.Turns(), lre.Usage(), lre.Transcript())
	}
	if !reflect.DeepEqual(re.Record(), lre.Record()) {
		t.Errorf("remote record = %+v, want %+v", re.Record(), lre.Record())
	}
}

func TestRemoteRefusal(t *testing.T) {
	t.Parallel()
	refuse := llmtest.Reply{Response: llm.Response{
		Message: llm.Assistant(llm.Text{Text: "no"}),
		Stop:    llm.StopReason{Kind: llm.StopRefusal, Category: "cyber", Explanation: "not allowed"},
	}}
	_, err := collect(remote(t, newHarness(t, refuse)), harness.Input{Message: llm.User(llm.Text{Text: "x"})})
	r, ok := errors.AsType[*harness.RefusalError](err)
	if !ok || r.Category != "cyber" || r.Explanation != "not allowed" || !errors.Is(err, harness.ErrRefused) {
		t.Fatalf("err = %v, want the refusal with its category and explanation", err)
	}
	if _, ok := errors.AsType[*harness.RunError](err); !ok {
		t.Errorf("err = %v, want it inside a *harness.RunError", err)
	}
}

func TestUnknownAgent(t *testing.T) {
	t.Parallel()
	s := rpc.NewServer()
	if err := runnerrpc.Register(s, map[string]runner.Agent{"b": newHarness(t), "a": newHarness(t)}); err != nil {
		t.Fatal(err)
	}
	c := runnerrpc.NewClient(rpc.Pipe(s))
	names, err := c.Agents(t.Context())
	if err != nil || !slices.Equal(names, []string{"a", "b"}) {
		t.Errorf("Agents = %v, %v; want [a b]", names, err)
	}
	_, err = collect(c.Agent("nope", harness.ContextWindow{}), harness.Input{Message: llm.User(llm.Text{Text: "x"})})
	if !errors.Is(err, runnerrpc.ErrNoAgent) {
		t.Errorf("err = %v, want ErrNoAgent", err)
	}
}

// Stopping reading a remote run stops the run on the server.
func TestStoppingStopsTheRemoteRun(t *testing.T) {
	t.Parallel()
	ran := make(chan error, 1)
	a := agentFunc(func(ctx context.Context, yield func(harness.Event, error) bool) {
		for {
			if !yield(harness.Event{Kind: harness.EventTextDelta, Turn: 1, Text: "x"}, nil) {
				<-ctx.Done()
				ran <- ctx.Err()
				return
			}
		}
	})
	for ev, err := range remote(t, a).Run(t.Context(), harness.Input{Message: llm.User(llm.Text{Text: "x"})}) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Text == "x" {
			break
		}
	}
	select {
	case err := <-ran:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("server run's context = %v, want canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the server's run did not stop")
	}
}

type agentFunc func(ctx context.Context, yield func(harness.Event, error) bool)

func (f agentFunc) Run(ctx context.Context, _ harness.Input) iter.Seq2[harness.Event, error] {
	return func(yield func(harness.Event, error) bool) { f(ctx, yield) }
}

// A runner that cannot be reached fails the run as a retryable model error,
// so the runner retries it as it would a dropped model connection.
func TestUnreachableRunnerIsRetryable(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(rpc.NewServer())
	url := srv.URL
	srv.Close()
	conn, err := rpc.Dial(url)
	if err != nil {
		t.Fatal(err)
	}
	_, err = collect(runnerrpc.NewClient(conn).Agent("default", harness.ContextWindow{}), harness.Input{Message: llm.User(llm.Text{Text: "x"})})
	if !llm.IsRetryable(err) || !errors.Is(err, rpc.ErrUnavailable) {
		t.Errorf("err = %v, want retryable and ErrUnavailable", err)
	}
}

// A runner shutting down cuts its runs; their callers retry them.
func TestRunnerShutdownIsRetryable(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	a := agentFunc(func(ctx context.Context, yield func(harness.Event, error) bool) {
		close(started)
		<-ctx.Done()
		yield(harness.Event{}, ctx.Err())
	})
	s := rpc.NewServer()
	if err := runnerrpc.Register(s, map[string]runner.Agent{"default": a}); err != nil {
		t.Fatal(err)
	}
	go func() { <-started; s.Shutdown() }()
	_, err := collect(runnerrpc.NewClient(rpc.Pipe(s)).Agent("default", harness.ContextWindow{}), harness.Input{Message: llm.User(llm.Text{Text: "x"})})
	if !llm.IsRetryable(err) || errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want retryable, not canceled", err)
	}
}

// A remote agent compacts on the server, and decides ahead of time by the
// window it was given.
func TestRemoteCompact(t *testing.T) {
	t.Parallel()
	s := rpc.NewServer()
	h := newHarness(t, llmtest.Text("the summary").WithUsage(llm.Usage{InputTokens: 4}))
	plain := agentFunc(func(context.Context, func(harness.Event, error) bool) {})
	if err := runnerrpc.Register(s, map[string]runner.Agent{"h": h, "plain": plain}); err != nil {
		t.Fatal(err)
	}
	c := runnerrpc.NewClient(rpc.Pipe(s))
	a := c.Agent("h", harness.ContextWindow{Tokens: 10, Share: 0.5})
	got, err := a.Compact(t.Context(), harness.Transcript{harness.UserText("hi"), llm.Assistant(llm.Text{Text: "hello"})})
	if err != nil || !reflect.DeepEqual(got, harness.Summary{Text: "the summary", Usage: llm.Usage{InputTokens: 4}}) {
		t.Errorf("Compact = %+v, %v", got, err)
	}
	if a.ShouldCompact(llm.Usage{InputTokens: 4}) || !a.ShouldCompact(llm.Usage{InputTokens: 5}) {
		t.Errorf("ShouldCompact does not follow the window")
	}
	if _, err := c.Agent("plain", harness.ContextWindow{}).Compact(t.Context(), harness.Transcript{harness.UserText("hi")}); !errors.Is(err, runnerrpc.ErrCannotCompact) {
		t.Errorf("Compact of an agent that cannot = %v, want %v", err, runnerrpc.ErrCannotCompact)
	}
	if _, err := c.Agent("nope", harness.ContextWindow{}).Compact(t.Context(), harness.Transcript{harness.UserText("hi")}); !errors.Is(err, runnerrpc.ErrNoAgent) {
		t.Errorf("Compact of an unknown agent = %v, want %v", err, runnerrpc.ErrNoAgent)
	}
	if _, err := c.Agent("h", harness.ContextWindow{}).Compact(t.Context(), nil); !errors.Is(err, harness.ErrInvalidInput) {
		t.Errorf("Compact of nothing = %v, want %v", err, harness.ErrInvalidInput)
	}
}
