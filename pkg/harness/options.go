package harness

import (
	"context"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/policy"
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
	policy      *policy.Policy
	toolsetOf   func(name string) string
	interaction Interaction
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

// WithToolPolicy decides every tool call by p before it runs, and before
// the [WithBeforeTool] hooks. A run's [Input.ToolPolicy] narrows p. toolsetOf
// names the toolset of a tool by the name the model calls it, for rules on
// toolsets; nil, or "", means not known. It is called from the calls' own
// goroutines, so it must be safe for concurrent use.
//
// A call p does not plainly allow gets an [EventToolPolicy] between its
// EventToolStart and EventToolDone, and a flagged one an [EventToolFlagged]
// after that, then runs. A denied call does not run: the model is shown "not
// allowed in this run: " and the decision's reason as an error result, and
// the run goes on. A call that needs approval waits for one if the run's
// [Interaction] may wait (the run suspends, see [WithInteraction]), and is
// refused like a denied one, saying why, if it may not. The [WithAfterTool]
// hooks see a refusal like any result.
//
// p must be valid ([policy.Policy.Validate]); New fails with
// [ErrInvalidConfig] otherwise. Without this option, every call is allowed,
// unless an Input.ToolPolicy says otherwise.
func WithToolPolicy(p policy.Policy, toolsetOf func(name string) string) Option {
	return func(c *config) {
		own := p.Clone() // not p: the option may be applied more than once
		c.policy, c.toolsetOf = &own, toolsetOf
	}
}

// WithInteraction sets how the harness's runs may involve a person (default
// [Autonomous]); a run's [Input.Interaction] can only narrow it.
//
// In a mode that may wait ([Interactive], [Supervised]), a tool call that
// asks a person (its tool returns a [*tool.InterruptError]) or that the tool
// policy wants approved waits: it gets an [EventInterrupt] in place of its
// EventToolDone, the other calls of the turn finish, and the run ends with
// [EventSuspended] carrying the [Pending] turn. Nothing of that turn is in the
// transcript, which stays valid. A later run given the pending turn and the
// answers ([Input.Pending], [Input.Answers]) finishes it first: an answered
// question's text is the call's result, an approved call runs with its
// original input, and a denied one's result is the denial. Then it goes on
// as usual.
//
// In [Autonomous] mode, tools that declare [tool.Metadata.Interrupts] are not
// offered to the model, a call that asks a person anyway gets an error
// result, and a call the policy wants approved is refused as denied. m must
// be one of [Interactions]; New fails with [ErrInvalidConfig] otherwise.
func WithInteraction(m Interaction) Option { return func(c *config) { c.interaction = m } }

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
