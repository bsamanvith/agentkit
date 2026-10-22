package llm

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"

	"github.com/bsamanvith/agentkit/pkg/tool"
)

// Model is a language model. Implementations must be safe for concurrent use.
type Model interface {
	// Generate sends req to the model and streams the reply.
	//
	// The stream is a sequence of [Event]s in the order described on
	// [EventKind], ending with one [EventMessageDone]. On failure it yields
	// the error as its last element (with a zero Event) and ends; no
	// [EventMessageDone] follows an error. Errors are classified with the
	// sentinels in this package, or are the context's error when ctx is done.
	// Stopping the iteration early cancels the request.
	//
	// Generate never modifies req or anything it points to: the history it is
	// given is append-only.
	//
	// The returned sequence is single use, and the request is sent when it is
	// first iterated. Failures that are not a provider's answer (a dropped
	// connection, a 5xx, a stream cut short) are mapped by adapters to
	// [ErrOverloaded], so that a retry loop treats them as transient.
	Generate(ctx context.Context, req Request) iter.Seq2[Event, error]
}

// Request is what a model is asked to continue.
type Request struct {
	// System is the system prompt.
	System string
	// Messages is the conversation so far; it ends with a user message.
	Messages []Message
	// Tools the model may call. The same definitions the tool registry lists.
	Tools []tool.Definition
	// Deferred names tools of Tools that the provider should hold back
	// until a [ToolResult.Loads] names them (its native deferred loading,
	// such as Claude's defer_loading): the model sees their definitions
	// only from then on. The harness sets it only for a model wired for
	// native tool search; an adapter without it must not be given any.
	Deferred []string
	// MaxTokens limits the output tokens of the reply. Zero means the
	// adapter's default.
	MaxTokens int
	// Provider holds options for one provider, a typed value such as
	// anthropic.Options. An adapter ignores options that are not its own
	// type, and documents whether it takes the value or a pointer. Treat the value as immutable once it is in a request: [Request.Clone]
	// copies the reference.
	Provider any
}

// Clone returns a copy of r that shares no memory with it, except for the
// Provider options.
func (r Request) Clone() Request {
	if r.Messages != nil {
		msgs := make([]Message, len(r.Messages))
		for i, m := range r.Messages {
			msgs[i] = m.Clone()
		}
		r.Messages = msgs
	}
	if r.Tools != nil {
		defs := make([]tool.Definition, len(r.Tools))
		for i, d := range r.Tools {
			defs[i] = d.Clone()
		}
		r.Tools = defs
	}
	r.Deferred = slices.Clone(r.Deferred)
	return r
}

// Response is a finished reply from a model.
type Response struct {
	// Message is the assistant message: text, tool calls and opaque blocks in
	// the order the model produced them.
	Message Message    `json:"message"`
	Stop    StopReason `json:"stop"`
	Usage   Usage      `json:"usage"`
}

// StopKind says why a model stopped.
type StopKind string

// The stop kinds. A model that needs its tool calls answered stops with
// [StopToolUse].
const (
	StopEndTurn   StopKind = "end_turn"
	StopToolUse   StopKind = "tool_use"
	StopMaxTokens StopKind = "max_tokens"
	// StopRefusal means the model declined to answer; see [StopReason.Category]
	// and [StopReason.Explanation].
	StopRefusal StopKind = "refusal"
	// StopOther is a provider stop reason with no neutral equivalent; the
	// provider's own value is in [StopReason.Raw].
	StopOther StopKind = "other"
)

// StopReason is why a reply ended. The zero value means "not stopped yet".
type StopReason struct {
	Kind StopKind `json:"kind"`
	// Category is the provider's classification of a refusal.
	Category string `json:"category,omitempty"`
	// Explanation is the model's or provider's account of a refusal.
	Explanation string `json:"explanation,omitempty"`
	// Raw is the provider's stop reason as received. It is set for
	// [StopOther] and empty for every other kind.
	Raw string `json:"raw,omitempty"`
}

// Usage counts tokens. InputTokens does not include the cached tokens: the
// whole prompt is InputTokens + CacheReadTokens + CacheWriteTokens. Adapters
// whose provider counts cached tokens in the input subtract them.
type Usage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

// Add returns the field-by-field sum of u and v: the usage of two calls
// together.
func (u Usage) Add(v Usage) Usage {
	return Usage{
		InputTokens:      u.InputTokens + v.InputTokens,
		OutputTokens:     u.OutputTokens + v.OutputTokens,
		CacheReadTokens:  u.CacheReadTokens + v.CacheReadTokens,
		CacheWriteTokens: u.CacheWriteTokens + v.CacheWriteTokens,
	}
}

// EventKind identifies an [Event].
//
// Blocks of a reply are streamed one after the other, in the order of the
// final message, and never interleaved. For each block:
//
//   - a text block: zero or more [EventTextDelta], then [EventBlockDone];
//   - a tool-use block: [EventToolUseStart], zero or more [EventToolInputDelta],
//     then [EventBlockDone];
//   - an opaque block: only [EventBlockDone].
//
// After the last block comes one [EventMessageDone].
type EventKind uint8

// The event kinds.
const (
	// EventTextDelta carries a piece of text in Event.Text.
	EventTextDelta EventKind = iota + 1
	// EventToolUseStart announces a tool call: Event.Block is a [ToolUse] with
	// ID and Name set and no Input yet.
	EventToolUseStart
	// EventToolInputDelta carries a fragment of the tool call's JSON input in
	// Event.Text. Fragments are not valid JSON on their own.
	EventToolInputDelta
	// EventBlockDone says the block at Event.Index is complete: Event.Block is
	// the finished block, equal to the block in the final message.
	EventBlockDone
	// EventMessageDone ends the stream: Event.Response is the finished reply.
	EventMessageDone
)

func (k EventKind) String() string {
	switch k {
	case EventTextDelta:
		return "text_delta"
	case EventToolUseStart:
		return "tool_use_start"
	case EventToolInputDelta:
		return "tool_input_delta"
	case EventBlockDone:
		return "block_done"
	case EventMessageDone:
		return "message_done"
	}
	return fmt.Sprintf("EventKind(%d)", uint8(k))
}

// Event is one step of a streamed reply. Which fields are set depends on Kind;
// it is a flat struct so that streaming does not allocate per event.
type Event struct {
	Kind EventKind
	// Index is the position of the block in the message, for every kind but
	// [EventMessageDone].
	Index int
	// Text is the delta for [EventTextDelta] and [EventToolInputDelta].
	Text string
	// Block is set for [EventToolUseStart] and [EventBlockDone].
	Block Block
	// Response is set for [EventMessageDone]. It is the one authoritative
	// result: [Collect] returns it, and a conforming model makes it agree
	// with the block events before it.
	Response *Response
}

// ErrIncompleteStream is returned by [Collect] for a stream that ended with
// neither a result nor an error.
var ErrIncompleteStream = errors.New("stream ended without a final message")

// Collect reads a stream to its end and returns the finished [Response].
//
// If the stream fails, even after yielding some events, Collect returns the
// stream's error and a zero Response: there is no partial result.
func Collect(seq iter.Seq2[Event, error]) (Response, error) {
	for ev, err := range seq {
		if err != nil {
			return Response{}, err
		}
		if ev.Kind == EventMessageDone {
			if ev.Response == nil {
				return Response{}, ErrIncompleteStream
			}
			return cloneResponse(*ev.Response), nil
		}
	}
	return Response{}, ErrIncompleteStream
}

func cloneResponse(r Response) Response {
	r.Message = r.Message.Clone()
	return r
}
