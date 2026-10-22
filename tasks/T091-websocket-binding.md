---
id: T091
title: "WebSocket binding of the agent API's messages (later)"
epic: E18
status: done
depends_on: [T090]
---

# T091: WebSocket binding

## Context

SSE + HTTP commands cover the console and CLI. Consumer apps may want one connection with many subscriptions (mobile), realtime voice, or a transport that survives networks that buffer SSE. Decisions: [E18](E18-streaming-backend.md) ("WebSocket").

## Scope

- **Endpoint:** `GET /v1/ws` (upgrade). After the client sends a `hello` with its token, the connection carries:
  - `subscribe` and `unsubscribe` (run, task, user streams; resumable with the same IDs as SSE);
  - the same commands as the HTTP endpoints (send, steer, answer, cancel), each with a client `id` and a `result` reply carrying the same status and error codes.
- **Messages:** the same JSON envelope and event objects as SSE, nothing WebSocket-only.
- **Connection health:** heartbeats; backpressure (slow clients get `gap` then are closed); per-connection limits; auth renewal.
- **Clients:** `agentclient` and the `web` packages get a WebSocket transport behind the same interfaces.

## References

- [RFC 6455](https://www.rfc-editor.org/rfc/rfc6455); [coder/websocket](https://github.com/coder/websocket) (maintained, context-aware Go library)
- The OpenAI Realtime API and Phoenix Channels, for command-and-reply framing over one socket

## Acceptance criteria

- [x] **Same results either way:** every command and subscription gives the same results over WebSocket as over HTTP and SSE (one shared test table run on both transports).
- [x] **Resume:** works with the same IDs; slow clients are cut off with a `gap`, not by blocking the server.
- [x] **Mutation list** recorded.

## Notes

Built:

- `pkg/agentapi/wire/ws.go`: the message envelope (`WSMessage`: `type`, `id`, `sub`, and per type `token`, `csrf`, `run_id`, `after`, `body`, `status`, `conversation_id`, `retry_after`, `event`), the types, `WSPath` (`/v1/ws`) and the close codes (4001 unauthorized, 4008 slow).
- `pkg/agentapi/wsapi`: the server. It takes the API's `http.Handler` (agentapi's `Server.Handler()`) and serves each command or subscription as the HTTP request it stands for, through that handler, with the connection's credentials. A result is that response's status and body; a stream's events are the SSE data byte for byte. So auth, scopes, tenants, CSRF, error codes and resume IDs are the HTTP API's by construction, and the package does not import `pkg/agentapi` (`TestBindingDoesNotImportTheServer`).
- `pkg/agentclient/wsclient`: an `http.RoundTripper` for `agentclient`. Runs' streams (`Stream`, `Events`, `Resume`), runs' commands (`Stop`, `Cancel`, `Answer`, `Decide`) and tasks' commands (`SteerTask`, `PauseTask`, `ResumeTask`) go over one connection; every other request goes to a fallback transport. The `Client` is unchanged ("behind the same interfaces"). A gap ends a stream with `wsclient.ErrGap` / `*GapError{After}`. The connection is dialed on first use and dialed again after it is lost or after a gap.
- `cmd/agentkit/ws.go`: the agent component serves the binding at `/v1/ws` beside the API, so `/readyz` still lists the components it did. On shutdown the API drains first, so WebSocket streams get the runs' last events as SSE streams do, and then the connections close. `TestE2EServeWebSocket` covers this. The `nows` tag leaves it out (`ws_none.go`). Runner and toolsets (`noagent`) never link it, and `make deps-check` checks that.

Decisions (defaults, change them where they prove wrong):

- **Mapping:** `hello` → `GET /v1/auth/me`; `subscribe` → `GET /v1/runs/{id}/events` (`after` is the `Last-Event-ID`); `send` → `POST /v1/runs` (streams); `resume` → `POST .../resume` (streams); `stop`, `cancel` → `POST .../stop|cancel`; `answer` → `POST .../input`; `decide` → `POST .../decisions`; `steer` → `POST /v1/tasks/{id}/messages` (`key` is its Idempotency-Key); `pause` → `POST /v1/tasks/{id}/pause`; `resume_task` → `POST /v1/tasks/{id}/resume` (T086's endpoints, merged while this task ran). A task is a run, so its stream is a run stream and its ID is its run's.
- **Hello first:** anything before a successful hello gets a 401 result and the connection closes with 4001, even on an open server. A hello carries `token` (bearer) or `csrf` (for a session cookie sent on the upgrade). Without either, the upgrade request's own `Authorization` and cookies stand. The upgrade needs no credentials (browsers cannot set headers on it); coder/websocket's origin check (RFC 6455) keeps other sites' pages from using a session cookie (`WithOriginPatterns` widens it). Hello timeout 10s.
- **Renewal:** a later hello replaces the credentials for the commands after it. A refused renewal closes the connection. Streams already open go on, as an SSE response does after its token expires.
- **Backpressure:** each connection has one outbox (256 messages by default) and one writer. Putting a message never waits. When the outbox is full, every open stream gets a `gap` whose `after` is the last event put on it, the streams are cancelled, and the connection closes with 4008 once the outbox is written. The API handler's writes then fail, as a cut SSE connection's do: a chat run nobody follows goes into the usual reconnect grace.
- **Heartbeats:** a ping every 30s; no pong within 10s closes the connection. The SSE keep-alive comments are dropped (heartbeats stand for them).
- **Limits per connection:** 64 streams, 16 commands awaiting results (503 `over_capacity`, `retry_after` 1), 1 MiB + 4 KiB per message (coder closes with 1009). No WebSocket-only error codes.
- **Commands run concurrently.** Results come as they are ready, matched by `id`. An `id` is unique among unanswered commands and open streams. Unsubscribe answers 200, and the stream then ends with `end`.
- **Shutdown:** `wsapi.Server.Shutdown` closes connections with 1001 once their outbox is written, and answers new upgrades with 503.
- **Library:** `github.com/coder/websocket` v1.8.15, in `wsapi` and `wsclient` only (archtest). One quirk found: `Close` waits up to 5s for the peer's close answer while our reader holds the read lock, so the server drops the connection as soon as its reader ends, and the client sends its close from a goroutine and drops the connection when its reader has the answer.
- **Size:** +188,416 bytes on every image that serves the agent API (docs/SIZES.md). On main after T072, T086 and T069 this put api 136,256 bytes over its 23 MB budget and dev 69,632 over 32 MB, so both went up by 1 MB, under the planner's budget rule. The first thing to cut is `-tags nows`.

Tests (`pkg/agentapi/wsapi`, `pkg/agentclient/wsclient`):

- `TestSameResultsEitherWay`, the shared table. Each script runs on a fresh server through `agentclient` over HTTP/SSE and again over `wsclient`, and the transcripts must match: events (without times), statuses, error status, code, retryable, Retry-After and run ID. Scripts cover send; subscribe from the start and after IDs; a busy conversation, stop and resume; the limit of runs (503 with Retry-After); answering an interrupt, then answer, cancel or decide on a done run; cancelling a suspended run; steering, pausing and resuming a task (the resume takes the pause back, so the task runs to its end with the steered message); and refused requests (empty message, unknown conversation or run, bad Last-Event-ID, commands to unknown runs). Over WebSocket, none of them may reach the API over HTTP. `TestUnauthorizedEitherWay` covers 401.
- `TestSlowClientGetsAGapAndResumes`: the server's end of the connection is held (a gated `net.Conn`). The client gets its events, a gap naming the last one, and a 4008 close. Subscribing again after each gap gives exactly the SSE stream's events, with none missing and none repeated. `TestSlowClientDoesNotBlockTheRun`: the run finishes while its WebSocket viewer cannot be written to. `TestClientResumesAfterAGap` does the same through `wsclient`, and `TestAfterAGapTheTransportDialsAgain` covers the client's half against a fake server.
- `TestHelloFirst`, `TestHelloTimeout`, `TestRenewal` (API keys with read and run scopes, raw and through `Transport.Renew`), `TestMalformedMessages`, `TestStreamLimitAndUnsubscribe`, `TestHeartbeatClosesADeadClient`, `TestShutdown`, `TestNotAnUpgrade`, `TestCSRFHeader`, `TestBindingDoesNotImportTheServer`.
- `go test -race -shuffle=on -count=20 ./pkg/agentapi/wsapi/` passes.

### Mutation check

Each bug was planted alone, then `./pkg/agentapi/wsapi/` (and, for the client's dial-again bug, `./pkg/agentclient/wsclient/`) was run.

| Planted bug | Caught by |
|---|---|
| subscribe ignores `after` (no Last-Event-ID) | `TestSameResultsEitherWay`, `TestSlowClientGetsAGapAndResumes`, `TestClientResumesAfterAGap` |
| a full outbox drops messages without gaps | `TestSlowClientGetsAGapAndResumes`, `TestSlowClientDoesNotBlockTheRun`, `TestClientResumesAfterAGap` |
| the gap names no event | `TestSlowClientGetsAGapAndResumes`, `TestClientResumesAfterAGap` |
| a full outbox waits for the writer (blocks the server) | `TestSlowClientGetsAGapAndResumes`, `TestSlowClientDoesNotBlockTheRun`, `TestClientResumesAfterAGap` |
| a renewal keeps the old credentials | `TestRenewal` |
| commands before the hello are served | `TestHelloFirst` |
| a refused hello leaves the connection open | `TestRenewal` |
| no heartbeat | `TestHeartbeatClosesADeadClient` |
| no hello deadline | `TestHelloTimeout` |
| results lose their body | `TestSameResultsEitherWay`, `TestHelloFirst`, `TestRenewal`, `TestUnauthorizedEitherWay`, `TestClientResumesAfterAGap` |
| the stream limit is not enforced | `TestStreamLimitAndUnsubscribe` |
| Retry-After dropped from results | `TestSameResultsEitherWay` |
| the slow close is a normal close | `TestSlowClientGetsAGapAndResumes` |
| unsubscribe does not cancel the stream | `TestStreamLimitAndUnsubscribe` |
| client: a gap ends the stream like its end | `TestClientResumesAfterAGap` |
| client: subscribe drops the Last-Event-ID | `TestSameResultsEitherWay`, `TestClientResumesAfterAGap` |
| client: stop is sent as cancel | `TestSameResultsEitherWay` |
| steer goes to the task's pause | `TestSameResultsEitherWay` |
| client: a task's resume is sent as pause | `TestSameResultsEitherWay` |
| client: a connection that sent a gap is reused | `TestAfterAGapTheTransportDialsAgain` |
| messages encoded with HTML escaping | survives, and is equivalent: the API's own `json.Marshal` already escapes the events and bodies, so escaping them again changes no byte. `SetEscapeHTML(false)` stays so that the binding never changes a byte of what the API wrote |

### Left for later

- **The user stream** (`GET /v1/events`, T090's notifications, waiting on T040's identities): add `subscribe` with a `stream` field (`"user"`) mapped to that endpoint. The envelope already carries everything else.
- **The console (`web/`)** does not use the binding yet. A TypeScript transport behind the console's API client, speaking `wire.WSMessage`, is its own task (it touches the web workspace and its first-load budget).
- No idle limit and no limit on connections per credential or overall: put them behind the proxy, or add an option when a deployment needs one.
- Realtime voice and audio (E18's first case) need binary frames. The binding refuses binary messages (1003) for now.

## For whoever adds an endpoint

A new command of the API joins the binding with one row in `routeOf` (`pkg/agentapi/wsapi/conn.go`), one case in the client's `command` (`pkg/agentclient/wsclient`), and a script in `same_test.go`'s table, which runs it over both transports.
