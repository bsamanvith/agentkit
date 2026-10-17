package agentapi

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/pkg/bus"
	memorybus "github.com/bsamanvith/agentkit/pkg/bus/memory"
	"github.com/bsamanvith/agentkit/pkg/component"
	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/stream"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// Name is the component's name, in /readyz and logs.
const Name = "agent"

// DefaultTenant is the tenant of a request that [WithTenant] gives none.
const DefaultTenant = "default"

var (
	// ErrInvalidConfig reports an unusable argument to [New].
	ErrInvalidConfig = errors.New("invalid agent API configuration")
	// ErrShutdown is the cause of a run ended by the drain or by
	// [Server.Shutdown].
	ErrShutdown = errors.New("server shutting down")
	// ErrRunTimeout is the cause of a run ended by [WithRunTimeout].
	ErrRunTimeout = errors.New("run timeout")
	// ErrRunsCutOff is returned by [Server.Shutdown] when runs were ended by
	// the shutdown instead of finishing.
	ErrRunsCutOff = errors.New("runs cut off by the shutdown")
)

// Server is the agent API: a [component.Component] serving /v1/ on top of a
// [runner.Runner] that runs one [harness.Harness] per agent and keeps runs in
// a store. Build it with [New] or [NewAgents]; it is safe for concurrent
// use.
type Server struct {
	cfg     config
	log     *slog.Logger
	handler http.Handler
	slots   chan struct{}
	routes  []string

	agents       map[string]*agent
	agentOrder   []*agent
	defaultAgent *agent // nil when a run must name its agent
	toolsets     map[string]*Toolset

	store  Store
	runs   Runs
	runner *runner.Runner // the default runs; nil with WithRuns
	bus    bus.Bus
	ownBus bool // the server made the bus, and closes it
	pub    *stream.Publisher
	cmds   *stream.Commands

	closeOnce sync.Once
	closed    chan struct{} // closed by Shutdown
	drainDone chan struct{} // closed when the drain goroutine has returned

	mu       sync.Mutex
	draining bool
	active   sync.WaitGroup // executions going on; Add only under mu while !draining
	hubs     map[hubKey]*hub
	finished []*hub   // hubs whose execution ended, oldest first: the retention order
	cut      []string // runs ended by the shutdown

	wmu     sync.Mutex
	watches map[hubKey]*sharedWatch // runs held elsewhere that this server follows (T103)
}

type hubKey struct{ tenant, runID string }

var _ component.Component = (*Server)(nil)

// New returns the agent API for h, the one agent, named [DefaultAgent]. It
// fails with [ErrInvalidConfig] for a nil harness or an option with an
// unusable value.
func New(h *harness.Harness, opts ...Option) (*Server, error) {
	if h == nil {
		return nil, fmt.Errorf("%w: nil harness", ErrInvalidConfig)
	}
	return NewAgents([]Agent{{Name: DefaultAgent, Harness: h}}, opts...)
}

func newServer(cfg config, agents map[string]*agent, order []*agent, def *agent, toolsets map[string]*Toolset) (*Server, error) {
	switch {
	case cfg.token != "" && len(cfg.token) < MinTokenLength:
		return nil, fmt.Errorf("%w: the token must be at least %d bytes", ErrInvalidConfig, MinTokenLength)
	case cfg.maxConcurrent < 1:
		return nil, fmt.Errorf("%w: max concurrent runs must be at least 1", ErrInvalidConfig)
	case cfg.runTimeout < 0, cfg.retention <= 0, cfg.keepAlive < 0, cfg.grace < 0, cfg.streamPoll < 0:
		return nil, fmt.Errorf("%w: negative run timeout, keep-alive or reconnect grace, or a retention that is not positive", ErrInvalidConfig)
	case cfg.maxRuns < 1, cfg.maxConvs < 1, cfg.maxBodyBytes < 1:
		return nil, fmt.Errorf("%w: retention counts and the body limit must be at least 1", ErrInvalidConfig)
	case cfg.store == nil:
		return nil, fmt.Errorf("%w: no store (WithStore)", ErrInvalidConfig)
	case cfg.clock == nil, cfg.newID == nil:
		return nil, fmt.Errorf("%w: nil clock or ID function", ErrInvalidConfig)
	case cfg.drainRequested && (cfg.drainStop == nil || cfg.drainAfter < 0):
		return nil, fmt.Errorf("%w: drain needs a stop channel and a time that is not negative", ErrInvalidConfig)
	}
	s := &Server{
		cfg: cfg, log: cfg.logger,
		agents: agents, agentOrder: order, defaultAgent: def, toolsets: toolsets,
		slots:   make(chan struct{}, cfg.maxConcurrent),
		closed:  make(chan struct{}),
		hubs:    map[hubKey]*hub{},
		watches: map[hubKey]*sharedWatch{},
		store:   cfg.store,
		bus:     cfg.bus,
	}
	if s.bus == nil {
		s.bus, s.ownBus = memorybus.New(memorybus.WithClock(cfg.clock)), true
	}
	sopts := []stream.Option{stream.WithLogger(cfg.logger), stream.WithClock(cfg.clock), stream.WithPoll(cfg.streamPoll)}
	var err error
	if s.pub, err = stream.NewPublisher(s.store, s.bus, sopts...); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	if s.cmds, err = stream.NewCommands(s.store, s.bus, sopts...); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	if cfg.runs != nil {
		if s.runs, err = cfg.runs(s.store, s.agentFor); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
		}
		if s.runs == nil {
			return nil, fmt.Errorf("%w: WithRuns made no runs", ErrInvalidConfig)
		}
	} else {
		ropts := append([]runner.Option{
			runner.WithClock(cfg.clock),
			runner.WithIDs(func() string { return cfg.newID("run_") }),
		}, cfg.runnerOpts...)
		rn, err := runner.New(s.store, s.agentFor, ropts...)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
		}
		s.runner, s.runs = rn, localRuns{rn}
	}

	mux := http.NewServeMux()
	// Every endpoint is in this table, which [Server.Routes] lists, so the
	// OpenAPI test sees each one.
	for _, rt := range []struct {
		pattern string
		handler http.HandlerFunc
	}{
		{"POST /v1/runs", s.postRun},
		{"GET /v1/runs", s.listRuns},
		{"GET /v1/runs/{id}", s.getRun},
		{"GET /v1/runs/{id}/events", s.runEvents},
		{"POST /v1/runs/{id}/resume", s.resumeRun},
		{"POST /v1/runs/{id}/stop", s.stopRun},
		{"POST /v1/runs/{id}/cancel", s.cancelRun},
		{"POST /v1/runs/{id}/decisions", s.decideRun},
		{"POST /v1/conversations", s.createConversation},
		{"GET /v1/conversations", s.listConversations},
		{"GET /v1/conversations/{id}", s.getConversation},
		{"DELETE /v1/conversations/{id}", s.deleteConversation},
		{"GET /v1/agents", s.listAgents},
		{"GET /v1/agents/{name}", s.getAgent},
		{"GET /v1/toolsets", s.listToolsets},
		{"GET /v1/toolsets/{name}/tools", s.listTools},
	} {
		mux.HandleFunc(rt.pattern, rt.handler)
		s.routes = append(s.routes, rt.pattern)
	}
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "no such endpoint: " + r.Method + " " + r.URL.Path})
	})
	s.handler = s.auth(mux)
	if cfg.drainStop != nil {
		s.drainDone = make(chan struct{})
		go s.drain(cfg.drainStop, cfg.drainAfter)
	}
	return s, nil
}

// errNoAgent means a conversation's agent is not one this server runs.
var errNoAgent = errors.New("agent not served")

// agentFor is the runner's agent factory: what runs the conversation's agent
// (its harness, or its runner), from its record.
func (s *Server) agentFor(ctx context.Context, conversationID string) (runner.Agent, error) {
	c, err := s.store.GetConversation(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	a := s.agents[c.Agent]
	if a == nil {
		return nil, fmt.Errorf("%w: conversation %q is of agent %q", errNoAgent, conversationID, c.Agent)
	}
	return a.run(), nil
}

// Routes returns the server's endpoints as "METHOD /path" patterns
// (net/http's, such as "GET /v1/runs/{id}"), in a fixed order.
func (s *Server) Routes() []string { return slices.Clone(s.routes) }

// Runner returns the runner the server runs its runs with, for the recovery
// loop ([runner.NewRecovery]) of the process; nil when [WithRuns] gave
// another orchestrator.
func (s *Server) Runner() *runner.Runner { return s.runner }

// Name implements [component.Component]: "agent".
func (s *Server) Name() string { return Name }

// Handler serves the API under /v1/. Mount it at "/v1/".
func (s *Server) Handler() http.Handler { return s.handler }

// Ready implements [component.Component]: the server is ready once built.
func (s *Server) Ready(ctx context.Context) error { return ctx.Err() }

// Shutdown stops the runs still going on, which end with [CodeShutdown] and
// stay interrupted in the store, and waits for them to be stored, until ctx
// is done. It returns an error matching [ErrRunsCutOff] if the shutdown
// stopped any run (here or at the drain deadline), so an unclean shutdown is
// reported. Later requests get 503. Stop the recovery loop first.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.draining = true
	s.mu.Unlock()
	s.closeOnce.Do(func() { close(s.closed) })

	var errs []error
	if err := s.runs.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("stopping the runs: %w", err))
	}
	done := make(chan struct{})
	go func() {
		s.active.Wait()
		if s.drainDone != nil {
			<-s.drainDone
		}
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		errs = append(errs, fmt.Errorf("runs still ending: %w", ctx.Err()))
	}
	s.stopWatches()
	s.mu.Lock()
	cut := slices.Clone(s.cut)
	s.mu.Unlock()
	if s.ownBus {
		_ = s.bus.Close()
	}
	if len(cut) > 0 {
		errs = append(errs, fmt.Errorf("%w: %d (%s)", ErrRunsCutOff, len(cut), strings.Join(cut, ", ")))
	}
	return errors.Join(errs...)
}

// drain waits for stop, then refuses new runs, and after the given time stops
// the runs still going on.
func (s *Server) drain(stop <-chan struct{}, after time.Duration) {
	defer close(s.drainDone)
	select {
	case <-stop:
	case <-s.closed:
		return
	}
	s.mu.Lock()
	s.draining = true
	n := len(s.slots)
	s.mu.Unlock()
	s.log.Info("draining runs", "active", n, "cut_after", after.String())
	t := s.cfg.clock.NewTimer(after)
	defer t.Stop()
	select {
	case <-t.C():
		if n := len(s.slots); n > 0 {
			s.log.Warn("stopping runs at the drain deadline", "active", n)
		}
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			select {
			case <-s.closed:
			case <-ctx.Done():
			}
			cancel()
		}()
		_ = s.runs.Shutdown(ctx) // Shutdown waits for what is left
		cancel()
	case <-s.closed:
	}
}

// auth checks the bearer token on every request, before routing.
func (s *Server) auth(next http.Handler) http.Handler {
	if s.cfg.token == "" {
		return next
	}
	want := []byte(s.cfg.token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme, got, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="agentkit"`)
			writeError(w, http.StatusUnauthorized, &Error{Code: CodeUnauthorized, Message: "missing or wrong bearer token"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// scope returns r's context with the request's tenant, which every store call
// is scoped by. It answers 500 for a tenant the middleware got wrong.
func (s *Server) scope(w http.ResponseWriter, r *http.Request) (context.Context, bool) {
	id := s.tenantOf(r)
	if id == "" {
		id = DefaultTenant
	}
	t, err := tenancy.NewTenant(id)
	if err != nil {
		s.internal(w, "reading the request's tenant", err)
		return nil, false
	}
	return tenancy.With(r.Context(), t), true
}

func (s *Server) postRun(w http.ResponseWriter, r *http.Request) {
	req, apiErr, code := s.decode(w, r)
	if apiErr != nil {
		writeError(w, code, apiErr)
		return
	}
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	if s.isDraining() {
		s.unavailable(w, shuttingDown())
		return
	}
	hasMessage := strings.TrimSpace(req.Message) != ""
	convID := req.ConversationID
	var ag *agent
	if convID != "" {
		c, err := s.store.GetConversation(ctx, convID)
		switch {
		case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrInvalid):
			writeError(w, http.StatusNotFound, notFoundConversation(convID))
			return
		case err != nil:
			s.internal(w, "reading conversation "+quote(convID), err)
			return
		case req.Agent != "" && req.Agent != c.Agent:
			writeError(w, http.StatusConflict, &Error{Code: CodeAgentMismatch, Message: "the conversation belongs to agent " + quote(c.Agent) + ", not " + quote(req.Agent) + "; continue it without \"agent\", or start a new conversation"})
			return
		}
		if ag = s.agents[c.Agent]; ag == nil {
			writeError(w, http.StatusConflict, &Error{Code: CodeAgentMismatch, Message: "the conversation belongs to agent " + quote(c.Agent) + ", which this server no longer runs"})
			return
		}
		if !hasMessage {
			s.resumeConversation(ctx, w, r, convID)
			return
		}
	} else {
		if !hasMessage {
			writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "message is empty"})
			return
		}
		if ag, apiErr, code = s.resolveAgent(req.Agent); apiErr != nil {
			writeError(w, code, apiErr)
			return
		}
	}
	if apiErr := s.admit(); apiErr != nil {
		s.unavailable(w, apiErr)
		return
	}
	created := false
	if convID == "" {
		convID = s.cfg.newID("conv_")
		if _, err := s.store.CreateConversation(ctx, store.Conversation{ID: convID, Agent: ag.Name}); err != nil {
			s.release()
			s.internal(w, "creating a conversation", err)
			return
		}
		created = true
	}
	execCtx, cancel := execContext(ctx)
	x, err := s.runs.Start(execCtx, convID, harness.UserText(req.Message))
	if err != nil {
		cancel(nil)
		s.release()
		if created {
			if derr := s.store.DeleteConversation(ctx, convID); derr != nil {
				s.log.Warn("deleting the conversation of a run that did not start", "conversation_id", convID, "err", derr)
			}
		}
		s.startError(w, err, CodeBusy)
		return
	}
	if _, err := s.store.SetTitle(ctx, convID, title(req.Message)); err != nil {
		s.log.Warn("setting a conversation's title", "conversation_id", convID, "err", err)
	}
	s.log.Info("run started", "run_id", x.Run.ID, "conversation_id", convID, "agent", ag.Name)
	h := s.launch(execCtx, cancel, x, replayed{})
	s.respond(w, r, h, bus.ID{})
}

// decode reads and checks the body of POST /v1/runs. Unknown fields and
// trailing data are errors.
func (s *Server) decode(w http.ResponseWriter, r *http.Request) (RunRequest, *Error, int) {
	var req RunRequest
	body, apiErr, code := s.readBody(w, r)
	if apiErr != nil {
		return req, apiErr, code
	}
	if err := decodeStrict(body, &req); err != nil {
		return req, &Error{Code: CodeInvalidRequest, Message: "the body must be a JSON object {\"message\": ..., \"conversation_id\": ...}: " + err.Error()}, http.StatusBadRequest
	}
	return req, nil, 0
}

// readBody reads a request's body, up to the body limit.
func (s *Server) readBody(w http.ResponseWriter, r *http.Request) ([]byte, *Error, int) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.cfg.maxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, &Error{Code: CodeTooLarge, Message: fmt.Sprintf("the body is larger than %d bytes", s.cfg.maxBodyBytes)}, http.StatusRequestEntityTooLarge
		}
		return nil, &Error{Code: CodeInvalidRequest, Message: "reading the body: " + err.Error()}, http.StatusBadRequest
	}
	return body, nil, 0
}

func decodeStrict(body []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data after the JSON object")
	}
	return nil
}

// wantsJSON reports whether the client asked for the final result as JSON
// instead of an event stream (the default).
func wantsJSON(accept string) bool {
	return strings.Contains(accept, "application/json") && !strings.Contains(accept, "text/event-stream")
}

func (s *Server) isDraining() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.draining
}

func shuttingDown() *Error {
	return &Error{Code: CodeShuttingDown, Message: "the server is shutting down", Retryable: true}
}

// admit takes a slot for an execution. On success the caller must call
// release when the execution has ended, or did not start.
func (s *Server) admit() *Error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.draining {
		return shuttingDown()
	}
	select {
	case s.slots <- struct{}{}:
	default:
		return &Error{Code: CodeOverCapacity, Message: fmt.Sprintf("%d runs are going on, the most this server takes; retry later", s.cfg.maxConcurrent), Retryable: true}
	}
	s.active.Add(1)
	return nil
}

func (s *Server) release() {
	<-s.slots
	s.active.Done()
}

// unavailable answers 503 with Retry-After.
func (s *Server) unavailable(w http.ResponseWriter, e *Error) {
	w.Header().Set("Retry-After", retryAfter)
	writeError(w, http.StatusServiceUnavailable, e)
}

// execContext is the context of an execution: the request's tenant, but
// not its cancellation, since a run outlives its request (the reconnect
// grace).
func execContext(req context.Context) (context.Context, context.CancelCauseFunc) {
	t, _ := tenancy.From(req)
	return context.WithCancelCause(tenancy.With(context.Background(), t))
}

// startError answers a run the runner would not start or resume. busy is the
// code of a run that is going on.
func (s *Server) startError(w http.ResponseWriter, err error, busy string) {
	var unfinished *runner.UnfinishedError
	switch {
	case errors.As(err, &unfinished):
		writeError(w, http.StatusConflict, &Error{Code: CodeBusy, RunID: unfinished.RunID, Retryable: true, Message: "run " + quote(unfinished.RunID) + " of this conversation is going on; wait for it to end, or stop it"})
	case errors.Is(err, store.ErrLeased):
		writeError(w, http.StatusConflict, &Error{Code: busy, Retryable: true, Message: "the run is going on: " + err.Error()})
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: err.Error()})
	case errors.Is(err, runner.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: err.Error()})
	case errors.Is(err, store.ErrFinal), errors.Is(err, runner.ErrNotStarted):
		writeError(w, http.StatusConflict, &Error{Code: CodeRunFinal, Message: err.Error()})
	case errors.Is(err, runner.ErrAwaitingDecision):
		writeError(w, http.StatusConflict, &Error{Code: CodeAwaitingDecision, Message: "the run waits for a decision (POST /v1/runs/{id}/decisions)"})
	case errors.Is(err, runner.ErrNotPending):
		writeError(w, http.StatusConflict, &Error{Code: CodeNotPending, Message: err.Error()})
	case errors.Is(err, flow.ErrShapeChanged):
		writeError(w, http.StatusConflict, &Error{Code: CodeShapeChanged, Message: err.Error()})
	case errors.Is(err, runner.ErrNoFlows):
		writeError(w, http.StatusConflict, &Error{Code: CodeInvalidRequest, Message: "this server runs no flows"})
	case errors.Is(err, runner.ErrShutdown):
		s.unavailable(w, shuttingDown())
	default:
		s.internal(w, "starting the run", err)
	}
}

// RunRecovered runs an execution the runner's recovery loop resumed as the
// server runs its own: its events go to the run's log, the bus and the
// clients that follow the run, and commands sent through other servers reach
// it. Give it to the loop with [runner.WithExecute]. It returns when the
// execution has ended; cancelling ctx stops it. Past the server's limit of
// runs going on, or while it shuts down, the execution runs without its
// events.
func (s *Server) RunRecovered(ctx context.Context, x *runner.Execution) {
	if apiErr := s.admit(); apiErr != nil {
		_, _ = x.Wait()
		return
	}
	execCtx, cancel := context.WithCancelCause(ctx)
	seed, err := s.history(ctx, x.Run, true)
	if err != nil {
		s.log.Error("reading a recovered run's events", "run_id", x.Run.ID, "err", err)
	}
	s.log.Info("run recovered", "run_id", x.Run.ID, "conversation_id", x.Run.ConversationID, "turns", seed.turns)
	h := s.launch(execCtx, cancel, x, seed)
	<-h.ended
}

// launch registers the hub of an execution, seeded with the events the run
// had, and runs the execution in its own goroutine. The admit slot is the
// execution's: drive releases it.
func (s *Server) launch(ctx context.Context, cancel context.CancelCauseFunc, x *runner.Execution, seed replayed) *hub {
	t, _ := tenancy.From(ctx)
	h := newHub(x.Run.ID, x.Run.ConversationID, t, x.Run.Interactive, seed.events)
	em := &emitter{log: s.log, h: h, seq: lastSeq(seed.events)}
	rs, err := s.pub.Open(ctx, x.Run.ID)
	if err != nil {
		s.log.Error("opening a run's event stream; its events reach this server's clients only", "run_id", x.Run.ID, "err", err)
	} else {
		em.rs = rs
	}
	s.mu.Lock()
	k := hubKey{t.ID(), h.runID}
	if old := s.hubs[k]; old != nil {
		s.finished = slices.DeleteFunc(s.finished, func(f *hub) bool { return f == old })
	}
	s.hubs[k] = h
	s.mu.Unlock()
	go s.drive(ctx, cancel, x, em, seed)
	return h
}

// drive runs an execution to its end, whoever follows it: its events go to
// the hub, and the last one says how the execution ended.
func (s *Server) drive(ctx context.Context, cancel context.CancelCauseFunc, x *runner.Execution, em *emitter, seed replayed) {
	h := em.h
	freeSlot := sync.OnceFunc(func() { <-s.slots })
	defer func() { freeSlot(); s.active.Done() }() // s.release, in two steps
	defer cancel(nil)
	defer s.obey(ctx, x, h)() // commands sent through other servers
	if s.cfg.runTimeout > 0 {
		t := s.cfg.clock.NewTimer(s.cfg.runTimeout)
		defer t.Stop()
		go func() {
			select {
			case <-t.C():
				cancel(ErrRunTimeout)
			case <-ctx.Done():
			}
		}()
	}
	// base is the run's turns and usage before the harness's current attempt,
	// which numbers its turns from 1.
	baseTurns, baseUsage := seed.turns, seed.usage
	turns, usage, final := seed.turns, seed.usage, seed.final
	started := len(seed.events) > 0
	if seed.rewind != nil {
		em.emit(ctx, *seed.rewind, true)
	}
	var (
		done   *runner.Event
		runErr error
	)
	for ev, err := range x.Events() {
		if err != nil {
			runErr = err
			break
		}
		switch ev.Kind {
		case runner.EventHarness:
			switch ev.Harness.Kind {
			case harness.EventRunDone:
				continue
			case harness.EventRunStart:
				if started { // a resume or a retry: the run started before
					continue
				}
				started = true
			case harness.EventTurnDone:
				turns, usage = turns+1, usage.Add(ev.Harness.TurnRecord.Usage)
				reply := ev.Harness.TurnRecord.Messages[0].Message
				final = &reply
			}
			if e, ok := wireEvent(h.runID, h.convID, baseTurns, ev.Harness); ok {
				em.emit(ctx, e, !streamedOnly(e.Type))
			}
		case runner.EventRetrying:
			baseTurns, baseUsage = turns, usage
			em.emit(ctx, Event{
				V: WireVersion, Type: TypeRetrying, RunID: h.runID, At: s.cfg.clock.Now(),
				Attempt: ev.Attempt, DelayMS: ms(ev.Delay), Error: s.classify(h.runID, ev.Err),
			}, true)
		case runner.EventDone:
			e := ev
			done = &e
		}
	}
	now := s.cfg.clock.Now()
	if !started { // the run failed before it started: announce it anyway
		em.emit(ctx, Event{V: WireVersion, Type: TypeRunStart, RunID: h.runID, ConversationID: h.convID, At: now}, true)
	}
	end := Event{V: WireVersion, Type: TypeRunError, RunID: h.runID, At: now}
	switch {
	case done != nil && done.Run.Status == store.StatusCompleted:
		end.Type = TypeRunDone
		if r := done.Result; r != nil {
			f := r.Final
			final, turns, usage = &f, baseTurns+r.Turns, baseUsage.Add(r.Usage)
		}
		if final != nil {
			f := *final
			end.Answer, end.Final = f.Text(), &f
		}
		end.Usage, end.Turns = &usage, turns
	case done != nil:
		end = *ending(done.Run, replayed{turns: turns, usage: usage, final: final}, now)
	default:
		var hre *harness.RunError
		if errors.As(runErr, &hre) {
			turns, usage = baseTurns+hre.Turns(), baseUsage.Add(hre.Usage())
		}
		end.Usage, end.Turns = &usage, turns
		end.Error = s.stopped(h.runID, runErr, context.Cause(ctx))
	}
	// The slot is free before the run's last event reaches its clients, so
	// one that starts its next run at once is not refused.
	freeSlot()
	// The last event is stored only when the run is final: a run that goes
	// on later (resumed, decided, retried) streams on after it.
	switch endStatus(end, h.interactive) {
	case StatusDone, StatusCanceled, StatusRejected:
		end = em.emit(ctx, end, true)
	default:
		end = em.emit(ctx, end, false)
	}
	h.finish(now)

	s.mu.Lock()
	if end.Error != nil && end.Error.Code == CodeShutdown {
		s.cut = append(s.cut, h.runID)
	}
	s.finished = append(s.finished, h)
	s.evict(now)
	s.mu.Unlock()

	attrs := []any{
		"run_id", h.runID, "conversation_id", h.convID, "event", end.Type, "turns", end.Turns,
		"input_tokens", usage.InputTokens, "output_tokens", usage.OutputTokens,
		"cache_read_tokens", usage.CacheReadTokens, "cache_write_tokens", usage.CacheWriteTokens,
	}
	switch {
	case end.Error == nil:
		s.log.Info("run done", attrs...)
	case end.Error.Code == CodeShutdown:
		s.log.Warn("run cut off by the shutdown", append(attrs, "error", end.Error.Message)...)
	default:
		s.log.Info("run ended", append(attrs, "code", end.Error.Code, "error", end.Error.Message)...)
	}
}

// stopped is the wire error of an execution that ended with err. cause is
// the execution context's cause.
func (s *Server) stopped(runID string, err, cause error) *Error {
	var re *runner.RunError
	switch {
	case errors.Is(err, runner.ErrInterrupted):
		return interruptedError(runID)
	case errors.Is(err, runner.ErrShutdown):
		return &Error{Code: CodeShutdown, Message: "the server shut down before the run finished; the stored turns are kept, resume it", Retryable: true}
	case errors.Is(err, runner.ErrCanceled):
		return &Error{Code: CodeCancelled, Message: "the run was canceled"}
	case errors.Is(err, store.ErrLeaseLost), errors.Is(err, store.ErrLeased),
		errors.Is(cause, store.ErrLeaseLost), errors.Is(cause, store.ErrLeased):
		// The store gave the run to another process, or could not renew its
		// lease in time: this server stopped executing it.
		s.log.Warn("run lost its lease", "run_id", runID, "err", cause)
		return interruptedError(runID)
	case errors.Is(cause, ErrRunTimeout):
		return &Error{Code: CodeTimeout, Message: fmt.Sprintf("the run took longer than %s; the stored turns are kept, resume it", s.cfg.runTimeout)}
	case errors.As(err, &re):
		e := s.classify(runID, re.Err)
		e.Retryable = e.Retryable || re.Run.Retryable
		return e
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return interruptedError(runID)
	default:
		s.log.Error("run stopped by an internal error", "run_id", runID, "err", err)
		return &Error{Code: CodeInternal, Message: "internal error; see the server log for run " + runID}
	}
}

// classify turns the error of a failed run into its wire form.
func (s *Server) classify(runID string, err error) *Error {
	msg := err.Error()
	var refusal *harness.RefusalError
	switch {
	case errors.As(err, &refusal):
		return &Error{Code: CodeRefused, Message: msg, Category: refusal.Category, Explanation: refusal.Explanation}
	case errors.Is(err, harness.ErrBudgetExceeded):
		return &Error{Code: CodeBudgetExceeded, Message: msg}
	case errors.Is(err, harness.ErrMaxTokens):
		return &Error{Code: CodeMaxTokens, Message: msg}
	case errors.Is(err, harness.ErrUnexpectedStop):
		return &Error{Code: CodeUnexpectedStop, Message: msg}
	case errors.Is(err, llm.ErrRateLimited):
		return &Error{Code: CodeRateLimited, Message: msg, Retryable: true}
	case errors.Is(err, llm.ErrOverloaded):
		return &Error{Code: CodeOverloaded, Message: msg, Retryable: true}
	case errors.Is(err, llm.ErrAuth):
		return &Error{Code: CodeModelAuth, Message: msg}
	case errors.Is(err, llm.ErrContextTooLong):
		return &Error{Code: CodeContextTooLong, Message: msg}
	case errors.Is(err, llm.ErrInvalidRequest):
		return &Error{Code: CodeModelRejected, Message: msg}
	case errors.Is(err, flow.ErrShapeChanged):
		return &Error{Code: CodeShapeChanged, Message: msg}
	default:
		s.log.Error("run failed with an internal error", "run_id", runID, "err", err)
		return &Error{Code: CodeInternal, Message: "internal error; see the server log for run " + runID}
	}
}

// evict lets go of the hubs retention no longer allows: past the count, and
// older than the retention. Called with Server.mu held.
func (s *Server) evict(now time.Time) {
	cutoff := now.Add(-s.cfg.retention)
	n := 0
	for n < len(s.finished) && (len(s.finished)-n > s.cfg.maxRuns || s.finished[n].finished.Before(cutoff)) {
		h := s.finished[n]
		if k := (hubKey{h.tenant.ID(), h.runID}); s.hubs[k] == h {
			delete(s.hubs, k)
		}
		n++
	}
	clear(s.finished[:n])
	s.finished = s.finished[n:]
}

// hubOf returns the run's hub, or nil.
func (s *Server) hubOf(ctx context.Context, runID string) *hub {
	t, _ := tenancy.From(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evict(s.cfg.clock.Now())
	return s.hubs[hubKey{t.ID(), runID}]
}

// respond answers a request that started or resumed a run: its events after
// the ID after, or, for a client that asked for JSON, the run once it ended.
func (s *Server) respond(w http.ResponseWriter, r *http.Request, h *hub, after bus.ID) {
	if !wantsJSON(r.Header.Get("Accept")) {
		s.follow(w, r, h, after)
		return
	}
	h.subscribe()
	select {
	case <-h.ended:
	case <-r.Context().Done():
	}
	s.leave(h)
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	if v, ok := s.viewByID(ctx, w, h.runID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// follow streams the hub's events after the ID after, as they come, until
// the run ends or the client goes away.
func (s *Server) follow(w http.ResponseWriter, r *http.Request, h *hub, after bus.ID) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	out := newSSEWriter(w, cancel)
	hd := w.Header()
	hd.Set("Content-Type", "text/event-stream")
	hd.Set("Cache-Control", "no-store")
	hd.Set("X-Accel-Buffering", "no")
	hd.Set("Agentkit-Run-Id", h.runID)
	hd.Set("Agentkit-Conversation-Id", h.convID)
	h.subscribe() // before the client sees the response: it is back
	defer s.leave(h)
	out.open()
	defer out.close()
	if s.cfg.keepAlive > 0 {
		stopKeepAlive := out.keepAlive(s.cfg.clock, s.cfg.keepAlive)
		defer stopKeepAlive()
	}
	for {
		evs, _ := h.next(ctx, after)
		if evs == nil {
			return
		}
		for _, e := range evs {
			out.event(e)
		}
		after = eventID(evs[len(evs)-1])
	}
}

// leave counts a client of h gone, and starts the reconnect grace if it was
// the last one of a chat run going on.
func (s *Server) leave(h *hub) {
	off, ok := h.unsubscribe()
	if !ok {
		return
	}
	t := s.cfg.clock.NewTimer(s.cfg.grace)
	if s.cfg.graceArmed != nil {
		s.cfg.graceArmed(h.runID)
	}
	go func() {
		defer t.Stop()
		select {
		case <-t.C():
		case <-off:
			return
		}
		if !h.abandoned() {
			return
		}
		s.log.Info("interrupting a run nobody follows", "run_id", h.runID, "grace", s.cfg.grace.String())
		if _, err := s.runs.Interrupt(tenancy.With(context.Background(), h.tenant), h.runID); err != nil {
			s.log.Warn("interrupting a run nobody follows", "run_id", h.runID, "err", err)
		}
	}()
}

// quote shows a client-supplied ID in a message, shortened.
func quote(s string) string {
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return fmt.Sprintf("%q", s)
}

func writeError(w http.ResponseWriter, code int, e *Error) {
	writeJSON(w, code, ErrorResponse{V: WireVersion, Error: e})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		body, code = []byte(`{"v":1,"error":{"code":"internal","message":"encoding the response failed"}}`), http.StatusInternalServerError
	}
	writeRaw(w, code, append(body, '\n'))
}

func writeRaw(w http.ResponseWriter, code int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = w.Write(body)
}
