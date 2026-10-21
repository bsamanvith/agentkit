package openaicompat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/respjson"

	"github.com/bsamanvith/agentkit/pkg/llm"
)

// reasoningNames are the delta fields servers stream reasoning in, in the
// order they are looked for: reasoning_content (DeepSeek, vLLM, llama.cpp)
// and reasoning (Ollama, OpenRouter, Groq).
var reasoningNames = []string{"reasoning_content", "reasoning"}

// errIncomplete is a stream that ended without a finish_reason.
var errIncomplete = errors.New("openaicompat: stream ended without a finish_reason")

// errGarbled is a stream whose content cannot be made into a reply.
var errGarbled = errors.New("openaicompat: garbled stream")

type blockKind uint8

const (
	noBlock blockKind = iota
	textBlock
	reasoningBlock
)

// call is a tool call being streamed.
type call struct {
	index int64
	id    string
	name  string
	args  []string // the argument fragments, in order
}

// reader turns the chunks of a reply into llm events. Text and reasoning are
// blocks of the message as they arrive; tool calls are collected and become
// the message's last blocks when the reply ends.
type reader struct {
	blocks   []llm.Block
	open     blockKind
	text     strings.Builder
	field    string // the reasoning field of the open reasoning block
	calls    []*call
	reason   string // the finish_reason
	refusal  strings.Builder
	filtered []string
	usage    llm.Usage
}

// read handles one chunk and returns the events it completes.
func (r *reader) read(chunk openai.ChatCompletionChunk) []llm.Event {
	if chunk.JSON.Usage.Valid() {
		r.usage = usageOf(chunk.Usage)
	}
	var events []llm.Event
	for _, choice := range chunk.Choices {
		if choice.Index != 0 {
			continue // n is never set, so there is one choice
		}
		d := choice.Delta
		if name, text := reasoningDelta(d.JSON.ExtraFields); text != "" {
			if r.open != reasoningBlock || r.field != name {
				events = r.close(events)
				r.open, r.field = reasoningBlock, name
			}
			r.text.WriteString(text)
		}
		if d.Content != "" {
			if r.open != textBlock {
				events = r.close(events)
				r.open = textBlock
			}
			r.text.WriteString(d.Content)
			events = append(events, llm.Event{Kind: llm.EventTextDelta, Index: len(r.blocks), Text: d.Content})
		}
		r.refusal.WriteString(d.Refusal)
		for _, tc := range d.ToolCalls {
			c := r.callFor(tc)
			if c == nil {
				events = r.close(events)
				c = &call{index: tc.Index, id: tc.ID}
				r.calls = append(r.calls, c)
			}
			if c.name == "" {
				c.name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				c.args = append(c.args, tc.Function.Arguments)
			}
		}
		if choice.FinishReason != "" {
			r.reason = choice.FinishReason
		}
		if f, ok := choice.JSON.ExtraFields["content_filter_results"]; ok {
			r.filtered = filteredCategories(f.Raw(), r.filtered)
		}
	}
	return events
}

// callFor returns the call a tool-call delta continues, or nil if it starts a
// new one: a new index, or a new ID at a known index (some servers give every
// parallel call index 0).
func (r *reader) callFor(tc openai.ChatCompletionChunkChoiceDeltaToolCall) *call {
	for i := len(r.calls) - 1; i >= 0; i-- {
		c := r.calls[i]
		if c.index == tc.Index {
			if tc.ID != "" && c.id != "" && tc.ID != c.id {
				return nil
			}
			if c.id == "" {
				c.id = tc.ID
			}
			return c
		}
	}
	return nil
}

// close ends the open text or reasoning block, if any.
func (r *reader) close(events []llm.Event) []llm.Event {
	var b llm.Block
	switch r.open {
	case noBlock:
		return events
	case textBlock:
		b = llm.Text{Text: r.text.String()}
	case reasoningBlock:
		b = llm.Opaque{Provider: Provider, Data: reasoningData(r.field, r.text.String())}
	}
	events = append(events, llm.Event{Kind: llm.EventBlockDone, Index: len(r.blocks), Block: b})
	r.blocks = append(r.blocks, b)
	r.open = noBlock
	r.text.Reset()
	return events
}

// finish ends the reply: it closes the open block, streams the tool calls and
// returns the finished response.
func (r *reader) finish() ([]llm.Event, llm.Response, error) {
	if r.reason == "" {
		return nil, llm.Response{}, errIncomplete
	}
	events := r.close(nil)
	truncated := r.reason == "length"
	calls := 0
	for i, c := range r.calls {
		input := json.RawMessage(strings.Join(c.args, ""))
		if len(c.args) == 0 {
			input = json.RawMessage(`{}`)
		}
		if !json.Valid(input) {
			if truncated {
				continue // cut off by the token limit: the reply says max_tokens
			}
			return nil, llm.Response{}, fmt.Errorf("%w: arguments of tool call %d are not valid JSON", errGarbled, i)
		}
		if c.name == "" {
			return nil, llm.Response{}, fmt.Errorf("%w: tool call %d has no name", errGarbled, i)
		}
		id := c.id
		if id == "" {
			id = "call_" + strconv.Itoa(i) // a server that sends none (the API requires one back)
		}
		idx := len(r.blocks)
		events = append(events, llm.Event{Kind: llm.EventToolUseStart, Index: idx, Block: llm.ToolUse{ID: id, Name: c.name}})
		for _, a := range c.args {
			events = append(events, llm.Event{Kind: llm.EventToolInputDelta, Index: idx, Text: a})
		}
		b := llm.ToolUse{ID: id, Name: c.name, Input: input}
		events = append(events, llm.Event{Kind: llm.EventBlockDone, Index: idx, Block: b})
		r.blocks = append(r.blocks, b)
		calls++
	}
	resp := llm.Response{
		Message: llm.Assistant(r.blocks...),
		Stop:    r.stop(calls > 0),
		Usage:   r.usage,
	}
	return events, resp, nil
}

// stop maps the finish reason, the refusal and the content filter.
func (r *reader) stop(hasCalls bool) llm.StopReason {
	if r.reason == "content_filter" || r.refusal.Len() > 0 {
		return llm.StopReason{Kind: llm.StopRefusal, Category: strings.Join(r.filtered, ","), Explanation: r.refusal.String()}
	}
	switch r.reason {
	case "stop":
		if hasCalls {
			return llm.StopReason{Kind: llm.StopToolUse}
		}
		return llm.StopReason{Kind: llm.StopEndTurn}
	case "tool_calls", "function_call":
		return llm.StopReason{Kind: llm.StopToolUse}
	case "length":
		return llm.StopReason{Kind: llm.StopMaxTokens}
	}
	return llm.StopReason{Kind: llm.StopOther, Raw: r.reason}
}

// usageOf maps the API's usage. prompt_tokens counts the cached tokens too:
// those read (prompt_tokens_details.cached_tokens) and, where a server
// reports them, those written (prompt_tokens_details.cache_write_tokens, or
// LiteLLM's cache_creation_input_tokens).
func usageOf(u openai.CompletionUsage) llm.Usage {
	read := max(u.PromptTokensDetails.CachedTokens, 0)
	written := max(u.PromptTokensDetails.CacheWriteTokens, 0)
	if written == 0 {
		written = extraInt(u.JSON.ExtraFields["cache_creation_input_tokens"])
	}
	return llm.Usage{
		InputTokens:      int(max(u.PromptTokens-read-written, 0)),
		OutputTokens:     int(max(u.CompletionTokens, 0)),
		CacheReadTokens:  int(read),
		CacheWriteTokens: int(written),
	}
}

// extraInt reads a field the SDK does not know as a count; anything but a
// non-negative integer is zero.
func extraInt(f respjson.Field) int64 {
	var n int64
	if json.Unmarshal([]byte(f.Raw()), &n) != nil || n < 0 {
		return 0
	}
	return n
}

// reasoningDelta returns the reasoning field of a delta and its text, if it
// has one.
func reasoningDelta(extra map[string]respjson.Field) (name, text string) {
	for _, name := range reasoningNames {
		f, ok := extra[name]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal([]byte(f.Raw()), &s) == nil && s != "" {
			return name, s
		}
	}
	return "", ""
}

// reasoningData is the data of a reasoning block: {"<field>":"<text>"}.
func reasoningData(field, text string) json.RawMessage {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]string{field: text}) // strings always encode
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// filteredCategories adds the categories an Azure-style
// content_filter_results marks as filtered to seen, sorted and without
// repeats.
func filteredCategories(raw string, seen []string) []string {
	var results map[string]struct {
		Filtered bool `json:"filtered"`
	}
	if json.Unmarshal([]byte(raw), &results) != nil {
		return seen
	}
	for name, res := range results {
		if res.Filtered && !slices.Contains(seen, name) {
			seen = append(seen, name)
		}
	}
	slices.Sort(seen)
	return seen
}
