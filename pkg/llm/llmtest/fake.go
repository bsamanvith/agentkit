// Package llmtest provides a scripted [llm.Model] for tests of code that uses
// one, and a conformance suite that every model adapter runs.
//
// The fake and the suite share one vocabulary, [Reply]: the fake plays replies
// back, and the suite asks an adapter's replay server for the same replies, so
// the fake cannot drift from what real models must do:
//
//	func TestFakeConformance(t *testing.T) {
//		llmtest.RunModel(t, func(t *testing.T, s llmtest.Scenario) llm.Model {
//			return llmtest.NewFake(t, s.Replies...)
//		})
//	}
package llmtest

import (
	"context"
	"errors"
	"iter"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/pkg/llm"
)

// ErrNoReply is what a [Fake] returns when it is asked for more replies than
// it was given. The test also fails.
var ErrNoReply = errors.New("llmtest: no scripted reply left")

// Reply is one scripted answer of a model.
type Reply struct {
	// Response is the finished reply. It is streamed as events, block by block.
	Response llm.Response
	// Err, if set, makes the stream fail with this error after After events
	// (zero: before the first one). Response is only used for those events.
	Err   error
	After int
	// Stall makes the stream stop after its first event and wait until the
	// context is done, then fail with the context's error. It stands for a
	// model that is slow, and is how cancellation is tested.
	Stall bool
}

// Text returns a reply that is a complete answer of the given text.
func Text(s string) Reply {
	return Reply{Response: llm.Response{
		Message: llm.Assistant(llm.Text{Text: s}),
		Stop:    llm.StopReason{Kind: llm.StopEndTurn},
	}}
}

// ToolCalls returns a reply asking for the given tool calls.
func ToolCalls(calls ...llm.ToolUse) Reply {
	blocks := make([]llm.Block, len(calls))
	for i, c := range calls {
		blocks[i] = c
	}
	return Reply{Response: llm.Response{
		Message: llm.Assistant(blocks...),
		Stop:    llm.StopReason{Kind: llm.StopToolUse},
	}}
}

// Fail returns a reply that fails with err before streaming anything.
func Fail(err error) Reply { return Reply{Err: err} }

// WithUsage returns r with the given token usage.
func (r Reply) WithUsage(u llm.Usage) Reply {
	r.Response.Usage = u
	return r
}

// Fake is an [llm.Model] that plays back scripted replies, one per call to
// Generate, in order, and records every request. It is safe for concurrent
// use, but which call gets which reply then depends on call order.
type Fake struct {
	tb      testing.TB
	mu      sync.Mutex
	replies []Reply
	next    int
	reqs    []llm.Request
}

// NewFake returns a Fake that plays back replies. A call beyond the last
// reply fails the test through tb and returns [ErrNoReply].
func NewFake(tb testing.TB, replies ...Reply) *Fake {
	return &Fake{tb: tb, replies: replies}
}

// Requests returns copies of the requests received so far, oldest first.
// Changing the requests passed to Generate afterwards does not change them.
func (f *Fake) Requests() []llm.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]llm.Request, len(f.reqs))
	for i, r := range f.reqs {
		out[i] = r.Clone()
	}
	return out
}

// Generate records req and streams the next scripted reply. It never modifies
// req. The context is checked before every event.
func (f *Fake) Generate(ctx context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	f.tb.Helper()
	f.mu.Lock()
	f.reqs = append(f.reqs, req.Clone())
	n := len(f.reqs)
	var reply Reply
	ok := f.next < len(f.replies)
	if ok {
		reply = f.replies[f.next]
		f.next++
	}
	f.mu.Unlock()

	if !ok {
		f.tb.Errorf("llmtest.Fake: request %d has no scripted reply (only %d given)", n, len(f.replies))
		return func(yield func(llm.Event, error) bool) { yield(llm.Event{}, ErrNoReply) }
	}
	return func(yield func(llm.Event, error) bool) {
		events := streamOf(reply.Response)
		if reply.Err != nil {
			events = events[:len(events)-1] // a failed stream has no final message
		}
		for i, ev := range events {
			if reply.Err != nil && i == reply.After {
				yield(llm.Event{}, reply.Err)
				return
			}
			if err := ctx.Err(); err != nil {
				yield(llm.Event{}, err)
				return
			}
			if !yield(ev, nil) {
				return
			}
			if reply.Stall {
				<-ctx.Done()
				yield(llm.Event{}, ctx.Err())
				return
			}
		}
		if reply.Err != nil {
			yield(llm.Event{}, reply.Err)
		}
	}
}

// chunk is the size, in runes, of the pieces text and tool input are streamed in.
const chunk = 8

// streamOf lays out resp as the events a model streams, in the order the
// contract on [llm.EventKind] describes.
func streamOf(resp llm.Response) []llm.Event {
	var events []llm.Event
	msg := resp.Message.Clone()
	for i, b := range msg.Content {
		switch b := b.(type) {
		case llm.Text:
			for _, piece := range split(b.Text, chunk) {
				events = append(events, llm.Event{Kind: llm.EventTextDelta, Index: i, Text: piece})
			}
		case llm.ToolUse:
			events = append(events, llm.Event{
				Kind: llm.EventToolUseStart, Index: i,
				Block: llm.ToolUse{ID: b.ID, Name: b.Name},
			})
			for _, piece := range split(string(b.Input), chunk) {
				events = append(events, llm.Event{Kind: llm.EventToolInputDelta, Index: i, Text: piece})
			}
		}
		events = append(events, llm.Event{Kind: llm.EventBlockDone, Index: i, Block: b})
	}
	resp.Message = msg
	return append(events, llm.Event{Kind: llm.EventMessageDone, Response: &resp})
}

// split cuts s into pieces of at most n runes.
func split(s string, n int) []string {
	var out []string
	for len(s) > 0 {
		end, runes := 0, 0
		for end < len(s) && runes < n {
			_, size := utf8.DecodeRuneInString(s[end:])
			end += size
			runes++
		}
		out = append(out, s[:end])
		s = s[end:]
	}
	return out
}
