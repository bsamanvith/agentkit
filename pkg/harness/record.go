package harness

import (
	"time"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// TimedMessage is a message and when it was written.
type TimedMessage struct {
	Message llm.Message
	At      time.Time
}

func (m TimedMessage) clone() TimedMessage { return TimedMessage{Message: m.Message.Clone(), At: m.At} }

// Record is what a run did around its messages: when they were written, how
// long the model and the tools took, what it cost and why each turn stopped.
// It is for display, billing and tracing, and is never sent to a model.
//
// Applying a run's record to the [Input.Transcript] it started from gives its
// final transcript: first Prompt (if any), then the Messages of every turn in
// order.
type Record struct {
	// StartedAt and FinishedAt bracket the run.
	StartedAt, FinishedAt time.Time
	// Prompt is the user message the run started with; nil when a run resumed
	// a transcript without a new message.
	Prompt *TimedMessage
	// Turns are the turns that completed, in order.
	Turns []TurnRecord
}

func (r Record) clone() Record {
	out := r
	if r.Prompt != nil {
		p := r.Prompt.clone()
		out.Prompt = &p
	}
	out.Turns = make([]TurnRecord, len(r.Turns))
	for i, t := range r.Turns {
		out.Turns[i] = t.clone()
	}
	return out
}

// TurnRecord describes one turn: a model call and the tools it asked for.
type TurnRecord struct {
	// Turn counts model calls in the run, starting at 1.
	Turn int
	// Messages are the messages the turn added to the transcript: the
	// assistant's reply and, if it called tools, the user message of results.
	// The results message is stamped when all tools had finished.
	Messages []TimedMessage
	// RequestedAt is when the model was called, FirstEventAt when its first
	// event arrived, and DoneAt when the reply was complete.
	RequestedAt, FirstEventAt, DoneAt time.Time
	// Usage and Stop are the model's.
	Usage llm.Usage
	Stop  llm.StopReason
	// Tools describes the tool calls in the order of the calls.
	Tools []ToolRecord
}

func (t TurnRecord) clone() TurnRecord {
	out := t
	out.Messages = make([]TimedMessage, len(t.Messages))
	for i, m := range t.Messages {
		out.Messages[i] = m.clone()
	}
	out.Tools = append([]ToolRecord(nil), t.Tools...)
	return out
}

// ToolRecord describes one tool call. The call's input and output are in the
// turn's messages.
type ToolRecord struct {
	ToolUseID string
	Name      string
	// Kind is the tool's kind as [tool.Metadata.Class] reads it from the
	// definition the run listed: [tool.KindWrite] for a tool that declares
	// none, or that the run did not list.
	Kind tool.Kind
	// StartedAt is when the call began to run, after waiting for a free slot
	// under [WithMaxParallelTools]; Duration is how long it ran.
	StartedAt time.Time
	Duration  time.Duration
	// IsError is true when the model was shown an error result.
	IsError bool
}

// Result is what a run produced.
type Result struct {
	// Transcript is the history including this run: pass it to the next run.
	Transcript Transcript
	// Final is the model's last message: the answer.
	Final llm.Message
	// Usage is the total token usage of the run.
	Usage llm.Usage
	// Turns is how many model calls the run made.
	Turns int
	// Record is what the run did.
	Record Record
}

func addUsage(a, b llm.Usage) llm.Usage {
	return llm.Usage{
		InputTokens:      a.InputTokens + b.InputTokens,
		OutputTokens:     a.OutputTokens + b.OutputTokens,
		CacheReadTokens:  a.CacheReadTokens + b.CacheReadTokens,
		CacheWriteTokens: a.CacheWriteTokens + b.CacheWriteTokens,
	}
}

// totalTokens counts every token a turn paid for. InputTokens excludes cached
// tokens, so all four fields are added.
func totalTokens(u llm.Usage) int {
	return u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens + u.OutputTokens
}
