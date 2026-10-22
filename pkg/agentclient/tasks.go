package agentclient

import (
	"context"
	"net/http"

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
