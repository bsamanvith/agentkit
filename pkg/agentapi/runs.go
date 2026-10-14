package agentapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/stream"
)

// runStatus is the run's status on the wire, at now.
func runStatus(run store.Run, now time.Time) string {
	switch run.Status {
	case store.StatusCompleted:
		return StatusDone
	case store.StatusFailed:
		return StatusFailed
	case store.StatusCanceled:
		return StatusCanceled
	case store.StatusRejected:
		return StatusRejected
	case store.StatusSuspended:
		return StatusSuspended
	}
	if run.Interrupted(now) {
		return StatusInterrupted
	}
	return StatusRunning
}

// endStatus is the status a run has after an execution ended with e.
func endStatus(e Event, interactive bool) string {
	if e.Type == TypeRunDone {
		return StatusDone
	}
	switch e.Error.Code {
	case CodeRunInterrupted, CodeTimeout, CodeShutdown:
		if interactive {
			return StatusInterrupted
		}
		return StatusRunning
	case CodeCancelled:
		return StatusCanceled
	case CodeRejected:
		return StatusRejected
	case CodeSuspended:
		return StatusSuspended
	}
	return StatusFailed
}

// eventsOf returns the run's events: its hub's, while they say what the
// store says, else the stored ones, with a last event from its status unless
// it is going on.
func (s *Server) eventsOf(ctx context.Context, run store.Run) ([]Event, *hub, error) {
	now := s.cfg.clock.Now()
	if h := s.hubOf(ctx, run.ID); h != nil {
		evs, done := h.snapshot()
		if !done || endStatus(evs[len(evs)-1], h.interactive) == runStatus(run, now) {
			return evs, h, nil
		}
	}
	rp, err := s.history(ctx, run, false)
	if err != nil {
		return nil, nil, err
	}
	return withEnding(run, rp, now), nil, nil
}

// withEnding returns the run's events, and after them, unless the last one
// ends the run or the run is going on, the last event its status says.
func withEnding(run store.Run, rp replayed, now time.Time) []Event {
	evs := rp.events
	if n := len(evs); n > 0 && isTerminal(evs[n-1].Type) {
		return evs
	}
	end := ending(run, rp, now)
	if end == nil {
		return evs
	}
	*end = withID(*end, endingID(lastSeq(evs)))
	if n := len(evs); n > 0 && end.At.Before(evs[n-1].At) {
		end.At = evs[n-1].At
	}
	return append(evs[:len(evs):len(evs)], *end)
}

// runView is the run as GET /v1/runs/{id} returns it, from its events.
func (s *Server) runView(ctx context.Context, run store.Run) (Run, error) {
	evs, _, err := s.eventsOf(ctx, run)
	if err != nil {
		return Run{}, err
	}
	v := s.runSummary(ctx, run)
	if len(evs) == 0 { // a run whose execution has not announced it yet
		evs = []Event{{Type: TypeRunStart, At: run.CreatedAt}}
	}
	rec := &Record{StartedAt: evs[0].At, Prompt: evs[0].Prompt, Turns: []TurnRecord{}}
	var usage llm.Usage
	for _, e := range evs {
		switch e.Type {
		case TypeTurnDone:
			rec.Turns = append(rec.Turns, *e.Record)
			usage = usage.Add(e.Record.Usage)
		case TypeRewind:
			if e.Turns < len(rec.Turns) {
				rec.Turns = rec.Turns[:max(e.Turns, 0)]
				usage = llm.Usage{}
				for _, t := range rec.Turns {
					usage = usage.Add(t.Usage)
				}
			}
		case TypeRunDone, TypeRunError:
			at := e.At
			v.FinishedAt, rec.FinishedAt = &at, at
			v.Answer, v.Error, v.Turns = e.Answer, e.Error, e.Turns
			if e.Usage != nil {
				usage = *e.Usage
			}
			v.Usage, v.Record = usage, rec
			return v, nil
		}
	}
	// No ending yet: the run goes on, or has just ended and its execution
	// has not said so; then its error and end are as stored.
	v.Usage, v.Turns, v.Record = usage, len(rec.Turns), rec
	return v, nil
}

// runSummary is what a list shows of a run: its state as stored, without
// reading its events.
func (s *Server) runSummary(ctx context.Context, run store.Run) Run {
	now := s.cfg.clock.Now()
	v := Run{V: WireVersion, ID: run.ID, ConversationID: run.ConversationID, Status: runStatus(run, now), CreatedAt: run.CreatedAt, Usage: run.Usage}
	if c, err := s.store.GetConversation(ctx, run.ConversationID); err == nil {
		v.Agent = c.Agent
	}
	if !run.FinishedAt.IsZero() {
		at := run.FinishedAt
		v.FinishedAt = &at
	}
	switch v.Status {
	case StatusFailed:
		v.Error = &Error{Code: CodeRunFailed, Message: run.Error, Retryable: run.Retryable}
	case StatusRejected:
		v.Error = &Error{Code: CodeRejected, Message: run.Error}
	case StatusInterrupted:
		v.Error = interruptedError(run.ID)
	}
	return v
}

// getStored reads a run of the request's tenant; it answers 404 or 500 and
// returns false if it cannot.
func (s *Server) getStored(ctx context.Context, w http.ResponseWriter, id string) (store.Run, bool) {
	run, err := s.store.Get(ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "no run " + quote(id)})
		return run, false
	case err != nil:
		s.internal(w, "reading run "+quote(id), err)
		return run, false
	}
	return run, true
}

// viewByID reads the run and its view; it answers 404 or 500 and returns
// false if it cannot.
func (s *Server) viewByID(ctx context.Context, w http.ResponseWriter, id string) (Run, bool) {
	run, ok := s.getStored(ctx, w, id)
	if !ok {
		return Run{}, false
	}
	v, err := s.runView(ctx, run)
	if err != nil {
		s.internal(w, "reading the events of run "+quote(id), err)
		return Run{}, false
	}
	return v, true
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	if v, ok := s.viewByID(ctx, w, r.PathValue("id")); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// runEvents serves GET /v1/runs/{id}/events: the run's events after
// Last-Event-ID (all of them without it), then the new ones as they come
// while the run goes on, here or in another process (E18).
func (s *Server) runEvents(w http.ResponseWriter, r *http.Request) {
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	var after bus.ID
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		id, err := bus.ParseID(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "Last-Event-ID must be the id of an event"})
			return
		}
		after = id
	}
	run, ok := s.getStored(ctx, w, r.PathValue("id"))
	if !ok {
		return
	}
	evs, h, err := s.eventsOf(ctx, run)
	if err != nil {
		s.internal(w, "reading the events of run "+quote(run.ID), err)
		return
	}
	if h != nil {
		s.follow(w, r, h, after)
		return
	}
	out, stop := s.openStream(w, r, run, nil)
	defer stop()
	if !held(run, s.cfg.clock.Now()) || s.ownBus {
		// Nobody runs it, or no bus joins this server to the one that
		// does: what the store has is all there is.
		for _, e := range withText(evs, after) {
			if eventID(e).Compare(after) > 0 {
				out.event(e)
			}
		}
		return
	}
	s.watch(ctx, out, run, after)
}

// openStream sends an event stream's headers; stop ends it.
func (s *Server) openStream(w http.ResponseWriter, r *http.Request, run store.Run, gone func()) (*sseWriter, func()) {
	if gone == nil {
		gone = func() {}
	}
	out := newSSEWriter(w, gone)
	hd := w.Header()
	hd.Set("Content-Type", "text/event-stream")
	hd.Set("Cache-Control", "no-store")
	hd.Set("X-Accel-Buffering", "no")
	hd.Set("Agentkit-Run-Id", run.ID)
	hd.Set("Agentkit-Conversation-Id", run.ConversationID)
	out.open()
	stopKeepAlive := func() {}
	if s.cfg.keepAlive > 0 {
		stopKeepAlive = out.keepAlive(s.cfg.clock, s.cfg.keepAlive)
	}
	return out, func() {
		stopKeepAlive()
		out.close()
	}
}

// errReleased means the process running a run let it go.
var errReleased = errors.New("the run is no longer held")

// watch streams the events of a run another process runs, after the ID
// after, from the log and the bus, until its last event. When the process
// lets the run go without that event reaching the bus (it crashed, or was
// stopped), the stream ends with the run's stored events and the last event
// its status says; when another process takes it over, the stream follows
// that one.
func (s *Server) watch(ctx context.Context, out *sseWriter, run store.Run, after bus.ID) {
	s.watchRun(ctx, run, after, out.event)
}

// watchRun is watch, for any reader of the events: emit gets each one.
func (s *Server) watchRun(ctx context.Context, run store.Run, after bus.ID, emit func(Event)) {
	poll := s.poll()
	for {
		wctx, cancel := context.WithCancelCause(ctx)
		stopCheck := s.checkHeld(wctx, cancel, run.ID, poll)
		for se, err := range stream.Watch(wctx, s.store, s.bus, run.ID, after, isTerminal, stream.WithClock(s.cfg.clock), stream.WithPoll(poll)) {
			if err != nil {
				if !errors.Is(context.Cause(wctx), errReleased) && ctx.Err() == nil {
					s.log.Error("following a run's events", "run_id", run.ID, "err", err)
				}
				break
			}
			e, err := decodeEvent(se.ID, se.Data)
			if err != nil {
				s.log.Error("decoding a run's event", "run_id", run.ID, "err", err)
				continue
			}
			emit(e)
			after = se.ID
			if isTerminal(e.Type) {
				stopCheck()
				cancel(nil)
				return
			}
		}
		stopCheck()
		released := errors.Is(context.Cause(wctx), errReleased)
		cancel(nil)
		if !released || ctx.Err() != nil {
			return
		}
		cur, err := s.store.Get(ctx, run.ID)
		if err != nil {
			s.log.Error("reading a run let go", "run_id", run.ID, "err", err)
			return
		}
		if held(cur, s.cfg.clock.Now()) {
			continue // taken over: follow its new process
		}
		rp, err := s.history(ctx, cur, false)
		if err != nil {
			s.log.Error("reading the events of a run let go", "run_id", run.ID, "err", err)
			return
		}
		for _, e := range withText(withEnding(cur, rp, s.cfg.clock.Now()), after) {
			if eventID(e).Compare(after) > 0 {
				emit(e)
			}
		}
		return
	}
}

// checkHeld cancels ctx with errReleased once nobody holds the run, checking
// every d; the returned function stops it and waits for it.
func (s *Server) checkHeld(ctx context.Context, cancel context.CancelCauseFunc, runID string, d time.Duration) func() {
	t := time.NewTicker(d) // real time, as obey's poll
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		defer t.Stop()
		for {
			select {
			case <-t.C:
			case <-stop:
				return
			case <-ctx.Done():
				return
			}
			run, err := s.store.Get(ctx, runID)
			if err == nil && !held(run, s.cfg.clock.Now()) {
				cancel(errReleased)
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(stop) })
		<-done
	}
}

// resumeRun serves POST /v1/runs/{id}/resume.
func (s *Server) resumeRun(w http.ResponseWriter, r *http.Request) {
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	run, ok := s.getStored(ctx, w, r.PathValue("id"))
	if !ok {
		return
	}
	s.resume(ctx, w, r, run, CodeRunLeased)
}

// resumeConversation resumes the conversation's unfinished run: a run
// request with an empty message.
func (s *Server) resumeConversation(ctx context.Context, w http.ResponseWriter, r *http.Request, convID string) {
	run, err := s.store.Unfinished(ctx, convID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "message is empty, and the conversation has no run to resume"})
		return
	case err != nil:
		s.internal(w, "reading the conversation's runs", err)
		return
	}
	s.resume(ctx, w, r, run, CodeBusy)
}

// resume continues a run that is not final and not going on. busy is the
// code of a run that is going on.
func (s *Server) resume(ctx context.Context, w http.ResponseWriter, r *http.Request, run store.Run, busy string) {
	switch {
	case s.isDraining():
		s.unavailable(w, shuttingDown())
		return
	case run.Status.Final():
		writeError(w, http.StatusConflict, &Error{Code: CodeRunFinal, RunID: run.ID, Message: "run " + quote(run.ID) + " is " + string(run.Status) + "; it never changes again"})
		return
	case run.Status == store.StatusSuspended:
		writeError(w, http.StatusConflict, &Error{Code: CodeAwaitingDecision, RunID: run.ID, Message: "run " + quote(run.ID) + " waits for a decision (POST /v1/runs/{id}/decisions)"})
		return
	case held(run, s.cfg.clock.Now()):
		writeError(w, http.StatusConflict, &Error{Code: busy, RunID: run.ID, Retryable: true, Message: "run " + quote(run.ID) + " is going on; wait for it to end, or stop it"})
		return
	}
	if apiErr := s.admit(); apiErr != nil {
		s.unavailable(w, apiErr)
		return
	}
	execCtx, cancel := execContext(ctx)
	x, err := s.runner.Resume(execCtx, run.ID)
	if err != nil {
		cancel(nil)
		s.release()
		s.startError(w, err, busy)
		return
	}
	seed, err := s.history(ctx, x.Run, true)
	if err != nil {
		// The execution holds the lease: run it anyway, from a log of its
		// own, rather than leave the run claimed.
		s.log.Error("reading a resumed run's events", "run_id", run.ID, "err", err)
	}
	s.log.Info("run resumed", "run_id", run.ID, "conversation_id", run.ConversationID, "turns", seed.turns)
	h := s.launch(execCtx, cancel, x, seed)
	s.respond(w, r, h, lastID(seed.events))
}

// lastID is the ID of the last of evs, or the zero ID.
func lastID(evs []Event) bus.ID {
	if len(evs) == 0 {
		return bus.ID{}
	}
	return eventID(evs[len(evs)-1])
}

// stopRun serves POST /v1/runs/{id}/stop: the run's execution stops at once,
// its turns so far stored, and a chat run is interrupted until it is resumed.
func (s *Server) stopRun(w http.ResponseWriter, r *http.Request) {
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	_, err := s.runner.Interrupt(ctx, id)
	switch {
	case errors.Is(err, store.ErrLeased) && !s.ownBus:
		s.command(ctx, w, r, id, commandStop)
	case err != nil:
		s.commandError(w, id, err)
	default:
		s.afterCommand(ctx, w, r, id)
	}
}

// cancelRun serves POST /v1/runs/{id}/cancel: the run is canceled, which is
// final. Canceling a canceled run is a no-op.
func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if _, err := s.runner.Cancel(ctx, id); err != nil {
		if errors.Is(err, store.ErrLeased) && !s.ownBus {
			s.command(ctx, w, r, id, commandCancel)
			return
		}
		if errors.Is(err, store.ErrFinal) {
			if run, gerr := s.store.Get(ctx, id); gerr == nil && run.Status == store.StatusCanceled {
				s.afterCommand(ctx, w, r, id)
				return
			}
		}
		s.commandError(w, id, err)
		return
	}
	s.afterCommand(ctx, w, r, id)
}

func (s *Server) commandError(w http.ResponseWriter, id string, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "no run " + quote(id)})
	case errors.Is(err, store.ErrLeased):
		writeError(w, http.StatusConflict, &Error{Code: CodeRunLeased, RunID: id, Retryable: true, Message: "run " + quote(id) + " is run by another process, or is about to start; retry"})
	case errors.Is(err, store.ErrFinal):
		writeError(w, http.StatusConflict, &Error{Code: CodeRunFinal, RunID: id, Message: "run " + quote(id) + " is final"})
	default:
		s.internal(w, "stopping run "+quote(id), err)
	}
}

// afterCommand answers a stop or cancel with the run, once this server's
// execution of it has ended.
func (s *Server) afterCommand(ctx context.Context, w http.ResponseWriter, r *http.Request, id string) {
	if h := s.hubOf(ctx, id); h != nil {
		select {
		case <-h.ended:
		case <-r.Context().Done():
			return
		}
	}
	if v, ok := s.viewByID(ctx, w, id); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// decideRun serves POST /v1/runs/{id}/decisions: an approval or rejection of
// a step a flow run waits for. The run goes on in the background; the answer
// is the run as it is then (or its events, for Accept: text/event-stream).
func (s *Server) decideRun(w http.ResponseWriter, r *http.Request) {
	body, apiErr, code := s.readBody(w, r)
	if apiErr != nil {
		writeError(w, code, apiErr)
		return
	}
	var req DecisionRequest
	if err := decodeStrict(body, &req); err != nil || req.Path == "" {
		msg := "the body must be a JSON object {\"path\": ..., \"approved\": ..., \"reason\": ...} with a path"
		if err != nil {
			msg += ": " + err.Error()
		}
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: msg})
		return
	}
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if apiErr := s.admit(); apiErr != nil {
		s.unavailable(w, apiErr)
		return
	}
	execCtx, cancel := execContext(ctx)
	x, err := s.runner.Decide(execCtx, id, req.Path, flow.Decision{Approved: req.Approved, Reason: req.Reason})
	if err != nil {
		cancel(nil)
		s.release()
		if errors.Is(err, store.ErrFinal) {
			writeError(w, http.StatusConflict, &Error{Code: CodeRunFinal, RunID: id, Message: "run " + quote(id) + " is final"})
			return
		}
		s.startError(w, err, CodeRunLeased)
		return
	}
	seed, err := s.history(ctx, x.Run, true)
	if err != nil {
		s.log.Error("reading a decided run's events", "run_id", id, "err", err)
	}
	s.log.Info("run decided", "run_id", id, "path", req.Path, "approved", req.Approved)
	h := s.launch(execCtx, cancel, x, seed)
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		s.follow(w, r, h, lastID(seed.events))
		return
	}
	if v, ok := s.viewByID(ctx, w, id); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// listRuns serves GET /v1/runs: the runs of a conversation
// (conversation_id), newest first, or with status=suspended those of every
// conversation waiting for a decision. status filters the runs.
func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, ok := listLimit(w, q.Get("limit"))
	if !ok {
		return
	}
	status := q.Get("status")
	if status != "" && !slices.Contains([]string{StatusRunning, StatusInterrupted, StatusDone, StatusFailed, StatusCanceled, StatusRejected, StatusSuspended}, status) {
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "no run status " + quote(status)})
		return
	}
	convID := q.Get("conversation_id")
	if convID == "" && status != StatusSuspended {
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "name a conversation (conversation_id), or list status=suspended"})
		return
	}
	var after *runKey
	if v := q.Get("cursor"); v != "" {
		k, ok := decodeRunCursor(v)
		if !ok {
			writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "cursor is not one this server gave"})
			return
		}
		after = &k
	}
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	if convID != "" {
		if _, err := s.store.GetConversation(ctx, convID); err != nil {
			if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrInvalid) {
				writeError(w, http.StatusNotFound, notFoundConversation(convID))
			} else {
				s.internal(w, "reading conversation "+quote(convID), err)
			}
			return
		}
	}
	p := store.Page{Order: store.Descending, Limit: store.MaxPageLimit}
	if after != nil {
		p.FromTime = after.created
	}
	now := s.cfg.clock.Now()
	out := RunList{V: WireVersion, Runs: []Run{}}
	for {
		var page store.RunPage
		var err error
		if convID != "" {
			page, err = s.store.List(ctx, convID, p)
		} else {
			page, err = s.store.ListSuspended(ctx, p)
		}
		if err != nil {
			s.internal(w, "listing runs", err)
			return
		}
		for _, run := range page.Runs {
			k := runKey{run.CreatedAt, run.ID}
			if after != nil && !k.before(*after) {
				continue
			}
			if status != "" && runStatus(run, now) != status {
				continue
			}
			if len(out.Runs) == limit {
				out.NextCursor = encodeRunCursor(runKey{out.Runs[limit-1].CreatedAt, out.Runs[limit-1].ID})
				writeJSON(w, http.StatusOK, out)
				return
			}
			out.Runs = append(out.Runs, s.runSummary(ctx, run))
		}
		if page.Next == "" {
			break
		}
		p = store.Page{Order: store.Descending, Limit: store.MaxPageLimit, Cursor: page.Next}
	}
	writeJSON(w, http.StatusOK, out)
}

// listLimit reads ?limit=; it answers 400 and returns false for a bad one.
func listLimit(w http.ResponseWriter, v string) (int, bool) {
	if v == "" {
		return DefaultListLimit, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > MaxListLimit {
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: fmt.Sprintf("limit must be a number from 1 to %d", MaxListLimit)})
		return 0, false
	}
	return n, true
}

// runKey is a run's place in a list: by creation, then ID.
type runKey struct {
	created time.Time
	id      string
}

// before reports whether k comes after o in a newest-first list.
func (k runKey) before(o runKey) bool {
	return k.created.Before(o.created) || k.created.Equal(o.created) && k.id < o.id
}

// runCursorPrefix makes cursors this server's: an opaque token is base64url
// of it and the JSON of the last run of the page.
const runCursorPrefix = "runs-before:"

type runCursor struct {
	Created time.Time `json:"t"`
	ID      string    `json:"id"`
}

func encodeRunCursor(k runKey) string {
	b, _ := json.Marshal(runCursor{k.created, k.id})
	return base64.RawURLEncoding.EncodeToString(append([]byte(runCursorPrefix), b...))
}

func decodeRunCursor(s string) (runKey, bool) {
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil {
		return runKey{}, false
	}
	body, ok := strings.CutPrefix(string(b), runCursorPrefix)
	if !ok {
		return runKey{}, false
	}
	var c runCursor
	if err := json.Unmarshal([]byte(body), &c); err != nil || c.ID == "" {
		return runKey{}, false
	}
	return runKey{c.Created, c.ID}, true
}
