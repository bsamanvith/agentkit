package agentapi

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store"
)

// RunContext is what [WithRunContext] is told about the chat run an agent
// runs.
type RunContext struct {
	// Agent is the conversation's agent.
	Agent string
	// ConversationID is the run's conversation.
	ConversationID string
}

// WithRunContext sets f, which makes the context every execution of a chat
// run's agent runs in from the execution's own and rc: the agent's tools
// see it. Agent messaging (package agentmsg) learns there who sends a
// message. It reaches only agents that run in this process (a pool that runs
// agents elsewhere, such as in a sandbox, gets none of the context's values).
func WithRunContext(f func(ctx context.Context, rc RunContext) context.Context) Option {
	return func(c *config) { c.runContext = f }
}

// withRunContext wraps the runner's agent factory so that the agents it
// gives run in the context WithRunContext makes.
func (s *Server) withRunContext(f runner.AgentFactory) runner.AgentFactory {
	if s.cfg.runContext == nil {
		return f
	}
	return func(ctx context.Context, p runner.Placement) (runner.Agent, error) {
		a, err := f(ctx, p)
		if err != nil {
			return nil, err
		}
		c, err := s.store.GetConversation(ctx, p.ConversationID)
		if err != nil {
			return nil, err
		}
		rc := RunContext{Agent: c.Agent, ConversationID: p.ConversationID}
		return contextAgent{a: a, ctx: func(ctx context.Context) context.Context { return s.cfg.runContext(ctx, rc) }}, nil
	}
}

// contextAgent runs a in the context ctx makes.
type contextAgent struct {
	a   runner.Agent
	ctx func(context.Context) context.Context
}

func (c contextAgent) Run(ctx context.Context, in harness.Input) iter.Seq2[harness.Event, error] {
	return c.a.Run(c.ctx(ctx), in)
}

// Deliver starts a background run of agent on input in the conversation
// conversationID, of the tenant in ctx, creating the conversation for the
// agent (titled title) if there is none: how agent messaging (package
// agentmsg) hands a message to its recipient. The run is stored with its
// prompt at once ([Server.Enqueue]), in autonomous mode, and starts here when
// the server and the run's pool have a free slot; otherwise the pool's
// dispatcher starts it. A conversation with an unfinished run gives an error
// matching [runner.ErrRunUnfinished]: deliver again once it has ended. A
// conversation of another agent, or an agent the server does not run, is an
// error matching [ErrInvalidConfig].
func (s *Server) Deliver(ctx context.Context, agent, conversationID, title, input string) (store.Run, error) {
	a := s.agents[agent]
	if a == nil {
		return store.Run{}, fmt.Errorf("%w: no agent %q", ErrInvalidConfig, agent)
	}
	c, err := s.store.GetConversation(ctx, conversationID)
	if errors.Is(err, store.ErrNotFound) {
		c, err = s.store.CreateConversation(ctx, store.Conversation{ID: conversationID, Agent: agent, Title: title})
		if errors.Is(err, store.ErrConflict) { // created at once by another delivery
			c, err = s.store.GetConversation(ctx, conversationID)
		}
	}
	if err != nil {
		return store.Run{}, err
	}
	if c.Agent != agent {
		return store.Run{}, fmt.Errorf("%w: conversation %q is of agent %q, not %q", ErrInvalidConfig, conversationID, c.Agent, agent)
	}
	run, err := s.Enqueue(ctx, conversationID, harness.UserText(input), runner.Interaction(harness.Autonomous))
	if err != nil {
		return store.Run{}, err
	}
	s.startQueued(ctx, run, a)
	return run, nil
}

// startQueued starts a queued run of agent a here, if the server and the
// run's pool have a slot free; otherwise, or if another process took it
// first, the run waits for its dispatcher.
func (s *Server) startQueued(ctx context.Context, run store.Run, a *agent) {
	if apiErr := s.admit(); apiErr != nil {
		return
	}
	_, free, _, apiErr := s.admitPool(a, runner.Placement{ConversationID: run.ConversationID, RunID: run.ID, Resume: true, Pool: run.Pool})
	if apiErr != nil {
		s.release()
		return
	}
	execCtx, cancel := execContext(ctx)
	x, err := s.runs.Resume(execCtx, run.ID)
	if err != nil {
		cancel(nil)
		s.release()
		free()
		return
	}
	seed, err := s.history(ctx, x.Run, true)
	if err != nil {
		s.log.Error("reading a delivered run's events", "run_id", run.ID, "err", err)
	}
	s.log.Info("delivered run started", "run_id", run.ID, "conversation_id", run.ConversationID, "agent", a.Name, "pool", x.Run.Pool)
	s.launch(execCtx, cancel, x, seed, free)
}

// RunResult returns run runID, of the tenant in ctx, as GET /v1/runs/{id}
// shows it but without its record and webhook. FinishedAt is set only once
// the run has ended for good (it is final, or failed and not retried) and
// its last event is stored, with its answer or error.
func (s *Server) RunResult(ctx context.Context, runID string) (Run, error) {
	run, err := s.store.Get(ctx, runID)
	if err != nil {
		return Run{}, err
	}
	v, err := s.runView(ctx, run)
	if err != nil {
		return Run{}, err
	}
	ended := run.Status.Final() || (run.Status == store.StatusFailed && !run.Retryable)
	if !ended || v.Record == nil || v.Record.FinishedAt.IsZero() {
		v.FinishedAt = nil
	}
	v.Record, v.Notify = nil, nil
	return v, nil
}

// listThreads serves GET /v1/threads: the tenant's threads of messages
// between agents, newest first.
func (s *Server) listThreads(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, ok := listLimit(w, q.Get("limit"))
	if !ok {
		return
	}
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	page, err := s.store.ListThreads(ctx, store.Page{Cursor: q.Get("cursor"), Limit: limit, Order: store.Descending})
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "cursor is not one this server gave"})
		return
	case err != nil:
		s.internal(w, "listing threads", err)
		return
	}
	out := wire.ThreadList{V: WireVersion, Threads: []wire.Thread{}, NextCursor: page.Next}
	for _, th := range page.Threads {
		out.Threads = append(out.Threads, threadView(th))
	}
	writeJSON(w, http.StatusOK, out)
}

// getThread serves GET /v1/threads/{id}: the thread and its messages.
func (s *Server) getThread(w http.ResponseWriter, r *http.Request) {
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	th, err := s.store.GetThread(ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "no thread " + quote(id)})
		return
	case err != nil:
		s.internal(w, "reading thread "+quote(id), err)
		return
	}
	v := threadView(th)
	v.Messages = []wire.ThreadMessage{}
	for after := 0; ; {
		ms, err := s.store.ThreadMessages(ctx, id, after, store.MaxPageLimit)
		if err != nil {
			s.internal(w, "reading the messages of thread "+quote(id), err)
			return
		}
		for _, m := range ms {
			v.Messages = append(v.Messages, messageView(m))
			after = m.Hop
		}
		if len(ms) < store.MaxPageLimit {
			break
		}
	}
	writeJSON(w, http.StatusOK, v)
}

func threadView(th store.Thread) wire.Thread {
	v := wire.Thread{
		V: WireVersion, ID: th.ID, Status: string(th.Status), Hops: th.Hops, Tokens: th.Tokens,
		CreatedAt: th.CreatedAt, UpdatedAt: th.UpdatedAt,
	}
	if th.Status == store.ThreadStopped {
		v.Error = &Error{Code: th.StopCode, Message: th.StopReason}
	}
	return v
}

func messageView(m store.Message) wire.ThreadMessage {
	v := wire.ThreadMessage{
		ID: m.ID, Hop: m.Hop, From: m.From, To: m.To, FromConversation: m.FromConversation, ToConversation: m.ToConversation,
		Kind: string(m.Kind), Text: m.Text, Data: m.Data, InReplyTo: m.InReplyTo, State: string(m.State), RunID: m.RunID,
		CreatedAt: m.CreatedAt,
	}
	if !m.DeliveredAt.IsZero() {
		at := m.DeliveredAt
		v.DeliveredAt = &at
	}
	if !m.ProcessedAt.IsZero() {
		at := m.ProcessedAt
		v.ProcessedAt = &at
	}
	return v
}
