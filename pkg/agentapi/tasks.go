package agentapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// postTask serves POST /v1/tasks: a background run of an agent on an input,
// in a conversation of its own. It answers 202 with the run at once; the run
// goes on until it ends or is canceled, whoever watches it, and the recovery
// loop resumes it after a crash.
func (s *Server) postTask(w http.ResponseWriter, r *http.Request) {
	body, apiErr, code := s.readBody(w, r)
	if apiErr != nil {
		writeError(w, code, apiErr)
		return
	}
	var req TaskRequest
	if err := decodeStrict(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "the body must be a JSON object {\"input\": ..., \"agent\": ...}: " + err.Error()})
		return
	}
	// A task never waits for a person unless it is supervised (T084),
	// whatever the agent's interaction: nobody is watching it.
	start := StartOptions{Background: true, Interaction: harness.Autonomous}
	switch {
	case strings.TrimSpace(req.Input) == "":
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "input is empty"})
		return
	case req.Interaction == wire.InteractionSupervised:
		start.Interaction = harness.Supervised
	case req.Interaction != "" && req.Interaction != wire.InteractionAutonomous:
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "interaction " + quote(req.Interaction) + " is not served: a task is \"autonomous\" or \"supervised\""})
		return
	case req.Notify != nil && req.Notify.URL == "":
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "notify needs a url"})
		return
	}
	if len(req.Policy) > 0 {
		p, err := runPolicy(req.Policy)
		if err != nil {
			writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "policy: " + err.Error()})
			return
		}
		start.ToolPolicy = &p
	}
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	if s.isDraining() {
		s.unavailable(w, shuttingDown())
		return
	}
	ag, apiErr, code := s.resolveAgent(ctx, r, req.Agent)
	if apiErr != nil {
		writeError(w, code, apiErr)
		return
	}
	notify := ag.Notify
	if req.Notify != nil {
		notify = req.Notify.URL
	}
	if notify != "" {
		if err := s.checkWebhook(notify); err != nil {
			writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "notify: " + err.Error()})
			return
		}
	}

	run, rf := s.startTask(ctx, taskStart{agent: ag, msg: harness.UserText(req.Input), title: title(req.Input), start: start, notify: notify})
	if rf != nil {
		s.writeRefusal(w, rf)
		return
	}
	s.answerTask(ctx, w, run)
}

// answerTask answers 202 with a task that started.
func (s *Server) answerTask(ctx context.Context, w http.ResponseWriter, run store.Run) {
	v, err := s.taskView(ctx, run)
	if err != nil {
		s.log.Warn("reading a task's webhook", "run_id", run.ID, "err", err)
	}
	hd := w.Header()
	hd.Set("Location", "/v1/runs/"+run.ID)
	hd.Set("Agentkit-Run-Id", run.ID)
	hd.Set("Agentkit-Conversation-Id", run.ConversationID)
	writeJSON(w, http.StatusAccepted, v)
}

// taskView is a task as its start is answered: its summary and webhook.
func (s *Server) taskView(ctx context.Context, run store.Run) (Run, error) {
	v := s.runSummary(ctx, run)
	var err error
	v.Notify, err = s.notification(ctx, run.ID)
	return v, err
}

// taskStart is a task to start, past the checks of its request.
type taskStart struct {
	agent *agent
	// convID is the conversation the task continues; empty starts it in a
	// conversation of its own, with title.
	convID string
	title  string
	msg    llm.Message
	start  StartOptions
	notify string
}

// startTask starts a background run, counting it against the tenant's limit
// of background runs, or says why not.
func (s *Server) startTask(ctx context.Context, t taskStart) (store.Run, *refusal) {
	ag, start := t.agent, t.start
	// Counting the tenant's tasks and starting this one are one step here:
	// two requests at once cannot both take the last place.
	s.tmu.Lock()
	if rf := s.backgroundLimit(ctx); rf != nil {
		s.tmu.Unlock()
		return store.Run{}, rf
	}
	if apiErr := s.admit(); apiErr != nil {
		s.tmu.Unlock()
		return store.Run{}, &refusal{status: http.StatusServiceUnavailable, retryAfter: retryAfter, err: apiErr}
	}
	// A task runs in a pool that takes background runs, in its lane, whose
	// dispatcher resumes it after a crash.
	poolName, freePool, status, apiErr := s.admitPool(ag, runner.Placement{ConversationID: t.convID})
	if apiErr != nil {
		s.tmu.Unlock()
		s.release()
		rf := &refusal{status: status, err: apiErr}
		if status == http.StatusTooManyRequests {
			rf.retryAfter = retryAfter
		}
		return store.Run{}, rf
	}
	start.Pool, start.AgentVersion = poolName, ag.Version
	convID, own := t.convID, t.convID == ""
	if own {
		convID = s.cfg.newID("conv_")
		if _, err := s.store.CreateConversation(ctx, store.Conversation{ID: convID, Agent: ag.Name}); err != nil {
			s.tmu.Unlock()
			s.release()
			freePool()
			return store.Run{}, s.internalRefusal("creating a conversation", err)
		}
	}
	execCtx, cancel := execContext(ctx)
	x, err := s.runs.Start(execCtx, convID, t.msg, start)
	s.tmu.Unlock()
	if err != nil {
		cancel(nil)
		s.release()
		freePool()
		if own {
			if derr := s.store.DeleteConversation(ctx, convID); derr != nil {
				s.log.Warn("deleting the conversation of a task that did not start", "conversation_id", convID, "err", derr)
			}
		}
		return store.Run{}, s.startErrorOf(err, CodeBusy)
	}
	if own {
		if _, err := s.store.SetTitle(ctx, convID, t.title); err != nil {
			s.log.Warn("setting a conversation's title", "conversation_id", convID, "err", err)
		}
	}
	var notifyErr error
	if t.notify != "" {
		if _, notifyErr = s.store.CreateNotification(ctx, x.Run.ID, t.notify); notifyErr != nil {
			// A task told to report must not run unreported: end it.
			if _, err := s.runs.Cancel(ctx, x.Run.ID); err != nil {
				s.log.Error("canceling a task whose webhook was not stored", "run_id", x.Run.ID, "err", err)
			}
		}
	}
	s.log.Info("task started", "run_id", x.Run.ID, "conversation_id", convID, "agent", ag.Name, "pool", x.Run.Pool, "notify", t.notify != "")
	s.launch(execCtx, cancel, x, replayed{}, freePool)
	if notifyErr != nil {
		return store.Run{}, s.internalRefusal("storing the task's webhook", notifyErr)
	}
	return x.Run, nil
}

// backgroundLimit refuses one more background run of the tenant when it has
// as many going on as the server allows (429), or when they cannot be
// counted; nil lets it start. The caller holds s.tmu until it has started it.
func (s *Server) backgroundLimit(ctx context.Context) *refusal {
	n, err := s.backgroundRuns(ctx)
	if err != nil {
		return s.internalRefusal("counting the background runs", err)
	}
	if n >= s.cfg.maxBackground {
		return &refusal{status: http.StatusTooManyRequests, retryAfter: retryAfterTask, err: &Error{Code: CodeTooManyBackgroundRuns, Retryable: true, Message: fmt.Sprintf("%d background runs are going on, the most a tenant may have; retry once one has ended", n)}}
	}
	return nil
}

// backgroundRuns counts the tenant's background runs going on (queued or
// running), up to the limit.
func (s *Server) backgroundRuns(ctx context.Context) (int, error) {
	f := store.RunFilter{Status: store.StatusRunning, Kind: store.BackgroundRuns}
	p := store.Page{Limit: min(s.cfg.maxBackground, store.MaxPageLimit)}
	n := 0
	for {
		page, err := s.store.ListRuns(ctx, f, p)
		if err != nil {
			return 0, err
		}
		n += len(page.Runs)
		if page.Next == "" || n >= s.cfg.maxBackground {
			return n, nil
		}
		p.Cursor = page.Next
	}
}

// checkWebhook refuses a webhook URL the server does not post to.
func (s *Server) checkWebhook(raw string) error {
	if s.cfg.webhooks == nil {
		return errors.New("this server sends no webhooks")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return errors.New("want an http or https URL with a host and no user info")
	}
	if len(raw) > 2048 {
		return errors.New("the URL is longer than 2048 bytes")
	}
	if c := s.cfg.webhooks.CheckURL; c != nil {
		return c(raw)
	}
	return nil
}

// notification is the run's webhook on the wire; nil for a run that has
// none.
func (s *Server) notification(ctx context.Context, runID string) (*wire.Notification, error) {
	n, err := s.store.GetNotification(ctx, runID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, nil
	case err != nil:
		return nil, err
	}
	out := &wire.Notification{URL: n.URL, State: string(n.State), Attempts: make([]wire.DeliveryAttempt, len(n.Attempts))}
	for i, a := range n.Attempts {
		out.Attempts[i] = wire.DeliveryAttempt{At: a.At, Status: a.Status, Error: a.Error}
	}
	if !n.NextAttemptAt.IsZero() && n.State == store.NotificationPending {
		at := n.NextAttemptAt
		out.NextAttemptAt = &at
	}
	return out, nil
}

// WebhookPayload is the body posted to the webhook of run runID, of the
// tenant in ctx, when it ends: a [wire.WebhookEvent] with the run as GET
// /v1/runs/{id} shows it once it has ended, without its record or webhook.
// Give it to the webhook notifier (package webhook).
func (s *Server) WebhookPayload(ctx context.Context, runID string) ([]byte, error) {
	// The store says a run ended a moment before its execution stores its
	// last event, which has its answer: wait for it, up to endingWait.
	deadline := time.Now().Add(endingWait) // real time, as storedEnding's
	var v Run
	for {
		run, err := s.store.Get(ctx, runID)
		if err != nil {
			return nil, err
		}
		if v, err = s.runView(ctx, run); err != nil {
			return nil, err
		}
		if v.Record == nil || !v.Record.FinishedAt.IsZero() || !time.Now().Before(deadline) {
			break
		}
		t := time.NewTimer(endingStep)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		}
	}
	v.Record, v.Notify = nil, nil
	return json.Marshal(wire.WebhookEvent{V: WireVersion, Type: wire.TypeRunEnded, Run: v})
}

// webhookSecret serves GET /v1/webhooks/secret: the secret the tenant's
// webhooks are signed with, for its admins.
func (s *Server) webhookSecret(w http.ResponseWriter, r *http.Request) {
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	if s.cfg.webhooks == nil {
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "this server sends no webhooks"})
		return
	}
	t, _ := tenancy.From(ctx)
	secret, err := s.cfg.webhooks.Secret(t)
	if err != nil {
		s.internal(w, "reading the webhook secret", err)
		return
	}
	writeJSON(w, http.StatusOK, wire.WebhookSecret{V: WireVersion, Secret: secret})
}

// awaitsPickup reports whether run is a background run going on that nobody
// holds at now: its process stopped, and the recovery loop (or a worker)
// resumes it once its lease has expired.
func awaitsPickup(run store.Run, now time.Time) bool {
	return !run.Interactive && run.Status == store.StatusRunning && !held(run, now)
}

// pickup streams to out the events after the ID after of a background run
// nobody holds: the stored ones, evs, and then, once a process takes the
// run up again, its new ones as they come, until its last event or ctx is
// done. A watcher of a task never sees it end with the process that ran it.
func (s *Server) pickup(ctx context.Context, out *sseWriter, run store.Run, evs []Event, after bus.ID) {
	for _, e := range withText(evs, after) {
		if eventID(e).Compare(after) > 0 {
			out.event(e)
			after = eventID(e)
		}
	}
	cur, ok := s.awaitHeld(ctx, run.ID, nil)
	if !ok {
		return
	}
	if h := s.hubOf(ctx, run.ID); h != nil {
		h.subscribe()
		defer s.leave(h)
		pump(ctx, out, h, after)
		return
	}
	if held(cur, s.cfg.clock.Now()) {
		s.readView(ctx, s.joinView(ctx, cur, after), out.event)
		return
	}
	// It ended before this watch saw a process hold it (canceled, or run to
	// its end between two reads): what the store has, waiting for the last
	// event, which the store gets a moment after the run's status.
	rp, err := s.storedEnding(ctx, cur, s.poll())
	if err != nil {
		s.log.Error("reading the events of a task", "run_id", run.ID, "err", err)
		return
	}
	for _, e := range withText(withEnding(cur, rp, s.cfg.clock.Now()), after) {
		if eventID(e).Compare(after) > 0 {
			out.event(e)
		}
	}
}

// awaitHeld waits, reading the store every poll, until the background run is
// no longer waiting for a process: one holds it (and, for one of this
// server's, its hub is up), or it has ended. ok is false when ctx is done or
// the store fails. stop, if set, stops it too.
func (s *Server) awaitHeld(ctx context.Context, runID string, stop <-chan struct{}) (store.Run, bool) {
	t := time.NewTicker(s.poll()) // real time, as checkHeld's
	defer t.Stop()
	for {
		select {
		case <-t.C:
		case <-stop:
			return store.Run{}, false
		case <-ctx.Done():
			return store.Run{}, false
		}
		cur, err := s.store.Get(ctx, runID)
		if err != nil {
			if ctx.Err() == nil {
				s.log.Error("reading a task waiting for a process", "run_id", runID, "err", err)
			}
			return store.Run{}, false
		}
		now := s.cfg.clock.Now()
		switch {
		case s.hubOf(ctx, runID) != nil:
			return cur, true
		case awaitsPickup(cur, now):
		case held(cur, now) && s.ownBus:
			// Held, and no bus to follow it on: this server's recovery,
			// whose hub is about to be up, or another process, whose events
			// the store has when it ends.
		default:
			return cur, true
		}
	}
}
