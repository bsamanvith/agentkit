package wire

import "time"

// Codes of triggers (T072).
const (
	// CodeTriggerExists ("trigger_exists", HTTP 409): a trigger has that ID.
	CodeTriggerExists = "trigger_exists"
	// CodeStaticTrigger ("static_trigger", HTTP 409): the trigger is from
	// the server's config file; the API pauses and resumes it, and changes
	// nothing else.
	CodeStaticTrigger = "static_trigger"
	// CodeTriggerPaused ("trigger_paused", HTTP 409): a webhook was posted
	// to a paused trigger; nothing was recorded.
	CodeTriggerPaused = "trigger_paused"
	// CodeBadSignature ("bad_signature", HTTP 401): an inbound webhook is
	// not signed by the trigger's secret (Standard Webhooks), or its
	// timestamp is too far from now.
	CodeBadSignature = "bad_signature"
	// CodeReplayed ("replayed", HTTP 409): the trigger already took the
	// delivery with this webhook-id; no run was started.
	CodeReplayed = "replayed"
)

// Firing states.
const (
	FiringPending = "pending" // claimed; its run not started yet
	FiringStarted = "started" // its run was started (run_id)
	FiringSkipped = "skipped" // no run, on purpose (reason): the previous one was still going on
	FiringFailed  = "failed"  // its run could not be started (reason)
)

// TriggerRequest is the body of POST /v1/triggers: a trigger that starts a
// background run of an agent on a prompt, on a schedule or when its webhook
// is called. Give it a schedule or a webhook, not both.
type TriggerRequest struct {
	// ID names the trigger in the tenant: ^[a-zA-Z0-9_-]{1,64}$.
	ID string `json:"id"`
	// Agent runs the firings (empty: the default agent, or the agent of
	// ConversationID).
	Agent string `json:"agent,omitempty"`
	// Prompt is the template of the message each run starts from:
	// {{trigger}}, {{time}} (RFC 3339, in the schedule's zone) and {{date}}
	// are filled in. A webhook's body follows it as a data block, never in
	// it.
	Prompt string `json:"prompt"`
	// ConversationID, if set, is the conversation every firing continues;
	// empty starts a new conversation each time.
	ConversationID string `json:"conversation_id,omitempty"`
	// Schedule is a cron schedule ("0 9 * * 1-5", "@hourly"), optionally
	// after "CRON_TZ=<zone> " (default UTC).
	Schedule string `json:"schedule,omitempty"`
	// Webhook gives the trigger an inbound webhook ([TriggerWebhook]).
	Webhook bool `json:"webhook,omitempty"`
	// Paused creates it paused.
	Paused bool `json:"paused,omitempty"`
}

// TriggerUpdate is the body of PATCH /v1/triggers/{id}: the fields given
// replace the trigger's. A schedule replaces a webhook, and a webhook a
// schedule.
type TriggerUpdate struct {
	// Version, if not 0, is the version the change is to: another is 409
	// conflict.
	Version        int64   `json:"version,omitempty"`
	Agent          *string `json:"agent,omitempty"`
	Prompt         *string `json:"prompt,omitempty"`
	ConversationID *string `json:"conversation_id,omitempty"`
	Schedule       *string `json:"schedule,omitempty"`
	Webhook        *bool   `json:"webhook,omitempty"`
	// RotateWebhook gives the trigger a new webhook URL and secret; the old
	// ones stop working.
	RotateWebhook bool `json:"rotate_webhook,omitempty"`
}

// Trigger is a trigger as the API shows it.
type Trigger struct {
	V              int    `json:"v"`
	ID             string `json:"id"`
	Agent          string `json:"agent,omitempty"`
	Prompt         string `json:"prompt"`
	ConversationID string `json:"conversation_id,omitempty"`
	Schedule       string `json:"schedule,omitempty"`
	// Webhook is the trigger's inbound webhook; absent for a schedule.
	Webhook *TriggerWebhook `json:"webhook,omitempty"`
	Paused  bool            `json:"paused"`
	// Static triggers are from the server's config file.
	Static bool `json:"static,omitempty"`
	// NextFireAt is when the schedule next fires; absent for a webhook or
	// a paused trigger.
	NextFireAt *time.Time `json:"next_fire_at,omitempty"`
	// LastFiring is the newest firing, if any.
	LastFiring *Firing   `json:"last_firing,omitempty"`
	Version    int64     `json:"version"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// TriggerWebhook is where a trigger's webhook is posted, and the secret its
// deliveries are signed with (Standard Webhooks: headers webhook-id,
// webhook-timestamp and webhook-signature; "whsec_" and base64). The secret
// is shown with a single trigger, not in lists.
type TriggerWebhook struct {
	// Path is the URL's path on this server: POST it.
	Path   string `json:"path"`
	Secret string `json:"secret,omitempty"`
}

// TriggerList is the body of GET /v1/triggers: the tenant's triggers,
// sorted by ID.
type TriggerList struct {
	V        int       `json:"v"`
	Triggers []Trigger `json:"triggers"`
}

// Firing is one time a trigger fired, whatever became of it.
type Firing struct {
	// ID is the schedule's due time ("at_...") or the webhook's webhook-id.
	ID string `json:"id"`
	// ScheduledFor is the schedule's time it is for; absent for a webhook.
	ScheduledFor *time.Time `json:"scheduled_for,omitempty"`
	At           time.Time  `json:"at"`
	State        string     `json:"state"`
	RunID        string     `json:"run_id,omitempty"`
	Reason       string     `json:"reason,omitempty"`
	EndedAt      *time.Time `json:"ended_at,omitempty"`
}

// FiringList is the body of GET /v1/triggers/{id}/firings: newest first.
type FiringList struct {
	V       int      `json:"v"`
	Firings []Firing `json:"firings"`
}

// HookAccepted is the answer to an inbound webhook (POST /v1/hooks/{id})
// that the trigger took: 202 with the run it started (Location), or 200 with
// a firing that was skipped.
type HookAccepted struct {
	V       int    `json:"v"`
	Trigger string `json:"trigger"`
	Firing  Firing `json:"firing"`
}
