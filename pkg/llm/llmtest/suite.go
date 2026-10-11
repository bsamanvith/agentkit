package llmtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// Scenario is one conversation the conformance suite runs. The model returned
// by the adapter's factory must answer its Generate calls with Replies, one
// per call, in order: an adapter test serves them from a replay server
// (typically from a recorded fixture named after Name) and asserts nothing
// else about the wire.
type Scenario struct {
	// Name identifies the scenario: "text", "tool_call", "opaque", "cancel",
	// "cancelled_context", "error_rate_limited", "error_overloaded",
	// "error_invalid_request", "error_auth", "error_context_too_long" and
	// "error_mid_stream".
	Name string
	// Replies are the model's answers, in call order. A reply with Err set
	// stands for the provider's failure that maps to Err; Stall stands for a
	// server that stops answering after its first event.
	Replies []Reply
}

// Option configures [RunModel].
type Option func(*config)

type config struct {
	opaque llm.Opaque
}

// WithOpaque sets the opaque block the "opaque" scenario expects the model to
// return, for adapters whose recorded fixture has a block of their own
// provider (for example a Claude thinking block). The default is a block of a
// made-up provider whose data is not compact and contains <, & and non-ASCII
// text.
func WithOpaque(o llm.Opaque) Option { return func(c *config) { c.opaque = o } }

// watchdog is how long the suite waits for a model that should have stopped.
const watchdog = 10 * time.Second

// RunModel runs the conformance suite against a model implementation. For each
// scenario it calls newModel, which must return a model that answers with the
// scenario's replies; see [Scenario].
//
// The suite checks that a model streams events in the order [llm.EventKind]
// describes and consistently with its final message; that text, tool calls,
// usage, stop reasons and [llm.Opaque] blocks come through intact; that a tool
// call can be answered and the conversation continued; that the request is
// never modified; that cancellation ends the stream with the context's error;
// and that failures map to the [llm] sentinels.
func RunModel(t *testing.T, newModel func(t *testing.T, s Scenario) llm.Model, opts ...Option) {
	t.Helper()
	cfg := config{opaque: llm.Opaque{
		Provider: "fake",
		Data:     json.RawMessage(`{"type": "thinking", "thinking": "2 < 3 && 5 > 4: naïve ✓", "signature": "EqQBCkYI+/Zg=="}`),
	}}
	for _, o := range opts {
		o(&cfg)
	}

	run := func(name string, replies []Reply, body func(t *testing.T, m llm.Model)) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			body(t, newModel(t, Scenario{Name: name, Replies: replies}))
		})
	}

	textReply := Text("Hello from the model, streamed in several pieces.").
		WithUsage(llm.Usage{InputTokens: 12, OutputTokens: 9, CacheReadTokens: 3, CacheWriteTokens: 4})
	run("text", []Reply{textReply}, func(t *testing.T, m llm.Model) {
		got := generate(t, m, userRequest("Say hello."), textReply)
		if got.Stop.Kind != llm.StopEndTurn {
			t.Errorf("stop = %+v, want end_turn", got.Stop)
		}
	})

	call := llm.ToolUse{ID: "toolu_1", Name: "echo", Input: json.RawMessage(`{"text":"hi"}`)}
	first := Reply{Response: llm.Response{
		Message: llm.Assistant(llm.Text{Text: "Let me check."}, call),
		Stop:    llm.StopReason{Kind: llm.StopToolUse},
		Usage:   llm.Usage{InputTokens: 20, OutputTokens: 15},
	}}
	second := Text("The tool said: hi")
	run("tool_call", []Reply{first, second}, func(t *testing.T, m llm.Model) {
		req := userRequest("Echo hi.")
		req.Tools = []tool.Definition{{
			Name:        "echo",
			Description: "Repeat the input text.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		}}
		req.MaxTokens = 256
		got := generate(t, m, req, first)
		if got.Stop.Kind != llm.StopToolUse {
			t.Errorf("stop = %+v, want tool_use", got.Stop)
		}
		req.Messages = append(req.Messages, got.Message,
			llm.User(llm.ToolResult{ToolUseID: call.ID, Content: "hi"}))
		generate(t, m, req, second)
	})

	withOpaque := Reply{Response: llm.Response{
		Message: llm.Assistant(cfg.opaque, llm.Text{Text: "Done thinking."}),
		Stop:    llm.StopReason{Kind: llm.StopEndTurn},
	}}
	run("opaque", []Reply{withOpaque, Text("Still here.")}, func(t *testing.T, m llm.Model) {
		req := userRequest("Think about it.")
		got := generate(t, m, req, withOpaque)
		// The next request carries the block back. It must be accepted, and the
		// block must come out of the call unchanged.
		req.Messages = append(req.Messages, got.Message, llm.User(llm.Text{Text: "And now?"}))
		generate(t, m, req, Text("Still here."))
		o, ok := got.Message.Content[0].(llm.Opaque)
		if !ok || o.Provider != cfg.opaque.Provider || !bytes.Equal(o.Data, cfg.opaque.Data) {
			t.Errorf("opaque block = %#v, want %#v", got.Message.Content[0], cfg.opaque)
		}
	})

	run("cancel", []Reply{{Response: textReply.Response, Stall: true}}, func(t *testing.T, m llm.Model) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		events, err := drainWith(t, m.Generate(ctx, userRequest("Hello.")), func(n int) {
			if n == 1 {
				cancel() // after the first event, with the server silent
			}
		})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
		if len(events) == 0 {
			t.Error("no event before the cancellation")
		}
		for _, ev := range events {
			if ev.Kind == llm.EventMessageDone {
				t.Error("message done after cancellation")
			}
		}
	})

	run("cancelled_context", []Reply{textReply}, func(t *testing.T, m llm.Model) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		events, err := drainWith(t, m.Generate(ctx, userRequest("Hello.")), nil)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
		for _, ev := range events {
			if ev.Kind == llm.EventMessageDone {
				t.Error("message done on a cancelled context")
			}
		}
	})

	for _, c := range []struct {
		name      string
		err       error
		retryable bool
	}{
		{"error_rate_limited", llm.ErrRateLimited, true},
		{"error_overloaded", llm.ErrOverloaded, true},
		{"error_invalid_request", llm.ErrInvalidRequest, false},
		{"error_auth", llm.ErrAuth, false},
		{"error_context_too_long", llm.ErrContextTooLong, false},
	} {
		run(c.name, []Reply{Fail(c.err)}, func(t *testing.T, m llm.Model) {
			checkFailure(t, m, c.err, c.retryable)
		})
	}

	midStream := Reply{Response: textReply.Response, Err: llm.ErrOverloaded, After: 2}
	run("error_mid_stream", []Reply{midStream}, func(t *testing.T, m llm.Model) {
		checkFailure(t, m, llm.ErrOverloaded, true)
	})
}

func userRequest(text string) llm.Request {
	return llm.Request{
		System:   "You are a test.",
		Messages: []llm.Message{llm.User(llm.Text{Text: text})},
	}
}

// generate runs one request to the end, checks the stream and the request,
// and that the response equals want's.
func generate(t *testing.T, m llm.Model, req llm.Request, want Reply) llm.Response {
	t.Helper()
	snapshot := req.Clone()
	events, err := drainWith(t, m.Generate(t.Context(), req), nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	got, err := checkStream(events)
	if err != nil {
		t.Fatalf("event stream: %v", err)
	}
	if !reflect.DeepEqual(got, want.Response) {
		t.Errorf("response:\n got %#v\nwant %#v", got, want.Response)
	}
	if !reflect.DeepEqual(req, snapshot) {
		t.Errorf("the model modified its request:\n got %#v\nwant %#v", req, snapshot)
	}
	return got
}

// checkFailure checks a stream that must fail with want, and that Collect then
// returns that error and no response.
func checkFailure(t *testing.T, m llm.Model, want error, retryable bool) {
	t.Helper()
	seq := m.Generate(t.Context(), userRequest("Hello."))
	events, err := drainWith(t, seq, nil)
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	for _, other := range []error{llm.ErrRateLimited, llm.ErrOverloaded, llm.ErrInvalidRequest, llm.ErrAuth, llm.ErrContextTooLong} {
		if !errors.Is(want, other) && errors.Is(err, other) {
			t.Errorf("err also matches %v", other)
		}
	}
	if llm.IsRetryable(err) != retryable {
		t.Errorf("IsRetryable = %v, want %v", !retryable, retryable)
	}
	for _, ev := range events {
		if ev.Kind == llm.EventMessageDone {
			t.Error("message done in a failed stream")
		}
	}
}

// drainWith reads a stream until it ends or fails, calling each (if not nil)
// with the number of events read so far after each event. It fails the test
// if the stream does not end in time.
func drainWith(t *testing.T, seq func(yield func(llm.Event, error) bool), each func(n int)) ([]llm.Event, error) {
	t.Helper()
	type result struct {
		events []llm.Event
		err    error
	}
	done := make(chan result, 1)
	go func() {
		var r result
		for ev, err := range seq {
			if err != nil {
				r.err = err
				break
			}
			r.events = append(r.events, ev)
			if each != nil {
				each(len(r.events))
			}
		}
		done <- r
	}()
	select {
	case r := <-done:
		return r.events, r.err
	case <-time.After(watchdog):
		t.Fatalf("stream did not end within %v", watchdog)
		return nil, nil
	}
}

// checkStream verifies the order and consistency rules of [llm.EventKind] and
// returns the final response.
func checkStream(events []llm.Event) (llm.Response, error) {
	var (
		blocks   []llm.Block // finished blocks, from block-done events
		kind     llm.EventKind
		text     []byte
		input    []byte
		started  llm.ToolUse
		inBlock  bool
		finished *llm.Response
	)
	for i, ev := range events {
		if finished != nil {
			return llm.Response{}, fmt.Errorf("event %d (kind %d) after message done", i, ev.Kind)
		}
		if ev.Kind != llm.EventMessageDone && ev.Index != len(blocks) {
			return llm.Response{}, fmt.Errorf("event %d: index %d, want %d (blocks are streamed in order)", i, ev.Index, len(blocks))
		}
		switch ev.Kind {
		case llm.EventTextDelta:
			if inBlock && kind != llm.EventTextDelta {
				return llm.Response{}, fmt.Errorf("event %d: text delta inside a tool-use block", i)
			}
			inBlock, kind = true, llm.EventTextDelta
			text = append(text, ev.Text...)
		case llm.EventToolUseStart:
			if inBlock {
				return llm.Response{}, fmt.Errorf("event %d: tool-use start inside another block", i)
			}
			tu, ok := ev.Block.(llm.ToolUse)
			if !ok || tu.ID == "" || tu.Name == "" || len(tu.Input) != 0 {
				return llm.Response{}, fmt.Errorf("event %d: tool-use start block = %#v, want a ToolUse with ID and Name only", i, ev.Block)
			}
			inBlock, kind, started, input = true, llm.EventToolUseStart, tu, nil
		case llm.EventToolInputDelta:
			if !inBlock || kind != llm.EventToolUseStart {
				return llm.Response{}, fmt.Errorf("event %d: tool input delta outside a tool-use block", i)
			}
			input = append(input, ev.Text...)
		case llm.EventBlockDone:
			if err := checkBlock(ev.Block, inBlock, kind, text, input, started); err != nil {
				return llm.Response{}, fmt.Errorf("event %d: %w", i, err)
			}
			blocks = append(blocks, ev.Block)
			inBlock, kind, text, input = false, 0, nil, nil
		case llm.EventMessageDone:
			if inBlock {
				return llm.Response{}, fmt.Errorf("event %d: message done inside a block", i)
			}
			if ev.Response == nil {
				return llm.Response{}, fmt.Errorf("event %d: message done without a response", i)
			}
			finished = ev.Response
		default:
			return llm.Response{}, fmt.Errorf("event %d: unknown kind %d", i, ev.Kind)
		}
	}
	if finished == nil {
		return llm.Response{}, errors.New("no message done event")
	}
	if finished.Message.Role != llm.RoleAssistant {
		return llm.Response{}, fmt.Errorf("final message role = %q, want assistant", finished.Message.Role)
	}
	if finished.Stop.Kind == "" {
		return llm.Response{}, errors.New("final response has no stop reason")
	}
	if !reflect.DeepEqual(finished.Message.Content, blocks) && (len(blocks) != 0 || len(finished.Message.Content) != 0) {
		return llm.Response{}, fmt.Errorf("final message content differs from the block-done events:\n content %#v\n events  %#v", finished.Message.Content, blocks)
	}
	return *finished, nil
}

// checkBlock checks a finished block against the deltas streamed for it.
func checkBlock(b llm.Block, inBlock bool, kind llm.EventKind, text, input []byte, started llm.ToolUse) error {
	switch b := b.(type) {
	case llm.Text:
		if inBlock && kind != llm.EventTextDelta {
			return errors.New("text block finished inside a tool-use block")
		}
		if b.Text != string(text) {
			return fmt.Errorf("text block %q differs from its deltas %q", b.Text, text)
		}
	case llm.ToolUse:
		if !inBlock || kind != llm.EventToolUseStart {
			return errors.New("tool-use block finished without a start event")
		}
		if b.ID != started.ID || b.Name != started.Name {
			return fmt.Errorf("tool use %s/%s differs from its start %s/%s", b.ID, b.Name, started.ID, started.Name)
		}
		var want, got bytes.Buffer
		if err := json.Compact(&want, b.Input); err != nil {
			return fmt.Errorf("tool input is not valid JSON: %w", err)
		}
		if len(input) > 0 {
			if err := json.Compact(&got, input); err != nil {
				return fmt.Errorf("tool input deltas are not valid JSON together: %w", err)
			}
			if !bytes.Equal(want.Bytes(), got.Bytes()) {
				return fmt.Errorf("tool input %s differs from its deltas %s", want.Bytes(), got.Bytes())
			}
		}
	case llm.Opaque:
		if inBlock {
			return errors.New("opaque block finished inside another block")
		}
	default:
		return fmt.Errorf("unexpected block %#v in an assistant message", b)
	}
	return nil
}
