package agentapi

import (
	"context"

	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/llm"
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
	// Start creates an interactive run of conversationID on msg, as
	// [runner.Runner.Run] with [runner.Interactive].
	Start(ctx context.Context, conversationID string, msg llm.Message) (*runner.Execution, error)
	// Resume, Decide, Cancel, Interrupt and Shutdown are as on
	// [runner.Runner].
	Resume(ctx context.Context, runID string) (*runner.Execution, error)
	Decide(ctx context.Context, runID, path string, d flow.Decision) (*runner.Execution, error)
	Cancel(ctx context.Context, runID string) (store.Run, error)
	Interrupt(ctx context.Context, runID string) (store.Run, error)
	Shutdown(ctx context.Context) error
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

func (r localRuns) Start(ctx context.Context, conversationID string, msg llm.Message) (*runner.Execution, error) {
	return r.Run(ctx, conversationID, msg, runner.Interactive())
}

func (r localRuns) Resume(ctx context.Context, runID string) (*runner.Execution, error) {
	return r.Runner.Resume(ctx, runID)
}

func (r localRuns) Decide(ctx context.Context, runID, path string, d flow.Decision) (*runner.Execution, error) {
	return r.Runner.Decide(ctx, runID, path, d)
}
