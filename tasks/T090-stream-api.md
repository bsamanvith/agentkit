---
id: T090
title: "Streaming across replicas: resumable run streams, the user notification stream, commands to the lease holder"
epic: E18
status: todo
depends_on: [T088, T089, T030]
---

# T090: streaming API across replicas

## Context

With several replicas, the client watching a run is often connected to a different replica from the one running it. Decisions: [E18](E18-streaming-backend.md) ("Resume from any replica", "What is durable vs live", "Commands to a running run").

## Scope

- **Runner side:**
  - each event gets a sequence ID (`<seq>` durable, `<seq>.<n>` for deltas);
  - durable events are appended to the store's `EventLog` and published on the bus; deltas are published only;
  - Postgres first, then publish; a failed publish is logged and left to the readers' gap handling.
- **`GET /v1/runs/{id}/events`** (SSE) from any replica: replay durable events after `Last-Event-ID` from the event log, switch to the bus tail with no gap or duplicate (dedupe by ID), backfill from the log on `ErrGap`, end after the terminal event.
- **`GET /v1/events`** (SSE): the caller's notification stream (tenant and user scoped): run and task status changes, interrupts waiting, conversation updated, thread messages. Resumable for a short window (bus history); clients refetch lists after a gap.
- **Commands:**
  - steering, interrupt answers and cancel are stored, then signalled on `run:{id}:cmd`;
  - the lease-holding runner consumes them at turn boundaries;
  - a runner resuming a run reads pending commands from the store.
- **Foreground chats** keep working unchanged on a single replica. With several replicas, a chat's stream may also be served from another replica (the same mechanism).

## References

- [WHATWG SSE: `Last-Event-ID`, `retry`](https://html.spec.whatwg.org/multipage/server-sent-events.html)
- T071's scope (tasks build on this), T086 (steering), T084 (interrupt answers)
- Centrifugo's "history and recovery" design, for the replay-then-tail pattern

## Acceptance criteria

- [ ] **Cross-replica stream:** with two `agentkit serve` processes sharing Postgres and Redis (integration tier), a run started on A and watched on B streams every event in order. Killing B mid-stream and reconnecting to A with `Last-Event-ID` continues with no gap or duplicate (replay e2e with real binaries).
- [ ] **Trimmed history:** after the bus history was trimmed, a reconnect still gets every durable event and the full final text (from `turn_done`).
- [ ] **Steering across replicas:** a steering message sent to B reaches the run on A at the next boundary. After A is killed, the runner that takes over still delivers it.
- [ ] **Notifications:** the user notification stream shows another user's task to nobody (tenant and user isolation).
- [ ] **Mutation list** recorded.
