package wire

import (
	"encoding/json"
	"time"
)

// Codes of background runs (tasks, T071).
const (
	// CodeTooManyBackgroundRuns ("too_many_background_runs", HTTP 429 with
	// Retry-After): the tenant has as many background runs going on as the
	// server allows; retry once one has ended.
	CodeTooManyBackgroundRuns = "too_many_background_runs"
	// CodeWallClockLimit ("wall_clock_limit"): a background run was still
	// going on at its wall-clock limit, and failed. It is not retryable.
	CodeWallClockLimit = "wall_clock_limit"
)

// Interaction modes of a task: whether it may wait for a person.
const (
	// InteractionAutonomous: the task never waits for a person (the default,
	// and for now the only mode).
	InteractionAutonomous = "autonomous"
)

// TaskRequest is the body of POST /v1/tasks: a background run of an agent on
// an input, in a conversation of its own. The answer is 202 and the run
// ([Run]); the run goes on whoever watches it, until it ends or is canceled
// (POST /v1/runs/{id}/cancel).
type TaskRequest struct {
	// Agent names the agent (empty: the default agent).
	Agent string `json:"agent,omitempty"`
	// Input is the task: the user message the run starts from.
	Input string `json:"input"`
	// Interaction is how the task treats a tool that needs a person; only
	// [InteractionAutonomous], the default, is served.
	Interaction string `json:"interaction,omitempty"`
	// Policy narrows the agent's tool policy for the task, as
	// [RunRequest.ToolPolicy].
	Policy json.RawMessage `json:"policy,omitempty"`
	// Notify is the webhook told when the run ends; without it, the agent's
	// default, if it has one.
	Notify *Notify `json:"notify,omitempty"`
}

// Notify says where a run's end is posted.
type Notify struct {
	URL string `json:"url"`
}

// Notification states.
const (
	NotificationPending   = "pending"   // not delivered yet; tried when the run ends, and again after a failure
	NotificationDelivered = "delivered" // an attempt was answered with a 2xx
	NotificationFailed    = "failed"    // given up on, a day after the first attempt
)

// Notification is the webhook of a run and its delivery so far, in [Run].
type Notification struct {
	URL      string            `json:"url"`
	State    string            `json:"state"`
	Attempts []DeliveryAttempt `json:"attempts"`
	// NextAttemptAt is when a pending notification is tried again after a
	// failed attempt.
	NextAttemptAt *time.Time `json:"next_attempt_at,omitempty"`
}

// DeliveryAttempt is one post of a run's end to its webhook.
type DeliveryAttempt struct {
	At time.Time `json:"at"`
	// Status is the HTTP status of the answer; absent when none came.
	Status int `json:"status,omitempty"`
	// Error says why the attempt failed; absent for one that succeeded.
	Error string `json:"error,omitempty"`
}

// TypeRunEnded is the type of a [WebhookEvent].
const TypeRunEnded = "run.ended"

// WebhookEvent is the body a webhook is posted: the run as it ended, without
// its record. It is signed (header Agentkit-Signature; see package
// [github.com/bsamanvith/agentkit/pkg/webhook]), and Agentkit-Webhook-Id,
// the run's ID, is the same on every attempt: a receiver keeps the first.
type WebhookEvent struct {
	V    int    `json:"v"`
	Type string `json:"type"`
	Run  Run    `json:"run"`
}

// WebhookSecret is the body of GET /v1/webhooks/secret: the secret the
// tenant's webhooks are signed with, "whsec_" and base64url.
type WebhookSecret struct {
	V      int    `json:"v"`
	Secret string `json:"secret"`
}
