// Package agentapi serves agents over HTTP: a client posts a prompt, streams
// back what the harness does as Server-Sent Events, and continues the
// conversation; it lists the agents, the caller's conversations and the
// toolsets. [Server] is the "agent" [component.Component] of
// `agentkit serve`; it runs one [harness.Harness] per agent through a
// [runner.Runner] on the [store] [WithStore] gives, and imports no model
// adapter and no store backend (cmd/agentkit wires the models, the tools and
// the store).
//
// The API's reference, with request and response schemas, is
// docs/api/openapi.yaml; a test checks it against the server.
//
// # Endpoints
//
// All under /v1. With [WithToken], every request needs
// "Authorization: Bearer <token>" (401 otherwise, on every path).
//
//	POST   /v1/runs                      {"message", "conversation_id"?, "agent"?}
//	GET    /v1/runs                      ?conversation_id=&status=&limit=&cursor=: runs, newest first
//	GET    /v1/runs/{id}                 a run: status, answer, usage, record
//	GET    /v1/runs/{id}/events          its events, after Last-Event-ID; then, while it goes on, the new ones
//	POST   /v1/runs/{id}/stop            interrupt it now, keeping its completed turns
//	POST   /v1/runs/{id}/resume          continue an interrupted or failed run: its events from there
//	POST   /v1/runs/{id}/cancel          end it for good
//	POST   /v1/runs/{id}/decisions       {"path", "approved", "reason"?}: answer a flow run's approval
//	POST   /v1/conversations             {"agent"?}: 201, an empty conversation
//	GET    /v1/conversations             ?agent=&limit=&cursor=: the caller's conversations
//	GET    /v1/conversations/{id}        a conversation's messages
//	DELETE /v1/conversations/{id}        204; the conversation and its runs are gone
//	GET    /v1/agents                    the agents: model, toolsets and tool names
//	GET    /v1/agents/{name}             one agent, with its system prompt
//	GET    /v1/toolsets                  status, skip reason, MCP path, credential status
//	GET    /v1/toolsets/{name}/tools     the toolset's tool definitions
//
// A conversation belongs to one agent: the request that creates it names the
// agent ("agent"), or gets the default one (the only agent, else the one named
// [DefaultAgent]). A run that continues it may leave "agent" out; naming
// another agent is 409 agent_mismatch.
//
// A conversation is created either by a run without "conversation_id", or
// empty by POST /v1/conversations, for a client that needs the ID first (a
// page addressed by its conversation, before anything is sent). An empty
// conversation is "idle", has no messages and an empty title until its first
// run; for retention it was used when it was created.
//
// GET /v1/conversations lists the conversations most recently started first,
// in pages of ?limit= (default [DefaultListLimit], at most [MaxListLimit]).
// A page's next_cursor, passed as ?cursor=, fetches the next one. Pages
// follow the order of creation, so conversations started while a client
// pages come before its first page: it sees every older one exactly once.
// With [WithTenant], a caller sees only its tenant's conversations and runs.
//
// The toolsets endpoints describe what [WithToolsets] gives: never a
// credential's value, only its status and the variable it is read from.
// POST /v1/runs starts a run in a new conversation, or continues the given
// one. It answers with a text/event-stream of [Event]s by default, or, with
// "Accept: application/json", waits and answers with the [Run]. Either way the
// response has status 200 once the run has started; whether it answered is in
// the last event (run_done or run_error) or the run's status. The run and
// conversation IDs are in the run_start event and in the Agentkit-Run-Id and
// Agentkit-Conversation-Id headers.
//
// Errors before a run starts have an [ErrorResponse] body; a 409 about a run
// names it in "run_id":
//
//	400 invalid_request           malformed body, unknown field, empty message, bad
//	                              limit or cursor, no agent named when there is no default
//	401 unauthorized              missing or wrong token
//	404 not_found                 unknown conversation, run, agent, toolset or endpoint
//	409 agent_mismatch            the conversation belongs to another agent
//	409 conversation_busy         a run of the conversation is going on (also DELETE); run_id names it
//	409 run_final                 the run is done, canceled or rejected: it cannot be resumed or decided
//	409 run_leased                another process runs the run
//	409 awaiting_decision         the run waits for a decision, not a resume
//	409 not_pending               the run waits for no decision at that path
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
// sends the model that history unchanged. One run per conversation at a time.
//
// A run started by POST /v1/runs stops with its user ([runner.Interactive]).
// If its last client goes away, the run goes on for the reconnect grace
// ([DefaultReconnectGrace], [WithReconnectGrace]): a client that follows it
// again with GET /v1/runs/{id}/events within it sees the run to its end.
// Past the grace, or on POST /v1/runs/{id}/stop, the run is interrupted: no
// further model or tool calls, and its completed turns are kept.
// POST /v1/runs/{id}/resume continues it (so does an empty message to its
// conversation); a new message starts a new run instead, which cancels it.
//
// A run's events have one seq order. A resumed run's events go on from the
// interruption: the run_error that ended the stopped stream is replaced, at
// its seq, by the continuation's next event.
//
// # Store
//
// Runs and conversations are stored, so they outlive the process and any
// replica on the same store reads them; a run another process executes is
// read from what was stored. A process that dies leaves its runs to their
// leases: a chat run whose lease ran out reads as interrupted, and only its
// user resumes it. Runs started without a user (background work) are resumed
// by the runner's recovery loop, which `agentkit serve` runs. The server
// keeps the events of recent runs in memory to stream them, within
// [WithRetention]; older ones are replayed from the store.
//
// # Shutdown
//
// The runtime drains in-flight requests, and a run is a request, so a
// running stream keeps going during the drain. With [WithDrain], runs still
// going shortly before the deadline are ended with a run_error of code
// "shutdown", which their clients receive; [Server.Shutdown] ends any left
// and reports the runs it cut ([ErrRunsCutOff]).
package agentapi
