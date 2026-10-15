---
id: T090
title: "Streaming across replicas: resumable run streams, the user notification stream, commands to the lease holder"
epic: E18
status: in-progress
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

- [x] **Cross-replica stream:** with two `agentkit serve` processes sharing Postgres and Redis (integration tier), a run started on A and watched on B streams every event in order. Killing B mid-stream and reconnecting to A with `Last-Event-ID` continues with no gap or duplicate (replay e2e with real binaries).
- [x] **Trimmed history:** after the bus history was trimmed, a reconnect still gets every durable event and the full final text (from `turn_done`).
- [ ] **Steering across replicas:** a steering message sent to B reaches the run on A at the next boundary. After A is killed, the runner that takes over still delivers it.
- [ ] **Notifications:** the user notification stream shows another user's task to nobody (tenant and user isolation).
- [x] **Mutation list** recorded.

## Notes

Phase 2 (bsamanvith/agentkit#13): the agent API streams through `pkg/stream`.

- Event IDs are E18's: `12` for a stored event (`seq` 12), `12.3` for a text delta after it; `Last-Event-ID` takes either. The Go client, the console and the OpenAPI spec use them.
- An execution that ends without the run being final (interrupted, failed, suspended, shutdown) publishes its last event and does not store it; the run's status gives it to later readers.
- A run served from the store gets each turn's text back as text deltas (`B.2147483648` on) when the client has none of that turn's.
- A resumed run whose log holds turns its entries do not (a crash before the runner flushed) starts with a `rewind` event that names the turns kept.
- Stop and cancel sent to a replica that does not hold the run go through the command log and the bus to the holder.
- `agentkit serve` takes `AGENTKIT_BUS` (`memory`, or `redis://`). Without a shared bus a server is a single replica: stop of a run held elsewhere is 409, and its events are served from the store.
- Tests: `pkg/agentapi/replicas_test.go` (two servers, one store and bus), `TestReopenContinuesLiveIDs`, `TestRecoveryWithExecute`, the Redis `TestOpen`, and `TestE2EServeReplicas` (integration: two serve processes on Postgres and Redis, run in CI's integration job).

Left for later:

- **Steering across replicas** needs steering itself (T086); the command path it will use is the one stop and cancel use.
- **`GET /v1/events`** (notifications) needs a user identity (T040); `pkg/stream` has the user stream and its isolation tests.
- A viewer on another replica does not count toward the holder's reconnect grace for interactive runs.

### Mutation check

Each bug planted alone; `./pkg/agentapi/... ./pkg/stream/...` and the e2e tests (`TestE2EServeReplicas`, `TestE2EServeKilledMidRun`, `TestE2EServeAgent`, integration tier) run; each plant made a test fail.

| Planted bug | Caught by |
|---|---|
| live IDs restart after a reopen (`Publisher.Open` does not continue `n`) | `TestReopenContinuesLiveIDs`, `TestStop`, `TestResumeInterruptedConversation`, `TestRunLosesItsLease`, `TestOpenAPIResponses` |
| a cancel command interrupts instead | `TestStopAndCancelAcrossReplicas` |
| stop of a run held elsewhere is 409 | `TestStopAndCancelAcrossReplicas`, `TestE2EServeReplicas` |
| `Last-Event-ID` ignored | `TestFollowAcrossReplicas`, `TestClientDisconnect`, `TestEventsOfAnEvictedRun`, `TestE2EServeReplicas` |
| stored turns' text not replayed | `TestFollowAcrossReplicas`, `TestEventsOfAnEvictedRun`, `TestRetention`, `TestE2EServeReplicas` |
| non-final endings stored | `TestStop`, `TestNewMessageReplacesAnInterruptedRun`, `TestResumeInterruptedConversation`, `TestRunLosesItsLease`, `TestEventJSONGolden`, `TestOpenAPIResponses` |
| no rewind for turns the runner did not keep | `TestE2EServeKilledMidRun` |
| text deltas stored | `TestFollowAcrossReplicas`, `TestClientDisconnect`, `TestEventsOfAnEvictedRun`, `TestRetention`, `TestEventJSONGolden`, `TestE2EServeReplicas` |
| SSE `id:` is the seq, not the event ID | most of `pkg/agentapi` (the rig checks every SSE id) |
