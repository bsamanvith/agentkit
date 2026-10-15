---
id: T103
title: One watch per run per replica, shared by its viewers
epic: E18
status: todo
depends_on: [T090]
---

# T103: One watch per run per replica, shared by its viewers

## Context

Since T090 (bsamanvith/agentkit#13), a replica serves a run another replica
holds through `Server.watchRun` (`pkg/agentapi/runs.go`). Each viewer gets
its own watch:

- its own `stream.Watch`, which holds a bus subscription. On Redis that is a
  connection blocked in `XREAD`, from the subscribe pool of
  `redisbus.SubscribePoolSize` (1000);
- its own log polls, every `stream.DefaultPoll`, while the log is ahead of
  the bus;
- its own `checkHeld`, which reads the run from the store every poll to see
  whether its holder let it go.

So 100 viewers of one run on a replica that does not hold it cost 100 Redis
connections and about 100 store reads per poll interval. The replica holding
the run is not affected: its viewers read the run's hub. The owner asked
(2026-10-15) for this to be a task after T090 merged.

## Scope

- One watch per run per replica: the first viewer of a run held elsewhere
  starts it, and later viewers join it. Each viewer replays from its own
  `Last-Event-ID` (the store's log for what came before the shared watch's
  position, then the shared stream), so the no-gap, no-repeat guarantee holds
  per viewer.
- One held-check per run per replica, not per viewer.
- The shared watch stops when its last viewer leaves, and on the run's end,
  its release (served from the store, as now) or a takeover (it follows the
  new holder, as `watchRun` does now).
- A slow viewer does not hold up the others. Its buffer is bounded, and a
  viewer that falls behind is dropped to the log replay path (or its stream
  ends), not the shared watch.
- Stop and cancel through `Server.command` use the same shared watch.

## Out of scope

- Fan-out across replicas (each replica still has its own watch per run).
- The holder's hub, which already shares one stream between its viewers.

## Acceptance criteria

- [ ] N viewers of one run on a replica that does not hold it use one bus
  subscription and one held-check, and each gets every event once, in order.
  — test: `agentapi.TestSharedWatch` (a counting bus and store)
- [ ] Viewers that join mid-run with different `Last-Event-ID`s each get the
  run from their own position with no gap or repeat. — test: same, plus the
  checks in `rig.check`
- [ ] A viewer that stops reading is dropped, and the others keep streaming.
  — test: `agentapi.TestSharedWatchSlowViewer`
- [ ] The watch ends when the last viewer leaves, and no goroutine outlives
  it. — test: goleak
- [ ] A takeover mid-stream moves the shared watch to the new holder for every
  viewer. — test: extends `TestE2EServeReplicas` with several viewers on B
- [ ] Mutation list recorded.

## Notes

- From T090's notes, still open and related: a viewer on a replica that does
  not hold an interactive run does not count toward the holder's reconnect
  grace, so a chat watched only from another replica can be interrupted as
  abandoned. A shared watch is a natural place to report "this run has
  viewers here" to the holder (a periodic presence write, or a command).
  Decide here or split it out.
- Batching the store writes of a run's durable events was considered and
  left: a turn has few of them, and text deltas never touch the store.
