---
id: T089
title: "pkg/bus: event bus interface, in-process and Redis Streams backends, conformance suite"
epic: E18
status: todo
depends_on: []
---

# T089: event bus

## Context

Live events must reach subscribers on any replica, and commands must reach the replica running a run. Redis Streams carries that across replicas; an in-process bus covers one process. Decisions: [E18](E18-streaming-backend.md) ("Live fan-out", "Without Redis").

## Scope

- **`pkg/bus` interface:**
  - `Publish(ctx, topic, Message{ID, Data})`;
  - `Subscribe(ctx, topic, after ID) (<-chan Message, error)`, which delivers in order from after `ID`, then live;
  - `Trim` policy per topic (max length, max age).
  - Topics: `run:{id}`, `run:{id}:cmd`, `user:{tenant}:{user}`.
  - Message IDs are assigned by the publisher (the run's sequence), so readers can dedupe.
- **Backends:**
  - **memory** (bounded ring per topic, for single process and tests);
  - **redis** (`github.com/redis/go-redis/v9`): `XADD` with `MAXLEN ~` and expiry, `XREAD BLOCK`, reconnect with backoff, a context-cancelled read returning promptly.
- **`bustest.Run`** conformance suite for both backends:
  - ordering;
  - resume after an ID;
  - fan-out to many subscribers;
  - trimmed history reported as `ErrGap` (not silent loss);
  - slow subscribers don't block publishers (bounded buffers, then `ErrGap`);
  - context cancellation;
  - tenant-safe topic names.
- **Integration:** tests read `AGENTKIT_REDIS_URL` (the integration tier); a CI job with a Redis service container.

## References

- [Redis Streams](https://redis.io/docs/latest/develop/data-types/streams/) (`XADD`, `XREAD BLOCK`, `MAXLEN ~`, consumer groups vs plain reads)
- [Redis Pub/Sub delivery semantics](https://redis.io/docs/latest/develop/interact/pubsub/) (at-most-once: why we don't rely on it)
- [go-redis v9](https://github.com/redis/go-redis)
- How Centrifugo and Phoenix PubSub handle history, recovery and gaps (resume from offset, then "insufficient state")

## Acceptance criteria

- [ ] **Suite:** both backends pass `bustest.Run`, with Redis against a real server in the integration tier.
- [ ] **Resume after a restart:** a subscriber resuming after a Redis restart (data kept) continues; after history was trimmed, it gets `ErrGap`.
- [ ] **Slow subscribers:** a stalled subscriber doesn't slow the publisher or other subscribers (measured, not timed: the publisher never blocks).
- [ ] **Leak checks:** no goroutine leaks after cancel (goleak or a counted check).
- [ ] **Mutation list** recorded.
