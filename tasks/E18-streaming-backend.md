---
id: E18
title: "Streaming and the server backend: SSE + HTTP commands (WebSocket later), Postgres + Redis"
status: todo
---

# E18: streaming and the server backend

Owner request (2026-10-12): plan how clients and agents talk **both ways** in real time, and how the backend fans events out across replicas, with **Postgres + Redis** as the server backend.

## Decisions (made in planning; change them in the task that finds them wrong)

| Question | Decision |
|---|---|
| Client ↔ server transport | **SSE for everything the server pushes, plain HTTP requests for everything the client sends.** Both directions are covered: <br>- events stream down over SSE; <br>- messages, steering, interrupt answers, approvals and cancel go up as small `POST`s. <br>Why: SSE is what the Claude and OpenAI APIs and MCP's Streamable HTTP use. It works through every proxy, load balancer and CDN, multiplexes over HTTP/2, reconnects with `Last-Event-ID`, and our API and `agentclient` already speak it. A command is one idempotent request with its own status code, which is easier to retry, authorize and log than a message inside a socket. |
| WebSocket | **Later, as a second binding of the same messages**, not a different protocol (T091). For the cases SSE + POST serves badly: <br>- realtime voice and audio; <br>- many live subscriptions over one connection on mobile; <br>- clients behind networks that buffer SSE. <br>One JSON envelope (`{type, id, ...}`) is used on both, so servers and clients share one message model. |
| RSocket | **No.** Little browser, proxy or load-balancer support, few maintained Go libraries, and nothing SSE + POST (or WebSocket later) can't do for us. |
| Streams to subscribe to | Per **run or task**: `GET /v1/runs/{id}/events`, resumable with `Last-Event-ID` from any replica (T071, built on T090). Per **user**: `GET /v1/events`, one stream of notifications for the console's live lists: a task finished, waiting for you, a conversation updated, a new message in a thread. |
| Source of truth | **Postgres**: conversations, runs, tasks, the **durable event log** (append-only, one row per durable event with a per-run sequence number), messages between agents, agent definitions (E15), credentials (E07), triggers. SQLite (T029) stays for local and single-node use; both pass the same store suites. |
| Live fan-out | **Redis Streams**, not plain Pub/Sub. A run's live events are `XADD`ed to `run:{id}` (trimmed by length and expiry). Any replica serving a subscriber reads with `XREAD BLOCK` from the last ID it sent. Pub/Sub drops whatever arrives while a subscriber reconnects, and Streams don't. Pub/Sub is used only for signals where loss doesn't matter (cache invalidation). |
| What is durable vs live | Every event goes to the Redis stream. Only **durable events** go to Postgres: `run_start`, `tool_call`, `tool_result`, `turn_done` (which carries the full messages), interrupts and answers, steering, policy decisions, `run_done`/`run_error`. **Text deltas are live-only**: a client reconnecting after the stream has trimmed them replays the durable events and gets the full text from `turn_done`. Postgres doesn't take a write per token, and nothing is lost. |
| Resume from any replica | The SSE handler: <br>1. replays durable events from Postgres after `Last-Event-ID`; <br>2. switches to tailing the Redis stream from the point the replay reached; <br>3. dedupes by sequence; <br>4. on a gap (stream trimmed, Redis restarted), backfills from Postgres. <br>Event IDs are `<seq>` for durable events and `<seq>.<n>` for deltas within a turn, so ordering is total. |
| Commands to a running run | Steering, interrupt answers and cancel are written to Postgres first (durable, so a failover can't lose them), then signalled on Redis (`run:{id}:cmd`). The runner holding the run's **lease** (E05) consumes them at the next turn boundary. A runner taking over after a crash reads pending commands from Postgres. |
| Leases and locks | **In Postgres** (the store's lease rows, E05), not Redis locks: correctness stays in one place. Redis is a fast path that can be lost and rebuilt. |
| Other Redis uses | Rate limits (per tenant, agent, tool), short-lived caches (published agent definitions, tool catalogs), the user notification stream's live tail. Never the only copy of anything. |
| Without Redis | `agentkit serve` alone (local, tests, the exported image) uses an **in-process bus** with the same interface. A single replica needs no Redis; several replicas do. Config: `bus: memory|redis`. |
| Alternatives considered | Postgres `LISTEN/NOTIFY`: no replay, an 8 KB payload limit, one connection per listener; kept as a possible signal-only option. NATS JetStream: excellent, but the owner chose Redis. Kafka: too heavy for this. |

## Tasks

- [ ] [T088](T088-store-postgres.md) `pkg/store/postgres`: the server store, the same suites as SQLite, plus the durable event log table
- [ ] [T089](T089-event-bus.md) `pkg/bus`: event bus interface, in-process and Redis Streams backends, conformance suite
- [ ] [T090](T090-stream-api.md) Streaming API across replicas: resumable run streams (Postgres replay + Redis tail), the user notification stream, commands routed to the lease holder
- [ ] [T091](T091-websocket-binding.md) WebSocket binding of the same messages (later)
- T006 (Compose) gains Postgres and Redis services and an integration tier against them

Order:
1. T088 and T089 in parallel now (new packages).
2. T090 after both, plus T030. T071 (tasks) builds on T090.
3. T006 alongside.
4. T091 when a consumer app needs it.
