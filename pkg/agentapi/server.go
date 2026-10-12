package agentapi

import (
	"cmp"
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

	"github.com/bsamanvith/agentkit/pkg/component"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
)

// Name is the component's name, in /readyz and logs.
const Name = "agent"

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

// errClientGone is the cause of a run whose client could not be written to.
var errClientGone = errors.New("client went away")

// Server is the agent API: a [component.Component] serving /v1/ on top of
// one [harness.Harness] per agent. Build it with [New] or [NewAgents]; it is
// safe for concurrent use.
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

	stopCtx context.Context // done when runs must end (the drain deadline, Shutdown)
	stop    context.CancelCauseFunc

	closeOnce sync.Once
	closed    chan struct{} // closed by Shutdown
	drainDone chan struct{} // closed when the drain goroutine has returned

	mu       sync.Mutex
	draining bool
	active   sync.WaitGroup // runs going on; Add only under mu while !draining
	runs     map[string]*run
	runOrder []*run // creation order
	convs    map[string]*conversation
	convSeq  uint64   // counts conversations created, for conversation.seq
	touches  uint64   // counts conversation starts and ends, for conversation.touched
	cut      []string // runs ended by the shutdown
}

var _ component.Component = (*Server)(nil)

// run is a run's state. Guarded by Server.mu.
type run struct {
	id, convID string
	agent      string
	tenant     string
	created    time.Time
	finished   time.Time
	status     string
	answer     string
	usage      llm.Usage
	turns      int
	err        *Error
	record     *Record
}

// conversation is a conversation's state. Guarded by Server.mu.
type conversation struct {
	id               string
	agent            *agent
	tenant           string
	seq              uint64 // order of creation, from 1: the listing order
	title            string
	created, updated time.Time
	touched          uint64 // Server.touches when a run of it last started or ended: the eviction order
	messages         []harness.TimedMessage
	running          bool
}

func (c *conversation) status() string {
	switch {
	case c.running:
		return StatusRunning
	case len(c.messages) > 0 && c.messages[len(c.messages)-1].Message.Role == llm.RoleUser:
		return StatusInterrupted
	default:
		return StatusIdle
	}
}

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
	case cfg.runTimeout < 0, cfg.retention <= 0, cfg.keepAlive < 0:
		return nil, fmt.Errorf("%w: negative run timeout or keep-alive, or a retention that is not positive", ErrInvalidConfig)
	case cfg.maxRuns < 1, cfg.maxConvs < 1, cfg.maxBodyBytes < 1:
		return nil, fmt.Errorf("%w: retention counts and the body limit must be at least 1", ErrInvalidConfig)
	case cfg.clock == nil, cfg.newID == nil:
		return nil, fmt.Errorf("%w: nil clock or ID function", ErrInvalidConfig)
	case cfg.drainRequested && (cfg.drainStop == nil || cfg.drainAfter < 0):
		return nil, fmt.Errorf("%w: drain needs a stop channel and a time that is not negative", ErrInvalidConfig)
	}
	s := &Server{
		cfg: cfg, log: cfg.logger,
		agents: agents, agentOrder: order, defaultAgent: def, toolsets: toolsets,
		slots:  make(chan struct{}, cfg.maxConcurrent),
		closed: make(chan struct{}),
		runs:   map[string]*run{},
		convs:  map[string]*conversation{},
	}
	s.stopCtx, s.stop = context.WithCancelCause(context.Background())
	mux := http.NewServeMux()
	// Every endpoint is in this table, which [Server.Routes] lists, so the
	// OpenAPI test sees each one.
	for _, rt := range []struct {
		pattern string
		handler http.HandlerFunc
	}{
		{"POST /v1/runs", s.postRun},
		{"GET /v1/runs/{id}", s.getRun},
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

// Routes returns the server's endpoints as "METHOD /path" patterns
// (net/http's, such as "GET /v1/runs/{id}"), in a fixed order.
func (s *Server) Routes() []string { return slices.Clone(s.routes) }

// Name implements [component.Component]: "agent".
func (s *Server) Name() string { return Name }

// Handler serves the API under /v1/. Mount it at "/v1/".
func (s *Server) Handler() http.Handler { return s.handler }

// Ready implements [component.Component]: the server is ready once built.
func (s *Server) Ready(ctx context.Context) error { return ctx.Err() }

// Shutdown ends the runs still going on with [CodeShutdown] and waits for
// them to be recorded, until ctx is done. It returns an error matching
// [ErrRunsCutOff] if the shutdown ended any run (here or at the drain
// deadline), so an unclean shutdown is reported. Later requests get 503.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.draining = true
	s.mu.Unlock()
	s.closeOnce.Do(func() { close(s.closed) })
	s.stop(ErrShutdown)

	done := make(chan struct{})
	go func() {
		s.active.Wait()
		if s.drainDone != nil {
			<-s.drainDone
		}
		close(done)
	}()
	var errs []error
	select {
	case <-done:
	case <-ctx.Done():
		errs = append(errs, fmt.Errorf("runs still ending: %w", ctx.Err()))
	}
	s.mu.Lock()
	cut := slices.Clone(s.cut)
	s.mu.Unlock()
	if len(cut) > 0 {
		errs = append(errs, fmt.Errorf("%w: %d (%s)", ErrRunsCutOff, len(cut), strings.Join(cut, ", ")))
	}
	return errors.Join(errs...)
}

// drain waits for stop, then refuses new runs, and after the given time ends
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
			s.log.Warn("ending runs at the drain deadline", "active", n)
		}
		s.stop(ErrShutdown)
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

// started is a run that passed admission.
type started struct {
	agent      *agent
	run        *run
	conv       *conversation
	message    string // empty to resume
	transcript harness.Transcript
}

func (s *Server) postRun(w http.ResponseWriter, r *http.Request) {
	req, apiErr, code := s.decode(w, r)
	if apiErr != nil {
		writeError(w, code, apiErr)
		return
	}
	st, apiErr, code := s.admit(req, s.tenantOf(r))
	if apiErr != nil {
		if code == http.StatusServiceUnavailable {
			w.Header().Set("Retry-After", retryAfter)
		}
		writeError(w, code, apiErr)
		return
	}
	defer s.active.Done()
	s.execute(w, r, st, wantsJSON(r.Header.Get("Accept")))
}

// decode reads and checks the body of POST /v1/runs. Unknown fields and
// trailing data are errors.
func (s *Server) decode(w http.ResponseWriter, r *http.Request) (RunRequest, *Error, int) {
	var req RunRequest
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.cfg.maxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return req, &Error{Code: CodeTooLarge, Message: fmt.Sprintf("the body is larger than %d bytes", s.cfg.maxBodyBytes)}, http.StatusRequestEntityTooLarge
		}
		return req, &Error{Code: CodeInvalidRequest, Message: "reading the body: " + err.Error()}, http.StatusBadRequest
	}
	if err := decodeStrict(body, &req); err != nil {
		return req, &Error{Code: CodeInvalidRequest, Message: "the body must be a JSON object {\"message\": ..., \"conversation_id\": ...}: " + err.Error()}, http.StatusBadRequest
	}
	return req, nil, 0
}

func decodeStrict(body []byte, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(body)))
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

// admit checks the request against the conversation and the limits, and
// registers the run. On success the caller must call s.active.Done when the
// run has been finished.
func (s *Server) admit(req RunRequest, tenant string) (*started, *Error, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.draining {
		return nil, &Error{Code: CodeShuttingDown, Message: "the server is shutting down", Retryable: true}, http.StatusServiceUnavailable
	}
	now := s.cfg.clock.Now()
	s.evict(now)
	hasMessage := strings.TrimSpace(req.Message) != ""
	conv := (*conversation)(nil)
	var ag *agent
	if req.ConversationID != "" {
		conv = s.convs[req.ConversationID]
		if conv != nil && conv.tenant != tenant {
			conv = nil // another tenant's conversation does not exist for this one
		}
		switch {
		case conv == nil:
			return nil, notFoundConversation(req.ConversationID), http.StatusNotFound
		case req.Agent != "" && req.Agent != conv.agent.Name:
			return nil, &Error{Code: CodeAgentMismatch, Message: "the conversation belongs to agent " + quote(conv.agent.Name) + ", not " + quote(req.Agent) + "; continue it without \"agent\", or start a new conversation"}, http.StatusConflict
		case conv.running:
			return nil, &Error{Code: CodeBusy, Message: "a run of this conversation is going on; wait for it to end", Retryable: true}, http.StatusConflict
		case conv.status() == StatusInterrupted && hasMessage:
			return nil, &Error{Code: CodeInterrupted, Message: "the conversation's last run stopped before answering; resume it first with an empty message"}, http.StatusConflict
		case conv.status() != StatusInterrupted && !hasMessage:
			return nil, &Error{Code: CodeInvalidRequest, Message: "message is empty"}, http.StatusBadRequest
		}
		ag = conv.agent
	} else {
		if !hasMessage {
			return nil, &Error{Code: CodeInvalidRequest, Message: "message is empty"}, http.StatusBadRequest
		}
		var apiErr *Error
		var code int
		if ag, apiErr, code = s.resolveAgent(req.Agent); apiErr != nil {
			return nil, apiErr, code
		}
	}
	select {
	case s.slots <- struct{}{}:
	default:
		return nil, &Error{Code: CodeOverCapacity, Message: fmt.Sprintf("%d runs are going on, the most this server takes; retry later", s.cfg.maxConcurrent), Retryable: true}, http.StatusServiceUnavailable
	}
	if conv == nil {
		s.convSeq++
		conv = &conversation{id: s.cfg.newID("conv_"), agent: ag, tenant: tenant, seq: s.convSeq, title: title(req.Message), created: now}
		s.convs[conv.id] = conv
	}
	s.touches++
	conv.running, conv.updated, conv.touched = true, now, s.touches
	transcript := make(harness.Transcript, len(conv.messages))
	for i, m := range conv.messages {
		transcript[i] = m.Message
	}
	ru := &run{id: s.cfg.newID("run_"), convID: conv.id, agent: ag.Name, tenant: tenant, created: now, status: StatusRunning}
	s.runs[ru.id] = ru
	s.runOrder = append(s.runOrder, ru)
	s.evict(now)
	s.active.Add(1)
	msg := ""
	if hasMessage {
		msg = req.Message
	}
	return &started{agent: ag, run: ru, conv: conv, message: msg, transcript: transcript}, nil, 0
}

// evict drops what retention no longer allows: finished runs and idle
// conversations older than the retention, then the oldest past the counts.
func (s *Server) evict(now time.Time) {
	cutoff := now.Add(-s.cfg.retention)
	kept := s.runOrder[:0]
	finished := 0
	for _, r := range s.runOrder {
		if r.status != StatusRunning {
			finished++
		}
	}
	for _, r := range s.runOrder {
		if r.status != StatusRunning && (r.finished.Before(cutoff) || finished > s.cfg.maxRuns) {
			delete(s.runs, r.id)
			finished--
			continue
		}
		kept = append(kept, r)
	}
	clear(s.runOrder[len(kept):])
	s.runOrder = kept

	var idle []*conversation
	for _, c := range s.convs {
		if c.running {
			continue
		}
		if c.updated.Before(cutoff) {
			delete(s.convs, c.id)
			continue
		}
		idle = append(idle, c)
	}
	if extra := len(s.convs) - s.cfg.maxConvs; extra > 0 {
		slices.SortFunc(idle, func(a, b *conversation) int { return cmp.Compare(a.touched, b.touched) })
		for _, c := range idle[:min(extra, len(idle))] {
			delete(s.convs, c.id)
		}
	}
}

// execute runs the harness for st and writes the outcome: events as they come,
// or the final run as JSON.
func (s *Server) execute(w http.ResponseWriter, r *http.Request, st *started, asJSON bool) {
	ctx, cancel := context.WithCancelCause(r.Context())
	defer cancel(nil)
	stopAfter := context.AfterFunc(s.stopCtx, func() { cancel(context.Cause(s.stopCtx)) })
	defer stopAfter()
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
	s.log.Info("run started", "run_id", st.run.id, "conversation_id", st.conv.id, "agent", st.agent.Name, "resume", st.message == "")

	var out *sseWriter
	if !asJSON {
		out = newSSEWriter(w, func() { cancel(errClientGone) })
		h := w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-store")
		h.Set("X-Accel-Buffering", "no")
		h.Set("Agentkit-Run-Id", st.run.id)
		h.Set("Agentkit-Conversation-Id", st.conv.id)
		out.open()
		defer out.close()
		if s.cfg.keepAlive > 0 {
			stopKeepAlive := out.keepAlive(s.cfg.clock, s.cfg.keepAlive)
			defer stopKeepAlive()
		}
	}

	in := harness.Input{Transcript: st.transcript}
	if st.message != "" {
		in.Message = harness.UserText(st.message)
	}
	seq := 0
	emit := func(e Event) {
		if out != nil {
			out.event(e)
		}
		seq++
	}
	var (
		result *harness.Result
		runErr error
	)
	for ev, err := range st.agent.Harness.Run(ctx, in) {
		if err != nil {
			runErr = err
			break
		}
		if ev.Kind == harness.EventRunDone {
			result = ev.Result
			continue
		}
		e, ok := wireEvent(st.run.id, st.conv.id, seq, ev)
		if !ok {
			continue
		}
		s.apply(st, ev)
		emit(e)
	}
	if seq == 0 { // the run failed before it started: announce it anyway
		emit(Event{V: WireVersion, Type: TypeRunStart, Seq: 0, RunID: st.run.id, ConversationID: st.conv.id, At: s.cfg.clock.Now()})
	}

	end := Event{V: WireVersion, Seq: seq, RunID: st.run.id}
	var rec *Record
	switch {
	case result != nil:
		final := result.Final
		usage := result.Usage
		end.Type, end.At = TypeRunDone, result.Record.FinishedAt
		end.Answer, end.Final, end.Usage, end.Turns = final.Text(), &final, &usage, result.Turns
		rec = record(result.Record)
	default:
		if runErr == nil { // cannot happen: a run ends with run_done or an error
			runErr = errors.New("the run ended without an outcome")
		}
		end.Type, end.At = TypeRunError, s.cfg.clock.Now()
		end.Error = s.classify(st.run.id, runErr, context.Cause(ctx))
		var re *harness.RunError
		if errors.As(runErr, &re) {
			usage := re.Usage()
			end.Usage, end.Turns = &usage, re.Turns()
			rec = record(re.Record())
			end.At = rec.FinishedAt
		} else {
			end.Usage = &llm.Usage{}
		}
	}
	s.finish(st, end, rec)
	emit(end)
	if asJSON {
		writeJSON(w, http.StatusOK, s.view(st.run))
	}
}

// apply records a harness event in the run and its conversation: the
// conversation grows by the prompt and by each completed turn, so it always
// holds what the model has seen, even if the run fails later.
func (s *Server) apply(st *started, ev harness.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch ev.Kind {
	case harness.EventRunStart:
		if ev.Prompt != nil {
			st.conv.messages = append(st.conv.messages, *ev.Prompt)
		}
	case harness.EventTurnDone:
		st.conv.messages = append(st.conv.messages, ev.TurnRecord.Messages...)
		st.run.turns = ev.TurnRecord.Turn
		st.run.usage = addUsage(st.run.usage, ev.TurnRecord.Usage)
	default:
		return
	}
	st.conv.updated = ev.At
}

// finish records the run's outcome and frees its slot.
func (s *Server) finish(st *started, end Event, rec *Record) {
	s.mu.Lock()
	ru := st.run
	ru.finished, ru.record, ru.turns = end.At, rec, end.Turns
	if end.Usage != nil {
		ru.usage = *end.Usage
	}
	if end.Type == TypeRunDone {
		ru.status, ru.answer = StatusDone, end.Answer
	} else {
		ru.status, ru.err = StatusFailed, end.Error
		if end.Error.Code == CodeShutdown {
			s.cut = append(s.cut, ru.id)
		}
	}
	s.touches++
	st.conv.running, st.conv.updated, st.conv.touched = false, s.cfg.clock.Now(), s.touches
	<-s.slots
	s.mu.Unlock()

	attrs := []any{
		"run_id", ru.id, "conversation_id", ru.convID, "status", ru.status, "turns", ru.turns,
		"input_tokens", ru.usage.InputTokens, "output_tokens", ru.usage.OutputTokens,
		"cache_read_tokens", ru.usage.CacheReadTokens, "cache_write_tokens", ru.usage.CacheWriteTokens,
	}
	switch {
	case ru.err == nil:
		s.log.Info("run done", attrs...)
	case ru.err.Code == CodeShutdown:
		s.log.Warn("run cut off by the shutdown", append(attrs, "error", ru.err.Message)...)
	default:
		s.log.Info("run failed", append(attrs, "code", ru.err.Code, "error", ru.err.Message)...)
	}
}

// classify turns a run's error into its wire form. cause is the run
// context's cause, which says why a cancelled run was cancelled.
func (s *Server) classify(runID string, err, cause error) *Error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		s.mu.Lock()
		draining := s.draining
		s.mu.Unlock()
		switch {
		case errors.Is(cause, ErrShutdown), draining:
			return &Error{Code: CodeShutdown, Message: "the server shut down before the run finished; the completed turns are kept", Retryable: true}
		case errors.Is(cause, ErrRunTimeout):
			return &Error{Code: CodeTimeout, Message: fmt.Sprintf("the run took longer than %s; the completed turns are kept", s.cfg.runTimeout)}
		default:
			return &Error{Code: CodeCancelled, Message: "the client went away; the completed turns are kept"}
		}
	}
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
	default:
		s.log.Error("run failed with an internal error", "run_id", runID, "err", err)
		return &Error{Code: CodeInternal, Message: "internal error; see the server log for run " + runID}
	}
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	ru := s.runs[id]
	if ru != nil && ru.tenant != s.tenantOf(r) {
		ru = nil
	}
	var v Run
	if ru != nil {
		v = s.viewLocked(ru)
	}
	s.mu.Unlock()
	if ru == nil {
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "no run " + quote(id) + " (runs are kept in memory, for a while, and lost on restart)"})
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) view(ru *run) Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.viewLocked(ru)
}

func (s *Server) viewLocked(ru *run) Run {
	v := Run{
		V: WireVersion, ID: ru.id, ConversationID: ru.convID, Agent: ru.agent, Status: ru.status, CreatedAt: ru.created,
		Answer: ru.answer, Usage: ru.usage, Turns: ru.turns, Error: ru.err, Record: ru.record,
	}
	if ru.status != StatusRunning {
		at := ru.finished
		v.FinishedAt = &at
	}
	return v
}

func (s *Server) getConversation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	c := s.convs[id]
	if c != nil && c.tenant != s.tenantOf(r) {
		c = nil
	}
	var body []byte
	var err error
	if c != nil {
		body, err = json.Marshal(Conversation{
			V: WireVersion, ID: c.id, Agent: c.agent.Name, Status: c.status(), CreatedAt: c.created, UpdatedAt: c.updated,
			Messages: timedMessages(c.messages),
		})
	}
	s.mu.Unlock()
	switch {
	case c == nil:
		writeError(w, http.StatusNotFound, notFoundConversation(id))
	case err != nil:
		s.log.Error("encoding a conversation", "conversation_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, &Error{Code: CodeInternal, Message: "the conversation could not be encoded"})
	default:
		writeRaw(w, http.StatusOK, append(body, '\n'))
	}
}

func addUsage(a, b llm.Usage) llm.Usage {
	a.InputTokens += b.InputTokens
	a.OutputTokens += b.OutputTokens
	a.CacheReadTokens += b.CacheReadTokens
	a.CacheWriteTokens += b.CacheWriteTokens
	return a
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
