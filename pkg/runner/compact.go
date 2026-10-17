package runner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/store"
)

// Compactor is an [Agent] that can summarize its conversation's history, so
// that the runner can compact a conversation that outgrows the model's
// context window. [*harness.Harness] is one. The runner compacts the
// conversation of a chat run whose agent is a Compactor:
//
//   - reactively, when the run fails with [llm.ErrContextTooLong]: its turns
//     are stored, the conversation compacted, and the run resumed from the
//     compacted context, once per execution (a second such failure fails the
//     run);
//   - ahead of time, before a new run's message is stored, when ShouldCompact
//     says the last request of the conversation filled the context window.
//
// Compaction appends a [store.KindCompaction] entry that covers the whole log
// before it: a user message with the summary, then what the next request
// must still see verbatim. That is the last assistant message and its tool
// results when the context ends on results, the last user message when it
// ends on one (after a fixed assistant reply, [CompactedText], to keep the
// roles alternating), and only that reply when it ends on the assistant.
// Nothing is rewritten: [store.ConversationStore.History] keeps every entry,
// and the next request starts at the compaction, so opaque blocks before it
// are not sent again, except in the verbatim tail.
//
// A compaction that fails leaves the log as it was: a run that failed for
// want of room fails with its own error (the compaction's added), and a new
// run goes ahead uncompacted.
type Compactor interface {
	Agent
	// Compact summarizes history, the context up to the part kept verbatim.
	Compact(ctx context.Context, history harness.Transcript) (harness.Summary, error)
	// ShouldCompact reports whether a conversation whose last request used
	// u should be compacted before its next run.
	ShouldCompact(u llm.Usage) bool
}

// The texts of a compaction entry's messages.
const (
	// SummaryPrefix starts the user message that holds a compaction's
	// summary.
	SummaryPrefix = "This conversation was compacted. A summary of what came before:\n\n"
	// CompactedText is the assistant reply that follows the summary when the
	// compacted context does not end on a tool call.
	CompactedText = "Understood. I'll continue from the summary."
)

// errNothingToCompact means a context has nothing before the part a
// compaction would keep verbatim.
var errNothingToCompact = errors.New("nothing to compact")

// CompactionRecord is the record of a compaction entry.
type CompactionRecord struct {
	At time.Time `json:"at"`
	// Reason is why the conversation was compacted: "context_too_long" or
	// "window_full".
	Reason string `json:"reason"`
	// Usage is the summary request's.
	Usage llm.Usage `json:"usage"`
}

// The reasons of a [CompactionRecord].
const (
	ReasonContextTooLong = "context_too_long"
	ReasonWindowFull     = "window_full"
)

// splitForCompaction cuts a context into the history a summary replaces and
// the tail kept verbatim, and says whether the summary needs the
// [CompactedText] reply after it.
func splitForCompaction(tr harness.Transcript) (head, tail harness.Transcript, ack bool) {
	n := len(tr)
	switch {
	case n == 0:
		return nil, nil, false
	case tr[n-1].Role == llm.RoleAssistant:
		return tr, nil, true
	case n >= 2 && hasResults(tr[n-1]):
		return tr[:n-2], tr[n-2:], false
	}
	return tr[:n-1], tr[n-1:], true
}

func hasResults(m llm.Message) bool {
	for _, b := range m.Content {
		if _, ok := b.(llm.ToolResult); ok {
			return true
		}
	}
	return false
}

// compact summarizes the context v and appends the compaction entry after
// it, labelled with the run of the entry before it (so a run whose message is
// not stored yet still reads as not started). The summary request's tokens
// are in the entry's record, not in the run's usage.
func (x *exec) compact(ctx context.Context, c Compactor, v *view, reason string, emit func(Event) bool) error {
	head, tail, ack := splitForCompaction(v.tr)
	if len(head) == 0 {
		return errNothingToCompact
	}
	s, err := c.Compact(ctx, head.Clone())
	if err != nil {
		return fmt.Errorf("compact: %w", err)
	}
	msgs := []llm.Message{llm.User(llm.Text{Text: SummaryPrefix + s.Text})}
	if ack {
		msgs = append(msgs, llm.Assistant(llm.Text{Text: CompactedText}))
	}
	for _, m := range tail {
		msgs = append(msgs, m.Clone())
	}
	seq, err := x.r.append(ctx, x.run.ConversationID, v, store.Entry{
		Kind:     store.KindCompaction,
		Messages: msgs,
		RunID:    v.lastRun,
		Covers:   v.seq,
		Record:   mustJSON(Record{Compaction: &CompactionRecord{At: x.r.clk.Now(), Reason: reason, Usage: s.Usage}}),
	})
	if err != nil {
		return fmt.Errorf("compact: %w", err)
	}
	if emit(Event{Kind: EventCompacted, Seq: seq, Usage: s.Usage}) {
		emit(Event{Kind: EventSaved, Seq: seq})
	}
	return nil
}
