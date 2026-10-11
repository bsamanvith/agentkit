package llm_test

import (
	"errors"
	"fmt"
	"iter"
	"reflect"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
)

// stream returns a model stream that yields the given events, then err if
// set. It reports through *stopped whether the consumer stopped early.
func stream(stopped *bool, err error, events ...llm.Event) iter.Seq2[llm.Event, error] {
	return func(yield func(llm.Event, error) bool) {
		for _, ev := range events {
			if !yield(ev, nil) {
				*stopped = true
				return
			}
		}
		if err != nil {
			yield(llm.Event{}, err)
		}
	}
}

func TestCollect(t *testing.T) {
	t.Parallel()
	call := llm.ToolUse{ID: "toolu_1", Name: "search", Input: []byte(`{"q":"go"}`)}
	want := llm.Response{
		Message: llm.Assistant(llm.Text{Text: "Hello, world"}, call),
		Stop:    llm.StopReason{Kind: llm.StopToolUse},
		Usage:   llm.Usage{InputTokens: 10, OutputTokens: 7, CacheReadTokens: 2},
	}
	var stopped bool
	got, err := llm.Collect(stream(&stopped, nil,
		llm.Event{Kind: llm.EventTextDelta, Index: 0, Text: "Hello, "},
		llm.Event{Kind: llm.EventTextDelta, Index: 0, Text: "world"},
		llm.Event{Kind: llm.EventBlockDone, Index: 0, Block: llm.Text{Text: "Hello, world"}},
		llm.Event{Kind: llm.EventToolUseStart, Index: 1, Block: llm.ToolUse{ID: "toolu_1", Name: "search"}},
		llm.Event{Kind: llm.EventToolInputDelta, Index: 1, Text: `{"q":`},
		llm.Event{Kind: llm.EventToolInputDelta, Index: 1, Text: `"go"}`},
		llm.Event{Kind: llm.EventBlockDone, Index: 1, Block: call},
		llm.Event{Kind: llm.EventMessageDone, Response: &want},
		llm.Event{Kind: llm.EventTextDelta, Text: "never read"},
	))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("response:\n got %#v\nwant %#v", got, want)
	}
	if !stopped {
		t.Error("Collect kept reading after the message was done")
	}
	if got.Message.Text() != "Hello, world" {
		t.Errorf("Text() = %q", got.Message.Text())
	}
}

// Collect over a streamed reply equals the same reply handed over whole, and
// the result does not share memory with the stream's event.
func TestCollectStreamedEqualsWhole(t *testing.T) {
	t.Parallel()
	reply := llmtest.ToolCalls(
		llm.ToolUse{ID: "a", Name: "one", Input: []byte(`{"x":1}`)},
		llm.ToolUse{ID: "b", Name: "two"},
	)
	reply.Response.Message.Content = append([]llm.Block{llm.Text{Text: "Thinking out loud, in pieces."}}, reply.Response.Message.Content...)
	reply.Response.Usage = llm.Usage{InputTokens: 3, OutputTokens: 4}

	got, err := llm.Collect(llmtest.NewFake(t, reply).Generate(t.Context(), llm.Request{}))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, reply.Response) {
		t.Errorf("streamed:\n got %#v\nwant %#v", got, reply.Response)
	}

	var shared llm.Response
	seq := func(yield func(llm.Event, error) bool) {
		shared = llm.Response{Message: llm.Assistant(llm.ToolUse{ID: "a", Name: "n", Input: []byte(`{"k":1}`)})}
		yield(llm.Event{Kind: llm.EventMessageDone, Response: &shared}, nil)
	}
	out, err := llm.Collect(seq)
	if err != nil {
		t.Fatal(err)
	}
	out.Message.Content[0].(llm.ToolUse).Input[2] = 'X'
	if string(shared.Message.Content[0].(llm.ToolUse).Input) != `{"k":1}` {
		t.Error("changing the collected response changed the event's response")
	}
}

func TestCollectStreamError(t *testing.T) {
	t.Parallel()
	partial := llm.Response{Message: llm.Assistant(llm.Text{Text: "Hel"})}
	cause := fmt.Errorf("%w: %w", llm.ErrOverloaded, errors.New("provider says 529"))
	var stopped bool
	got, err := llm.Collect(stream(&stopped, cause,
		llm.Event{Kind: llm.EventTextDelta, Index: 0, Text: "Hel"},
		llm.Event{Kind: llm.EventBlockDone, Index: 0, Block: llm.Text{Text: "Hel"}},
	))
	if !errors.Is(err, llm.ErrOverloaded) {
		t.Fatalf("err = %v, want ErrOverloaded", err)
	}
	if !reflect.DeepEqual(got, llm.Response{}) {
		t.Errorf("response = %#v, want none (not the partial %#v)", got, partial)
	}
}

func TestCollectIncompleteStream(t *testing.T) {
	t.Parallel()
	var stopped bool
	_, err := llm.Collect(stream(&stopped, nil, llm.Event{Kind: llm.EventTextDelta, Text: "Hel"}))
	if !errors.Is(err, llm.ErrIncompleteStream) {
		t.Errorf("err = %v, want ErrIncompleteStream", err)
	}
}

func TestIsRetryable(t *testing.T) {
	t.Parallel()
	other := errors.New("boom")
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"rate limited", llm.ErrRateLimited, true},
		{"overloaded", llm.ErrOverloaded, true},
		{"rate limited wrapped", fmt.Errorf("anthropic: %w", llm.ErrRateLimited), true},
		{"overloaded wrapped twice", fmt.Errorf("a: %w", fmt.Errorf("b: %w", llm.ErrOverloaded)), true},
		{"wrapped with the provider error", fmt.Errorf("%w: %w", llm.ErrRateLimited, other), true},
		{"joined", errors.Join(other, llm.ErrOverloaded), true},
		{"invalid request", llm.ErrInvalidRequest, false},
		{"auth", llm.ErrAuth, false},
		{"context too long", llm.ErrContextTooLong, false},
		{"invalid request wrapped", fmt.Errorf("x: %w", llm.ErrInvalidRequest), false},
		{"unrelated", other, false},
		{"nil", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := llm.IsRetryable(tc.err); got != tc.want {
				t.Errorf("IsRetryable = %v, want %v", got, tc.want)
			}
		})
	}
}
