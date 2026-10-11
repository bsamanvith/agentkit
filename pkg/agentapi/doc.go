// Package agentapi serves an agent over HTTP: a client posts a prompt, streams
// back what the harness does as Server-Sent Events, and continues the
// conversation. [Server] is the "agent" [component.Component] of
// `agentkit serve`; it wraps one [harness.Harness] and imports no model
// adapter (cmd/agentkit wires the model and the tools).
//
// # Endpoints
//
// All under /v1. With [WithToken], every request needs
// "Authorization: Bearer <token>" (401 otherwise, on every path).
//
//	POST /v1/runs                {"message": "...", "conversation_id": "..."?}
//	GET  /v1/runs/{id}           a recent run: status, answer, usage, record
//	GET  /v1/conversations/{id}  a conversation's messages
//
// POST /v1/runs starts a run in a new conversation, or continues the given
// one. It answers with a text/event-stream of [Event]s by default, or, with
// "Accept: application/json", waits and answers with the [Run]. Either way the
// response has status 200 once the run has started; whether it answered is in
// the last event (run_done or run_error) or the run's status. The run and
// conversation IDs are in the run_start event and in the Agentkit-Run-Id and
// Agentkit-Conversation-Id headers.
//
// Errors before a run starts have an [ErrorResponse] body:
//
//	400 invalid_request           malformed body, unknown field, empty message
//	401 unauthorized              missing or wrong token
//	404 not_found                 unknown conversation, run or endpoint
//	409 conversation_busy         a run of the conversation is going on
//	409 conversation_interrupted  the last run stopped before answering: resume it
//	413 too_large                 body over the limit (WithMaxBodyBytes)
//	503 over_capacity             too many runs at once (WithMaxConcurrentRuns); Retry-After
//	503 shutting_down             the server is draining; Retry-After
//
// # Events
//
// SSE messages are "id: <seq>", "event: <type>" and "data: <JSON>". The JSON
// is versioned ("v": 1) and only grows: clients must ignore unknown fields,
// event types and error codes. A run's events are one run_start, then per
// turn its text_delta, tool_call and tool_result events and one turn_done,
// and last exactly one run_done or run_error. The text_delta events of the
// last turn add up to run_done's answer. Comment lines (": keep-alive") may
// come in between; SSE clients skip them.
//
// # Conversations and disconnects
//
// A conversation holds every message the model has seen, in order: the
// prompt as soon as a run starts and each turn once it completes. A follow-up
// sends the model that history unchanged. If the client goes away, the run is
// cancelled (no further model or tool calls) and the conversation keeps its
// completed turns. A run that stopped between a prompt or tool results and
// the model's answer leaves the conversation "interrupted": post to it with
// an empty message to resume (the model is asked to continue), and only then
// with a new message. One run per conversation at a time.
//
// # Memory only
//
// This version (v0) keeps runs and conversations in memory: they are lost
// when the process stops, and a conversation is only reachable on the replica
// that holds it. Retention is bounded by count and age ([WithRetention]).
// A later version moves the same API onto the persistent runner.
//
// # Shutdown
//
// The runtime drains in-flight requests, and a run is a request, so a
// running stream keeps going during the drain. With [WithDrain], runs still
// going shortly before the deadline are ended with a run_error of code
// "shutdown", which their clients receive; [Server.Shutdown] ends any left
// and reports the runs it cut ([ErrRunsCutOff]).
package agentapi
