---
id: T090
title: "Streaming across replicas: resumable run streams, the user notification stream, commands to the lease holder"
epic: E18
status: done
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
- [x] **Steering across replicas:** a steering message sent to B reaches the run on A at the next boundary. After A is killed, the runner that takes over still delivers it.
- [x] **Notifications:** the user notification stream shows another user's task to nobody (tenant and user isolation).
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

- A viewer on another replica does not count toward the holder's reconnect grace for interactive runs.

### Phase 3: steering across replicas, `GET /v1/events`

**Steering across replicas** held as built: T086's commands are stored in the command log first (Postgres), the holder's runner reads them at its next turn boundary, and a runner that resumes a run reads what is pending at its start. Nothing needed fixing. Proved by `TestE2ESteeringAcrossReplicas` (integration tier, `cmd/agentkit/steer_integration_test.go`, run in CI's integration job and `scripts/integration.sh`): two `agentkit serve` processes on Postgres and Redis. A task runs on A; a message sent to B while A's model works on turn 1 is delivered after the tool result and before the second model call (the `steered` event is at the boundary, the second request carries the text and the first does not). A second task is steered through B while A waits for the model, then A is killed (SIGKILL); B's recovery takes the task over once A's lease runs out, delivers the message before its first model call, and finishes the task. Fixtures: `testdata/e2e/serve-steer` (serve-replicas' two recordings, once per task).

**Notifications (`GET /v1/events`)**, built on `pkg/stream`'s user stream:

- **Who a run belongs to:** `store.Run.User` (new, set by `store.WithUser`, migrations SQLite `0017_run_user.sql`, Postgres `0018_run_user.sql`, all three backends and storetest). The runner stores every run it creates with the user its context acts for (`tenancy.User`, which the auth middleware sets from the principal). It never changes, so a task resumed or taken over by a process that acts for nobody (the recovery loop) still notifies its user.
- **What is sent** (`wire.UserEvent`, `agentapi.UserEvent`; the name `Notification` was taken by the webhook record): `run_status` when an execution starts (`running`) and when it stops, in the status the run's stream gives it (done, failed, canceled, rejected, suspended, paused, interrupted), with `background` and the error `code`; then `conversation_updated`. A task whose process stopped (shutdown, lost lease) sends nothing then: the process that takes it up says `running`. A stop or cancel of a run nobody executes is sent by the server that did it. `resync` when notifications were lost.
- **Where:** published by the process executing the run (`drive`), on the bus topic of the run's user in its tenant; any replica on the bus serves the stream. IDs are `stream.Notifier`'s (`<unix ms>[.n]`).
- **The endpoint:** `GET /v1/events` (scope `agent:read`), SSE as `GET /v1/runs/{id}/events` (`id:`, `event:` the type, `data:` the JSON, keep-alives). Without `Last-Event-ID` it starts now: the newest ID is read before the headers go (`stream.LastNotification`, new), so a client that has the headers misses nothing; `stream.WatchUser`'s zero ID now means from the start of the topic (it was "from now", which raced). With `Last-Event-ID` it goes on after it, or sends `resync` when the bus no longer has it. The stream ends when the server drains (`WithDrain`'s stop: SIGTERM) or shuts down, so it never holds up `http.Server.Shutdown`. A server serves at most 500 at once, and 8 to one caller (a user, or the callers with no user, of a tenant); one more is 503 `over_capacity`: each holds a bus subscription, on Redis a connection of the subscriptions' pool (1000) that run streams and command inboxes share.
- OpenAPI: path `/v1/events`, schema `UserEvent`; `TestOpenAPIResponses` probes 200 (every event checked against `UserEvent`, a resync too), 400, 401, 403. Docs: `pkg/agentapi` package doc ("Notifications"), QUICKSTART.

Decisions (defaults):

- **A caller with no user** (an open server, `AGENTKIT_API_TOKEN`, a key with no user) follows the tenant's runs started with no user: the bus topic `none`, while every user's is `u.<user>`, so no user id (whatever an authenticator allows) reaches it. Every caller of a tenant can list all its runs (`GET /v1/runs` is not user scoped); the stream is stricter.
- **Thread messages** (agent to agent, E14) are not separate notifications: a delivered message is a run of the recipient, stored for the user the sending run's context acts for, so its status reaches that user. A dedicated `thread_message` type can come with the console's threads view.
- **Duplicates are allowed** (a stop of a run executing here is sent by the stop and by the execution's end): notifications are hints to refetch.
- No notification when a run is queued (it says `running` once it starts).

Tests: `pkg/agentapi/notify_test.go` (`TestNotificationsReachOnlyTheirUser`: ann's task and chat reach ann only, not bob in her tenant, not a key with no user, not ann of another tenant, and theirs do not reach her; `TestNotificationsAcrossReplicas`: from another replica, `Last-Event-ID`, resync, 400; `TestNotificationsOfATaskTakenOver`: a task taken over by a process with no user still tells its user; `TestNotificationsOfStopAndCancel`; `TestNotificationsOfAnotherCallersStopAndCancel`: bob's stop and cancel of ann's runs, on the holder and through another replica, reach ann and not bob or a key with no user; `TestNotificationStreamsPerCaller`; `TestNotificationsEndWithTheServer`), `pkg/runner` `TestRunsAreStoredForTheirUser`, storetest (`QueueOptionsAreStored`, the rig's model), `pkg/stream` `TestWatchUserPositions`, and the e2e above, which also follows B's `GET /v1/events` through Redis: both tasks' statuses, the second one's `running` from A, then from B after the kill.

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

Phase 3 (steering, notifications): each bug planted alone; `./pkg/agentapi/` and `./pkg/runner/` (notification tests, `TestOpenAPIResponses`), `./pkg/store/sqlite/` and `./pkg/store/memory/` for the store plants, and `TestE2ESteeringAcrossReplicas` (integration tier, Postgres and Redis) for the steering plants.

| Planted bug | Caught by |
|---|---|
| notifications all sent to the no-user stream | `TestNotificationsOfATaskTakenOver`, `TestNotificationsReachOnlyTheirUser` |
| notifications sent to the user of the executing context, not the run's | `TestNotificationsOfATaskTakenOver` |
| `GET /v1/events` follows the no-user stream | `TestNotificationsOfATaskTakenOver`, `TestNotificationsReachOnlyTheirUser` |
| the runner does not store the run's user | `TestRunsAreStoredForTheirUser` |
| SQLite does not store the user | `TestRunStore`, `TestTwoHandles` |
| the memory store does not keep the user | `TestRunStore` |
| `Last-Event-ID` ignored on `/v1/events` | `TestNotificationsAcrossReplicas`, `TestOpenAPIResponses` |
| a task whose process stopped (lost lease, shutdown) sends `running` again | `TestNotificationsOfATaskTakenOver` |
| the stream does not end on drain | `TestNotificationsEndWithTheServer` |
| a cancel of a run nobody executes sends nothing | `TestNotificationsOfStopAndCancel` |
| no `conversation_updated` | `TestNotificationsAcrossReplicas`, `TestNotificationsOfATaskTakenOver`, `TestNotificationsReachOnlyTheirUser`, `TestOpenAPIResponses` |
| `resync` sent as a `run_status` | `TestNotificationsAcrossReplicas` |
| a status that is not a documented one | `TestNotificationsAcrossReplicas`, `TestNotificationsOfATaskTakenOver`, `TestNotificationsOfStopAndCancel`, `TestNotificationsReachOnlyTheirUser`, `TestOpenAPIResponses` |
| a resumed run does not read pending steering at its start | `TestE2ESteeringAcrossReplicas` |
| the server's command watcher consumes the runner's commands (steer, pause, resume) | `TestE2ESteeringAcrossReplicas` |

Review (PR #107), each planted alone, `./pkg/agentapi/`:

| Planted bug | Caught by |
|---|---|
| user topics named without the tenant | `TestNotificationsReachOnlyTheirUser` |
| a cancel of a run nobody executes notifies the run without its user | `TestNotificationsOfAnotherCallersStopAndCancel` |
| a stop notifies the run without its user | `TestNotificationsOfAnotherCallersStopAndCancel` |
| a user's bus id not prefixed (a user named `none` follows the no-user stream) | `TestNotificationsReachOnlyTheirUser` |
| a closed stream not counted closed | `TestNotificationStreamsPerCaller` |
| the per-caller limit off by one | `TestNotificationStreamsPerCaller`, `TestOpenAPIResponses` |
| the per-caller count not keyed by tenant | `TestNotificationStreamsPerCaller` |

Not caught: the cancel through the holder notifying the run without its user (`command`, `commandCancel`). The holder's own cancel almost always wins the race, and its execution's end notifies the run's user; the server that sent the command notifies only when the run had nobody executing it (a holder that died), which no test reaches.
