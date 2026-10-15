---
id: T103
title: One watch per run per replica, shared by its viewers
epic: E18
status: review
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

- [x] N viewers of one run on a replica that does not hold it use one bus
  subscription and one held-check, and each gets every event once, in order.
  — test: `agentapi.TestSharedWatch` (a counting bus and store)
- [x] Viewers that join mid-run with different `Last-Event-ID`s each get the
  run from their own position with no gap or repeat. — test: same, plus the
  checks in `rig.check`
- [x] A viewer that stops reading is dropped, and the others keep streaming.
  — test: `agentapi.TestSharedWatchSlowViewer`, `TestSharedWatchViewersFallBehind`
- [x] The watch ends when the last viewer leaves, and no goroutine outlives
  it. — test: `TestSharedWatchEndsWithLastViewer`, goleak (`pkg/agentapi`'s TestMain)
- [x] A takeover mid-stream moves the shared watch to the new holder for every
  viewer. — test: `TestSharedWatchTakeover` (in process; not in
  `TestE2EServeReplicas`, see Notes)
- [x] Mutation list recorded.

## Notes

- From T090's notes, still open and related: a viewer on a replica that does
  not hold an interactive run does not count toward the holder's reconnect
  grace, so a chat watched only from another replica can be interrupted as
  abandoned. A shared watch is a natural place to report "this run has
  viewers here" to the holder (a periodic presence write, or a command).
  Decide here or split it out.
- Batching the store writes of a run's durable events was considered and
  left: a turn has few of them, and text deltas never touch the store.

## Notes (implementation, bsamanvith/agentkit#18)

- `pkg/agentapi/watch.go`: a `sharedWatch` per (tenant, run) in `Server.watches`.
  One goroutine runs `watchRun` (the bus subscription and the held-check, as in
  T090) and publishes each event to every viewer's channel (buffer
  `defaultWatchBuffer`, 256) and to a ring of up to `maxWatchRing` (4096)
  events, trimmed by half at a turn boundary (a stored `run_start`,
  `turn_done` or `retrying`) so the store has the text of the turns dropped.
- A watch starts at the first viewer's stored position (`{Seq, 0}` of its
  `Last-Event-ID`), so the bus brings the live events of that turn for later
  viewers too. A viewer joining from before the ring reads the store up to
  the ring's start (with `withText`, as a run served from the store), then the
  ring, then its channel; everything is filtered by its own position, so no
  gap and no repeat.
- A viewer whose channel is full is dropped (its channel closed, `dropped`
  set) and joins again from its position: first the same watch (whose ring
  still has the run's end if it finished meanwhile), then any. A dropped
  viewer stays a member until it leaves, so the watch is not stopped while it
  comes back; the last member to leave stops the watch and waits for its
  goroutine. `Shutdown` stops the rest.
- Stop and cancel through `Server.command` wait for the run's end on the same
  shared watch (`viewRun` with no reader).
- **Fix found by these tests (T090's code):** `drive` stores a run's last event
  after the runner lets the run go, so a held-check in between ended the watch
  with an ending made up from the status, with another ID (`8.4294967295`
  instead of `9`). `storedEnding` now waits up to `endingWaits` polls for the
  stored last event of a run let go as done, canceled or rejected.
- **Takeover test in process, not e2e.** API runs are interactive, so across
  `serve` processes a run whose holder dies reads as interrupted and only its
  user resumes it: a takeover a watch can see needs a resume within one poll
  of the lease running out, which an e2e test cannot drive without a race.
  `TestSharedWatchTakeover` drives it: a's lease renewals hang, the fake clock
  passes the lease, b's held-check sees the run let go, and its next read is
  held until c has resumed the run; c answers only once b follows it.
- **Presence for the reconnect grace: split out, not done.** A viewer on a
  replica that does not hold an interactive run still does not count toward
  the holder's reconnect grace. The shared watch is where a presence signal
  would go (one per run per replica); it needs a store or command write per
  interval and a holder-side reader, so it is its own task.
- Still as in T090, and recorded here because the tests meet them: a stopped
  chat run's `run_error` is streamed, not stored, so a viewer on another
  replica gets either the holder's (`1.1`) or, when it sees the run let go
  first, the one its status says (`1.4294967295`); and a watch that starts (or
  restarts after a takeover) after events were stored skips the live events
  before them, so the tests wait for b to follow the bus before the model
  answers.

### Mutation check

Each bug planted alone; `go test -race -count=3 -run 'SharedWatch|Replicas' ./pkg/agentapi/`.

| Planted bug | Caught by |
|---|---|
| no sharing: every viewer starts its own watch | `TestSharedWatch`, `TestSharedWatchCommands`, `TestSharedWatchEndsWithLastViewer`, `TestSharedWatchStoredEnding`, `TestSharedWatchTakeover`, `TestSharedWatchViewersFallBehind` |
| a full viewer blocks the watch instead of being dropped | the package times out (`TestSharedWatchSlowViewer`, `TestSharedWatchViewersFallBehind`) |
| no store read for a viewer from before the ring | `TestSharedWatch`, `TestSharedWatchStoredEnding` |
| the store read without the turns' text | `TestSharedWatch` |
| dropped viewers do not count as members | `TestSharedWatchStopsWithLastViewer` |
| a finished watch refuses viewers that come back | `TestSharedWatchSlowViewer`, `TestSharedWatchViewersFallBehind` |
| the last viewer leaving does not stop the watch | `TestSharedWatchEndsWithLastViewer` |
| the ring trimmed mid-turn | `TestSharedWatchTrim` |
| no wait for the stored ending | `TestSharedWatchStoredEnding` |
| a takeover not followed | `TestSharedWatchTakeover` |
| a viewer coming back skips its old watch and goes through the map | **not caught**: the map holds the same watch unless it finished and was removed in the moment between; the fanout's join after the end is covered by `TestSharedWatchSlowViewer` |
