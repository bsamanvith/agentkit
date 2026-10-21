---
id: T067
title: "A2A server: Agent Cards, message and task methods over our runs, streaming"
epic: E13
status: review
depends_on: [T062]
---

# T067: A2A server

## Context

Other agent systems (and orchestrators) talk A2A. Serving each of our agents as an A2A agent makes them usable from any of them. Decisions: [E13](E13-agent-protocols.md) (mapping: task = run, contextId = conversation; library choice).

## Scope

- Read the A2A v1.0 spec (a2a-protocol.org); evaluate `a2aproject/a2a-go` (record the decision and why).
- An `a2a` component: per agent, an Agent Card (name, description, skills from the agent's tools and skills, capabilities: streaming yes, push notifications no for now, security schemes from E07) at `/a2a/{agent}/.well-known/agent-card.json`, plus a directory at `/.well-known/agent-card.json` when one agent is the default.
- Methods (JSON-RPC binding first, HTTP+JSON if cheap): `SendMessage`, `SendStreamingMessage` (SSE from our run events), `GetTask`, `ListTasks`, `CancelTask`, `SubscribeToTask`. Task states from run statuses; `input-required` only once the runner (T030) can suspend runs, until then not advertised.
- Text, data and file parts mapped to our messages (files: refused with the spec's error until supported).

## References

- [A2A specification v1.0](https://a2a-protocol.org/latest/specification/) (Agent Card, methods, task states, bindings, errors)
- [a2aproject/a2a-go](https://github.com/a2aproject/a2a-go) (official Go SDK; evaluate first)
- [A2UI A2A extension](https://a2ui.org/) (surfaces in A2A messages)

## Acceptance criteria

- [x] The spec's examples (requests and responses) pass as golden tests; an A2A client (the SDK's, or our T068 client) completes a streamed task against our agent with a fake model.
- [x] A task started over A2A appears in `GET /v1/conversations` and its run in `GET /v1/runs/{id}`.
- [x] Cancel maps to our run cancel; unknown task is `TaskNotFoundError`; a message to a terminal task is `UnsupportedOperationError`.
- [x] Auth: with T040, A2A paths require credentials and respect tenants.
- [ ] Live (`-tags live`): real Claude answers an A2A `SendStreamingMessage`. Mutation list recorded.

## Notes

Status `review`: everything but the live criterion is done and tested. The
live test (`pkg/a2a/live_test.go`, `TestLiveStreamingMessage`) is written
but has not run: there is no model key in the session that built it. Run
`make test-live` (or `go test -tags live -run TestLive ./pkg/a2a`) with
`ANTHROPIC_API_KEY` set to tick it.

**Spec.** A2A v1.0.0: `docs/specification.md` and `specification/a2a.proto`
at tag `v1.0.0` of github.com/a2aproject/A2A (the document still carries a
"Release Candidate v1.0" heading). JSON is ProtoJSON (camelCase fields,
enums by their proto names: `TASK_STATE_WORKING`, `ROLE_USER`). Bindings:
JSON-RPC 2.0 and HTTP+JSON; no gRPC.

**SDK decision.** `a2aproject/a2a-go/v2` v2.7.0 was evaluated. Its server
(`a2asrv`) brings its own task store, event queue and executor model, which
would keep a second copy of what our runs, run log and leases already are,
and links gRPC and protobuf. So the server is about 2,200 lines of `net/http` on
the agent API's client, with no new dependency in any binary. The SDK's
client (`a2aclient`, which pulls only uuid and x/mod) is a test dependency:
`TestSDKClientCompletesAStreamedTask` drives a streamed task with it over
both bindings. Adding it to go.mod raised `golang.org/x/text` (indirect) from
v0.29.0 to v0.41.0 (no size change). The server adds 172 KB to the agentkit
binary (180 KB local, 233 KB runner). local is projected about 17 KB over its
28 MB budget; the budget is unchanged, for the owner to decide (docs/SIZES.md).

**How it is built.** `pkg/a2a` calls the agent API in process: an
`agentclient.Client` over an `http.RoundTripper` that hands the request to
the API's handler. Only the A2A request's `Authorization` and `X-Request-Id`
go along, so the API authenticates, scopes, picks the tenant and filters
agents exactly as on `/v1`; the server never reads, keeps or forwards the
credential elsewhere (bearer keys and JWTs; the API has no cookie sessions).
A 401's `WWW-Authenticate` challenge comes back to the A2A client. Every run
started over A2A is followed to its end in the background (the API stops an
interactive run when its user goes away), so a task outlives its streams.
`AGENTKIT_COMPONENTS=agent,a2a` serves it (`cmd/agentkit/a2a.go`), with
`AGENTKIT_A2A_URL` for the origin the cards name.

**Defaults.** Body limit 1 MiB; SSE keep-alive 15 s; page size 50, max 100;
ListTasks scans at most 1,000 runs (`WithMaxListScan`), newest status first;
card `Cache-Control: private, max-age=300` with an ETag; card version is the
build's version; skills are the agent, one per toolset (`toolset:<name>`)
and one per Agent Skill (`skill:<name>`); capabilities streaming yes, push
notifications no, extended card no; security schemes `bearer` whenever the
server takes credentials, plus `oidc` (the issuer's discovery URL) with JWTs.

**Deviations and choices** (each noted in `testdata/spec/examples.json`):
- No `A2A-Version` header is taken as 1.0 (the spec says 0.3); any other
  version but 1.0 is `VersionNotSupportedError`.
- HTTP+JSON errors are AIP-193 objects with a `google.rpc.ErrorInfo`
  (section 11.6); examples 6.4 and 11.6 show RFC 9457 problem documents, so
  their status and reason are checked, not their bodies.
- Cards need a credential, like every other path (the agents a key may use
  differ by key). `GET /.well-known/agent-card.json` is the default agent's.
- A message with a `taskId` is `UnsupportedOperationError` (a run takes no
  more messages); continue a conversation with its `contextId`.
- `history` is not returned (`historyLength` is validated only); the answer
  is the task's one artifact, `answer`.
- `interrupted` (a run whose process died; the API may resume it) shows as
  `TASK_STATE_FAILED`. `suspended` is `TASK_STATE_INPUT_REQUIRED`.
- The agent API's `GET /v1/runs/{id}` can say `done` a moment before the run's
  last event is logged, with no answer yet; A2A shows such a run as still
  working, and the blocking `SendMessage` answers from the run's last event.

**Tests**, one per criterion: `TestSpecExamples` (the spec's examples, golden
in `testdata/golden`) and `TestSDKClientCompletesAStreamedTask`;
`TestTaskIsARunOfTheAgentAPI`; `TestCancelAndTaskErrors`; `TestAuth`
(credentials, tenants, scopes, agent visibility) and `TestE2EServeA2A` (the
binary); `TestLiveStreamingMessage`. Invariants, checked on every stream a
test reads (the rig's cleanup): the task first, one terminal status and it
last, the answer rebuilt from the chunks equal to the run's. Also
`TestTaskOutlivesItsClient`, `TestSubscribeFollowsARunningTask`,
`TestConcurrentTasks`, `TestParts`, `TestJSONRPCEnvelope`, `TestAgentCard`,
`TestServerUsesNoSDKNorTheAgentAPIServer`, `FuzzServe` (seeded with the
examples), `ExampleNew`.

**Mutation self-check.** Each bug planted alone; every one made a test fail:
canceled shown as completed (TestCancelAndTaskErrors, TestSpecExamples);
unknown task not TaskNotFound (TestAuth, TestCancelAndTaskErrors, ...); a
terminal task cancelable (TestCancelAndTaskErrors); a message to a terminal
task not Unsupported (TestCancelAndTaskErrors); the credential not forwarded
(TestAuth and five more); every header forwarded instead of the two
(TestSDKClientCompletesAStreamedTask); no final status in a stream (the
stream invariants, via TestConcurrentTasks); the last chunk not marked
(four tests); ListTasks ignoring its status filter (TestSpecExamples); push
notifications advertised (TestAgentCard, ...); any protocol version accepted
(TestJSONRPCEnvelope, TestSpecExamples); subscribing to a terminal task
(TestCancelAndTaskErrors).

**For T068.** The wire types (`types.go`) and errors (`errors.go`) are the
client's too; `testdata/spec/examples.json` can drive its tests the other
way round.
