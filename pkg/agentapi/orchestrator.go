package agentapi

import (
	"context"

	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/policy"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store"
)

// Runs is what the server runs its runs with: creates, resumes, decides,
// cancels and interrupts them, and stops them all at shutdown. The default is
// a [*runner.Runner] over the server's store, which runs every execution in
// this process under a lease; [WithRuns] puts another orchestrator in its
// place, such as one that hands runs to a queue of workers. Its runs must be
// in the server's store, under the same rules: the server reads them, their
// events and their conversations there.
type Runs interface {
	// Start creates a run of conversationID on msg, as [runner.Runner.Run]
	// with the options in opts, which it must apply: a run must never run
	// without its ToolPolicy, and it runs in the Pool the server took a slot
	// of. The run is interactive ([runner.Interactive]) unless
	// opts.Background: a background run (a task) is the orchestrator's to
	// finish, resuming it after a crash, whoever watches.
	Start(ctx context.Context, conversationID string, msg llm.Message, opts StartOptions) (*runner.Execution, error)
	// Resume, Decide, Answer, Cancel, Interrupt and Shutdown are as on
	// [runner.Runner].
	Resume(ctx context.Context, runID string) (*runner.Execution, error)
	Decide(ctx context.Context, runID, path string, d flow.Decision) (*runner.Execution, error)
	Answer(ctx context.Context, runID string, answers ...harness.Answer) (*runner.Execution, error)
	Cancel(ctx context.Context, runID string) (store.Run, error)
	Interrupt(ctx context.Context, runID string) (store.Run, error)
	Shutdown(ctx context.Context) error
}

// StartOptions are the options of a run a request starts.
type StartOptions struct {
	// ToolPolicy narrows the agent's tool policy for the run and its
	// resumes ([runner.ToolPolicy]); nil for none.
	ToolPolicy *policy.Policy
	// Interaction narrows the agent's interaction mode for the run and its
	// resumes ([runner.Interaction]); empty for the agent's.
	Interaction harness.Interaction
	// Pool is the runner pool the server placed the run in, and took a slot
	// of ([runner.InPool]); "" for none.
	Pool string
	// Background starts a background run (POST /v1/tasks) instead of an
	// interactive one.
	Background bool
}

// RunOptions are opts as the runner takes them, [runner.Interactive]
// included unless Background.
func (o StartOptions) RunOptions() []runner.RunOption {
	var out []runner.RunOption
	if o.ToolPolicy != nil {
		out = append(out, runner.ToolPolicy(*o.ToolPolicy))
	}
	if o.Interaction != "" {
		out = append(out, runner.Interaction(o.Interaction))
	}
	if o.Pool != "" {
		out = append(out, runner.InPool(o.Pool))
		if o.Background {
			// In the pool's lane: its dispatcher resumes it after a crash
			// ([Server.Dispatcher]).
			out = append(out, runner.Lane(o.Pool))
		}
	}
	if !o.Background {
		out = append(out, runner.Interactive())
	}
	return out
}

// RunsFactory builds the [Runs] of a server over its store and the factory
// of its agents (what runs a conversation's agent).
type RunsFactory func(st runner.Store, agents runner.AgentFactory) (Runs, error)

// WithRuns sets what the server runs its runs with (default: a
// [*runner.Runner], and [Server.Runner] returns it). With it, [Server.Runner]
// is nil: recovering abandoned runs is the orchestrator's business.
func WithRuns(f RunsFactory) Option { return func(c *config) { c.runs = f } }

// localRuns is the default [Runs]: a [*runner.Runner].
type localRuns struct{ *runner.Runner }

func (r localRuns) Start(ctx context.Context, conversationID string, msg llm.Message, opts StartOptions) (*runner.Execution, error) {
	return r.Run(ctx, conversationID, msg, opts.RunOptions()...)
}

func (r localRuns) Resume(ctx context.Context, runID string) (*runner.Execution, error) {
	return r.Runner.Resume(ctx, runID)
}

func (r localRuns) Answer(ctx context.Context, runID string, answers ...harness.Answer) (*runner.Execution, error) {
	return r.Runner.Answer(ctx, runID, answers...)
}

func (r localRuns) Decide(ctx context.Context, runID, path string, d flow.Decision) (*runner.Execution, error) {
	return r.Runner.Decide(ctx, runID, path, d)
}
