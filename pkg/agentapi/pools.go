package agentapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/runner/pool"
	"github.com/bsamanvith/agentkit/pkg/store"
)

// ErrNoRunner is what [Server.Enqueue] and [Server.Dispatcher] give on a
// server whose runs another orchestrator runs ([WithRuns]).
var ErrNoRunner = errors.New("the server's runs are not run by its runner (WithRuns)")

// admitPool places a run of agent a whose placement is p in its pool, and
// takes one of the pool's slots ([pool.WithLimit]) for it. It answers 429
// over_capacity when the pool is full and 409 when no pool may run the
// agent. On success the run is to be placed in the returned pool
// ([runner.InPool]), and release gives the slot back once its execution has
// ended or did not start.
func (s *Server) admitPool(a *agent, p runner.Placement) (name string, release func(), status int, apiErr *Error) {
	pl, err := s.router.Route(pool.Request{Placement: p, Agent: a.Name, Isolation: a.Isolation})
	if err != nil {
		return "", nil, http.StatusConflict, noPool(err)
	}
	slots := s.router.Slots(pl.Name())
	if slots == nil {
		return pl.Name(), func() {}, 0, nil
	}
	if !slots.TryAcquire() {
		return "", nil, http.StatusTooManyRequests, &Error{
			Code:      CodeOverCapacity,
			Message:   fmt.Sprintf("pool %q runs %d runs, its limit; retry later", pl.Name(), slots.Limit()),
			Retryable: true,
		}
	}
	return pl.Name(), sync.OnceFunc(slots.Release), 0, nil
}

// noPool is the error of a run no pool may run ([pool.ErrNoPool]).
func noPool(err error) *Error {
	return &Error{Code: CodeInvalidRequest, Message: "this server has no pool isolated enough for the agent: " + err.Error()}
}

// refuse answers a run admitPool refused.
func (s *Server) refuse(w http.ResponseWriter, status int, e *Error) {
	if status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", retryAfter)
	}
	writeError(w, status, e)
}

// Enqueue creates a background run of msg on the conversation and leaves it
// in the run queue, as [runner.Runner.Enqueue] does: its pool's dispatcher
// ([Server.Dispatcher]) starts it once the pool has a free slot, so it
// waits in the store meanwhile and never takes a slot from another pool's
// chats. The run is routed as a background run of the conversation's agent
// and placed in that pool and in its lane (a pool's lane is its name). opts
// are the runner's: [runner.Priority], [runner.StartAt], [runner.ToolPolicy]
// and the like; a [runner.Lane] or [runner.InPool] among them is
// overridden. ctx carries the tenant. It gives [ErrNoRunner] with
// [WithRuns], and an error matching [pool.ErrNoPool] when no pool may run
// the agent.
func (s *Server) Enqueue(ctx context.Context, conversationID string, msg llm.Message, opts ...runner.RunOption) (store.Run, error) {
	if s.runner == nil {
		return store.Run{}, ErrNoRunner
	}
	c, err := s.store.GetConversation(ctx, conversationID)
	if err != nil {
		return store.Run{}, err
	}
	a := s.agents[c.Agent]
	if a == nil {
		return store.Run{}, fmt.Errorf("%w: conversation %q is of agent %q", errNoAgent, conversationID, c.Agent)
	}
	pl, err := s.router.Route(pool.Request{Placement: runner.Placement{ConversationID: conversationID}, Agent: a.Name, Isolation: a.Isolation})
	if err != nil {
		return store.Run{}, err
	}
	opts = append(slices.Clip(opts), runner.Lane(pl.Name()), runner.InPool(pl.Name()))
	run, err := s.runner.Enqueue(ctx, conversationID, msg, opts...)
	if err != nil {
		return store.Run{}, err
	}
	s.log.Info("run queued", "run_id", run.ID, "conversation_id", conversationID, "agent", a.Name, "pool", run.Pool)
	return run, nil
}

// Dispatcher returns the dispatcher of the named pool's lane: a
// [runner.Recovery] that claims the pool's background runs (those
// [Server.Enqueue] queued there, and those whose execution stopped) while
// the pool has a free slot, and runs each as the server runs its own
// ([Server.RunRecovered]). The default pool's dispatcher also claims the
// default lane "", where runs from before pools wait. A pool with no limit
// runs as many at once as [runner.WithWorkers] says (default 4). opts come
// after the server's ([runner.WithInterval], [runner.WithBatch]). Run one
// per pool that takes background runs, and stop them before
// [Server.Shutdown].
func (s *Server) Dispatcher(admin store.Admin, poolName string, opts ...runner.RecoveryOption) (*runner.Recovery, error) {
	if s.runner == nil {
		return nil, ErrNoRunner
	}
	if !slices.ContainsFunc(s.router.Pools(), func(p pool.Pool) bool { return p.Name() == poolName }) {
		return nil, fmt.Errorf("%w: no pool %q", ErrInvalidConfig, poolName)
	}
	lanes := []string{poolName}
	if poolName == s.router.DefaultPool().Name() {
		lanes = append(lanes, "")
	}
	ropts := []runner.RecoveryOption{runner.WithLanes(lanes...), runner.WithExecute(s.RunRecovered)}
	if slots := s.router.Slots(poolName); slots != nil {
		ropts = append(ropts, runner.WithSlots(slots))
	}
	return runner.NewRecovery(s.runner, admin, append(ropts, opts...)...)
}
