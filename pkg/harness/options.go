package harness

import (
	"context"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/llm"
)

const (
	defaultMaxTurns    = 25
	defaultMaxParallel = 8
)

// BeforeToolFunc runs before a tool call. Returning an error denies the call:
// the tool does not run, and the model is shown the error as an error result.
// Hooks run on the goroutine of the call, so they must be safe for concurrent
// use and should return promptly once ctx is done.
type BeforeToolFunc func(ctx context.Context, call llm.ToolUse) error

// AfterToolFunc runs after a tool call, with every result: the tool's output,
// its failure, or a denial. The result it returns is what the model sees; its
// ToolUseID is always set back to the call's. Same rules as [BeforeToolFunc].
type AfterToolFunc func(ctx context.Context, call llm.ToolUse, res llm.ToolResult) llm.ToolResult

// RequestOptions are the parts of every model request that the harness does
// not decide. The harness fills in the rest: the system prompt, the messages
// and the tool definitions.
type RequestOptions struct {
	// MaxTokens limits the output tokens of each reply; zero means the
	// adapter's default.
	MaxTokens int
	// Provider holds options for one provider, such as thinking or caching
	// settings; see [llm.Request.Provider]. It is sent as is on every turn
	// and must not be modified once the harness has it.
	Provider any
}

type config struct {
	request     RequestOptions
	system      string
	maxTurns    int
	maxTokens   int
	maxParallel int
	before      []BeforeToolFunc
	after       []AfterToolFunc
	clock       clock.Clock
	window      ContextWindow
}

// Option configures [New].
type Option func(*config)

// WithSystem sets the system prompt of every request.
func WithSystem(prompt string) Option { return func(c *config) { c.system = prompt } }

// WithRequest sets the options every model request carries. The messages and
// tools are never taken from it.
func WithRequest(opts RequestOptions) Option { return func(c *config) { c.request = opts } }

// WithMaxTurns limits a run to n model calls (default 25). A run that would
// need more fails with [ErrBudgetExceeded]. n must be at least 1.
func WithMaxTurns(n int) Option { return func(c *config) { c.maxTurns = n } }

// WithMaxTotalTokens limits the tokens a run may use; zero means no limit.
// A turn counts InputTokens, CacheReadTokens, CacheWriteTokens and
// OutputTokens, and the limit is checked before each model call, so the last
// turn can go over it. A run that reaches it fails with [ErrBudgetExceeded].
func WithMaxTotalTokens(n int) Option { return func(c *config) { c.maxTokens = n } }

// WithMaxParallelTools limits how many tool calls of one turn run at once
// (default 8). n must be at least 1.
func WithMaxParallelTools(n int) Option { return func(c *config) { c.maxParallel = n } }

// WithBeforeTool adds a hook that can deny tool calls. Hooks run in the order
// given; the first denial wins.
func WithBeforeTool(f BeforeToolFunc) Option {
	return func(c *config) { c.before = append(c.before, f) }
}

// WithAfterTool adds a hook that can replace tool results. Hooks run in the
// order given, each seeing the result of the one before.
func WithAfterTool(f AfterToolFunc) Option {
	return func(c *config) { c.after = append(c.after, f) }
}

// WithClock sets the clock used for every timestamp and duration (default
// [clock.Real]). The harness only reads its Now, from several goroutines:
// tool calls read it from their own. Durations are the difference of two
// readings, so they are monotonic only if the clock returns times that carry a
// monotonic reading, as the real clock does and the UTC or Round(0) forms of
// its times do not.
func WithClock(c clock.Clock) Option { return func(cfg *config) { cfg.clock = c } }
