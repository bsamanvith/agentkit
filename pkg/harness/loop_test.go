package harness_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/tool/tooltest"
)

func TestRunWithoutTools(t *testing.T) {
	leakcheck(t)
	usage := llm.Usage{InputTokens: 10, OutputTokens: 3}
	fake := llmtest.NewFake(t, llmtest.Text("hello there").WithUsage(usage))
	h := newHarness(t, fake, newRegistry(t, tooltest.Fixtures()...), harness.WithSystem("be brief"))

	_, res, err := run(t, h, prompt("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Final.Text(); got != "hello there" {
		t.Errorf("Final = %q", got)
	}
	if res.Turns != 1 || res.Usage != usage {
		t.Errorf("Turns = %d, Usage = %+v", res.Turns, res.Usage)
	}
	diff(t, "transcript", harness.Transcript{
		harness.UserText("hi"), llm.Assistant(llm.Text{Text: "hello there"}),
	}, res.Transcript)

	reqs := fake.Requests()
	if len(reqs) != 1 || reqs[0].System != "be brief" {
		t.Fatalf("requests = %+v", reqs)
	}
	var names []string
	for _, d := range reqs[0].Tools {
		names = append(names, d.Name)
	}
	diff(t, "tools sent to the model", []string{"block", "echo", "fail"}, names)
	if fake.Remaining() != 0 {
		t.Errorf("%d replies unused", fake.Remaining())
	}
}

func TestToolFailuresBecomeErrorResults(t *testing.T) {
	leakcheck(t)
	boom := fn(t, "boom", func(context.Context) (string, error) { panic("kaboom") })
	reg := newRegistry(t, append(tooltest.Fixtures(), boom)...)
	fake := llmtest.NewFake(t,
		llmtest.ToolCalls(
			use("c1", "echo", `{"text":"hi"}`),
			use("c2", "fail", `{}`),
			use("c3", "echo", `{"text":5}`), // wrong type
			use("c4", "no_such_tool", `{}`),
			use("c5", "boom", `{}`),
		),
		llmtest.Text("I saw the errors."),
	)
	h := newHarness(t, fake, reg)

	_, res, err := run(t, h, prompt("go"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Turns != 2 || res.Final.Text() != "I saw the errors." {
		t.Fatalf("the run did not continue after tool errors: turns %d, final %q", res.Turns, res.Final.Text())
	}
	got := results(t, fake.Requests()[1].Messages[2])
	wantErr := []bool{false, true, true, true, true}
	if len(got) != len(wantErr) {
		t.Fatalf("%d results, want %d", len(got), len(wantErr))
	}
	for i, r := range got {
		if r.ToolUseID != "c"+string(rune('1'+i)) || r.IsError != wantErr[i] {
			t.Errorf("result %d = %+v, want id c%d, IsError %v", i, r, i+1, wantErr[i])
		}
	}
	if !strings.Contains(got[1].Content, tooltest.ErrFixture.Error()) {
		t.Errorf("failure content = %q, want the tool's error", got[1].Content)
	}
	// A panic reaches the model as its message, never as a stack trace.
	if c := got[4].Content; !strings.Contains(c, "kaboom") ||
		strings.Contains(c, "goroutine") || strings.Contains(c, ".go:") || strings.Contains(c, "runtime.") {
		t.Errorf("panic content = %q, want the panic value without a stack", c)
	}
	var recorded []bool
	var kinds []tool.Kind
	for _, tr := range res.Record.Turns[0].Tools {
		recorded = append(recorded, tr.IsError)
		kinds = append(kinds, tr.Kind)
	}
	diff(t, "ToolRecord.IsError", wantErr, recorded)
	// echo declares read; fail and boom declare nothing; no_such_tool is not
	// listed. Only a declared read is recorded as one.
	diff(t, "ToolRecord.Kind", []tool.Kind{tool.KindRead, tool.KindWrite, tool.KindRead, tool.KindWrite, tool.KindWrite}, kinds)
}

// replies of the 3-turn script used by the append-only and resume tests.
func threeTurns() (replies []llmtest.Reply, opaque llm.Opaque) {
	// Not compact, with characters encoders like to rewrite.
	opaque = llm.Opaque{Provider: "test", Data: json.RawMessage("{ \"sig\" :  \"a<b>&c é\",\n \"n\": 1e2 }")}
	u := llm.Usage{InputTokens: 10, OutputTokens: 5}
	return []llmtest.Reply{
		reply(llm.StopToolUse, u, opaque, llm.Text{Text: "checking"}, use("c1", "echo", `{"text":"one"}`)),
		reply(llm.StopToolUse, u, use("c2", "echo", `{"text":"two"}`), use("c3", "echo", `{"text":"three"}`)),
		reply(llm.StopEndTurn, u, llm.Text{Text: "done"}),
	}, opaque
}

func TestHistoryIsAppendOnly(t *testing.T) {
	leakcheck(t)
	replies, opaque := threeTurns()
	fake := llmtest.NewFake(t, replies...)
	h := newHarness(t, fake, newRegistry(t, tooltest.Fixtures()...))

	_, res, err := run(t, h, prompt("start"))
	if err != nil {
		t.Fatal(err)
	}
	reqs := fake.Requests()
	if len(reqs) != 3 || fake.Remaining() != 0 {
		t.Fatalf("%d requests, %d replies left", len(reqs), fake.Remaining())
	}
	for n := 1; n < len(reqs); n++ {
		prev, cur := reqs[n-1].Messages, reqs[n].Messages
		if len(cur) != len(prev)+2 {
			t.Fatalf("request %d has %d messages, want %d", n, len(cur), len(prev)+2)
		}
		diff(t, "earlier messages", prev, cur[:len(prev)])
		diff(t, "the model's previous reply", replies[n-1].Response.Message, cur[len(prev)])
		if got := results(t, cur[len(prev)+1]); len(got) != len(toolUsesOf(replies[n-1])) {
			t.Errorf("request %d: %d results for %d calls", n, len(got), len(toolUsesOf(replies[n-1])))
		}
	}
	// The opaque block comes back byte for byte, in every later request.
	for n := 1; n < len(reqs); n++ {
		got := reqs[n].Messages[1].Content[0].(llm.Opaque)
		if got.Provider != opaque.Provider || !bytes.Equal(got.Data, opaque.Data) {
			t.Errorf("request %d: opaque block changed: %q", n, got.Data)
		}
	}
	diff(t, "final transcript starts with the last request", reqs[2].Messages, []llm.Message(res.Transcript[:5]))
	if len(res.Transcript) != 6 {
		t.Errorf("transcript has %d messages, want 6", len(res.Transcript))
	}
}

func toolUsesOf(r llmtest.Reply) []llm.ToolUse {
	var out []llm.ToolUse
	for _, b := range r.Response.Message.Content {
		if u, ok := b.(llm.ToolUse); ok {
			out = append(out, u)
		}
	}
	return out
}

func TestBudgetExceeded(t *testing.T) {
	leakcheck(t)
	u := llm.Usage{InputTokens: 10, CacheReadTokens: 20, CacheWriteTokens: 5, OutputTokens: 30} // 65 in all
	call := reply(llm.StopToolUse, u, use("c1", "echo", `{"text":"x"}`))
	call2 := reply(llm.StopToolUse, u, use("c2", "echo", `{"text":"x"}`))

	tests := []struct {
		name      string
		opt       harness.Option
		replies   []llmtest.Reply
		lim       limits
		wantTurns int
	}{
		{"turns", harness.WithMaxTurns(2), []llmtest.Reply{call, call2}, limits{maxTurns: 2}, 2},
		// 65 tokens only if cached tokens count too: the limit is 60.
		{"tokens", harness.WithMaxTotalTokens(60), []llmtest.Reply{call}, limits{maxTokens: 60}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := llmtest.NewFake(t, tc.replies...)
			h := newHarness(t, fake, newRegistry(t, tooltest.Fixtures()...), tc.opt)
			h.lim = tc.lim

			_, res, err := run(t, h, prompt("go"))
			if !errors.Is(err, harness.ErrBudgetExceeded) {
				t.Fatalf("err = %v, want ErrBudgetExceeded", err)
			}
			if got := len(fake.Requests()); got != tc.wantTurns {
				t.Errorf("%d model calls, want %d", got, tc.wantTurns)
			}
			// The transcript ends after a whole turn and can be resumed.
			if want := 1 + 2*tc.wantTurns; len(res.Transcript) != want {
				t.Errorf("transcript has %d messages, want %d", len(res.Transcript), want)
			}
			if err := res.Transcript.Validate(); err != nil {
				t.Errorf("partial transcript is not valid: %v", err)
			}
			if res.Turns != tc.wantTurns || len(res.Record.Turns) != tc.wantTurns {
				t.Errorf("Turns = %d, record has %d", res.Turns, len(res.Record.Turns))
			}
			var re *harness.RunError
			if !errors.As(err, &re) {
				t.Fatal("not a *RunError")
			}
			diff(t, "RunError.Transcript", res.Transcript, re.Transcript())
		})
	}
}

func TestCancelMidToolStopsRun(t *testing.T) {
	leakcheck(t)
	fake := llmtest.NewFake(t,
		llmtest.ToolCalls(use("c1", "block", `{}`)),
		llmtest.Text("never asked"),
	)
	h := newHarness(t, fake, newRegistry(t, tooltest.Fixtures()...))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	o := h.drive(ctx, t, prompt("go"), func(ev harness.Event) bool {
		if ev.Kind == harness.EventToolStart {
			cancel() // the tool is running and blocked
		}
		return false
	})
	if !errors.Is(o.err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", o.err)
	}
	if got := len(fake.Requests()); got != 1 || fake.Remaining() != 1 {
		t.Errorf("%d model calls, %d replies left: no further call may happen", got, fake.Remaining())
	}
	// The turn that was interrupted is not in the transcript: only the prompt is.
	diff(t, "transcript", harness.Transcript{harness.UserText("go")}, o.res.Transcript)
}

func TestCancelBetweenTurnsMakesNoModelCall(t *testing.T) {
	leakcheck(t)
	fake := llmtest.NewFake(t,
		llmtest.ToolCalls(use("c1", "echo", `{"text":"x"}`)),
		llmtest.Text("never asked"),
	)
	h := newHarness(t, fake, newRegistry(t, tooltest.Fixtures()...))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	o := h.drive(ctx, t, prompt("go"), func(ev harness.Event) bool {
		if ev.Kind == harness.EventTurnDone {
			cancel() // between two turns: nothing is running
		}
		return false
	})
	if !errors.Is(o.err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", o.err)
	}
	if got := len(fake.Requests()); got != 1 {
		t.Errorf("%d model calls, want 1: a cancelled run asks the model nothing more", got)
	}
	// The turn that finished before the cancel is kept.
	if len(o.res.Transcript) != 3 {
		t.Errorf("transcript has %d messages, want the prompt and the finished turn", len(o.res.Transcript))
	}
}

// Cancelling while some calls wait for a free slot ends the run without
// announcing calls that never began.
func TestCancelWithQueuedToolCalls(t *testing.T) {
	leakcheck(t)
	fake := llmtest.NewFake(t, llmtest.ToolCalls(
		use("c1", "block", `{}`), use("c2", "block", `{}`), use("c3", "block", `{}`)))
	h := newHarness(t, fake, newRegistry(t, tooltest.Fixtures()...), harness.WithMaxParallelTools(1))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var started []int
	o := h.drive(ctx, t, prompt("go"), func(ev harness.Event) bool {
		if ev.Kind == harness.EventToolStart {
			started = append(started, ev.Call)
			cancel() // c1 is running, c2 and c3 wait for its slot
		}
		return false
	})
	if !errors.Is(o.err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", o.err)
	}
	diff(t, "calls announced as started", []int{0}, started)
}

// A tool failure that ends the run does not wait for the other calls of the
// turn, and is reported as the cause rather than their cancellation.
func TestFatalToolErrorCancelsSiblings(t *testing.T) {
	leakcheck(t)
	errDown := errors.New("registry down")
	down := &failingRegistry{inner: newRegistry(t, tooltest.Fixtures()...), name: "echo", err: errDown}
	fake := llmtest.NewFake(t, llmtest.ToolCalls(use("c1", "block", `{}`), use("c2", "echo", `{"text":"x"}`)))
	h := newHarness(t, fake, down)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel() // a harness that waits for c1 would only get past it here
	o := h.drive(ctx, t, prompt("go"), nil)
	if !errors.Is(o.err, errDown) {
		t.Fatalf("err = %v, want the registry's error, not the cancellation of c1", o.err)
	}
	diff(t, "transcript", harness.Transcript{harness.UserText("go")}, o.res.Transcript)
}

// failingRegistry fails calls to one tool with an error the harness does not
// recognize, as a remote registry does when its connection drops.
type failingRegistry struct {
	inner tool.Registry
	name  string
	err   error
}

func (f *failingRegistry) List(ctx context.Context) ([]tool.Definition, error) {
	return f.inner.List(ctx)
}

func (f *failingRegistry) Invoke(ctx context.Context, name string, in json.RawMessage) (json.RawMessage, error) {
	if name == f.name {
		return nil, f.err
	}
	return f.inner.Invoke(ctx, name, in)
}

// A turn whose calls all finished is part of the conversation, even if the
// context is cancelled just after: its tools have run, and dropping it would
// have them run again on resume.
func TestFinishedTurnIsKeptWhenCancelledJustAfter(t *testing.T) {
	leakcheck(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// This tool ignores its context: it cancels the run while it works, and
	// finishes normally.
	rude := fn(t, "rude", func(context.Context) (string, error) { cancel(); return "done anyway", nil })
	fake := llmtest.NewFake(t, llmtest.ToolCalls(use("c1", "rude", `{}`)), llmtest.Text("never asked"))
	h := newHarness(t, fake, newRegistry(t, rude))

	o := h.drive(ctx, t, prompt("go"), nil)
	if !errors.Is(o.err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", o.err)
	}
	if len(o.res.Transcript) != 3 || len(o.res.Record.Turns) != 1 {
		t.Fatalf("transcript has %d messages and %d turns, want the prompt and the finished turn",
			len(o.res.Transcript), len(o.res.Record.Turns))
	}
	diff(t, "the kept results", []llm.ToolResult{{ToolUseID: "c1", Content: `"done anyway"`}}, results(t, o.res.Transcript[2]))
	if len(fake.Requests()) != 1 {
		t.Errorf("%d model calls: a cancelled run asks the model nothing more", len(fake.Requests()))
	}
}

func TestStopReasonsAreTypedErrors(t *testing.T) {
	leakcheck(t)
	text := llm.Text{Text: "partial"}
	dup := []llm.Block{use("same", "echo", `{"text":"a"}`), use("same", "echo", `{"text":"b"}`)}
	tests := []struct {
		name       string
		reply      llmtest.Reply
		want       error
		wantKept   bool // the reply is in the transcript
		wantRefuse *harness.RefusalError
	}{
		{
			"max_tokens", withStop(reply(llm.StopMaxTokens, llm.Usage{}, text), llm.StopReason{Kind: llm.StopMaxTokens}),
			harness.ErrMaxTokens, true, nil,
		},
		{
			"refusal", withStop(reply(llm.StopRefusal, llm.Usage{}, llm.Text{Text: "no"}),
				llm.StopReason{Kind: llm.StopRefusal, Category: "cyber", Explanation: "not allowed"}),
			harness.ErrRefused, true, &harness.RefusalError{Category: "cyber", Explanation: "not allowed"},
		},
		{
			"other", withStop(reply(llm.StopOther, llm.Usage{}, text), llm.StopReason{Kind: llm.StopOther, Raw: "pause_turn"}),
			harness.ErrUnexpectedStop, true, nil,
		},
		{"tool_use_without_calls", reply(llm.StopToolUse, llm.Usage{}, text), harness.ErrUnexpectedStop, false, nil},
		{
			"end_turn_with_calls", reply(llm.StopEndTurn, llm.Usage{}, use("c1", "echo", `{"text":"a"}`)),
			harness.ErrUnexpectedStop, false, nil,
		},
		{"repeated_call_id", reply(llm.StopToolUse, llm.Usage{}, dup...), harness.ErrUnexpectedStop, false, nil},
		// Cut off while calling a tool: the reply has a call nothing can answer.
		{"max_tokens_with_calls", withStop(reply(llm.StopMaxTokens, llm.Usage{}, text, use("c1", "echo", `{"text":"a"}`)),
			llm.StopReason{Kind: llm.StopMaxTokens}), harness.ErrMaxTokens, false, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := llmtest.NewFake(t, tc.reply)
			h := newHarness(t, fake, newRegistry(t, tooltest.Fixtures()...))

			events, res, err := run(t, h, prompt("go"))
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			var rf *harness.RefusalError
			if tc.wantRefuse != nil {
				if !errors.As(err, &rf) || *rf != *tc.wantRefuse {
					t.Errorf("refusal = %+v, want %+v", rf, tc.wantRefuse)
				}
			}
			wantLen := 1
			if tc.wantKept {
				wantLen = 2
			}
			if len(res.Transcript) != wantLen {
				t.Fatalf("transcript has %d messages, want %d", len(res.Transcript), wantLen)
			}
			if err := res.Transcript.Validate(); err != nil {
				t.Errorf("partial transcript is not valid: %v", err)
			}
			// Every message in the transcript was announced exactly once.
			diff(t, "messages announced by events", res.Transcript, announced(events))
		})
	}
}

func withStop(r llmtest.Reply, s llm.StopReason) llmtest.Reply {
	r.Response.Stop = s
	return r
}

// announced rebuilds the transcript from events: the prompt, then the
// messages of every finished turn.
func announced(events []harness.Event) harness.Transcript {
	var out harness.Transcript
	for _, ev := range events {
		switch ev.Kind {
		case harness.EventRunStart:
			if ev.Prompt != nil {
				out = append(out, ev.Prompt.Message)
			}
		case harness.EventTurnDone:
			for _, m := range ev.TurnRecord.Messages {
				out = append(out, m.Message)
			}
		}
	}
	return out
}

func TestHooksShapeWhatTheModelSees(t *testing.T) {
	leakcheck(t)
	var echoed []string // tool executions are recorded here by the tool itself
	echo := fn(t, "echo", func(context.Context) (string, error) { echoed = append(echoed, "echo"); return "real", nil })
	other := fn(t, "other", func(context.Context) (string, error) { return "raw output", nil })
	fake := llmtest.NewFake(t,
		llmtest.ToolCalls(use("c1", "echo", `{}`), use("c2", "other", `{}`)),
		llmtest.Text("ok"),
	)
	h := newHarness(t, fake, newRegistry(t, echo, other),
		harness.WithBeforeTool(func(_ context.Context, c llm.ToolUse) error {
			if c.Name == "echo" {
				return errors.New("policy: echo is not allowed")
			}
			return nil
		}),
		harness.WithAfterTool(func(_ context.Context, c llm.ToolUse, r llm.ToolResult) llm.ToolResult {
			if c.Name == "other" {
				return llm.ToolResult{ToolUseID: "wrong-id", Content: "[redacted]"}
			}
			return r
		}),
	)

	_, _, err := run(t, h, prompt("go"))
	if err != nil {
		t.Fatal(err)
	}
	if len(echoed) != 0 {
		t.Error("a denied tool call was run")
	}
	got := results(t, fake.Requests()[1].Messages[2])
	want := []llm.ToolResult{
		{ToolUseID: "c1", Content: "policy: echo is not allowed", IsError: true},
		{ToolUseID: "c2", Content: "[redacted]"},
	}
	diff(t, "results the model received", want, got)
}

func TestHookPanicEndsRunWithoutCrashing(t *testing.T) {
	leakcheck(t)
	fake := llmtest.NewFake(t, llmtest.ToolCalls(use("c1", "echo", `{"text":"x"}`)))
	h := newHarness(t, fake, newRegistry(t, tooltest.Fixtures()...),
		harness.WithBeforeTool(func(context.Context, llm.ToolUse) error { panic("hook bug") }))

	_, res, err := run(t, h, prompt("go"))
	var pe *tool.PanicError
	if !errors.Is(err, tool.ErrPanic) || !errors.As(err, &pe) || pe.Value != "hook bug" {
		t.Fatalf("err = %v, want a tool.PanicError for the hook's panic", err)
	}
	if len(res.Transcript) != 1 {
		t.Errorf("transcript has %d messages, want only the prompt", len(res.Transcript))
	}
}

func TestEventOrder(t *testing.T) {
	leakcheck(t)
	fake := llmtest.NewFake(t,
		reply(llm.StopToolUse, llm.Usage{}, llm.Text{Text: "Let me look that up now."}, use("c1", "echo", `{"text":"x"}`)),
		llmtest.Text("The answer is 42."),
	)
	h := newHarness(t, fake, newRegistry(t, tooltest.Fixtures()...))

	events, res, err := run(t, h, prompt("q"))
	if err != nil {
		t.Fatal(err)
	}
	// Runs of text deltas collapse into one entry; the text of each is kept.
	var order []string
	texts := map[int]string{}
	for _, ev := range events {
		if ev.Kind == harness.EventTextDelta {
			texts[ev.Turn] += ev.Text
			if n := len(order); n == 0 || order[n-1] != "text_delta" {
				order = append(order, "text_delta")
			}
			continue
		}
		order = append(order, ev.Kind.String())
	}
	diff(t, "event order", []string{
		"run_start", "text_delta", "tool_start", "tool_done", "turn_done", "text_delta", "turn_done", "run_done",
	}, order)
	diff(t, "streamed text", map[int]string{1: "Let me look that up now.", 2: "The answer is 42."}, texts)
	if texts[2] != res.Final.Text() {
		t.Errorf("deltas = %q, final text = %q", texts[2], res.Final.Text())
	}
	for i := 1; i < len(events); i++ {
		if events[i].At.Before(events[i-1].At) {
			t.Errorf("event %d (%v) is earlier than event %d", i, events[i].Kind, i-1)
		}
	}
	for _, ev := range events {
		if ev.Kind == harness.EventToolStart || ev.Kind == harness.EventToolDone {
			if ev.Turn != 1 || ev.Call != 0 {
				t.Errorf("%v event is for turn %d call %d, want turn 1 call 0", ev.Kind, ev.Turn, ev.Call)
			}
		}
	}
}

func TestModelErrorIsWrappedAndNotRetried(t *testing.T) {
	leakcheck(t)
	slow := fmtErr(llm.ErrRateLimited, "slow down")
	fake := llmtest.NewFake(t, llmtest.Fail(slow), llmtest.Text("a retry would get this"))
	h := newHarness(t, fake, newRegistry(t, tooltest.Fixtures()...))

	_, res, err := run(t, h, prompt("go"))
	if !errors.Is(err, llm.ErrRateLimited) || !llm.IsRetryable(err) {
		t.Fatalf("err = %v, want a retryable rate-limit error", err)
	}
	if len(fake.Requests()) != 1 {
		t.Errorf("%d model calls, want 1: the harness does not retry", len(fake.Requests()))
	}
	diff(t, "transcript", harness.Transcript{harness.UserText("go")}, res.Transcript)
}

func fmtErr(sentinel error, msg string) error { return errors.Join(sentinel, errors.New(msg)) }

func TestNewRejectsBadConfig(t *testing.T) {
	fake := llmtest.NewFake(t)
	reg := newRegistry(t)
	tests := []struct {
		name  string
		model llm.Model
		reg   tool.Registry
		opts  []harness.Option
	}{
		{"no model", nil, reg, nil},
		{"no registry", fake, nil, nil},
		{"zero turns", fake, reg, []harness.Option{harness.WithMaxTurns(0)}},
		{"negative tokens", fake, reg, []harness.Option{harness.WithMaxTotalTokens(-1)}},
		{"zero parallelism", fake, reg, []harness.Option{harness.WithMaxParallelTools(0)}},
		{"nil clock", fake, reg, []harness.Option{harness.WithClock(nil)}},
		{"nil hook", fake, reg, []harness.Option{harness.WithBeforeTool(nil)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := harness.New(tc.model, tc.reg, tc.opts...); !errors.Is(err, harness.ErrInvalidConfig) {
				t.Errorf("err = %v, want ErrInvalidConfig", err)
			}
		})
	}
}
