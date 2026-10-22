---
id: T069
title: "AG-UI adapter (later)"
epic: E13
status: done
depends_on: [T065]
---

# T069: AG-UI adapter

## Context

AG-UI (CopilotKit) is an event protocol between agent backends and frontends. An adapter lets AG-UI frontends (CopilotKit's React components, for example) use our agents, carrying A2UI surfaces as AG-UI events. Only when such a frontend is wanted; the console uses our own API. Decisions: [E13](E13-agent-protocols.md).

## Scope

- Map our run events to AG-UI events (run started/finished, text message start/content/end, tool call start/args/end/result, state snapshots, A2UI surfaces as the spec's custom or UI events) at `/agui/{agent}`; map AG-UI input (messages, tool results for frontend tools, interrupts) back.
- Verify against the current AG-UI spec and its reference client.

## References

- [AG-UI docs](https://docs.ag-ui.com/) (event types, state, interrupts) and the [AG-UI repo](https://github.com/ag-ui-protocol/ag-ui)
- [A2UI: how it compares and transports](https://a2ui.org/introduction/agent-ui-ecosystem/) (A2UI over AG-UI)

## Acceptance criteria

- [x] The reference AG-UI client completes a run with a tool call and an A2UI surface against our server (fake model).
- [x] Mutation list recorded.

## Notes

Done. `pkg/agui` serves every agent of the agent API at `POST /agui/{agent}`
(the `agui` component, with `agent`); the package doc is the reference.

*The reference client ran.* `TestReferenceClient` runs `@ag-ui/client`
1.0.2's `HttpAgent` (which checks every event with the protocol's zod
schemas and its verifier, and warns about what it strips) behind
`@ag-ui/a2ui-middleware` 0.0.12, as CopilotKit's runtime has it, against the
server over the real agent API on the fake model: a run with an `echo` call
and a `ui_render` surface (the client ends with an `a2ui-surface` activity
message of three A2UI operations), an action on that surface on the same
thread (the same conversation), and an approval that ends on an interrupt
and its resume (the held call runs once, and is not started again). No
warning. Without node or the packages the test skips; `make agui-client`
installs them (`npm ci`, pinned by the lockfile in `testdata/client`) and
sets `AGENTKIT_REQUIRE_AGUI_CLIENT`, so it must run; CI's smoke job runs it.

*Spec version.* AG-UI 1.0 (`@ag-ui/core` 1.0.2, `PROTOCOL_VERSION` "1.0"),
read from the published packages' sources and docs.ag-ui.com: the
RUN_FINISHED `outcome` (success, interrupt with `interrupts`, cancelled),
`RunAgentInput.resume` entries `{interruptId, status, payload}`, interrupt
reasons `tool_call` and `input_required`, ACTIVITY_SNAPSHOT.

*Mapping, and the defaults chosen.*
- A thread is a conversation, an AG-UI run an agent API run. Clients make up
  `threadId` and `runId`, which are echoed; RUN_STARTED's
  `metadata.agentkit` names the conversation and run. A request continues the
  conversation its `threadId` names (if it is this agent's), else the one of
  the run behind the newest of our message IDs in `messages` (at most three
  lookups), else starts one. IDs: `{run}/t{turn}[.gen]` for a turn's
  message, `{run}/r/{call}[.gen]` for a result, `{run}/i/{call}` for an
  interrupt.
- The agent reads only the last message, which must be the user's (text, or
  text parts joined by newlines); the history the client sends is not
  replayed: the conversation is the agent API's.
- Text is one message per turn, closed before any tool event. Tool calls are
  START, ARGS (the whole input), END at once (the agent API reports a call
  when it runs, not as it streams), then RESULT with what the model is shown
  (failures marked `metadata.agentkit.isError`).
- A2UI: the `ui` events of a call, grouped per surface, become one
  ACTIVITY_SNAPSHOT each (`a2ui-surface`, id `a2ui-surface-{call}`, or
  `a2ui-surface-{surface}-{call}` when a call draws several; content
  `{"a2ui_operations": [...]}`, replace true): the A2UI middleware's shape,
  which CopilotKit's renderer draws. Actions come as
  `forwardedProps.a2uiAction.userAction` and become the API's `ui_action`.
- Interrupts: an approval (`tool_call`, payload `{approved, reason}` or a
  bool) and an `ask_user` question (`input_required`, `{text}` or a string,
  with the choices as the response schema). Resume must answer every open
  interrupt of one run; the server answers them and follows the same run from
  where it stopped, so answered calls get only their RESULT. A cancelled
  approval is a denial; a cancelled question cancels the run. `editedArgs`
  is refused (`unsupported`). New input on a thread whose run waits is
  `pending_interrupts`.
- A retry or rewind that drops streamed text bumps the IDs' generation and
  ends with a MESSAGES_SNAPSHOT (the input's messages and the run's as they
  stand); text deltas a follower missed are caught up from `turn_done`.
- Errors: a body that is not a `RunAgentInput` (400), a refused credential
  (401, 403 without `agent:read`) and an unknown agent (404) are HTTP errors
  with the agent API's body; everything else is a run ending in RUN_ERROR
  with the API's code or ours (`invalid_input`, `unsupported`,
  `pending_interrupts`, `unknown_interrupt`).
- Auth: the in-process transport of `pkg/a2a` (copied) hands the request's
  `Authorization` to the API's handler, so `/v1`'s keys, tenants, scopes and
  visibility apply; the server never reads the credential.

*Not supported (deviations).* Frontend tools (a request's `tools`, a
trailing tool message: refused with a message saying so); `state` and
`context` (no STATE_* events); media parts; reasoning events; flow runs
that suspend for a decision (RUN_ERROR `unsupported`). Encoding/json matches
input field names regardless of case (the fuzz corpus has a case of it);
the client's zod schema is stricter, which is harmless.

*Image.* +106 KB on agentkit and +110 KB on local (docs/SIZES.md). Behind a
`noagui` tag, which runner and toolsets carry, and dev and api too, for
their budgets: with the server dev would use about all of its 32 MB (+106
KB, of about 106 KB left after T072) and api would cross its 23 MB (+110
KB, of about 36 KB left). So `agui` runs on the all-in-one and local
images. Whether dev or api should serve AG-UI (a budget raise, or a cut
elsewhere) is the owner's call.

*Tests.* `agui_test.go` over the real agent API on the fake model (runs with
a tool call and a surface, threads, actions, interrupts and resume, cancelled
answers, request errors, failed and cancelled runs, shutdown, options, and
that the package never links the agent API's server, the harness or
`pkg/a2ui`); `fakeapi_test.go` over a scripted fake API (retry, rewind,
missed deltas, several surfaces of one call, a failed call, runs that end
badly); `client_test.go` (the reference client); `FuzzServe`; `ExampleNew`;
`cmd/agentkit` `TestE2EServeAGUI` (the built binary: 401 without a key, a
stream with one, `/readyz`) and `TestAGUINotBuilt` (`-tags noagui`). Every
stream a test reads is checked against the invariants (`invariants_test.go`):
one RUN_STARTED echoing the IDs first and one end last, the fields of each
event type, messages and calls opened and closed in order, results of
started calls, unique IDs, A2UI content that `pkg/a2ui` parses and replaces,
and the client's reduction of the stream to messages.

*Mutation self-check.* Each bug planted alone in a copy, then reverted;
every one fails a test:

| Planted bug | Caught by |
|---|---|
| no TOOL_CALL_END | TestRunWithToolCallAndSurface, TestReferenceClient, and 7 more |
| text not closed before a tool call | TestRunWithToolCallAndSurface |
| answered calls started again on resume | TestInterruptsAndResume, TestReferenceClient |
| no snapshot after a retry or rewind | TestRetryIsSnapshotted, TestRewindIsSnapshotted |
| message generation not bumped on a drop | TestRetryIsSnapshotted, TestRewindIsSnapshotted |
| missed deltas not caught up | TestMissedTextIsCaughtUp |
| catch-up replays the resumed turn's text | TestInterruptsAndResume |
| surfaces of one call share an activity id | TestSurfacesOfOneCall |
| wrong activity type | TestRunWithToolCallAndSurface, TestReferenceClient, and 2 more |
| activity snapshot not `replace` | TestRunWithToolCallAndSurface, TestSurfacesOfOneCall, TestUIAction |
| suspended run not an interrupt outcome | TestInterruptsAndResume, TestCancelledInterrupts, TestReferenceClient |
| failed call not marked | TestFailedCallIsMarked |
| RUN_STARTED not echoing `runId` | 15 tests (the invariants) |
| thread ignores the messages' run IDs | TestThreads, TestUIAction, TestInterruptsAndResume, TestReferenceClient |
| thread takes another agent's conversation | TestThreads |
| resume not checking every interrupt is answered | TestInterruptsAndResume |
| cancelled approval approves | TestCancelledInterrupts |
| cancelled question does not cancel | TestCancelledInterrupts |
| busy not mapped to `pending_interrupts` | TestInterruptsAndResume |
| credential not forwarded | TestRequestErrors, TestReferenceClient, and 7 more |
| text parts joined without a newline | TestRequestErrors |
| an action's context dropped | TestUIAction, TestReferenceClient |

Two planted bugs survived the first round (catch-up replaying the resumed
turn, `replace` false); the `both` reply now has text before its calls and
the invariants check `replace`.

### Review fixes

From the review of PR #92 (each test failed before its fix):

- *Shutdown's deadline* ended a stream with RUN_ERROR `internal` and logged
  "context canceled" as a failure; it is now RUN_ERROR `shutting_down` (the
  run goes on in the agent API), not logged. `TestShutdownEndsStreams`.
- *"." and ".." IDs*: a `threadId` of "." was looked up as
  `GET /v1/conversations/.`, which the API's router cleans and redirects to
  the list of conversations, so the run failed as "a conversation of
  another agent". "." and ".." (as a thread, or as the run of a message or
  interrupt ID) are no longer looked up (`isID`). `TestDotIDs`.
- Tests for checks no test planted a bug in, both of which survived a
  plant: a resume sent to another agent's path (`TestResumeAcrossTenants`,
  which also covers another tenant's key and a read-only key: the run stays
  suspended and its call never runs), and a resume naming two runs
  (`TestResumeOfTwoRuns`). The keep-alive, off in every test before, is
  tested on a fake clock (`TestKeepAlive`: a comment, whole, that a client
  skips; a data line instead is caught). The dead "tenant b's run ID" case
  of `TestRequestErrors` is gone.

Looked at and left: tenant isolation through guessed message IDs (`GetRun`
and `GetConversation` go through the API with the caller's credential, so
another tenant's IDs are 404 and ignored; within a tenant `/v1` lets the
key do the same; run IDs are random); a key without the agent's toolsets
(`GET /v1/agents/{name}` is 404 for it, and every later call is bound to
that agent's name); double and racing resumes (the API's answer is
compare-and-set: the loser gets 409, which is `unknown_interrupt`, and the
call runs once); `DisallowUnknownFields` (AG-UI clients send fields this
server ignores, `parentRunId` among them, so it would refuse real clients;
case-insensitive matching only lets a client spell a field it could send
anyway); `forwardedProps` actions (the API checks them with
`a2ui.ActionText`, and an action is no more than the user's own message);
SSE escaping (each event is one `json.Marshal` line, which has no raw
newline). The reference client is pinned by `package-lock.json` with
integrity hashes and installed with `--ignore-scripts`; `npm audit` on it
finds nothing, but CI does not audit it (`--no-audit`) and the web
workspace's `pnpm audit` does not cover it.

### For later tasks

- *Frontend tools* (a request's `tools`, run in the browser): would need a
  tool kind whose call suspends the run until a tool message answers it, much
  like an interrupt; the resume path is the place.
- *State* (STATE_SNAPSHOT/DELTA): an agent with shared state would map it
  here; nothing in the agent API has state yet.
- *Edited arguments* on approval: needs the agent API to take edited input
  in an answer.
- *Shared in-process transport*: `pkg/a2a` and `pkg/agui` hold the same
  copy (`inprocess.go`); a small internal package would hold one.
- *dev and api images*: add `agui` to them if a deployment should serve
  AG-UI (see Image).
