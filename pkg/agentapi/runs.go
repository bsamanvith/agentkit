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
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/stream"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
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
	evs, h, err := s.eventsOf(ctx, run)
	if err != nil {
		return Run{}, err
	}
	if h != nil && run.Status != store.StatusRunning {
		// The run's end is stored a moment before its last event reaches
		// its log: end its events as its status says, as for a run no hub
		// follows, so the view has its answer (or error) and end.
		evs = withEnding(run, summarize(evs), s.cfg.clock.Now())
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
// reading its events (a suspended run's interrupts are its log's last entry).
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
	case StatusSuspended:
		ins, err := s.interruptsOf(ctx, run)
		if err != nil {
			s.log.Error("reading a suspended run's interrupts", "run_id", run.ID, "err", err)
		}
		v.Interrupts = ins
		v.Error = suspendedError(run, len(ins) > 0)
	}
	return v
}

// interruptsOf returns what a suspended chat run waits for: the interrupts
// of the pending turn its last entry holds, with the answers so far. A flow
// run has none. A suspended run is its conversation's one unfinished run, so
// its pending turn is the conversation's last entry: only that is read.
func (s *Server) interruptsOf(ctx context.Context, run store.Run) ([]Interrupt, error) {
	page, err := s.store.History(ctx, run.ConversationID, store.Page{Order: store.Descending, Limit: 1})
	if err != nil {
		return nil, fmt.Errorf("reading the log of conversation %q: %w", run.ConversationID, err)
	}
	if len(page.Entries) == 0 || page.Entries[0].RunID != run.ID || page.Entries[0].Kind != store.KindInterrupt {
		return nil, nil
	}
	p, answers, err := runner.PendingTurn(page.Entries[0])
	if err != nil {
		return nil, err
	}
	out := make([]Interrupt, len(p.Interrupts))
	for i, in := range p.Interrupts {
		var a *harness.Answer
		if j := slices.IndexFunc(answers, func(a harness.Answer) bool { return a.ID == in.ID }); j >= 0 {
			a = &answers[j]
		}
		out[i] = wireInterrupt(in, a)
	}
	return out, nil
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
	if !held(run, s.cfg.clock.Now()) || s.ownBus {
		// Nobody runs it, or no bus joins this server to the one that
		// does: what the store has is all there is.
		out, stop := s.openStream(w, r, run, nil)
		defer stop()
		for _, e := range withText(evs, after) {
			if eventID(e).Compare(after) > 0 {
				out.event(e)
			}
		}
		return
	}
	// Joined before the headers go: a client that has them gets the live
	// events from then on as they come.
	jv := s.joinView(ctx, run, after)
	out, stop := s.openStream(w, r, run, nil)
	defer stop()
	s.readView(ctx, jv, out.event)
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

// watchRun sends emit the events of a run another process runs, after the
// ID after, from the log and the bus, until its last event. When the process
// lets the run go, the watch first reads what the bus has of it on the
// subscription it has ([tail]), or on another if that one lags: the holder
// lets a run go before it publishes its last event, and a stopped run's is
// streamed, not stored. Without that event (the process crashed, or was
// slow to publish it), it ends with the run's stored events and the last
// event its status says; when another process takes it over, it follows
// that one. Readers share one per run through [Server.joinView] (T103).
func (s *Server) watchRun(ctx context.Context, run store.Run, after bus.ID, emit func(Event)) {
	poll := s.poll()
	for {
		wctx, cancel := context.WithCancelCause(ctx)
		tl := &tail{after: after, cancel: cancel}
		stopCheck := s.checkHeld(wctx, tl, run.ID, poll)
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
			tl.emitted(after)
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
		if tl.short() && s.pending(ctx, run.ID, &after, emit) {
			return
		}
		rp, err := s.storedEnding(ctx, cur, poll)
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

// pending sends emit what the bus has of a run let go after *after, which
// the watch, stopped at once, may not have had yet, and moves *after past
// it; it reports whether that ended the run. Without it the watch would take
// the turns those events belong to from the log, and their text_delta
// events would have the IDs of replayed text, not the ones the holder gave
// them. It waits for the bus up to endingWait.
func (s *Server) pending(ctx context.Context, runID string, after *bus.ID, emit func(Event)) bool {
	ctx, cancel := context.WithTimeout(ctx, endingWait) // real time, as checkHeld's
	defer cancel()
	for se, err := range stream.Pending(ctx, s.bus, runID, *after, isTerminal) {
		if err != nil {
			if ctx.Err() == nil {
				s.log.Error("reading what the bus has of a run let go", "run_id", runID, "err", err)
			}
			return false
		}
		e, err := decodeEvent(se.ID, se.Data)
		if err != nil {
			s.log.Error("decoding a run's event", "run_id", runID, "err", err)
			continue
		}
		emit(e)
		*after = se.ID
		if isTerminal(e.Type) {
			return true
		}
	}
	return false
}

// endingWait is how long a watch waits for the stored last event of a run
// let go as done, canceled or rejected, reading the log every endingStep (or
// poll, if shorter). The holder stores it right after it lets the run go.
const (
	endingWait = 2 * time.Second
	endingStep = 50 * time.Millisecond
)

// storedEnding is the history of a run let go. Its holder lets a run go
// before it stores the run's last event (drive, after the runner's end): for
// a run whose status says that event is stored, it waits for it, up to
// endingWait, so the watch ends with the event the log has, not one made up
// from the status with another ID.
func (s *Server) storedEnding(ctx context.Context, run store.Run, poll time.Duration) (replayed, error) {
	step := min(poll, endingStep)
	deadline := time.Now().Add(endingWait) // real time, as checkHeld's
	for {
		rp, err := s.history(ctx, run, false)
		if err != nil {
			return rp, err
		}
		switch runStatus(run, s.cfg.clock.Now()) {
		case StatusDone, StatusCanceled, StatusRejected:
		default:
			return rp, nil // its last event is streamed, never stored
		}
		if n := len(rp.events); n > 0 && isTerminal(rp.events[n-1].Type) || !time.Now().Before(deadline) {
			return rp, nil
		}
		t := time.NewTimer(step)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return rp, nil
		}
	}
}

// checkHeld checks every d that the run is held. Once nobody holds it, it
// has tl stop the watch at the topic's newest message, or after endingWait
// if the watch's subscription does not get there; the returned function
// stops it and waits for it.
func (s *Server) checkHeld(ctx context.Context, tl *tail, runID string, d time.Duration) func() {
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
			if err != nil || held(run, s.cfg.clock.Now()) {
				continue
			}
			last, ok := s.lastOnBus(ctx, runID)
			if !ok {
				tl.giveUp()
				return
			}
			tl.letGo(last)
			wait := time.NewTimer(endingWait) // real time, as the poll
			defer wait.Stop()
			select {
			case <-wait.C:
				tl.giveUp()
			case <-stop:
			case <-ctx.Done():
			}
			return
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(stop) })
		<-done
	}
}

// lastOnBus is the ID of the newest message on the run's topic.
func (s *Server) lastOnBus(ctx context.Context, runID string) (bus.ID, bool) {
	t, err := tenancy.Require(ctx)
	if err != nil {
		return bus.ID{}, false
	}
	topic, err := bus.RunTopic(t, runID)
	if err != nil {
		return bus.ID{}, false
	}
	last, err := s.bus.Last(ctx, topic)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("reading what the bus has of a run let go", "run_id", runID, "err", err)
		}
		return bus.ID{}, false
	}
	return last, true
}

// tail stops a watch of a run let go once it has emitted what the bus had
// of the run then: the messages the holder published before and just after
// it let the run go, deltas among them, which the log does not have (T090:
// a follower numbers a turn's text as its holder did only from the bus).
// The watch reads them on the subscription it has: one that stopped at once
// would need another to read them, and a run's viewers share one (T103).
// Only when that subscription does not bring them in time does the watch
// read them with another ([Server.pending]).
type tail struct {
	cancel context.CancelCauseFunc

	mu       sync.Mutex
	after    bus.ID // the last event the watch emitted
	draining bool   // the run was let go: stop at last
	last     bus.ID // the topic's newest message when the run was let go
	behind   bool   // the watch stopped before it got to last
}

// giveUp stops the watch short of what the bus has of the run.
func (tl *tail) giveUp() {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	if tl.draining && tl.after.Compare(tl.last) >= 0 {
		return // it got there
	}
	tl.behind = true
	tl.cancel(errReleased)
}

// short reports whether the watch stopped short of what the bus had.
func (tl *tail) short() bool {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	return tl.behind
}

// emitted records that the watch emitted the event id, and stops it if the
// run was let go and that was the last the bus had.
func (tl *tail) emitted(id bus.ID) {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	tl.after = id
	if tl.draining && id.Compare(tl.last) >= 0 {
		tl.cancel(errReleased)
	}
}

// letGo records that the run was let go when the bus had up to last, and
// stops the watch if it has emitted that already.
func (tl *tail) letGo(last bus.ID) {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	tl.draining, tl.last = true, last
	if tl.after.Compare(last) >= 0 {
		tl.cancel(errReleased)
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
	if acc := accessOf(r); acc.ok {
		// A resume runs the agent's tools: only for a request that may use
		// them all.
		c, err := s.store.GetConversation(ctx, run.ConversationID)
		if err != nil {
			s.internal(w, "reading conversation "+quote(run.ConversationID), err)
			return
		}
		if a := s.agents[c.Agent]; a != nil && !acc.agent(a) {
			writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "no agent " + quote(c.Agent)})
			return
		}
	}
	switch {
	case s.isDraining():
		s.unavailable(w, shuttingDown())
		return
	case run.Status.Final():
		writeError(w, http.StatusConflict, &Error{Code: CodeRunFinal, RunID: run.ID, Message: "run " + quote(run.ID) + " is " + string(run.Status) + "; it never changes again"})
		return
	case run.Status == store.StatusSuspended:
		writeError(w, http.StatusConflict, &Error{Code: CodeAwaitingDecision, RunID: run.ID, Message: "run " + quote(run.ID) + " waits for a decision (POST /v1/runs/{id}/decisions) or for input (POST /v1/runs/{id}/input)"})
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
	x, err := s.runs.Resume(execCtx, run.ID)
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
	_, err := s.runs.Interrupt(ctx, id)
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
	if _, err := s.runs.Cancel(ctx, id); err != nil {
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
	x, err := s.runs.Decide(execCtx, id, req.Path, flow.Decision{Approved: req.Approved, Reason: req.Reason})
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

// inputRun serves POST /v1/runs/{id}/input: answers to interrupts of a
// suspended chat run. Once every interrupt of its pending turn has its
// answer, the run goes on in the background; the answer is the run as it is
// then (or its events, for Accept: text/event-stream). Until then it is the
// run, still suspended, with the answers so far.
func (s *Server) inputRun(w http.ResponseWriter, r *http.Request) {
	body, apiErr, code := s.readBody(w, r)
	if apiErr != nil {
		writeError(w, code, apiErr)
		return
	}
	var req InputRequest
	if err := decodeStrict(body, &req); err != nil || len(req.Answers) == 0 || slices.ContainsFunc(req.Answers, func(a Answer) bool { return a.ID == "" }) {
		msg := "the body must be a JSON object {\"answers\": [{\"id\": ..., \"text\": ...} or {\"id\": ..., \"approved\": ..., \"reason\": ...}]} with at least one answer, each with an id"
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
	run, ok := s.getStored(ctx, w, id)
	if !ok {
		return
	}
	acc := accessOf(r)
	if acc.ok {
		// An approval runs the agent's tools: only for a request that may
		// use them all, as for a resume.
		c, err := s.store.GetConversation(ctx, run.ConversationID)
		if err != nil {
			s.internal(w, "reading conversation "+quote(run.ConversationID), err)
			return
		}
		if a := s.agents[c.Agent]; a != nil && !acc.agent(a) {
			writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "no run " + quote(id)})
			return
		}
	}
	answers := make([]harness.Answer, len(req.Answers))
	for i, a := range req.Answers {
		// Who answered is who the server authenticated, never the body.
		answers[i] = harness.Answer{ID: a.ID, Text: a.Text, Approved: a.Approved, Reason: a.Reason}
		if acc.ok {
			answers[i].By = acc.p.User
		}
	}
	if apiErr := s.admit(); apiErr != nil {
		s.unavailable(w, apiErr)
		return
	}
	execCtx, cancel := execContext(ctx)
	x, err := s.runs.Answer(execCtx, id, answers...)
	if err != nil {
		cancel(nil)
		s.release()
		switch {
		case errors.Is(err, store.ErrFinal):
			writeError(w, http.StatusConflict, &Error{Code: CodeRunFinal, RunID: id, Message: "run " + quote(id) + " is final"})
		case errors.Is(err, runner.ErrNotPending):
			writeError(w, http.StatusConflict, &Error{Code: CodeNotPending, RunID: id, Message: err.Error()})
		default:
			s.startError(w, err, CodeRunLeased)
		}
		return
	}
	s.log.Info("run answered", "run_id", id, "answers", len(answers))
	if x.Run.Status == store.StatusSuspended {
		// Answers still missing: nothing runs.
		_, _ = x.Wait()
		cancel(nil)
		s.release()
		if v, ok := s.viewByID(ctx, w, id); ok {
			writeJSON(w, http.StatusOK, v)
		}
		return
	}
	seed, err := s.history(ctx, x.Run, true)
	if err != nil {
		s.log.Error("reading an answered run's events", "run_id", id, "err", err)
	}
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
