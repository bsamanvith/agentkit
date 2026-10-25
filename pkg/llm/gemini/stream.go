package gemini

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/llm"
)

// errIncomplete is a stream that ended without a finishReason.
var errIncomplete = errors.New("gemini: stream ended without a finishReason")

// errGarbled is a stream whose content cannot be made into a reply.
var errGarbled = errors.New("gemini: garbled stream")

// chunk is one event of the stream, a GenerateContentResponse, as far as
// this adapter reads it. Parts are kept raw, so that one the adapter does
// not map goes back as it came.
type chunk struct {
	Candidates []struct {
		Content struct {
			Parts []json.RawMessage `json:"parts"`
		} `json:"content"`
		FinishReason  string `json:"finishReason"`
		FinishMessage string `json:"finishMessage"`
		SafetyRatings []struct {
			Category string `json:"category"`
			Blocked  bool   `json:"blocked"`
		} `json:"safetyRatings"`
		Index int64 `json:"index"`
	} `json:"candidates"`
	UsageMetadata  *usage `json:"usageMetadata"`
	PromptFeedback *struct {
		BlockReason        string `json:"blockReason"`
		BlockReasonMessage string `json:"blockReasonMessage"`
	} `json:"promptFeedback"`
}

// usage is the token usage so far; the last chunk has the reply's.
type usage struct {
	PromptTokenCount        int64 `json:"promptTokenCount"`
	CandidatesTokenCount    int64 `json:"candidatesTokenCount"`
	CachedContentTokenCount int64 `json:"cachedContentTokenCount"`
	ThoughtsTokenCount      int64 `json:"thoughtsTokenCount"`
	ToolUsePromptTokenCount int64 `json:"toolUsePromptTokenCount"`
}

// streamPart is a part of the reply, as far as the adapter maps it.
type streamPart struct {
	Text             *string `json:"text"`
	Thought          bool    `json:"thought"`
	ThoughtSignature string  `json:"thoughtSignature"`
	FunctionCall     *struct {
		ID   string          `json:"id"`
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"functionCall"`
}

// parseChunk decodes the data of one event: a chunk, or an error the server
// sent inside the stream ({"error":{...}}), which it returns as an *Error.
func parseChunk(data []byte) (*chunk, error) {
	var probe struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("%w: %w", errGarbled, err)
	}
	if len(probe.Error) > 0 && !bytes.Equal(probe.Error, []byte("null")) {
		return nil, streamError(data)
	}
	var c chunk
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("%w: %w", errGarbled, err)
	}
	return &c, nil
}

type blockKind uint8

const (
	noBlock blockKind = iota
	textBlock
	thoughtBlock
)

// reader turns the chunks of a reply into llm events, block by block as they
// arrive.
type reader struct {
	blocks    []llm.Block
	open      blockKind
	text      strings.Builder
	signature string // of the open thought block
	calls     int
	reason    string // the finishReason
	message   string // the finishMessage
	blocked   []string
	prompt    string // the promptFeedback's blockReason
	promptMsg string
	usage     llm.Usage
}

// read handles one chunk and returns the events it completes.
func (r *reader) read(c *chunk) ([]llm.Event, error) {
	if c.UsageMetadata != nil {
		r.usage = usageOf(*c.UsageMetadata)
	}
	if f := c.PromptFeedback; f != nil && f.BlockReason != "" {
		r.prompt, r.promptMsg = f.BlockReason, f.BlockReasonMessage
	}
	var events []llm.Event
	for _, cand := range c.Candidates {
		if cand.Index != 0 {
			continue // candidateCount is never set, so there is one
		}
		for _, raw := range cand.Content.Parts {
			var p streamPart
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, fmt.Errorf("%w: part: %w", errGarbled, err)
			}
			var err error
			if events, err = r.part(events, p, raw); err != nil {
				return nil, err
			}
		}
		if cand.FinishReason != "" {
			r.reason, r.message = cand.FinishReason, cand.FinishMessage
		}
		for _, s := range cand.SafetyRatings {
			if s.Blocked && !slices.Contains(r.blocked, s.Category) {
				r.blocked = append(r.blocked, s.Category)
			}
		}
	}
	return events, nil
}

// part handles one part of the reply.
func (r *reader) part(events []llm.Event, p streamPart, raw json.RawMessage) ([]llm.Event, error) {
	switch {
	case p.FunctionCall != nil:
		events = r.close(events)
		fc := p.FunctionCall
		if fc.Name == "" {
			return nil, fmt.Errorf("%w: function call without a name", errGarbled)
		}
		args := fc.Args
		if len(args) == 0 || string(args) == "null" {
			args = json.RawMessage(`{}`)
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, args); err != nil {
			return nil, fmt.Errorf("%w: arguments of %q: %w", errGarbled, fc.Name, err)
		}
		id := fc.ID
		if id == "" {
			id = newCallID()
		}
		idx := len(r.blocks)
		b := llm.ToolUse{ID: id, Name: fc.Name, Input: compact.Bytes()}
		events = append(events,
			llm.Event{Kind: llm.EventToolUseStart, Index: idx, Block: llm.ToolUse{ID: id, Name: fc.Name}},
			llm.Event{Kind: llm.EventToolInputDelta, Index: idx, Text: string(b.Input)},
			llm.Event{Kind: llm.EventBlockDone, Index: idx, Block: b})
		r.blocks = append(r.blocks, b)
		r.calls++
		if p.ThoughtSignature != "" {
			events = r.add(events, llm.Opaque{Provider: Provider, Data: signatureData(p.ThoughtSignature)})
		}
	case p.Text != nil && p.Thought:
		if r.open != thoughtBlock {
			events = r.close(events)
			r.open = thoughtBlock
		}
		r.text.WriteString(*p.Text)
		if p.ThoughtSignature != "" {
			r.signature = p.ThoughtSignature
			events = r.close(events)
		}
	case p.Text != nil:
		if *p.Text != "" {
			if r.open != textBlock {
				events = r.close(events)
				r.open = textBlock
			}
			r.text.WriteString(*p.Text)
			events = append(events, llm.Event{Kind: llm.EventTextDelta, Index: len(r.blocks), Text: *p.Text})
		}
		if p.ThoughtSignature == "" {
			break
		}
		if r.open == textBlock {
			events = r.close(events)
			events = r.add(events, llm.Opaque{Provider: Provider, Data: signatureData(p.ThoughtSignature)})
			break
		}
		// An empty text part with a signature, after no text: a part of
		// its own.
		events = r.close(events)
		events = r.add(events, llm.Opaque{Provider: Provider, Data: compactPart(raw)})
	default:
		// A part the adapter does not map (code execution, a file, ...):
		// kept to go back as it came.
		events = r.close(events)
		events = r.add(events, llm.Opaque{Provider: Provider, Data: compactPart(raw)})
	}
	return events, nil
}

// add appends a finished opaque block.
func (r *reader) add(events []llm.Event, b llm.Block) []llm.Event {
	events = append(events, llm.Event{Kind: llm.EventBlockDone, Index: len(r.blocks), Block: b})
	r.blocks = append(r.blocks, b)
	return events
}

// close ends the open text or thought block, if any.
func (r *reader) close(events []llm.Event) []llm.Event {
	var b llm.Block
	switch r.open {
	case noBlock:
		return events
	case textBlock:
		b = llm.Text{Text: r.text.String()}
	case thoughtBlock:
		b = llm.Opaque{Provider: Provider, Data: thoughtData(r.text.String(), r.signature)}
	}
	r.open, r.signature = noBlock, ""
	r.text.Reset()
	return r.add(events, b)
}

// finish ends the reply: it closes the open block and returns the finished
// response.
func (r *reader) finish() ([]llm.Event, llm.Response, error) {
	if r.reason == "" && r.prompt == "" {
		return nil, llm.Response{}, errIncomplete
	}
	switch r.reason {
	case "MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL":
		return nil, llm.Response{}, fmt.Errorf("%w: finishReason %s: %s", errGarbled, r.reason, r.message)
	}
	events := r.close(nil)
	resp := llm.Response{
		Message: llm.Assistant(r.blocks...),
		Stop:    r.stop(),
		Usage:   r.usage,
	}
	return events, resp, nil
}

// stop maps the finish reason, or the prompt's block reason.
func (r *reader) stop() llm.StopReason {
	if r.prompt != "" {
		return llm.StopReason{Kind: llm.StopRefusal, Category: r.prompt, Explanation: r.promptMsg}
	}
	switch r.reason {
	case "STOP":
		if r.calls > 0 {
			return llm.StopReason{Kind: llm.StopToolUse}
		}
		return llm.StopReason{Kind: llm.StopEndTurn}
	case "MAX_TOKENS":
		return llm.StopReason{Kind: llm.StopMaxTokens}
	case "SAFETY", "RECITATION", "LANGUAGE", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY":
		category := strings.Join(r.blocked, ",")
		if category == "" {
			category = r.reason
		}
		return llm.StopReason{Kind: llm.StopRefusal, Category: category, Explanation: r.message}
	}
	return llm.StopReason{Kind: llm.StopOther, Raw: r.reason}
}

// usageOf maps the API's usage. promptTokenCount counts the cached tokens
// too; thoughts are output.
func usageOf(u usage) llm.Usage {
	cached := max(u.CachedContentTokenCount, 0)
	return llm.Usage{
		InputTokens:     int(max(u.PromptTokenCount-cached, 0) + max(u.ToolUsePromptTokenCount, 0)),
		OutputTokens:    int(max(u.CandidatesTokenCount, 0) + max(u.ThoughtsTokenCount, 0)),
		CacheReadTokens: int(cached),
	}
}

// newCallID makes an ID for a function call that has none.
func newCallID() string {
	var b [8]byte
	for i := range b {
		b[i] = byte(rand.Uint32())
	}
	return "call_" + hex.EncodeToString(b[:])
}

// signatureData is the data of a signature block: {"thoughtSignature":"..."}.
func signatureData(sig string) json.RawMessage {
	data, _ := marshal(struct {
		ThoughtSignature string `json:"thoughtSignature"`
	}{sig}) // strings always encode
	return data
}

// thoughtData is the data of a thought block, a thought part.
func thoughtData(text, sig string) json.RawMessage {
	data, _ := marshal(struct {
		Text             string `json:"text"`
		Thought          bool   `json:"thought"`
		ThoughtSignature string `json:"thoughtSignature,omitempty"`
	}{text, true, sig})
	return data
}

// compactPart is a part of the reply as it came, compacted.
func compactPart(raw json.RawMessage) json.RawMessage {
	var buf bytes.Buffer
	if json.Compact(&buf, raw) != nil {
		return raw // decoded already, so valid
	}
	return buf.Bytes()
}
