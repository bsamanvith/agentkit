---
id: T036
title: "agent component v0: runs over HTTP + SSE, in memory"
epic: E09
status: done
depends_on: [T003, T021, T022, T026]
---

# T036: Agent API v0

## Context

The first API to run an agent: a client posts a prompt and streams back what the harness does. Decisions in [E09](E09-agent-api.md). Everything it needs exists: `pkg/component` and `agentkit serve` (T003), the Anthropic adapter (T021), the harness (T022), toolsets (T026).

## Scope

- An `agent` component in `agentkit serve`, selected with `AGENTKIT_COMPONENTS=agent` (alone or with `mcp-server`), configured in the same config file under `agent`:
  - model: provider `anthropic`, model ID (default `claude-opus-5-5`), effort and output limit;
  - system prompt;
  - the toolsets the agent may use (by name, built with `toolset.Build`, merged into one registry in process), with optional `<toolset>__` prefixing;
  - limits: max turns, max tokens, max concurrent runs (503 with `Retry-After` past it), run timeout.
- Endpoints (base `/v1`):
  - `POST /v1/runs` `{"message": "...", "conversation_id": "..."?}`: starts a run. Returns `text/event-stream` of versioned events (`run_start` carrying the run and conversation IDs, `text_delta`, `tool_call`, `tool_result`, `turn_done` with usage and timings, then `run_done` with the final answer or `run_error` with a typed error). With `Accept: application/json` it waits and returns the final result instead.
  - `GET /v1/runs/{id}`: status and result of a recent run (in memory, bounded, oldest evicted).
  - `GET /v1/conversations/{id}`: the conversation's messages (in memory).
- A client disconnecting cancels the run (the harness's context), and the transcript keeps its completed turns.
- Conversations and runs live in memory: lost on restart, bounded by count and age. Documented clearly; T037 replaces this with the runner.
- Auth: if `AGENTKIT_API_TOKEN` is set, every `/v1` request needs `Authorization: Bearer <token>`; the component refuses to start on a non-loopback address without it.
- JSON wire forms for harness `Event` and `Record` (T022 deferred): defined here, versioned, golden-tested.

## Out of scope

- Persistence, resume, approvals, cancel by ID, listing runs (T037 on the runner).
- Real auth and tenants (E07).
- Other providers (E08).

## Acceptance criteria

Tests are in `pkg/agentapi` unless named otherwise. Every test in `pkg/agentapi` goes through one rig (`helpers_test.go`) whose `check` runs after the test (see Invariants below).

- [x] Event JSON for every event kind matches golden files; unknown fields are additive (a client ignoring them still parses). — `TestEventJSONGolden` (`testdata/tool_run.sse` has `run_start`, `text_delta`, `tool_call`, `tool_result`, `turn_done`, `run_done`, an opaque thinking block, usage with cache tokens; `testdata/refusal.sse` has `run_error`; `tool_run.run.json` and `tool_run.conversation.json` are the run, with the `Record` wire form, and the conversation), `TestEventJSONIsAdditive` (every golden event, with unknown fields added at the top and inside `record`, `error`, `usage`, parses to the same `Event`)
- [x] A run streams events in order and ends with exactly one `run_done` or `run_error`; the concatenated `text_delta`s equal the final answer. Checked by an invariant helper after every test (§2.6). — `checkStream` in `rig.check`, after every test; `TestRunStreamsEventsInOrder` (text and two tool calls in one turn, then an answer), `TestJSONResponse` (the same run as JSON); `cmd/agentkit.checkAgentStream` on every stream of the replay e2e and live tests
- [x] A follow-up with `conversation_id` continues the same conversation: the model's request contains the earlier turns unchanged. — `TestFollowUpContinuesTheConversation` (three runs, each request is the previous request plus its reply plus the new message, compared as JSON); `rig.check` (every model request is a prefix of its conversation); `cmd/agentkit.TestE2EServeAgent` (`checkConversation`: byte for byte across the recorded run and follow-up)
- [x] The agent can call tools from two configured toolsets; a name clash fails startup unless prefixing is on. — `TestToolsFromTwoToolsets` (`MergeTools`: clash → `ErrToolNameClash`; with prefixes `a__echo` and `b__echo` each reach their own toolset); `cmd/agentkit.TestServeRejectsBadSetup/agent:_tool_name_clash`; `cmd/agentkit.TestE2EServeAgent` (`demo-b__echo`, then `demo-a__calculate`, against recorded Claude responses)
- [x] Client disconnect cancels the run: no further model or tool calls after it (driven from the client side, no timing). — `TestClientDisconnectCancelsTheRun/mid_tool` (the client goes away when it sees `tool_call`; the tool sees its context end, the second scripted reply is never asked for, the conversation keeps the prompt) and `/between_turns` (the client goes away after `turn_done` 1; the conversation keeps turn 1)
- [x] Concurrency limit: past it, 503 with `Retry-After`; concurrent runs on different conversations do not interfere (`-race`). — `TestConcurrencyLimit` (2 runs held, the third is 503 `over_capacity` with `Retry-After` and reaches no model; once they end, a new run goes through), `TestConcurrentConversationsDoNotInterfere` (8 conversations × 3 rounds at once, each holds exactly its own messages)
- [x] With `AGENTKIT_API_TOKEN` set, a missing or wrong token is 401 on every `/v1` path; on a non-loopback address without a token, startup fails. — `TestTokenRequiredOnEveryPath` (7 paths and methods, including unknown ones × missing, wrong, prefix, other scheme, no scheme; no model call); `cmd/agentkit.TestServeRejectsBadSetup/agent:_no_token_off_loopback`, `/agent:_no_token_on_all_ifs`, `/agent:_short_token`; `cmd/agentkit.TestE2EServeAgent` (401 without the token)
- [x] Replay e2e (the built binary, like T024/T003): `agentkit serve` with `agent` + `mcp-server`, a prompt that needs a tool, against recorded Claude responses; `/readyz` includes the agent component. — `cmd/agentkit.TestE2EServeAgent` (fixtures `testdata/e2e/serve-agent/01..05.http`, recorded 2026-10-11 from Claude Opus 5.5 at effort low; also MCP on the same process, and SIGTERM while a stream is held: it finishes with `run_done`, exit 0)
- [x] `-tags live`: the same against the real API. — `cmd/agentkit.TestLiveServeAgent`, passed 2026-10-11 (below)
- [x] Mutation list recorded (at least: events out of order, a missing terminal event, conversation history not passed on, disconnect not cancelling, token check skipped on one path, the concurrency limit ignored). — below, no survivors

Beyond the criteria (behaviors a caller handles): `TestResumeInterruptedConversation`, `TestRequestErrors` (400, 404, 409, 413), `TestRunErrorsAreTyped` (each error code, run timeout), `TestRetention`, `TestDrainEndsRunsWithShutdown`, `TestShutdownAfterCleanDrain`, `TestShutdownThroughTheRuntime` (the agent in a real `component.Runtime`: a stream during the drain finishes, or is ended at the drain deadline with `run_error` `shutdown` and `Serve` reports `ErrRunsCutOff`), `FuzzPostRun`, `Example`.

## Notes

### Files

- **`pkg/agentapi`** (new; reusable, so T037 keeps the handlers and swaps what is behind them): `doc.go` (the API reference), `wire.go` (wire types and the conversions from `harness`), `server.go` (`Server`, the `agent` component), `options.go`, `sse.go`, `tools.go` (`MergeTools`); tests `helpers_test.go` (rig and invariants), `server_test.go`, `wire_test.go` (golden), `fuzz_test.go`, `example_test.go`; `testdata/` (golden files, a fuzz seed).
  - Imports `harness`, `llm`, `tool`, `clock`, `component`, never an adapter. Its tests also use `toolset` (the demo toolsets) and `llmtest`.
- **`cmd/agentkit`**: `agent.go` (the `agent` section of the config, `newAgent`, `isLoopback`); `serve.go` (`agent` in `knownComponents` and in the per-name `switch`; toolsets now built once by `buildToolsets` and shared by both components); `mcpserver.go` (`newMCPServer` takes the built toolsets instead of building them); tests `agent_e2e_test.go`, new cases in `serve_test.go`, `TestLiveServeAgent` in `live_test.go`; `serve_e2e_test.go` (`startServe` takes extra env) and `e2e_test.go` (`replayAPI` can hold request N until released) gained one small option each; fixtures `testdata/e2e/serve-agent/`.
- **`internal/archtest/rules.go`**: a `ModuleImports` entry for `pkg/agentapi` (the module packages it may import: `harness`, `llm`, `tool`, `toolset`, `component`, `clock`, and the fakes in tests), so importing an adapter such as `pkg/llm/anthropic` fails the arch test. The rule kind arrived with T028 while this task was in progress.
- **Not changed:** `pkg/harness`, `pkg/llm`, `pkg/toolset`, `pkg/component`. No new third-party dependency.

### API reference (v1 wire, v0 server)

Base `/v1`, mounted at `/v1/` on the `agentkit serve` port.

| Request | Response |
|---|---|
| `POST /v1/runs` `{"message": "...", "conversation_id": "..."?}` | 200 `text/event-stream` of events (default), or 200 `application/json` `Run` with `Accept: application/json`. Headers `Agentkit-Run-Id`, `Agentkit-Conversation-Id`. Errors before the run starts: below. |
| `GET /v1/runs/{id}` | 200 `Run`; 404 when unknown or evicted |
| `GET /v1/conversations/{id}` | 200 `Conversation`; 404 when unknown or evicted |

- A run that has started always answers 200: whether it answered is in the last event (`run_done` / `run_error`) or the run's `status` (`running`, `done`, `failed`).
- `message` empty is allowed only to **resume** an interrupted conversation (status `interrupted`: its last run stopped after the prompt or after tool results). A new message to an interrupted conversation is 409 `conversation_interrupted`. One run per conversation at a time (409 `conversation_busy`).
- Body decoding is strict: unknown fields and trailing data are 400. (Go's decoder matches keys case-insensitively, so `"MESSAGE"` is accepted; the fuzz seed keeps that case.)
- HTTP errors: `{"v":1,"error":{"code","message","retryable"?}}`, codes `invalid_request` 400, `unauthorized` 401 (with `WWW-Authenticate: Bearer`), `not_found` 404, `conversation_busy` / `conversation_interrupted` 409, `too_large` 413, `over_capacity` / `shutting_down` 503 with `Retry-After: 2`.

**Events** (SSE `id: <seq>`, `event: <type>`, `data: <JSON>`; `: keep-alive` comments every 15 s). Header of every event: `v` (1), `type`, `seq` (0, 1, … no gaps), `run_id`, `at`.

| type | fields |
|---|---|
| `run_start` | `conversation_id`, `prompt` `{at, message}` (absent on a resume) |
| `text_delta` | `turn`, `block`, `text` |
| `tool_call` | `turn`, `call`, `tool_use_id`, `name`, `input` (JSON) |
| `tool_result` | `turn`, `call`, `tool_use_id`, `content`, `is_error`, `duration_ms` |
| `turn_done` | `turn`, `record` = `TurnRecord` `{turn, messages [{at, message}], requested_at, first_event_at, done_at, usage, stop, tools [{tool_use_id, name, started_at, duration_ms, is_error}]}` |
| `run_done` | `answer` (text of the final message), `final` (the message), `usage` (run totals), `turns` (model calls) |
| `run_error` | `error` `{code, message, retryable?, category?, explanation?}`, `usage`, `turns` |

Messages use `llm.Message`'s JSON (`{"role", "content": [{"type": "text"|"tool_use"|"tool_result"|"opaque", …}]}`; opaque blocks carry their provider's data as a string). `run_error` codes: `budget_exceeded`, `max_tokens`, `refused` (+ `category`, `explanation`), `unexpected_stop`, `cancelled` (client went away), `timeout`, `shutdown`, `rate_limited` and `overloaded` (retryable), `model_auth`, `context_too_long`, `model_rejected`, `internal` (details only in the log). `Run`: `{v, id, conversation_id, status, created_at, finished_at?, answer?, usage, turns, error?, record?}` (`record` is the `harness.Record` wire form: `started_at`, `finished_at`, `prompt`, `turns`). `Conversation`: `{v, id, status (idle|running|interrupted), created_at, updated_at, messages [{at, message}]}`.

### `curl` session (real Claude, 2026-10-11)

```sh
AGENTKIT_COMPONENTS=agent,mcp-server AGENTKIT_ADDR=127.0.0.1:8099 AGENTKIT_API_TOKEN=dev-token-0123456789 \
ANTHROPIC_API_KEY=... agentkit serve -config config.json   # config: {"mcp":{"toolsets":[{"name":"demo","type":"demo"}]},"agent":{"model":{"effort":"low"},"toolsets":["demo"]}}

$ curl -s localhost:8099/readyz
{"components":[{"name":"agent","status":"ready"},{"name":"mcp-server","status":"ready"}],"status":"ready"}

$ curl -sN localhost:8099/v1/runs -H 'Authorization: Bearer dev-token-0123456789' \
    -d '{"message": "What is 1234 times 5678? Use the calculate tool."}'
id: 0
event: run_start
data: {"v":1,"type":"run_start","seq":0,"run_id":"run_poyrf…","at":"…","conversation_id":"conv_pzxry…","prompt":{…}}

id: 1
event: tool_call
data: {"v":1,"type":"tool_call",…,"turn":1,"call":0,"tool_use_id":"toolu_01Gi…","name":"calculate","input":{"op":"mul","a":1234,"b":5678}}

id: 2
event: tool_result
data: {…,"content":"{\"result\":7006652}","is_error":false,"duration_ms":0.13}

id: 3
event: turn_done          … then text_delta "1234 × 5678 ", "= **7,006,652**", turn_done 2, and
id: 7
event: run_done
data: {…,"answer":"1234 × 5678 = **7,006,652**","final":{…},"usage":{…},"turns":2}

$ curl -s localhost:8099/v1/runs -H 'Authorization: Bearer dev-token-0123456789' -H 'Accept: application/json' \
    -d '{"message": "Now divide that by 2.", "conversation_id": "conv_pzxry…"}'
{"v":1,"id":"run_…","conversation_id":"conv_pzxry…","status":"done","answer":"7,006,652 ÷ 2 = **3,503,326**. It divides evenly, with no remainder.","usage":{"input_tokens":6,"output_tokens":138,"cache_read_tokens":1507,"cache_write_tokens":135},"turns":2,…}

$ curl -s localhost:8099/v1/conversations/conv_pzxry… -H 'Authorization: Bearer dev-token-0123456789'
{"v":1,"id":"conv_pzxry…","status":"idle",…,"messages":[8 messages: user, assistant, user (tool result), assistant, user, assistant, user, assistant]}

$ curl -s localhost:8099/v1/runs -d '{"message":"hi"}'          # no token
{"v":1,"error":{"code":"unauthorized","message":"missing or wrong bearer token"}}   (401)
```

On SIGTERM the log shows `"draining runs","component":"agent","active":0,"cut_after":"13.5s"`, then `shutting down`, `stopped`, exit 0.

### Environment and config reference

| Setting | Where | Default | Notes |
|---|---|---|---|
| components | `AGENTKIT_COMPONENTS` | `mcp-server` | add `agent`: `agent` alone or `agent,mcp-server` |
| bearer token | `AGENTKIT_API_TOKEN` | none | at least 16 bytes. **Required when `AGENTKIT_ADDR` is not loopback** (`:8080` and `0.0.0.0:8080` are not), else startup fails. With it, every `/v1` request needs `Authorization: Bearer <token>`. `/healthz`, `/readyz`, `/version` and `/mcp/` stay open (internal). |
| model credentials | `ANTHROPIC_API_KEY` (or `ANTHROPIC_AUTH_TOKEN`, an `ant` profile) | none | the SDK's chain; `ANTHROPIC_BASE_URL` is honored |
| others | `AGENTKIT_ADDR`, `AGENTKIT_CONFIG`, `AGENTKIT_LOG_LEVEL`, `AGENTKIT_SHUTDOWN_TIMEOUT` | as in T003 | the drain cuts runs at 9/10 of the shutdown timeout |

`agent` section of the config file (all optional; unknown fields fail startup):

```json
{
  "mcp": {"toolsets": [{"name": "demo-a", "type": "demo"}, {"name": "demo-b", "type": "demo"}]},
  "agent": {
    "model": {"provider": "anthropic", "id": "claude-opus-5-5", "effort": "low", "max_tokens": 16000},
    "system": "You are a helpful assistant. Use the tools when they help.",
    "toolsets": ["demo-a", "demo-b"],     // names of mcp.toolsets entries; default none
    "prefix_tool_names": true,            // "<toolset>__<tool>"; default false (a clash then fails startup)
    "max_turns": 25,                      // model calls per run
    "max_total_tokens": 0,                // per run; 0 = no limit
    "max_concurrent_runs": 16,            // 503 + Retry-After past it
    "run_timeout": "10m",                 // "0s" = no limit
    "max_runs": 1000, "max_conversations": 1000, "retention": "24h",   // in memory
    "max_body_bytes": 1048576
  }
}
```

(Comments only here; the file is JSON.) Defaults: model `claude-opus-5-5`, effort the adapter's (medium), output limit the adapter's (64000). The agent's toolsets come from `mcp.toolsets`, built once and shared with `mcp-server` when both run; with `agent` alone, only the named ones are built. An optional toolset that fails is skipped and makes `agent` degraded in `/readyz`.

### Decisions

- **`pkg/agentapi`, not `cmd/agentkit`**: the handlers, wire types and in-memory state are testable black box with fakes (`llmtest`, a fake clock), and T037 keeps the handlers. `New` takes a `*harness.Harness`, so the model, tools, system prompt and budgets are the wiring's choice.
- **Wire forms live in `pkg/agentapi`**, not `pkg/harness`: they are converted from harness values (`wire.go`), so the harness is unchanged. `Event` is one flat struct for clients to decode; its `MarshalJSON` writes exactly the fields of its type, zeros included (`"call": 0`, `"is_error": false`). `run_done` does not carry the transcript (`GET /v1/conversations/{id}` does); `turn_done` carries the `TurnRecord` with its messages.
- **The conversation is built from events**: the prompt at `run_start`, each turn's messages at `turn_done`. So it always equals what the model has seen, a failed or cancelled run keeps its completed turns, and nothing depends on the harness's `RunError` arriving (a disconnect does not break the loop; it cancels the run's context and the harness ends it).
- **Resume inside v0**: a run stopped after the prompt or after tool results leaves the conversation `interrupted` (it ends with a user message, which `harness.Validate` does not let a new message follow). Posting an empty message resumes it; a new message gets 409. Without this, such a conversation could never be continued.
- **One run per conversation** (409 `conversation_busy`): two runs would fork the history.
- **Shutdown and streams.** The runtime drains requests and a run is a request, so an in-flight stream keeps going after SIGTERM and finishes normally (e2e: held model answer released after the listener closed → `run_done`, exit 0). `WithDrain(ctx.Done(), 0.9 × AGENTKIT_SHUTDOWN_TIMEOUT)` ends runs still going at that point with `run_error` `shutdown`, which the client receives instead of a cut connection, and lets the drain finish before the runtime's deadline. `Server.Shutdown` returns `ErrRunsCutOff` naming the runs it cut, so the runtime logs it and the process exits 1: an unclean shutdown is reported (`TestShutdownThroughTheRuntime/cut`). During the drain new runs get 503 `shutting_down`. A client that disconnects during the drain is recorded as `shutdown` too (indistinguishable from a closed connection).
- **Turns** in `run_done`/`run_error`/`Run` count model calls (the harness's `Turns`); in a failed run that can include a call whose turn did not complete, which is why `record.turns` can be shorter.
- **Retry-After is a fixed 2 s**; there is no queue (a queue is the runner's job).
- **Write deadline** of one minute per SSE write: a client that stops reading cannot hold a run and its slot for longer; the failed write cancels the run.
- Internal errors show only `internal` and the run ID to the client; the cause is logged.
- **Eviction** happens when a run is admitted: finished runs and idle conversations older than `retention`, then the least recently used past the counts. Running ones are never evicted. Eviction order uses a counter, not timestamps (a 1-in-200 flake with equal times showed it).

### Invariants (`rig.check`, after every test in `pkg/agentapi`)

1. Every stream read to its end: starts with `run_start` (with a conversation ID), `v` 1, seq 0… with no gaps, one run ID, times not decreasing, SSE `id` = seq and `event` = type; turn events in turn order, each `tool_result` after its `tool_call`, every call finished before its `turn_done`; each turn's deltas equal the text of that turn's reply; exactly one `run_done`/`run_error`, last; `run_done`'s answer equals the final message's text and the last turn's deltas, its turns the number of turns and its usage their sum.
2. `GET /v1/runs/{id}` agrees with the stream (status, answer, usage, error code, turns).
3. Every conversation holds exactly the messages its streams announced, in order, and passes `harness.Transcript.Validate`.
4. Every model request ends with a user message and is a prefix of its conversation.

### Mutation check

Each bug planted alone in `pkg/agentapi` or `cmd/agentkit`, then `go test -race -count=1 ./pkg/agentapi/ ./cmd/agentkit/` (the driver is kept out of the repo; it restores each file after the run, and the tree was compared with a backup afterwards). Every plant compiled and failed tests:

| Planted bug | Caught by |
|---|---|
| events out of order: `tool_call` and `tool_result` swapped | `TestRunStreamsEventsInOrder`, `TestEventJSONGolden`, `TestToolsFromTwoToolsets`, `TestFollowUpContinuesTheConversation`, `TestResumeInterruptedConversation`, `TestClientDisconnectCancelsTheRun/mid_tool`, `TestE2EServeAgent` (`checkStream`) |
| events out of order: text deltas sent after their `turn_done` | `checkStream` in 13 tests (`TestRunStreamsEventsInOrder`, `TestConcurrencyLimit`, `TestTokenRequiredOnEveryPath`, `TestShutdownThroughTheRuntime/finishes`, …), `TestEventJSONGolden` |
| missing terminal event | `checkStream` in 13 tests, `TestE2EServeAgent` |
| conversation history not passed on (follow-up without the transcript) | `TestFollowUpContinuesTheConversation`, `TestConcurrentConversationsDoNotInterfere` (`rig.check`: request not a prefix of its conversation), `TestResumeInterruptedConversation`, `TestE2EServeAgent` (`checkConversation`). The driver listed only the last two; a run of this plant alone showed all four. |
| disconnect not cancelling (run context not derived from the request) | `TestClientDisconnectCancelsTheRun/mid_tool`, `/between_turns`, `TestRetention` |
| token check skipped on one path (`/v1/conversations/`) | `TestTokenRequiredOnEveryPath` |
| token compared by prefix | `TestTokenRequiredOnEveryPath` (the "prefix" case) |
| concurrency limit ignored | `TestConcurrencyLimit` |
| slot never released | `TestConcurrencyLimit`, `TestConcurrentConversationsDoNotInterfere` |
| busy conversation not refused (two runs fork the history) | `TestRequestErrors` |
| new message on an interrupted conversation accepted | `TestResumeInterruptedConversation` |
| a failed run's completed turns dropped from the conversation | `TestClientDisconnectCancelsTheRun` (both), `TestResumeInterruptedConversation`, `TestDrainEndsRunsWithShutdown`, `TestRunErrorsAreTyped` (every case, `rig.check`) |
| drain deadline does not end runs | `TestDrainEndsRunsWithShutdown`, `TestShutdownThroughTheRuntime/cut` |
| runs not tied to the server's stop | `TestDrainEndsRunsWithShutdown`, `TestShutdownThroughTheRuntime/cut` |
| runs cut by the shutdown not reported | `TestDrainEndsRunsWithShutdown`, `TestShutdownThroughTheRuntime/cut` |
| shutdown classified as `cancelled` | `TestDrainEndsRunsWithShutdown`, `TestShutdownThroughTheRuntime/cut` |
| run timeout ignored | `TestRunErrorsAreTyped/timeout` |
| unknown body fields accepted | `TestRequestErrors`, `FuzzPostRun` (seeds) |
| a running conversation evicted | `TestRetention` |
| tool name clash not detected | `TestToolsFromTwoToolsets`, `cmd/agentkit.TestServeRejectsBadSetup/agent:_tool_name_clash` |
| a prefixed tool called under its prefixed name | `TestToolsFromTwoToolsets`, `TestE2EServeAgent` |
| `tool_call`'s `"call": 0` omitted from the JSON | `TestEventJSONGolden` |
| cmd: no token needed off loopback | `TestServeRejectsBadSetup/agent:_no_token_off_loopback`, `/agent:_no_token_on_all_ifs` |
| cmd: `:8080` (all interfaces) counted as loopback | `TestServeRejectsBadSetup/agent:_no_token_on_all_ifs` |
| cmd: `prefix_tool_names` ignored | `TestE2EServeAgent` |
| cmd: agent built without its toolsets | `TestE2EServeAgent`, `TestServeRejectsBadSetup/agent:_tool_name_clash` |

Two plants were first written so they did not compile (an unused import, an unused variable) and were rewritten. The first run of "disconnect not cancelling" hung the package until the test-binary timeout: the failed test's `httptest.Server.Close` waited for the handler the plant never cancelled. The rig now shuts the agent down in its cleanup (ending any run), so the plant fails by name; streams also read through a client with a one-minute timeout, so a stream that never ends fails instead of hanging.

### Live result (2026-10-11, Claude Opus 5.5, effort low)

`ANTHROPIC_API_KEY=… go test -tags live -count=1 -run TestLiveServeAgent -v ./cmd/agentkit/`: **passed** in 10.8 s.
- Run 1: `tool_call demo-b__echo` → `{"text":"[b] pineapple"}`, answer "I called demo-b__echo with \"pineapple\" and it returned `{\"text\":\"[b] pineapple\"}`, adding a \"[b] \" prefix." (2 turns; 1943 tokens read from the prompt cache).
- Follow-up in the same conversation: `tool_call demo-a__calculate` → `{"result":469821643}`, answer "demo-a__calculate returned 469,821,643 for 48,271 × 9,733, which is correct." (2 turns; 2102 cache-read tokens: the history was sent unchanged).
- The replay fixtures were recorded in the same session (`AGENTKIT_E2E_RECORD=1`), and the manual `curl` session above also ran against the real API.

Also run: `go test -race -shuffle=on -count=200 ./pkg/agentapi/` (clean, after the eviction fix), `-count=5` on `TestE2EServe*`, `make fuzz FUZZTIME=60s`, `make check`.

### What T037 must keep compatible

- Paths, methods, request body, status codes and error codes above; `Accept: application/json` vs SSE; the `Agentkit-Run-Id`/`Agentkit-Conversation-Id` headers.
- Event wire v1: names, field names and meanings, `seq` from 0 without gaps, exactly one terminal event, `turn_done` carrying the record with its messages, `answer` = last turn's deltas. Changes additive only (new fields, event types, error codes); a breaking change is `"v": 2`.
- `Run` and `Conversation` shapes and statuses, including `interrupted` and resume-by-empty-message (the runner's resume will make this work across restarts too), one run per conversation, 503 + `Retry-After` past the limit, 401 on every `/v1` path with the token.
- What changes: persistence (no more "lost on restart", eviction becomes the store's), new endpoints (cancel by ID, list, approve) added beside these. `pkg/agentapi.Server` can keep its handlers and move state behind an interface the runner implements; `rig.check` and the tests should then run against both.

### For T005 (image) and T006 (Compose)

- Same binary and port as T003: `agentkit serve`, `AGENTKIT_ADDR=:8080`, expose 8080. The agent API is at `/v1/` on that port.
- **`:8080` is not loopback, so the image must get `AGENTKIT_API_TOKEN`** (≥ 16 bytes, a secret, not baked in) when `agent` is listed, else startup fails with a message naming the variable. Clients send `Authorization: Bearer $AGENTKIT_API_TOKEN`.
- `ANTHROPIC_API_KEY` from a secret. `AGENTKIT_COMPONENTS=agent,mcp-server` for a single container; `agent` alone also works (toolsets still come from `mcp.toolsets` in the config).
- Config example for `deploy/local/config/agent.json`: the JSON above with the demo toolsets and `"model": {"effort": "low"}` (cheap for smoke tests).
- Health: `/healthz`, `/readyz` (lists `agent`; degraded if an optional toolset it names was skipped). No auth on them.
- Streams are long-lived HTTP responses: proxies must not buffer (`X-Accel-Buffering: no` is set) and their idle timeout must exceed 15 s (keep-alive comments). Conversations live in one process: with several replicas, route a conversation to the same replica or run one replica until T037.
- Shutdown: keep the orchestrator's grace period above `AGENTKIT_SHUTDOWN_TIMEOUT`; runs still going at 90% of it end with `run_error` `shutdown` and the process exits 1 (reported), otherwise 0.
