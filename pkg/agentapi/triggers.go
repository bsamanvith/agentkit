package agentapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
	"github.com/bsamanvith/agentkit/pkg/trigger"
)

// Defaults of [Triggers].
const (
	// DefaultHookBodyBytes is the largest inbound webhook body a trigger
	// takes by default.
	DefaultHookBodyBytes = 64 << 10
	// staleFiring is how long a firing may stay pending before a later one
	// takes it for lost (its process stopped between claiming it and
	// starting its run) instead of in flight.
	staleFiring = 10 * time.Minute
	// overlapWindow is how many of a trigger's newest firings the overlap
	// check reads.
	overlapWindow = 20
	// maxCatchUp bounds the due times a schedule skips after downtime.
	maxCatchUp = 100_000
)

// Triggers are what the server needs to serve triggers (T072): schedules and
// inbound webhooks that start background runs (tasks). The scheduler that
// fires due schedules is the program's ([trigger.Scheduler], with
// [Server.FireDue]).
type Triggers struct {
	// Store keeps the triggers and their firings: the server's store's.
	Store store.TriggerStore
	// ByHook finds the trigger of an inbound webhook across tenants
	// ([store.TriggerAdmin.TriggerByHook]). With Secret, it turns inbound
	// webhooks on; without both, a trigger cannot have a webhook.
	ByHook func(ctx context.Context, hookID string) (store.TenantTrigger, error)
	// Secret returns the signing secret of the webhook hookID of tenant t.
	Secret func(t tenancy.Tenant, hookID string) ([]byte, error)
	// MaxBodyBytes is the largest webhook body taken (0:
	// [DefaultHookBodyBytes]); a larger one is 413.
	MaxBodyBytes int64
	// Tolerance is how far a delivery's webhook-timestamp may be from now
	// (0: [trigger.DefaultTolerance]), at most half of [store.FiringKeep]
	// so that a delivery's ID is kept for as long as a replay of it could
	// be accepted.
	Tolerance time.Duration
}

// WithTriggers serves /v1/triggers and the inbound webhooks
// (/v1/hooks/{id}). Without it they are 404.
func WithTriggers(t Triggers) Option { return func(c *config) { c.triggers = &t } }

var (
	triggerID  = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	deliveryID = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,128}$`)
)

// errNoTriggers is FireDue's error on a server without triggers.
var errNoTriggers = errors.New("this server has no triggers (WithTriggers)")

// triggerStore returns the trigger store, or answers 404 without one.
func (s *Server) triggerStore(w http.ResponseWriter) (store.TriggerStore, bool) {
	if s.cfg.triggers == nil {
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "this server has no triggers"})
		return nil, false
	}
	return s.cfg.triggers.Store, true
}

func (s *Server) hooksOn() bool { return s.cfg.triggers != nil && s.cfg.triggers.Secret != nil }

// postTrigger serves POST /v1/triggers.
func (s *Server) postTrigger(w http.ResponseWriter, r *http.Request) {
	ts, ok := s.triggerStore(w)
	if !ok {
		return
	}
	body, apiErr, code := s.readBody(w, r)
	if apiErr != nil {
		writeError(w, code, apiErr)
		return
	}
	var req wire.TriggerRequest
	if err := decodeStrict(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "the body must be a JSON object {\"id\": ..., \"prompt\": ..., \"schedule\" or \"webhook\": ...}: " + err.Error()})
		return
	}
	if !triggerID.MatchString(req.ID) {
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "id " + quote(req.ID) + " must match " + triggerID.String()})
		return
	}
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	def := trigger.Definition{Agent: req.Agent, Prompt: req.Prompt, ConversationID: req.ConversationID, Schedule: req.Schedule, Webhook: req.Webhook}
	rec, rf := s.checkTrigger(ctx, r, def, store.TriggerRecord{ID: req.ID, Paused: req.Paused}, false)
	if rf != nil {
		s.writeRefusal(w, rf)
		return
	}
	got, err := ts.PutTrigger(ctx, rec, 0)
	switch {
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, &Error{Code: CodeTriggerExists, Message: "a trigger is named " + quote(req.ID)})
		return
	case err != nil:
		s.internal(w, "storing a trigger", err)
		return
	}
	s.log.Info("trigger created", "trigger", got.ID, "schedule", def.Schedule, "webhook", def.Webhook)
	w.Header().Set("Location", "/v1/triggers/"+got.ID)
	s.writeTrigger(ctx, w, http.StatusCreated, got)
}

// checkTrigger checks a trigger's definition for the request r and returns
// the record to store: rec with its data, its webhook's ID (rec's own,
// unless rotate or it had none) and its next firing time.
func (s *Server) checkTrigger(ctx context.Context, r *http.Request, def trigger.Definition, rec store.TriggerRecord, rotate bool) (store.TriggerRecord, *refusal) {
	invalid := func(msg string) *refusal {
		return &refusal{status: http.StatusBadRequest, err: &Error{Code: CodeInvalidRequest, Message: msg}}
	}
	sched, err := def.Check()
	if err != nil {
		return store.TriggerRecord{}, invalid(err.Error())
	}
	if def.Webhook && !s.hooksOn() {
		return store.TriggerRecord{}, invalid("this server takes no inbound webhooks (set its webhook secret)")
	}
	if def.ConversationID != "" {
		c, err := s.store.GetConversation(ctx, def.ConversationID)
		switch {
		case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrInvalid):
			return store.TriggerRecord{}, &refusal{status: http.StatusNotFound, err: &Error{Code: CodeNotFound, Message: "no conversation " + quote(def.ConversationID)}}
		case err != nil:
			return store.TriggerRecord{}, s.internalRefusal("reading a conversation", err)
		case def.Agent != "" && def.Agent != c.Agent:
			return store.TriggerRecord{}, invalid("conversation " + quote(def.ConversationID) + " is of agent " + quote(c.Agent) + ", not " + quote(def.Agent))
		}
		def.Agent = c.Agent
		a, err := s.agentFor(ctx, c.Agent, 0)
		switch {
		case err != nil && !errors.Is(err, errNoAgent):
			return store.TriggerRecord{}, s.internalRefusal("reading a stored agent", err)
		case a == nil || !accessOf(r).agent(a):
			return store.TriggerRecord{}, invalid("conversation " + quote(def.ConversationID) + " is of agent " + quote(c.Agent) + ", which this server does not run for you")
		}
	} else {
		ag, apiErr, code := s.resolveAgent(ctx, r, def.Agent)
		if apiErr != nil {
			return store.TriggerRecord{}, &refusal{status: code, err: apiErr}
		}
		def.Agent = ag.Name
	}
	data, err := json.Marshal(def)
	if err != nil {
		return store.TriggerRecord{}, s.internalRefusal("encoding a trigger", err)
	}
	rec.Data = data
	switch {
	case !def.Webhook:
		rec.HookID = ""
	case rec.HookID == "" || rotate:
		rec.HookID = s.cfg.newID("hk_")
	}
	rec.NextFireAt = time.Time{}
	if sched != nil {
		rec.NextFireAt = sched.Next(s.cfg.clock.Now())
	}
	return rec, nil
}

// readTrigger reads the trigger of the request's {id}, answering 404.
func (s *Server) readTrigger(w http.ResponseWriter, r *http.Request) (context.Context, store.TriggerStore, store.TriggerRecord, bool) {
	ts, ok := s.triggerStore(w)
	if !ok {
		return nil, nil, store.TriggerRecord{}, false
	}
	ctx, ok := s.scope(w, r)
	if !ok {
		return nil, nil, store.TriggerRecord{}, false
	}
	id := r.PathValue("id")
	t, err := ts.GetTrigger(ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "no trigger " + quote(id)})
		return nil, nil, store.TriggerRecord{}, false
	case err != nil:
		s.internal(w, "reading a trigger", err)
		return nil, nil, store.TriggerRecord{}, false
	}
	return ctx, ts, t, true
}

// getTrigger serves GET /v1/triggers/{id}: with its webhook's secret.
func (s *Server) getTrigger(w http.ResponseWriter, r *http.Request) {
	ctx, _, t, ok := s.readTrigger(w, r)
	if !ok {
		return
	}
	s.writeTrigger(ctx, w, http.StatusOK, t)
}

// listTriggers serves GET /v1/triggers: without their secrets.
func (s *Server) listTriggers(w http.ResponseWriter, r *http.Request) {
	ts, ok := s.triggerStore(w)
	if !ok {
		return
	}
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	all, err := ts.ListTriggers(ctx)
	if err != nil {
		s.internal(w, "listing triggers", err)
		return
	}
	out := wire.TriggerList{V: WireVersion, Triggers: make([]wire.Trigger, 0, len(all))}
	for _, t := range all {
		v, err := s.triggerView(ctx, t, false)
		if err != nil {
			s.internal(w, "reading a trigger's firings", err)
			return
		}
		out.Triggers = append(out.Triggers, v)
	}
	writeJSON(w, http.StatusOK, out)
}

// patchTrigger serves PATCH /v1/triggers/{id}.
func (s *Server) patchTrigger(w http.ResponseWriter, r *http.Request) {
	body, apiErr, code := s.readBody(w, r)
	if apiErr != nil {
		writeError(w, code, apiErr)
		return
	}
	var req wire.TriggerUpdate
	if err := decodeStrict(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "the body must be a JSON object of the fields to change: " + err.Error()})
		return
	}
	ctx, ts, old, ok := s.readTrigger(w, r)
	if !ok {
		return
	}
	def, _, err := trigger.Parse(old.Data)
	if err != nil {
		s.internal(w, "reading a stored trigger", err)
		return
	}
	if def.Static {
		writeError(w, http.StatusConflict, staticTrigger(old.ID))
		return
	}
	if req.Version != 0 && req.Version != old.Version {
		writeError(w, http.StatusConflict, &Error{Code: CodeConflict, Message: fmt.Sprintf("trigger %q is at version %d, not %d", old.ID, old.Version, req.Version)})
		return
	}
	oldSchedule := def.Schedule
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	set(&def.Agent, req.Agent)
	set(&def.Prompt, req.Prompt)
	set(&def.ConversationID, req.ConversationID)
	if req.ConversationID != nil && req.Agent == nil {
		def.Agent = "" // the new conversation's agent, or the default
	}
	if req.Schedule != nil {
		def.Schedule = *req.Schedule
		if def.Schedule != "" && req.Webhook == nil {
			def.Webhook = false
		}
	}
	if req.Webhook != nil {
		def.Webhook = *req.Webhook
		if def.Webhook && req.Schedule == nil {
			def.Schedule = ""
		}
	}
	rec, rf := s.checkTrigger(ctx, r, def, store.TriggerRecord{ID: old.ID, HookID: old.HookID, Paused: old.Paused}, req.RotateWebhook)
	if rf != nil {
		s.writeRefusal(w, rf)
		return
	}
	if def.Schedule != "" && def.Schedule == oldSchedule && !old.NextFireAt.IsZero() {
		rec.NextFireAt = old.NextFireAt // the same schedule goes on as it was
	}
	got, err := ts.PutTrigger(ctx, rec, old.Version)
	switch {
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusConflict, &Error{Code: CodeConflict, Message: "trigger " + quote(old.ID) + " changed since it was read; read it again"})
		return
	case err != nil:
		s.internal(w, "storing a trigger", err)
		return
	}
	s.writeTrigger(ctx, w, http.StatusOK, got)
}

func staticTrigger(id string) *Error {
	return &Error{Code: CodeStaticTrigger, Message: "trigger " + quote(id) + " is from the server's config file: change it there (the API only pauses and resumes it)"}
}

// deleteTrigger serves DELETE /v1/triggers/{id}.
func (s *Server) deleteTrigger(w http.ResponseWriter, r *http.Request) {
	ctx, ts, old, ok := s.readTrigger(w, r)
	if !ok {
		return
	}
	if def, _, err := trigger.Parse(old.Data); err == nil && def.Static {
		writeError(w, http.StatusConflict, staticTrigger(old.ID))
		return
	}
	switch err := ts.DeleteTrigger(ctx, old.ID, 0); {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "no trigger " + quote(old.ID)})
		return
	case err != nil:
		s.internal(w, "deleting a trigger", err)
		return
	}
	s.log.Info("trigger deleted", "trigger", old.ID)
	w.WriteHeader(http.StatusNoContent)
}

// pauseTrigger serves POST /v1/triggers/{id}/pause.
func (s *Server) pauseTrigger(w http.ResponseWriter, r *http.Request) { s.setPaused(w, r, true) }

// resumeTrigger serves POST /v1/triggers/{id}/resume: a schedule goes on
// from now, without the times it missed while paused.
func (s *Server) resumeTrigger(w http.ResponseWriter, r *http.Request) { s.setPaused(w, r, false) }

func (s *Server) setPaused(w http.ResponseWriter, r *http.Request, paused bool) {
	ctx, ts, old, ok := s.readTrigger(w, r)
	if !ok {
		return
	}
	if old.Paused == paused {
		s.writeTrigger(ctx, w, http.StatusOK, old)
		return
	}
	rec := old
	rec.Paused = paused
	if !paused {
		_, sched, err := trigger.Parse(old.Data)
		if err != nil {
			s.internal(w, "reading a stored trigger", err)
			return
		}
		if sched != nil {
			rec.NextFireAt = sched.Next(s.cfg.clock.Now())
		}
	}
	got, err := ts.PutTrigger(ctx, rec, old.Version)
	switch {
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusConflict, &Error{Code: CodeConflict, Message: "trigger " + quote(old.ID) + " changed since it was read; read it again"})
		return
	case err != nil:
		s.internal(w, "storing a trigger", err)
		return
	}
	s.log.Info("trigger paused", "trigger", got.ID, "paused", paused)
	s.writeTrigger(ctx, w, http.StatusOK, got)
}

// listFirings serves GET /v1/triggers/{id}/firings?limit=: newest first.
func (s *Server) listFirings(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > store.MaxFirings {
			writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: fmt.Sprintf("limit %q is not a number from 1 to %d", v, store.MaxFirings)})
			return
		}
		limit = n
	}
	ctx, ts, t, ok := s.readTrigger(w, r)
	if !ok {
		return
	}
	fs, err := ts.ListFirings(ctx, t.ID, limit)
	if err != nil {
		s.internal(w, "listing a trigger's firings", err)
		return
	}
	out := wire.FiringList{V: WireVersion, Firings: make([]wire.Firing, len(fs))}
	for i, f := range fs {
		out.Firings[i] = firingView(f)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) writeTrigger(ctx context.Context, w http.ResponseWriter, status int, t store.TriggerRecord) {
	v, err := s.triggerView(ctx, t, true)
	if err != nil {
		s.internal(w, "reading a trigger's firings", err)
		return
	}
	writeJSON(w, status, v)
}

// triggerView is t on the wire, with its webhook's secret if withSecret.
func (s *Server) triggerView(ctx context.Context, t store.TriggerRecord, withSecret bool) (wire.Trigger, error) {
	v := wire.Trigger{V: WireVersion, ID: t.ID, Paused: t.Paused, Version: t.Version, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt}
	var def trigger.Definition
	if err := json.Unmarshal(t.Data, &def); err != nil {
		s.log.Error("a stored trigger does not decode", "trigger", t.ID, "err", err)
	}
	v.Agent, v.Prompt, v.ConversationID, v.Schedule, v.Static = def.Agent, def.Prompt, def.ConversationID, def.Schedule, def.Static
	if !t.NextFireAt.IsZero() && !t.Paused {
		at := t.NextFireAt
		v.NextFireAt = &at
	}
	if t.HookID != "" {
		v.Webhook = &wire.TriggerWebhook{Path: "/v1/hooks/" + t.HookID}
		if withSecret && s.hooksOn() {
			tn, _ := tenancy.From(ctx)
			secret, err := s.cfg.triggers.Secret(tn, t.HookID)
			if err != nil {
				return wire.Trigger{}, err
			}
			v.Webhook.Secret = trigger.EncodeStandardSecret(secret)
		}
	}
	fs, err := s.cfg.triggers.Store.ListFirings(ctx, t.ID, 1)
	if err != nil {
		return wire.Trigger{}, err
	}
	if len(fs) > 0 {
		f := firingView(fs[0])
		v.LastFiring = &f
	}
	return v, nil
}

func firingView(f store.Firing) wire.Firing {
	v := wire.Firing{ID: f.ID, At: f.At, State: string(f.State), RunID: f.RunID, Reason: f.Reason}
	if !f.ScheduledFor.IsZero() {
		at := f.ScheduledFor
		v.ScheduledFor = &at
	}
	if !f.EndedAt.IsZero() {
		at := f.EndedAt
		v.EndedAt = &at
	}
	return v
}

// postHook serves POST /v1/hooks/{id}: an inbound webhook, which needs no
// credentials but its Standard Webhooks signature. A signed delivery starts
// a run of its trigger with the body as a data block (202), or is recorded
// as skipped while the trigger's previous run goes on (200). A delivery
// whose webhook-id the trigger has taken is refused (409 replayed), unless
// its firing failed to start a run or was lost (pending for longer than
// staleFiring): the sender's retry fires it again.
func (s *Server) postHook(w http.ResponseWriter, r *http.Request) {
	if !s.hooksOn() {
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "this server takes no inbound webhooks"})
		return
	}
	tc := s.cfg.triggers
	limit := tc.MaxBodyBytes
	if limit == 0 {
		limit = DefaultHookBodyBytes
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, &Error{Code: CodeTooLarge, Message: fmt.Sprintf("the body is larger than %d bytes", limit)})
			return
		}
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "reading the body: " + err.Error()})
		return
	}
	hookID := r.PathValue("id")
	tt, err := tc.ByHook(r.Context(), hookID)
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "no webhook " + quote(hookID)})
		return
	case err != nil:
		s.internal(w, "finding a webhook's trigger", err)
		return
	}
	secret, err := tc.Secret(tt.Tenant, hookID)
	if err != nil {
		s.internal(w, "reading a webhook's secret", err)
		return
	}
	h := r.Header
	id := h.Get(trigger.StandardHeaderID)
	if err := trigger.VerifyStandard(secret, id, h.Get(trigger.StandardHeaderTimestamp), h.Get(trigger.StandardHeaderSignature), body, s.cfg.clock.Now(), tc.Tolerance); err != nil {
		writeError(w, http.StatusUnauthorized, &Error{Code: CodeBadSignature, Message: err.Error()})
		return
	}
	switch {
	case !deliveryID.MatchString(id):
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "webhook-id " + quote(id) + " must match " + deliveryID.String()})
		return
	case !json.Valid(body) || !utf8.Valid(body):
		// encoding/json takes bytes that are not UTF-8 inside strings; the
		// run's message must be text.
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "the body is not JSON in UTF-8"})
		return
	}
	ctx := tenancy.With(context.WithoutCancel(r.Context()), tt.Tenant)
	t := tt.Trigger
	def, _, err := trigger.Parse(t.Data)
	switch {
	case err != nil:
		s.internal(w, "reading a stored trigger", err)
		return
	case !def.Webhook:
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "no webhook " + quote(hookID)})
		return
	case t.Paused:
		writeError(w, http.StatusConflict, &Error{Code: CodeTriggerPaused, Message: "trigger " + quote(t.ID) + " is paused"})
		return
	case s.isDraining():
		s.unavailable(w, shuttingDown())
		return
	}
	f, err := tc.Store.StartFiring(ctx, t.ID, store.Firing{ID: id}, time.Time{}, time.Time{})
	if errors.Is(err, store.ErrConflict) && s.endLostFiring(ctx, t.ID, id) {
		// The sender's retry of a delivery whose firing was lost.
		f, err = tc.Store.StartFiring(ctx, t.ID, store.Firing{ID: id}, time.Time{}, time.Time{})
	}
	switch {
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, &Error{Code: CodeReplayed, Message: "trigger " + quote(t.ID) + " has taken delivery " + quote(id)})
		return
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "no webhook " + quote(hookID)})
		return
	case err != nil:
		s.internal(w, "recording a webhook's firing", err)
		return
	}
	data := &trigger.Data{Source: "webhook", Trigger: t.ID, Delivery: id, Body: body}
	f, run, rf := s.fireRun(ctx, t, def, f, f.At, nil, data)
	if rf != nil {
		s.writeRefusal(w, rf)
		return
	}
	out := wire.HookAccepted{V: WireVersion, Trigger: t.ID, Firing: firingView(f)}
	if f.State != store.FiringStarted {
		writeJSON(w, http.StatusOK, out)
		return
	}
	hd := w.Header()
	hd.Set("Location", "/v1/runs/"+run.ID)
	hd.Set("Agentkit-Run-Id", run.ID)
	hd.Set("Agentkit-Conversation-Id", run.ConversationID)
	writeJSON(w, http.StatusAccepted, out)
}

// FireDue fires a trigger whose schedule is due, as [trigger.Fire]: give it
// to a [trigger.Scheduler]. It claims the due time in the store, so that of
// several servers sharing the store one fires it (the others get an error
// matching [store.ErrConflict]), moves the trigger on to its next time after
// now (a schedule that missed several times while nothing ran fires once,
// for the latest of them), and starts the run, or records the firing as
// skipped while the trigger's previous run goes on. A server shutting down
// fires nothing.
func (s *Server) FireDue(ctx context.Context, tt store.TenantTrigger) error {
	if s.cfg.triggers == nil {
		return errNoTriggers
	}
	if s.isDraining() {
		return errors.New("the server is shutting down")
	}
	ctx = tenancy.With(ctx, tt.Tenant)
	t := tt.Trigger
	def, sched, err := trigger.Parse(t.Data)
	if err != nil {
		return err
	}
	if sched == nil {
		return fmt.Errorf("trigger %q is due, and has no schedule", t.ID)
	}
	due, now := t.NextFireAt, s.cfg.clock.Now()
	latest := due
	for i := 0; i < maxCatchUp; i++ {
		n := sched.Next(latest)
		if n.IsZero() || n.After(now) {
			break
		}
		latest = n
	}
	next := sched.Next(now)
	if !next.IsZero() && !next.After(due) {
		next = sched.Next(due)
	}
	f, err := s.cfg.triggers.Store.StartFiring(ctx, t.ID, store.Firing{ID: "at_" + latest.UTC().Format("20060102T150405Z"), ScheduledFor: latest}, due, next)
	if err != nil {
		return err
	}
	if latest.After(due) {
		s.log.Info("trigger missed times", "trigger", t.ID, "from", due, "to", latest)
	}
	f, _, rf := s.fireRun(ctx, t, def, f, latest, sched.Location(), nil)
	if rf != nil && rf.status >= http.StatusInternalServerError {
		return fmt.Errorf("trigger %q: %s", t.ID, rf.err.Message)
	}
	s.log.Info("trigger fired", "trigger", t.ID, "firing", f.ID, "state", f.State, "run_id", f.RunID)
	return nil
}

// fireRun runs a pending firing f of trigger t: it records it as skipped if
// the trigger's previous run is going on, starts its run otherwise, and
// records what became of it. at and loc are the time the prompt is rendered
// for; data is the firing's third-party data, if any. A run that could not
// start comes with its refusal.
func (s *Server) fireRun(ctx context.Context, t store.TriggerRecord, def trigger.Definition, f store.Firing, at time.Time, loc *time.Location, data *trigger.Data) (store.Firing, store.Run, *refusal) {
	ts := s.cfg.triggers.Store
	end := func(e store.FiringEnd) store.Firing {
		got, err := ts.EndFiring(ctx, t.ID, f.ID, e)
		if err != nil {
			s.log.Error("recording a trigger's firing", "trigger", t.ID, "firing", f.ID, "state", e.State, "err", err)
			f.State, f.RunID, f.Reason = e.State, e.RunID, e.Reason
			return f
		}
		return got
	}
	reason, err := s.triggerBusy(ctx, t.ID, def, f)
	if err != nil {
		rf := s.internalRefusal("checking a trigger's previous firing", err)
		return end(store.FiringEnd{State: store.FiringFailed, Reason: rf.err.Message}), store.Run{}, rf
	}
	if reason != "" {
		return end(store.FiringEnd{State: store.FiringSkipped, Reason: reason}), store.Run{}, nil
	}
	ag, err := s.agentFor(ctx, def.Agent, 0) // a stored agent at its published version
	if err != nil && !errors.Is(err, errNoAgent) {
		rf := s.internalRefusal("reading a stored agent", err)
		return end(store.FiringEnd{State: store.FiringFailed, Reason: rf.err.Message}), store.Run{}, rf
	}
	if ag == nil {
		rf := &refusal{status: http.StatusConflict, err: &Error{Code: CodeInvalidRequest, Message: "trigger " + quote(t.ID) + " runs agent " + quote(def.Agent) + ", which this server does not run"}}
		return end(store.FiringEnd{State: store.FiringFailed, Reason: rf.err.Message}), store.Run{}, rf
	}
	prompt := def.Render(t.ID, at, loc)
	run, rf := s.startTask(ctx, taskStart{
		agent: ag, convID: def.ConversationID, title: title(prompt), notify: ag.Notify,
		msg:   trigger.Message(prompt, data),
		start: StartOptions{Background: true, Interaction: harness.Autonomous},
	})
	if rf != nil {
		return end(store.FiringEnd{State: store.FiringFailed, Reason: rf.err.Code + ": " + rf.err.Message}), store.Run{}, rf
	}
	return end(store.FiringEnd{State: store.FiringStarted, RunID: run.ID}), run, nil
}

// triggerBusy says why trigger id must not start another run now ("" when
// it may) for its pending firing self: its conversation has a run going on,
// an earlier firing (a lower Seq) is still starting its run, or the run of
// its newest firing that started one is going on. Of two firings pending at
// once, the later defers to the earlier, so that one of them runs. A firing
// pending for longer than staleFiring was lost (its process stopped before
// it started a run): it is recorded as failed.
func (s *Server) triggerBusy(ctx context.Context, id string, def trigger.Definition, self store.Firing) (string, error) {
	if def.ConversationID != "" {
		run, err := s.store.Unfinished(ctx, def.ConversationID)
		switch {
		case err == nil:
			return "run " + run.ID + " of its conversation is going on", nil
		case !errors.Is(err, store.ErrNotFound):
			return "", err
		}
	}
	ts := s.cfg.triggers.Store
	fs, err := ts.ListFirings(ctx, id, overlapWindow)
	if err != nil {
		return "", err
	}
	now := s.cfg.clock.Now()
	for _, f := range fs {
		switch {
		case f.ID == self.ID:
		case f.State == store.FiringPending && now.Sub(f.At) < staleFiring:
			if f.Seq > self.Seq {
				continue // a later firing, which defers to this one
			}
			return "firing " + f.ID + " is still starting its run", nil
		case f.State == store.FiringPending:
			s.markLost(ctx, id, f.ID)
		case f.State == store.FiringStarted:
			run, err := s.store.Get(ctx, f.RunID)
			switch {
			case errors.Is(err, store.ErrNotFound):
				return "", nil
			case err != nil:
				return "", err
			case !ended(run):
				return "run " + run.ID + " of firing " + f.ID + " is going on", nil
			}
			return "", nil
		}
	}
	return "", nil
}

// markLost records the pending firing firingID of trigger id as failed: it
// was lost (its process stopped before it started a run). It reports
// whether the firing has ended, by this call or another.
func (s *Server) markLost(ctx context.Context, id, firingID string) bool {
	_, err := s.cfg.triggers.Store.EndFiring(ctx, id, firingID, store.FiringEnd{State: store.FiringFailed, Reason: "lost: the process firing it stopped before it started a run"})
	if err != nil && !errors.Is(err, store.ErrFinal) {
		s.log.Warn("recording a lost firing", "trigger", id, "firing", firingID, "err", err)
		return false
	}
	return true
}

// endLostFiring marks the firing firingID of trigger id as lost if it has
// been pending for longer than staleFiring, and reports whether it did: a
// delivery whose firing was lost may then be retried under its ID.
func (s *Server) endLostFiring(ctx context.Context, id, firingID string) bool {
	fs, err := s.cfg.triggers.Store.ListFirings(ctx, id, store.MaxPageLimit)
	if err != nil {
		s.log.Warn("reading a trigger's firings", "trigger", id, "err", err)
		return false
	}
	now := s.cfg.clock.Now()
	for _, f := range fs {
		if f.ID == firingID {
			return f.State == store.FiringPending && now.Sub(f.At) >= staleFiring && s.markLost(ctx, id, firingID)
		}
	}
	return false
}

// ended reports whether a run will not go on by itself: final, or failed and
// not retryable.
func ended(r store.Run) bool {
	return r.Status.Final() || (r.Status == store.StatusFailed && !r.Retryable)
}

// StaticTrigger is a trigger of the server's config file, which
// [Server.SyncTriggers] stores.
type StaticTrigger struct {
	// Tenant owns it; the zero Tenant is [DefaultTenant].
	Tenant     tenancy.Tenant
	ID         string
	Definition trigger.Definition
}

// SyncTriggers makes the static triggers of each tenant named in static (and
// of [DefaultTenant]) the ones given: it creates those that are new, replaces
// those whose definition changed (a schedule goes on from now), and deletes
// those no longer given. A trigger made through the API with a static one's
// ID is left as it is, and reported. Paused stays as it was. Replicas
// syncing at once are safe: one's write wins, and the others' are the same.
func (s *Server) SyncTriggers(ctx context.Context, static []StaticTrigger) error {
	if s.cfg.triggers == nil {
		if len(static) == 0 {
			return nil
		}
		return errNoTriggers
	}
	def, err := tenancy.NewTenant(DefaultTenant)
	if err != nil {
		return err
	}
	byTenant := map[tenancy.Tenant][]StaticTrigger{def: nil}
	for _, st := range static {
		if st.Tenant == (tenancy.Tenant{}) {
			st.Tenant = def
		}
		byTenant[st.Tenant] = append(byTenant[st.Tenant], st)
	}
	var errs []error
	for tn, list := range byTenant {
		if err := s.syncTenant(tenancy.With(ctx, tn), list); err != nil {
			errs = append(errs, fmt.Errorf("tenant %q: %w", tn.ID(), err))
		}
	}
	return errors.Join(errs...)
}

func (s *Server) syncTenant(ctx context.Context, static []StaticTrigger) error {
	ts := s.cfg.triggers.Store
	have, err := ts.ListTriggers(ctx)
	if err != nil {
		return err
	}
	stored := map[string]store.TriggerRecord{}
	for _, t := range have {
		stored[t.ID] = t
	}
	var errs []error
	want := map[string]bool{}
	for _, st := range static {
		want[st.ID] = true
		if err := s.syncOne(ctx, st, stored); err != nil {
			errs = append(errs, fmt.Errorf("trigger %q: %w", st.ID, err))
		}
	}
	for _, t := range have {
		if d, _, err := trigger.Parse(t.Data); err != nil || !d.Static || want[t.ID] {
			continue
		}
		if err := ts.DeleteTrigger(ctx, t.ID, t.Version); err != nil && !errors.Is(err, store.ErrNotFound) && !errors.Is(err, store.ErrConflict) {
			errs = append(errs, fmt.Errorf("deleting trigger %q: %w", t.ID, err))
			continue
		}
		s.log.Info("static trigger removed", "trigger", t.ID)
	}
	return errors.Join(errs...)
}

func (s *Server) syncOne(ctx context.Context, st StaticTrigger, stored map[string]store.TriggerRecord) error {
	d := st.Definition
	d.Static = true
	if d.ConversationID != "" {
		return errors.New("a static trigger starts a new conversation each time: it takes no conversation_id")
	}
	if !triggerID.MatchString(st.ID) {
		return fmt.Errorf("id must match %s", triggerID)
	}
	sched, err := d.Check()
	if err != nil {
		return err
	}
	if d.Webhook && !s.hooksOn() {
		return errors.New("this server takes no inbound webhooks (set its webhook secret)")
	}
	switch a := s.agents[d.Agent]; {
	case d.Agent == "" && s.defaultAgent != nil:
		d.Agent = s.defaultAgent.Name
	case a == nil:
		return fmt.Errorf("no agent %q", d.Agent)
	}
	data, err := json.Marshal(d)
	if err != nil {
		return err
	}
	old, exists := stored[st.ID]
	if exists {
		od, _, err := trigger.Parse(old.Data)
		if err != nil || !od.Static {
			s.log.Warn("a trigger made through the API has a static trigger's id; the config's is not stored", "trigger", st.ID)
			return nil
		}
		if bytes.Equal(old.Data, data) {
			return nil
		}
	}
	rec := store.TriggerRecord{ID: st.ID, Data: data, Paused: old.Paused}
	if d.Webhook {
		rec.HookID = old.HookID
		if rec.HookID == "" {
			rec.HookID = s.cfg.newID("hk_")
		}
	}
	if sched != nil {
		rec.NextFireAt = sched.Next(s.cfg.clock.Now())
	}
	if _, err := s.cfg.triggers.Store.PutTrigger(ctx, rec, old.Version); err != nil && !errors.Is(err, store.ErrConflict) {
		return err
	}
	s.log.Info("static trigger stored", "trigger", st.ID, "schedule", d.Schedule, "webhook", d.Webhook)
	return nil
}
