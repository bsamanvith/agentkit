---
id: T089
title: "pkg/bus: event bus interface, in-process and Redis Streams backends, conformance suite"
epic: E18
status: review
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

- [x] **Suite:** both backends pass `bustest.Run`, with Redis against a real server in the integration tier. (`memory.TestConformance`; `redis.TestConformance` against Redis 7 in the `integration-redis` CI job, and locally against 7.4 and 8.2, `-race -count=20`.)
- [x] **Resume after a restart:** a subscriber resuming after a Redis restart (data kept) continues; after history was trimmed, it gets `ErrGap`. (`TestRestart`, on a `redis-server` it restarts itself; it also covers a restored older snapshot and a failed publish.)
- [x] **Slow subscribers:** a stalled subscriber doesn't slow the publisher or other subscribers (measured, not timed: the publisher never blocks). (`bustest` SlowSubscriberBlocksNobody: a subscriber that never reads, 600 publishes each checked to return, another subscriber receiving all of them; the stalled one then gets `ErrGap`.)
- [x] **Leak checks:** no goroutine leaks after cancel (goleak or a counted check). (`goleak.VerifyTestMain` in `pkg/bus/memory` and `pkg/bus/redis`.)
- [x] **Mutation list** recorded (Notes: 26 plants, all caught).

## Notes

### API (package `bus`)

- `Bus`: `Publish(ctx, Topic, Message) error`, `Subscribe(ctx, Topic, after ID) (Subscription, error)`, `Close() error`. `Subscription` has `C() <-chan Message` and `Err() error`, which is nil until `C` is closed and then says why: `ErrGap`, `ErrClosed`, the context's cause, or a backend error. (The scope's `<-chan Message` return could not say why a stream ended.)
- `Message{ID, Data}`. `ID{Seq, N}` is ordered by `Seq`, then `N`; text form `"<seq>"` or `"<seq>.<n>"` (`String`, `ParseID`, which accepts only that form). The zero ID means "before everything" for `Subscribe` and is invalid for `Publish`.
- Topics: `RunTopic(tenant, run)` is `run:{tenant}:{run}`, `CommandTopic` is `run:{tenant}:{run}:cmd`, `UserTopic(tenant, user)` is `user:{tenant}:{user}`. Every part is percent-escaped (only `[A-Za-z0-9._-]` kept), so no ID can reach another tenant's or kind's topic. Unlike E18's `run:{id}`, the run topics carry the tenant.
- `Trim{MaxLen, MaxAge}` per topic; `DefaultTrim`: run 10000 / 24h, cmd 1000 / 24h, user 1000 / 1h. `MaxAge` is an idle expiry: a topic is dropped `MaxAge` after its last publish.
- Errors: `ErrGap`, `ErrStale` (the ID is not after the topic's last; nothing published; benign when retrying), `ErrInvalid`, `ErrClosed`.
- Backends: `memory.New(WithClock, WithTrim)` and `redis.New(client, WithPrefix, WithTrim, WithClock, WithBackoff, WithBlock)` (keys `agentkit:bus:<topic>`). `bustest.Run(t, NewBackend)` is the conformance suite; `Backend.Lose` makes a backend lose a topic's history (memory: idle expiry; Redis: `DEL`).

### Decisions

- **Gaps are detected by each entry's predecessor, not by trim bookkeeping.** Every stored entry records the ID of the entry before it (`p`), or none for the first of a topic. A subscriber at cursor `c` delivers the next entry only if its predecessor is at most `c` (none: only if `c` is zero); otherwise the subscription ends with `ErrGap`. This one rule covers trimming, expiry, a `DEL`, a Redis restart that lost data, and a subscriber that fell behind. Redis's own `max-deleted-entry-id` is no help: `MAXLEN` trimming does not advance it (checked, it stays `0-0`).
- **Slow subscribers:** a publisher never waits for a subscriber. History lives in the topic (ring or stream), and each subscriber reads from it at its own pace through an unbuffered channel. A subscriber that falls further behind than `MaxLen` gets `ErrGap` when it reads on, instead of a bounded per-subscriber buffer.
- **A missing topic with a non-zero `after` waits** instead of failing: the first entry's predecessor decides. That lets T090 subscribe after the last event it replayed before the bus has seen any.
- **Lost writes (Redis):** each bus remembers the last ID it published per stream, until the stream would expire. If the stream's last entry is before it (Redis restored an older snapshot), the next entry is marked as having no known predecessor, so a subscriber past it gets `ErrGap`.
- **A failed publish is a gap, on both backends.** A publish that fails (Redis down, a timeout, or a done `ctx`) may or may not have landed. The bus remembers the failed ID, and the next message links to it unless the stream reached it: subscribers past the message before it get `ErrGap` instead of silently missing it. Publishing the failed ID again links as usual. Redis marks the next entry as having no known predecessor; the memory bus gives it the failed ID as predecessor. `bustest` FailedPublishIsAGap covers both (with a done context), `TestRestart` covers Redis down.
- **Publish is one Lua script:** stale check, predecessor, `XADD MAXLEN ~`, `PEXPIRE`. IDs are compared as decimal strings in Lua (Lua numbers lose precision past 2^53).
- **One connection per Redis subscription**, reading with `XREAD BLOCK` (5s by default). A cancelled subscription is woken with `CLIENT UNBLOCK`, retried every 10ms while the read is in flight (a cancel racing the start of an `XREAD` made a single unblock a no-op). After a connection error the subscription drops that connection and reconnects with backoff (50ms doubling to 5s): go-redis's sticky `Conn()` would return the bad-connection error forever. Redis error replies are permanent, except `LOADING`, `BUSY`, `TRYAGAIN`, `MASTERDOWN` and `READONLY`.
- **`redis.New` rejects a client whose maintenance notifications are in auto mode** (go-redis's default). In v9.23 the auto mode's fallback writes the client's options while a `Conn()` derived from it reads them under a different lock, which the race detector reports. Use `ModeDisabled` (or `ModeEnabled`).
- **archtest:** `pkg/bus` may import `pkg/tenancy`, `pkg/clock` (and `pkg/clock/clocktest`, `internal/testkit` in tests). Third party: `pkg/bus/redis` gets `go-redis/v9` (and its `maintnotifications`); `go.uber.org/goleak` is test-only.

### Invariants (checked by the suite's harness on every delivery)

1. Each message a subscriber receives is the next one published to its topic after its cursor, with the same data, or (from cursor zero) the first one after a topic restart.
2. A subscription ends only with `ErrGap`, `ErrClosed`, its context's cause, or a backend error; never by a silent skip, including past a failed publish.
3. Neither a publisher nor a subscriber can change stored data through a slice it passed or received (the harness scribbles on both).
4. A publish never blocks (each runs with an escape that fails the test).
5. No goroutine outlives a test (`goleak.VerifyTestMain` in both backend packages).

### Testing

- `pkg/bus/memory`: `TestConformance` (fake clock), `Example`, `BenchmarkPublishFanOut` (8 subscribers, 256 B: ~1.4 µs, 3.4 KB and 9 allocs per publish on an M-series Mac).
- `pkg/bus/redis` (`//go:build integration`, `AGENTKIT_REDIS_URL`; skipped without it unless `AGENTKIT_REQUIRE_INTEGRATION` is set): `TestConformance`, `TestPublishSetsExpiry`, `TestReconnect` (kills the test's own connections with `CLIENT KILL ID`), `TestCancelUnblocksTheRead` (a 1-minute `BLOCK` ended by cancel), `TestNewRejectsAutoMaintNotifications`, and `TestRestart`, which runs its own `redis-server` (`AGENTKIT_REDIS_SERVER`). It covers: data kept across `SHUTDOWN SAVE` (a live subscriber continues, one resumed after 7 gets 8-10); an older snapshot restored (`SHUTDOWN NOSAVE`: live, resumed and newly subscribed all get `ErrGap`); history trimmed by `MAXLEN ~` (`ErrGap` after 11, a resume after 609 continues); and a publish that failed while Redis was down (`ErrGap` past it, or no gap once retried). `TestCancelRacingTheRead` pins the race between a cancel and an `XREAD` that has not reached Redis yet, through a test-only hook (`export_test.go`) and the fake clock.
- CI: the `integration-redis` job (Redis 7 service container, plus `redis-server` from apt for `TestRestart`). Locally the tier passed against Redis 8.2 and `redis:7` (7.4) at `-count=20`.
- `pkg/bus`: `FuzzParseID` (round trip), `TestIDRoundTripAndOrder`, `TestTopicNamesAreDistinct`.

### Mutation list

Each bug was planted on its own (script in the session's scratchpad), and the whole tier run with `go test -race -count=1 -tags integration ./pkg/bus/...` against `redis:7`. Every plant compiles and fails at least one test, by name; none survives, and none is caught only by a hang.

Four plants needed a second pass:
- `unblock tried once` survived: the race it guards showed up only under `-count=20`. `TestCancelRacingTheRead` now pins it.
- `Close does not end subscriptions` and `publisher waits for each reader` were caught only by the 180s test timeout (`Close` hung). The suite now closes buses with an escape, as it already did for publishes.
- `cancel leaves the channel open` did not compile as first written.

| Plant | Bug | Failing tests |
|---|---|---|
| `no prev check` | Every entry follows any cursor | TestConformance/SlowSubscriberBlocksNobody, TrimmedHistoryIsAGap; TestRestart |
| `fresh entry follows any cursor` | An entry with no known predecessor follows a non-zero cursor | TestConformance/LostHistoryIsAGap; TestRestart |
| `cursor not advanced` | The subscriber's cursor never moves | Example, nearly every conformance case, TestReconnect, TestRestart |
| `cancel leaves the channel open` | `C` is closed only after a gap or a clean end | TestConformance/Cancel, Close; TestCancelUnblocksTheRead, TestCancelRacingTheRead |
| `Close does not end subscriptions` | `Close` does not cancel subscriptions | TestConformance/Close (through its escape) |
| `memory: publish keeps the caller's slice` | The stored message aliases the publisher's slice | every conformance case (the harness scribbles on it) |
| `memory: readers share stored data` | Readers get the stored slice | TestConformance/FanOut, ResumeAfterAnID, SlowSubscriberBlocksNobody |
| `memory: delivers newest first` | A read returns entries in reverse | Example, TestConformance/Order and 7 others |
| `memory: unbounded history` | The ring never trims | TestConformance/TrimmedHistoryIsAGap, SlowSubscriberBlocksNobody |
| `memory: publisher waits for each reader` | Publish hands each reader a signal and waits for it | TestConformance/SlowSubscriberBlocksNobody, FanOut and 3 others (through the publish escape) |
| `memory: never expires` | `MaxAge` is ignored | TestConformance/LostHistoryIsAGap |
| `memory: no stale check` | An ID not after the last is stored | Example, TestConformance/StaleIDs |
| `topics: no escaping` | Topic parts are not escaped | TestConformance/TenantSafeTopics, TestTopicNamesAreDistinct |
| `topics: no tenant in the name` | The tenant is left out of topic names | TestConformance/TenantSafeTopics, TestTopicNamesAreDistinct |
| `redis: no stale check` | The script adds an ID not after the last | TestConformance/StaleIDs |
| `redis: no lost-writes check` | A stream behind this publisher is not noticed | TestRestart |
| `redis: first entry gets no fresh mark` | An entry with an empty `p` is read as following the zero ID | TestConformance (8 cases), TestCancelUnblocksTheRead, TestReconnect, TestRestart |
| `redis: no PEXPIRE` | Streams never expire | TestPublishSetsExpiry |
| `redis: cancel does not unblock` | A cancel waits for the block time | TestCancelUnblocksTheRead |
| `redis: unblock tried once` | The cancel hook gives up when nothing is blocked yet | TestCancelRacingTheRead, TestCancelUnblocksTheRead |
| `redis: failed connection kept` | A subscription keeps reading on its failed connection | TestReconnect, TestRestart |
| `memory: failed publish not remembered` | The next publish links past one whose context was done | TestConformance/FailedPublishIsAGap |
| `memory: a retry of the failed ID is a gap` | Publishing the failed ID again is reported as a gap | TestConformance/FailedPublishIsAGap |
| `redis: failed publish not remembered` | The next publish links past one that failed in Redis | TestRestart |
| `redis: cancelled publish not remembered` | The next publish links past one whose context was done | TestConformance/FailedPublishIsAGap |
| `redis: a retry of the failed ID is a gap` | Publishing the failed ID again is reported as a gap | TestConformance/FailedPublishIsAGap, TestRestart |

### For T090

- Build the client with `MaintNotificationsConfig: &maintnotifications.Config{Mode: maintnotifications.ModeDisabled}`, and size `PoolSize` for one connection per live subscription plus the publishers. Many SSE clients on one replica means many connections; multiplexing many streams per `XREAD` is a follow-up if that hurts (see below).
- Run stream: replay from the event log after `Last-Event-ID`, then `Subscribe(topic, lastReplayed)`. The bus sends only what comes after it, in order, so no dedupe is needed at the seam; a topic the bus has not seen yet just waits. On `ErrGap`, replay from the log again after the last ID sent and subscribe again. Deltas (`<seq>.<n>`) lost in a gap are gone, which is fine: the durable event that follows carries the state.
- Publish every event of a run, in order, on the bus. The bus can only see a gap in what it was given: an event the runner skips publishing (rather than one whose publish failed) is invisible to subscribers. A failed publish is reported as `ErrGap` to subscribers past it (see Decisions), so "log it and move on" (the T090 scope) is right. `ErrStale` after a retry means it already went out.
- The user topic needs an ID source: the bus assigns none. Per-user sequence from Postgres, or a time-based `<unix ms>.<n>` from the publishing replica (two replicas publishing to one user topic must not collide; `ErrStale` would drop the later one). Open question for T090.
- There is no "from now" subscribe: pass the last ID you know (for `/v1/events` with no `Last-Event-ID`, the latest notification's ID, or accept the replay of the retained window).
- After a lease failover, the new runner has no memory of what the old one published. If Redis also lost a snapshot at the same time, deltas can be skipped silently; durable events are still covered by the replay from Postgres.
- Commands: subscribe to `CommandTopic` after the last command the runner applied (stored in Postgres first, per E18); an `ErrGap` means reread pending commands from the store.

### Follow-ups for the planner

- `golangci-lint` does not see `//go:build integration` files: add `run.build-tags: [integration]` to `.golangci.yml` (owned by T001; I checked these files by hand with `--build-tags integration`).
- Merge the `integration-redis` CI job with T088's Postgres integration job once both exist, and mark it required.
- Report the go-redis maintnotifications auto-mode data race upstream, and drop the `ModeAuto` check in `redis.New` when it is fixed.
- Many subscriptions per replica: one `XREAD` over many streams per replica (a reader multiplexer) instead of a connection per subscription.
