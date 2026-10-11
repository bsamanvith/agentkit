// White-box test: the stream checker is internal to the suite, and the suite
// can only be run against models that conform. This feeds it streams that
// break each rule, so the suite is known to notice.
package llmtest

import (
	"encoding/json"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/llm"
)

func TestCheckStream(t *testing.T) {
	t.Parallel()
	text := llm.Text{Text: "hi"}
	call := llm.ToolUse{ID: "t", Name: "n", Input: json.RawMessage(`{"a":1}`)}
	done := func(content ...llm.Block) llm.Event {
		return llm.Event{Kind: llm.EventMessageDone, Response: &llm.Response{
			Message: llm.Assistant(content...),
			Stop:    llm.StopReason{Kind: llm.StopEndTurn},
		}}
	}
	delta := func(i int, s string) llm.Event { return llm.Event{Kind: llm.EventTextDelta, Index: i, Text: s} }
	blockDone := func(i int, b llm.Block) llm.Event { return llm.Event{Kind: llm.EventBlockDone, Index: i, Block: b} }
	start := llm.Event{Kind: llm.EventToolUseStart, Index: 0, Block: llm.ToolUse{ID: "t", Name: "n"}}
	input := func(s string) llm.Event { return llm.Event{Kind: llm.EventToolInputDelta, Index: 0, Text: s} }

	good := map[string][]llm.Event{
		"text":              {delta(0, "h"), delta(0, "i"), blockDone(0, text), done(text)},
		"tool use":          {start, input(`{"a"`), input(`:1}`), blockDone(0, call), done(call)},
		"tool use no input": {{Kind: llm.EventToolUseStart, Block: llm.ToolUse{ID: "t", Name: "n"}}, blockDone(0, llm.ToolUse{ID: "t", Name: "n", Input: json.RawMessage(`{}`)}), done(llm.ToolUse{ID: "t", Name: "n", Input: json.RawMessage(`{}`)})},
		"empty message":     {done()},
		"opaque":            {blockDone(0, llm.Opaque{Provider: "p", Data: json.RawMessage(`{}`)}), done(llm.Opaque{Provider: "p", Data: json.RawMessage(`{}`)})},
	}
	for name, events := range good {
		t.Run("good/"+name, func(t *testing.T) {
			t.Parallel()
			if _, err := checkStream(events); err != nil {
				t.Errorf("rejected a conforming stream: %v", err)
			}
		})
	}

	bad := map[string][]llm.Event{
		"no message done":           {delta(0, "hi"), blockDone(0, text)},
		"event after done":          {delta(0, "hi"), blockDone(0, text), done(text), delta(1, "x")},
		"done first":                {done(), delta(0, "hi")},
		"wrong index":               {delta(1, "hi"), blockDone(1, text), done(text)},
		"block differs from deltas": {delta(0, "ho"), blockDone(0, text), done(text)},
		"final differs from blocks": {delta(0, "hi"), blockDone(0, text), done(llm.Text{Text: "other"})},
		"missing block in final":    {delta(0, "hi"), blockDone(0, text), done()},
		"tool input dropped":        {start, blockDone(0, call), done(call)},
		"tool input differs":        {start, input(`{"a":2}`), blockDone(0, call), done(call)},
		"tool input not json":       {start, input(`{"a"`), blockDone(0, llm.ToolUse{ID: "t", Name: "n", Input: json.RawMessage(`{"a"`)}), done()},
		"tool done without start":   {blockDone(0, call), done(call)},
		"tool start with input":     {{Kind: llm.EventToolUseStart, Block: call}, blockDone(0, call), done(call)},
		"input delta outside":       {input(`{}`), done()},
		"text inside tool block":    {start, delta(0, "x"), blockDone(0, call), done(call)},
		"done inside a block":       {delta(0, "hi"), done(text)},
		"no response":               {delta(0, "hi"), blockDone(0, text), {Kind: llm.EventMessageDone}},
		"no stop reason":            {{Kind: llm.EventMessageDone, Response: &llm.Response{Message: llm.Assistant()}}},
		"user role":                 {{Kind: llm.EventMessageDone, Response: &llm.Response{Message: llm.User(), Stop: llm.StopReason{Kind: llm.StopEndTurn}}}},
		"tool result from model":    {blockDone(0, llm.ToolResult{ToolUseID: "t"}), done()},
		"unknown kind":              {{Kind: 99}},
	}
	for name, events := range bad {
		t.Run("bad/"+name, func(t *testing.T) {
			t.Parallel()
			if _, err := checkStream(events); err == nil {
				t.Error("accepted a stream that breaks the contract")
			}
		})
	}
}

// A model that panics fails the test that runs it; it does not crash the run.
func TestDrainWithReportsModelPanic(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	events, err := drainWith(rec, func(yield func(llm.Event, error) bool) {
		yield(llm.Event{Kind: llm.EventTextDelta, Text: "a"}, nil)
		panic("model bug")
	}, nil)
	if !rec.failed {
		t.Error("a panic in the model did not fail the test")
	}
	if len(events) != 1 || err != nil {
		t.Errorf("events = %d, err = %v; want the event read before the panic", len(events), err)
	}
}

// recorder is a testing.TB that records failures instead of reporting them.
type recorder struct {
	testing.TB
	failed bool
}

func (r *recorder) Helper() {}

func (r *recorder) Errorf(string, ...any) { r.failed = true }
