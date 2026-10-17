package harness

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/pkg/llm"
)

// CompactionPrompt is the system prompt of the request [Harness.Compact]
// sends.
const CompactionPrompt = `You compact conversations between a user and an AI assistant that uses tools. ` +
	`You are given a conversation's history as text. Write a summary that the assistant will read ` +
	`in place of that history and continue the conversation from, with nothing else to go on. Keep: ` +
	`what the user wants and every instruction or preference they gave; decisions made and why; ` +
	`facts learned from tools (names, IDs, numbers, paths, URLs, errors) that later steps may need; ` +
	`what was done and what is still open. Drop chit-chat and anything superseded. Write plain text ` +
	`in the third person, as short as it can be without losing any of that. Answer with the summary only.`

// maxRenderedBlock is how much of one tool call's input or one result
// [Harness.Compact] shows the model, in bytes: past it the block is cut, and
// says how much was left out.
const maxRenderedBlock = 4000

// Summary is what [Harness.Compact] returns: the text that stands in for the
// history, and the tokens the request used.
type Summary struct {
	Text  string    `json:"text"`
	Usage llm.Usage `json:"usage"`
}

// Compact asks the harness's model for a summary of history with
// [CompactionPrompt], for a conversation that has outgrown the model's
// context window. The history is sent as text in one user message: tool calls
// and results as short lines (each cut at 4000 bytes), opaque blocks left
// out, so the request is smaller than the history and needs no tools. It
// carries the harness's [RequestOptions].
//
// A reply that stops at its token limit fails with [ErrMaxTokens], and one
// with no text with [ErrUnexpectedStop]: a cut summary would lose what it
// stands for. Model errors are returned as they are.
func (h *Harness) Compact(ctx context.Context, history Transcript) (Summary, error) {
	if len(history) == 0 {
		return Summary{}, fmt.Errorf("%w: no history to summarize", ErrInvalidInput)
	}
	req := llm.Request{
		System:    CompactionPrompt,
		Messages:  []llm.Message{llm.User(llm.Text{Text: RenderHistory(history)})},
		MaxTokens: h.cfg.request.MaxTokens,
		Provider:  h.cfg.request.Provider,
	}
	resp, err := llm.Collect(h.model.Generate(ctx, req))
	if err != nil {
		return Summary{}, err
	}
	s := Summary{Text: strings.TrimSpace(resp.Message.Text()), Usage: resp.Usage}
	switch {
	case resp.Stop.Kind == llm.StopMaxTokens:
		return s, fmt.Errorf("%w: the summary was cut at its token limit", ErrMaxTokens)
	case s.Text == "":
		return s, fmt.Errorf("%w: the summary has no text (stop %q)", ErrUnexpectedStop, resp.Stop.Kind)
	}
	return s, nil
}

// RenderHistory writes a transcript as the text [Harness.Compact] sends: one
// paragraph per message, headed by its role, with tool calls and results as
// bracketed lines and opaque blocks left out.
func RenderHistory(history Transcript) string {
	var b strings.Builder
	b.WriteString("Summarize this conversation.\n")
	names := map[string]string{} // tool names by call ID
	for _, m := range history {
		fmt.Fprintf(&b, "\n## %s\n", m.Role)
		for _, blk := range m.Content {
			switch blk := blk.(type) {
			case llm.Text:
				b.WriteString(blk.Text)
				b.WriteByte('\n')
			case llm.ToolUse:
				names[blk.ID] = blk.Name
				fmt.Fprintf(&b, "[tool call %s: %s]\n", blk.Name, clip(string(blk.Input)))
			case llm.ToolResult:
				label := "result"
				if blk.IsError {
					label = "error"
				}
				fmt.Fprintf(&b, "[tool %s %s: %s]\n", label, names[blk.ToolUseID], clip(blk.Content))
			}
		}
	}
	return b.String()
}

// clip cuts s at maxRenderedBlock bytes, on a rune boundary.
func clip(s string) string {
	if len(s) <= maxRenderedBlock {
		return s
	}
	n := maxRenderedBlock
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return fmt.Sprintf("%s… (%d more bytes)", s[:n], len(s)-n)
}

// ContextWindow is a model's context window and the share of it past which a
// conversation is compacted before its next run, so that the run does not
// fail for want of room.
type ContextWindow struct {
	// Tokens is the window's size; 0 means unknown, and nothing is ever
	// full.
	Tokens int
	// Share is the part of Tokens a prompt may fill, above 0 and at most 1;
	// 0 means [DefaultCompactShare].
	Share float64
}

// DefaultCompactShare is the share of the context window a prompt may fill
// before the conversation is compacted.
const DefaultCompactShare = 0.8

// Full reports whether a request that used u filled the window past its
// share: its whole prompt (InputTokens + CacheReadTokens +
// CacheWriteTokens) is at least Share·Tokens.
func (w ContextWindow) Full(u llm.Usage) bool {
	if w.Tokens <= 0 {
		return false
	}
	share := w.Share
	if share <= 0 || share > 1 {
		share = DefaultCompactShare
	}
	prompt := u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens
	return float64(prompt) >= share*float64(w.Tokens)
}

// WithContextWindow tells the harness its model's context window, for
// [Harness.ShouldCompact]. Without it nothing is compacted ahead of time; a
// request that does not fit still fails with [llm.ErrContextTooLong], which a
// runner compacts and retries.
func WithContextWindow(w ContextWindow) Option { return func(c *config) { c.window = w } }

// ShouldCompact reports whether a conversation whose last request used u
// should be compacted before its next run ([ContextWindow.Full]).
func (h *Harness) ShouldCompact(u llm.Usage) bool { return h.cfg.window.Full(u) }
