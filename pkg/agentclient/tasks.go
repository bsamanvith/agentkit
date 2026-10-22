package agentclient

import (
	"context"
	"net/http"
	"net/url"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
)

// StartTask starts a task: a background run of an agent on an input, in a
// conversation of its own (POST /v1/tasks). It returns the run as it
// started; the run goes on whoever watches it. Follow it with
// [Client.Events] (resuming with the last event's ID after a break), read
// it with [Client.GetRun], cancel it with [Client.Cancel]. Past the
// tenant's tasks going on, it matches [ErrTooManyBackgroundRuns].
func (c *Client) StartTask(ctx context.Context, req wire.TaskRequest) (wire.Run, error) {
	var run wire.Run
	return run, c.send(ctx, http.MethodPost, "/v1/tasks", req, http.StatusAccepted, &run)
}

// WebhookSecret returns the secret the tenant's webhooks are signed with
// (GET /v1/webhooks/secret), to verify their Agentkit-Signature header
// (package webhook). It needs the agent:admin scope; a server that sends no
// webhooks matches [ErrNotFound].
func (c *Client) WebhookSecret(ctx context.Context) (string, error) {
	var s wire.WebhookSecret
	return s.Secret, c.get(ctx, "/v1/webhooks/secret", &s)
}

// SteerTask queues a message for a task going on or paused
// (POST /v1/tasks/{id}/messages) and returns it as queued. The task reads it
// at its next turn boundary, after the tool results of the turn going on and
// before its next model call; messages queued before one boundary arrive
// together, in order, in one steered event ([wire.TypeSteered]). A task that
// has ended matches [ErrRunState] (run_final); a run that is no task,
// [ErrNotFound].
func (c *Client) SteerTask(ctx context.Context, id, text string) (wire.TaskMessage, error) {
	var m wire.TaskMessage
	return m, c.send(ctx, http.MethodPost, taskPath(id, "messages"), wire.SteerRequest{Text: text}, http.StatusAccepted, &m)
}

// PauseTask asks a task to pause at its next turn boundary
// (POST /v1/tasks/{id}/pause) and returns it: still running until that
// boundary, or paused already. A paused task's execution ends with
// run_error paused ([wire.CodePaused]); nothing runs it, a restart included,
// until [Client.ResumeTask]. A task whose model answers at that boundary
// completes instead.
func (c *Client) PauseTask(ctx context.Context, id string) (wire.Run, error) {
	var run wire.Run
	path := taskPath(id, "pause")
	r, err := c.request(ctx, http.MethodPost, path, nil, "application/json")
	if err != nil {
		return run, err
	}
	resp, err := c.hc.Do(r)
	if err != nil {
		return run, err
	}
	status := http.StatusAccepted
	if resp.StatusCode == http.StatusOK { // paused already
		status = http.StatusOK
	}
	return run, c.decodeStatus(resp, status, "POST "+path, &run)
}

// ResumeTask continues a paused task (POST /v1/tasks/{id}/resume) and
// returns it going on; follow it with [Client.Events]. The messages queued
// while it was paused reach it first. A task asked to pause that has not
// paused yet keeps going. Past the tenant's tasks going on, it matches
// [ErrTooManyBackgroundRuns].
func (c *Client) ResumeTask(ctx context.Context, id string) (wire.Run, error) {
	var run wire.Run
	return run, c.send(ctx, http.MethodPost, taskPath(id, "resume"), nil, http.StatusAccepted, &run)
}

func taskPath(id, verb string) string { return "/v1/tasks/" + url.PathEscape(id) + "/" + verb }
