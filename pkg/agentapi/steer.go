package agentapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/stream"
)

// Steering a task (T086). A message, a pause or a resume for a task is a
// command in the run's command log ([runner.SteerCommand],
// [runner.PauseCommand], [runner.ResumeCommand]), sent through any server:
// the runner holding the task reads its commands at every turn boundary
// ([runner.WithCommands]), and a runner that takes the task up after a crash
// reads those still pending first. So a request never waits for the task;
// it answers 202 once the command is stored.

// steerTask serves POST /v1/tasks/{id}/messages: a steering message queued
// for the task's next turn boundary.
func (s *Server) steerTask(w http.ResponseWriter, r *http.Request) {
	body, apiErr, code := s.readBody(w, r)
	if apiErr != nil {
		writeError(w, code, apiErr)
		return
	}
	var req SteerRequest
	if err := decodeStrict(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "the body must be a JSON object {\"text\": ...}: " + err.Error()})
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "text is empty"})
		return
	}
	cmd, err := runner.SteerCommand(s.commandKey(r), harness.UserText(req.Text))
	if err != nil {
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: err.Error()})
		return
	}
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	run, ok := s.task(ctx, w, r.PathValue("id"))
	if !ok || !s.mayDirect(ctx, w, r, run) {
		return
	}
	stored, ok := s.sendTask(ctx, w, run, cmd)
	if !ok {
		return
	}
	s.log.Info("task steered", "run_id", run.ID, "seq", stored.Seq)
	writeJSON(w, http.StatusAccepted, TaskMessage{V: WireVersion, RunID: run.ID, Seq: stored.Seq, Text: req.Text, At: stored.CreatedAt})
}

// pauseTask serves POST /v1/tasks/{id}/pause: the task stops at its next
// turn boundary, a tool call going on included, and is paused until it is
// resumed. Pausing a paused task is a no-op (200).
func (s *Server) pauseTask(w http.ResponseWriter, r *http.Request) {
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	run, ok := s.task(ctx, w, r.PathValue("id"))
	if !ok {
		return
	}
	if run.Status == store.StatusPaused {
		writeJSON(w, http.StatusOK, s.runSummary(ctx, run))
		return
	}
	if _, ok := s.sendTask(ctx, w, run, runner.PauseCommand(s.commandKey(r))); !ok {
		return
	}
	s.log.Info("task pause asked", "run_id", run.ID)
	s.taskNow(ctx, w, run.ID, http.StatusAccepted)
}

// resumeTask serves POST /v1/tasks/{id}/resume: a paused task goes on from
// where it paused; a pause not acted on yet is withdrawn.
func (s *Server) resumeTask(w http.ResponseWriter, r *http.Request) {
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	run, ok := s.task(ctx, w, r.PathValue("id"))
	if !ok || !s.mayDirect(ctx, w, r, run) {
		return
	}
	if run.Status != store.StatusPaused {
		if _, ok := s.sendTask(ctx, w, run, runner.ResumeCommand(s.commandKey(r))); !ok {
			return
		}
		// The task may have paused before the resume was stored: it reads
		// it once it goes on, so continue it here.
		if run, ok = s.task(ctx, w, run.ID); !ok {
			return
		}
		if run.Status != store.StatusPaused {
			s.taskNow(ctx, w, run.ID, http.StatusAccepted)
			return
		}
	}
	// A task that has just paused lets its lease go a moment after: wait
	// for that, as a command does.
	s.awaitReleased(ctx, run.ID)
	if ctx.Err() != nil {
		return
	}
	// Read again: the run read before the wait may still show the lease
	// let go since, which relaunch would take for a run going on.
	if run, ok = s.task(ctx, w, run.ID); !ok {
		return
	}
	// relaunch counts a paused task against the tenant's background runs.
	h, _, ok := s.relaunch(ctx, w, r, run, CodeRunLeased)
	if !ok {
		return
	}
	s.log.Info("task resumed", "run_id", run.ID)
	s.taskNow(ctx, w, h.runID, http.StatusAccepted)
}

// task reads the run a /v1/tasks/{id} request names: a background chat run.
// It answers 404 for a run that is not one (a chat, a flow run).
func (s *Server) task(ctx context.Context, w http.ResponseWriter, id string) (store.Run, bool) {
	run, ok := s.getStored(ctx, w, id)
	if !ok {
		return run, false
	}
	if !run.Interactive {
		_, err := s.store.Load(ctx, run.ID)
		switch {
		case errors.Is(err, flow.ErrNotFound):
			return run, true
		case err != nil:
			s.internal(w, "reading run "+quote(id)+"'s checkpoint", err)
			return run, false
		}
	}
	writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "no task " + quote(id) + ": run " + quote(id) + " is not a task"})
	return run, false
}

// mayDirect reports whether the request's principal may direct the task: a
// steering message or a resume runs the agent's tools, as a message to the
// conversation or a resume of it does, so only for a request that may use
// them all. It answers 404 otherwise, as for no task.
func (s *Server) mayDirect(ctx context.Context, w http.ResponseWriter, r *http.Request, run store.Run) bool {
	acc := accessOf(r)
	if !acc.ok {
		return true
	}
	c, err := s.store.GetConversation(ctx, run.ConversationID)
	if err != nil {
		s.internal(w, "reading conversation "+quote(run.ConversationID), err)
		return false
	}
	a, err := s.agentFor(ctx, c.Agent, run.AgentVersion)
	if err != nil && !errors.Is(err, errNoAgent) {
		s.internal(w, "reading agent "+quote(c.Agent), err)
		return false
	}
	if a != nil && !acc.agent(a) {
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "no task " + quote(run.ID) + ": run " + quote(run.ID) + " is not a task"})
		return false
	}
	return true
}

// commandKey is the request's idempotency key, or a new one.
func (s *Server) commandKey(r *http.Request) string {
	if key := r.Header.Get("Idempotency-Key"); key != "" {
		return key
	}
	return s.cfg.newID("cmd_")
}

// sendTask stores cmd as the task's next command. It answers 409 for a task
// that has ended and 400 for a key used for another command, and reports
// false then.
func (s *Server) sendTask(ctx context.Context, w http.ResponseWriter, run store.Run, cmd store.Command) (store.Command, bool) {
	if run.Status.Final() {
		taskFinal(w, run)
		return store.Command{}, false
	}
	stored, err := s.cmds.Send(ctx, run.ID, cmd)
	switch {
	case errors.Is(err, store.ErrFinal):
		if cur, gerr := s.store.Get(ctx, run.ID); gerr == nil {
			run = cur
		}
		taskFinal(w, run)
		return store.Command{}, false
	case errors.Is(err, store.ErrConflict), errors.Is(err, stream.ErrInvalid), errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "Idempotency-Key was used for another command to task " + quote(run.ID)})
		return store.Command{}, false
	case err != nil:
		s.commandError(w, run.ID, err)
		return store.Command{}, false
	}
	return stored, true
}

// taskFinal answers a command to a task that has ended.
func taskFinal(w http.ResponseWriter, run store.Run) {
	writeError(w, http.StatusConflict, &Error{Code: CodeRunFinal, RunID: run.ID, Message: "task " + quote(run.ID) + " has ended (" + string(run.Status) + "); it takes no more messages"})
}

// taskNow answers with the task as it stands.
func (s *Server) taskNow(ctx context.Context, w http.ResponseWriter, id string, status int) {
	run, err := s.store.Get(ctx, id)
	if err != nil {
		s.internal(w, "reading run "+quote(id), err)
		return
	}
	writeJSON(w, status, s.runSummary(ctx, run))
}
