// Package a2a serves the agents of the agent API (package agentapi) as A2A
// agents: the Agent2Agent protocol, version 1.0 (a2a-protocol.org, tag
// v1.0.0 of github.com/a2aproject/A2A), so that other agent systems and
// orchestrators can discover them, send them messages and follow their tasks.
//
// # Endpoints
//
// Each agent is served under /a2a/{agent}:
//
//	GET  /a2a/{agent}/.well-known/agent-card.json   its Agent Card
//	POST /a2a/{agent}                               the JSON-RPC binding
//	POST /a2a/{agent}/message:send                  the HTTP+JSON binding
//	POST /a2a/{agent}/message:stream
//	GET  /a2a/{agent}/tasks                         ?contextId=&status=&pageSize=&pageToken=...
//	GET  /a2a/{agent}/tasks/{id}
//	POST /a2a/{agent}/tasks/{id}:cancel
//	POST /a2a/{agent}/tasks/{id}:subscribe          (GET too)
//	GET  /.well-known/agent-card.json               the default agent's card
//
// The methods are SendMessage, SendStreamingMessage, GetTask, ListTasks,
// CancelTask and SubscribeToTask. Push notifications are not supported
// (PushNotificationNotSupportedError), and there is no extended card.
//
// # Mapping
//
// A task is a run of the agent API and a contextId is a conversation (E13):
// a task started over A2A is a run in GET /v1/runs/{id} and its conversation
// is in GET /v1/conversations, like any other. A message without a contextId
// starts a conversation; one with a contextId starts the conversation's next
// task. A message with a taskId is refused with UnsupportedOperationError: a
// run takes no more messages (when it has ended, start the next task with
// the contextId).
//
// Text parts are the prompt's text and data parts their JSON; file parts are
// refused with ContentTypeNotSupportedError. The answer is the task's one
// artifact, "answer". States: running is working, done completed, canceled
// canceled, rejected rejected, suspended input-required, and failed or
// interrupted failed (the agent API can still resume those).
//
// A task goes on when its client goes away: the server follows every run it
// starts to its end, so SendMessage with returnImmediately and a stream that
// is closed leave the run going. SendStreamingMessage and SubscribeToTask
// stream the task first, then its answer as it is written (each turn's text
// replaces the last; the whole answer comes again as the last chunk), then
// its last status.
//
// # Auth
//
// The server calls the agent API in process, handing each call to the API's
// handler with the A2A request's Authorization header. The API authenticates
// it as it does its own requests, so A2A requests need the same credentials,
// act in the credential's tenant, need its scopes (agent:read for every
// request, which reads its agent and the task's events first; agent:run as
// well to send and cancel) and see only the agents the credential may use. The cards need a credential too. The server never reads or keeps the
// credential, and it never leaves the process.
package a2a
