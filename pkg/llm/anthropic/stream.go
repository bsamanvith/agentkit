package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/bsamanvith/agentkit/pkg/llm"
)

// reader turns the API's stream events into llm events and assembles the
// final response.
//
// The API numbers content blocks in order but may send the deltas of several
// open blocks interleaved. llm streams one block at a time, so blocks wait in
// a queue: the oldest open block streams its text as it arrives, the others
// keep it until they reach the head of the queue.
type reader struct {
	msg       anthropic.BetaMessage // message_start and message_delta: stop, usage
	queue     []*block              // blocks not yet finished, in API order
	next      int64                 // API index of the next content_block_start
	blocks    []llm.Block           // finished blocks, as streamed
	truncated bool                  // a tool call was cut off and dropped
	stopped   bool                  // message_stop arrived
}

type blockKind uint8

const (
	kindText blockKind = iota + 1
	kindToolUse
	kindOpaque
)

// block is a content block being received.
type block struct {
	index   int64
	kind    blockKind
	done    bool
	pending []string // text deltas not streamed yet, or a tool call's input fragments
	text    strings.Builder
	tool    llm.ToolUse
	streams bool                  // its first text delta was streamed
	acc     anthropic.BetaMessage // the opaque block, built by the SDK as content[0]
}

// read takes one stream event and returns the llm events it completes.
func (r *reader) read(ev anthropic.BetaRawMessageStreamEventUnion) ([]llm.Event, error) {
	switch ev.Type {
	case "message_start", "message_delta":
		if err := r.msg.Accumulate(ev); err != nil {
			return nil, err
		}
		return nil, nil
	case "message_stop":
		r.stopped = true
		for _, b := range r.queue {
			b.done = true // a block the API never closed ends with the message
		}
		return r.flush(), nil
	case "content_block_start":
		if ev.Index != r.next {
			return nil, fmt.Errorf("anthropic: content block %d started, want %d", ev.Index, r.next)
		}
		cb := ev.ContentBlock
		if cb.Type == "" {
			return nil, fmt.Errorf("anthropic: content block %d started without a content_block", ev.Index)
		}
		r.next++
		b := &block{index: ev.Index}
		switch cb.Type {
		case "text":
			b.kind = kindText
			b.add(cb.Text)
		case "tool_use":
			b.kind = kindToolUse
			b.tool = llm.ToolUse{ID: cb.ID, Name: cb.Name}
			if in := strings.TrimSpace(cb.JSON.Input.Raw()); in != "" && in != "{}" && in != "null" {
				b.pending = append(b.pending, in)
			}
		default:
			b.kind = kindOpaque
			if err := b.accumulate(ev); err != nil {
				return nil, err
			}
		}
		r.queue = append(r.queue, b)
	case "content_block_delta":
		b, err := r.open(ev.Index)
		if err != nil {
			return nil, err
		}
		switch b.kind {
		case kindText:
			if ev.Delta.Type == "text_delta" {
				b.add(ev.Delta.Text)
			}
		case kindToolUse:
			if ev.Delta.Type == "input_json_delta" && ev.Delta.PartialJSON != "" {
				b.pending = append(b.pending, ev.Delta.PartialJSON)
			}
		case kindOpaque:
			if err := b.accumulate(ev); err != nil {
				return nil, err
			}
		}
	case "content_block_stop":
		b, err := r.open(ev.Index)
		if err != nil {
			return nil, err
		}
		if b.kind == kindOpaque {
			if err := b.accumulate(ev); err != nil {
				return nil, err
			}
		}
		b.done = true
	}
	return r.flush(), nil
}

// open returns the unfinished block with the given API index.
func (r *reader) open(index int64) (*block, error) {
	for _, b := range r.queue {
		if b.index == index && !b.done {
			return b, nil
		}
	}
	return nil, fmt.Errorf("anthropic: event for content block %d, which is not open", index)
}

// add records a piece of a text block.
func (b *block) add(s string) {
	if s != "" {
		b.text.WriteString(s)
		b.pending = append(b.pending, s)
	}
}

// accumulate feeds an event of an opaque block to the SDK's accumulator, which
// keeps the block's wire JSON with the deltas applied.
func (b *block) accumulate(ev anthropic.BetaRawMessageStreamEventUnion) error {
	ev.Index = 0
	return b.acc.Accumulate(ev)
}

// flush streams what the head of the queue can stream: the pending text of
// the oldest block, and every finished block up to the first unfinished one.
func (r *reader) flush() []llm.Event {
	var out []llm.Event
	for len(r.queue) > 0 {
		b := r.queue[0]
		index := len(r.blocks)
		if b.kind == kindText {
			for _, s := range b.pending {
				out = append(out, llm.Event{Kind: llm.EventTextDelta, Index: index, Text: s})
				b.streams = true
			}
			b.pending = b.pending[:0]
		}
		if !b.done {
			return out
		}
		r.queue = r.queue[1:]
		var done llm.Block
		switch b.kind {
		case kindText:
			if !b.streams {
				continue // an empty text block; the API rejects them in requests
			}
			done = llm.Text{Text: b.text.String()}
		case kindToolUse:
			input := strings.Join(b.pending, "")
			if input == "" {
				input = "{}"
			}
			if !json.Valid([]byte(input)) {
				r.truncated = true // cut off by the token limit
				continue
			}
			out = append(out, llm.Event{Kind: llm.EventToolUseStart, Index: index, Block: b.tool})
			for _, s := range b.pending {
				out = append(out, llm.Event{Kind: llm.EventToolInputDelta, Index: index, Text: s})
			}
			u := b.tool
			u.Input = json.RawMessage(input)
			done = u
		case kindOpaque:
			if len(b.acc.Content) != 1 {
				continue // never started; cannot happen for a block in the queue
			}
			done = llm.Opaque{Provider: provider, Data: json.RawMessage(b.acc.Content[0].RawJSON())}
		}
		r.blocks = append(r.blocks, done)
		out = append(out, llm.Event{Kind: llm.EventBlockDone, Index: index, Block: done})
	}
	return out
}

// response returns the finished reply, once message_stop has arrived.
func (r *reader) response() (llm.Response, error) {
	if !r.stopped {
		return llm.Response{}, errIncomplete
	}
	u := r.msg.Usage
	resp := llm.Response{
		Message: llm.Assistant(r.blocks...).Clone(),
		Stop:    stopReason(r.msg),
		Usage: llm.Usage{
			InputTokens:      int(u.InputTokens),
			OutputTokens:     int(u.OutputTokens),
			CacheReadTokens:  int(u.CacheReadInputTokens),
			CacheWriteTokens: int(u.CacheCreationInputTokens),
		},
	}
	if r.truncated {
		resp.Stop = llm.StopReason{Kind: llm.StopMaxTokens}
	}
	return resp, nil
}

func stopReason(msg anthropic.BetaMessage) llm.StopReason {
	switch msg.StopReason {
	case anthropic.BetaStopReasonEndTurn:
		return llm.StopReason{Kind: llm.StopEndTurn}
	case anthropic.BetaStopReasonToolUse:
		return llm.StopReason{Kind: llm.StopToolUse}
	case anthropic.BetaStopReasonMaxTokens:
		return llm.StopReason{Kind: llm.StopMaxTokens}
	case anthropic.BetaStopReasonRefusal:
		return llm.StopReason{
			Kind:        llm.StopRefusal,
			Category:    string(msg.StopDetails.Category),
			Explanation: msg.StopDetails.Explanation,
		}
	default:
		return llm.StopReason{Kind: llm.StopOther, Raw: string(msg.StopReason)}
	}
}
