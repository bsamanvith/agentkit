package llm_test

import (
	"errors"
	"fmt"
	"iter"
	"reflect"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/llm"
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
