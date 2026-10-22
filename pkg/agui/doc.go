// Package agui serves the agents of the agent API (package agentapi) to
// AG-UI frontends: the Agent-User Interaction protocol of CopilotKit,
// version 1.0 (@ag-ui/core and @ag-ui/client 1.0.2), so that an AG-UI client
// such as the reference HttpAgent, or CopilotKit's React components, can run
// our agents, follow their tool calls, draw their A2UI surfaces and answer
// their questions and approvals.
//
// # Endpoint
//
//	POST /agui/{agent}   a RunAgentInput; the answer is the run's AG-UI
//	                     events as Server-Sent Events (text/event-stream)
//
// A request the server cannot take at all is an HTTP error with the agent
// API's error body: a body that is not a RunAgentInput (400), a credential
// the API refuses (401, or 403 without agent:read) and an agent the caller
// may not use (404). Anything else is a run: RUN_STARTED first, then exactly
// one RUN_FINISHED or RUN_ERROR, last; a credential without agent:run is a
// RUN_ERROR with code "forbidden". A client that goes away ends the stream
// and leaves the run to finish, as a /v1 client that stops reading does.
//
// # Threads and runs
//
// An AG-UI run is a run of the agent API, and a thread its conversation.
// AG-UI clients make up their own threadId and runId, which the server
// echoes: RUN_STARTED's metadata["agentkit"] names the conversation and run
// (GET /v1/conversations/{id}, GET /v1/runs/{id}), which the console lists
// like any other. A request continues the conversation its threadId names,
// if it is one of the agent's; otherwise the conversation of the run behind
// the last message the server gave the client (every message ID the server
// makes starts with its run's ID); otherwise it starts one. The conversation
// is the agent API's: the agent reads the request's last message, which must
// be the user's, and not the history the client sends with it.
//
// # Events
//
// A turn's text is one assistant message (TEXT_MESSAGE_START, _CONTENT,
// _END), with the message ID {run}/t{turn}; each tool call is
// TOOL_CALL_START (its parent the turn's message), TOOL_CALL_ARGS with its
// whole input and TOOL_CALL_END once it runs, then TOOL_CALL_RESULT with what
// the model is shown. The A2UI messages of a ui_render call (the agent API's
// ui events) are an ACTIVITY_SNAPSHOT per surface, of activity type
// "a2ui-surface", whose content is {"a2ui_operations": [message, ...]}: the
// shape of AG-UI's A2UI middleware, which CopilotKit's A2UI renderer draws.
// A run that answers ends with RUN_FINISHED, outcome success and the answer
// as its result; one canceled, outcome cancelled; one that fails, RUN_ERROR
// with the agent API's error code. A retry or rewind that drops text already
// streamed is followed by a MESSAGES_SNAPSHOT of the conversation as it
// stands, before the run's end.
//
// # Interrupts
//
// A run whose tool calls wait for people (an ask_user question, a call the
// tool policy holds for approval) ends with RUN_FINISHED, outcome interrupt:
// an approval is a "tool_call" interrupt whose answer is {"approved": bool,
// "reason": "..."}, a question an "input_required" one whose answer is
// {"text": "..."}. The next request on the thread resumes it with
// RunAgentInput.resume, which must answer every interrupt: the server
// answers them (POST /v1/runs/{id}/input) and streams the rest of the same
// agent API run, the answered calls' TOOL_CALL_RESULT first. A cancelled
// approval is a denial; a cancelled question cancels the run.
//
// # Actions
//
// An action on an A2UI surface comes as forwardedProps.a2uiAction.userAction
// ({name, surfaceId, sourceComponentId, context}), as CopilotKit's renderer
// sends it, and becomes the agent API's ui_action on the thread's
// conversation.
//
// # Not supported
//
// The agent's tools run on the server: a request's tools (frontend tools)
// are not offered to it, and a trailing tool message is refused. State and
// context are not given to the agent, so no STATE_* event is sent; nor are
// media parts (only text). The server sends no reasoning events.
//
// # Auth
//
// The server calls the agent API in process, handing each call to the API's
// handler with the request's Authorization header, so AG-UI requests need
// the same credentials as /v1, act in the credential's tenant, need its
// scopes (agent:read and agent:run) and see only the agents it may use. The
// server never reads or keeps the credential, and it never leaves the
// process.
package agui
