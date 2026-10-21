---
id: T105
title: "Pool capacity and config: chat and background pools with their own limits, lanes, serve config"
epic: E20
status: done
depends_on: [T104, T099]
---

# T105: pool capacity and config

## Context

After T104 every run goes through a router, but two `process` pools behave the same: the only point of a separate chat pool and background pool today is that **background work can never take the slots chats need** ([E20](E20-runner-pools.md)).

## Scope

- **Capacity per pool:** a pool has a limit of runs going on. A chat pool admits at the request (full: `429`, as `WithMaxConcurrentRuns` does today for the whole server). A background pool never refuses: its runs wait in the store.
- **Background pools claim by lane:** a background pool is a lane of T099's dispatcher; it claims with `ClaimNext` only while it has a free slot. Recovered runs go back to their pool's lane.
- **Serve config:** a `runners:` section: pools (name, isolation, limit), routes (pool, `runs: chat|background`, agents, isolation), default. Only `process` pools until T106/T107; any other isolation fails startup naming the task.
- **Agents:** `isolation:` in an agent's definition (default `process`).
- **The pool on each run:** stored with the run (migration on every SQL backend), shown by `GET /v1/runs/{id}`, logged, and counted in metrics per pool.
- **Flows:** decide whether a flow run's agent steps route like chats, or with the flow run.

## Acceptance criteria

- [x] A full background pool never delays a chat: chats start while background runs wait — test: `agentapi.TestFullBackgroundPoolNeverDelaysAChat` (and `agentapi.TestFullChatPoolRefusesAChat` for the chat pool's own 429)
- [x] A background run waits in the store until its pool has a slot, then starts (fake clock) — test: `runner.TestBackgroundRunWaitsForItsPoolsSlot`
- [x] The serve config routes runs as written; an unknown pool or isolation fails startup — tests: `main.TestServeRoutesRunsByConfig`, `main.TestServeRejectsBadSetup` (the `runners:` and `agents: ... isolation` cases)
- [x] A run's pool is stored and returned by the API — tests: `agentapi.TestRunPoolIsStoredAndReturned`, storetest `QueueOptionsAreStored` (memory, SQLite; Postgres in the integration tier), the 0011/0012 upgrade checks in `TestRunsFromBeforeTheQueueAreQueued` (sqlite, postgres)
- [x] Mutation list recorded.

## Notes

### What was built

- **Store:** `store.Run.Pool`, `store.WithPool` (text, checked like a lane), never changes; column `pool TEXT NOT NULL DEFAULT ''` by SQLite `0011_pool.sql` and Postgres `0012_pool.sql` (renumbered after T043 took 0010/0011) (constant default: no backfill, so no RLS lifting). Memory store too.
- **Runner:** `runner.InPool(name)` stores the pool; `Placement.Pool` carries it to the factory on the start and on every resume. `runner.Slots` and `runner.WithSlots(s)`: a dispatcher takes one of the pool's slots per run in place of its own workers, claims (`ClaimNext`) only while it holds one, and gives it back when the run's execution ends.
- **pkg/runner/pool:** `Slots` (`TryAcquire`, `Acquire`, `Release`, `Limit`, `InUse`); `NewRouter(..., opts...)` with `WithLimit(pool, n)`; `Router.Slots`, `Pools`, `DefaultPool`. `Route` sends a run with a `Placement.Pool` back to that pool while it exists and is isolated enough (never a weaker one), else routes afresh.
- **Agent API:** a chat (POST /v1/runs, a resume) is routed before it starts and takes a slot of its pool: none free is **429 `over_capacity`** with `Retry-After` (the global `WithMaxConcurrentRuns` stays 503); the slot goes back when the execution ends. `StartOptions.Pool` reaches a `WithRuns` orchestrator. `Server.Enqueue` routes a background run and queues it in its pool's lane (lane = pool name) with its pool; `Server.Dispatcher(admin, pool)` is that lane's `runner.Recovery` on the pool's slots (the default pool's also takes lane `""`). `pool` is in `wire.Run` (GET /v1/runs/{id}, the list) and in the run logs (started, resumed, recovered, queued, done).
- **Serve:** `runners` section (`cmd/agentkit/runners.go`): `pools` (name, isolation, limit), `routes` (pool, `runs: chat|background`, agents, isolation), `default`; `isolation` on agents. Startup fails on an unknown pool, field or isolation, on a `sandbox`/`vm` pool (naming T106/T107), and when some agent's chats or background runs have no pool isolated enough. One dispatcher per pool that background runs can reach (the default, and every route that is not chat-only); none for chat-only pools. Documented in docs/QUICKSTART.md section 5.

### Decisions

- **One slot count per pool**, shared by its chats and its background runs: a pool is "chat" or "background" by its routes, not by a kind. What keeps background work from chats is routing them to different pools.
- **429 with the existing code `over_capacity`**, not a new code: clients (agentclient, `agentkit` exit code 3) already treat it as retryable; the message names the pool.
- **Lane = pool name.** Runs from before pools (lane `""`, pool `""`) are claimed by the default pool's dispatcher and routed by the rules as before.
- **No dispatcher for chat-only pools**, so no dispatcher holds a chat slot even for the moment of an empty `ClaimNext`. A pool that takes both chats and background runs (e.g. the default with a limit) shares its slots: a chat can get 429 for the instant the dispatcher holds a freed slot while finding its lane empty.
- **Flows:** a flow run's agent steps run with the flow run, not routed like chats (one run, one lease, one slot; a step waiting on a full chat pool would hold the flow's slot). Flow runs themselves are in no pool yet (decisions are not admitted to a pool); recorded in E20.
- **Metrics:** there is no metrics system in the repo yet; the pool is on every run log line, which is what a log-based counter reads. Counting per pool in metrics waits for one (owner question).
- A run whose stored pool has been removed from the config is routed afresh on resume; its stored `pool` keeps the old name. A queued run in the lane of a removed pool waits until a dispatcher serves that lane again.
- Default limit: none (`limit` 0); a pool with no limit dispatches 4 runs at once, as before.
- Size: no new dependency; `cmd/agentkit` (stripped, `CGO_ENABLED=0`) 30,056,608 → 30,089,376 bytes (+32 KB), under the 100 KB that needs a docs/SIZES.md row.

### Mutation list

Each plant was run against the package named, then restored. All but one caught.

| Plant | Caught by |
|---|---|
| Recovery ignores `WithSlots` (own workers) | `runner.TestBackgroundRunWaitsForItsPoolsSlot` |
| `admitPool` never refuses | `agentapi.TestFullChatPoolRefusesAChat` (over-release panic), `TestOpenAPIResponses` |
| `drive` never frees the pool slot | `agentapi.TestFullChatPoolRefusesAChat` |
| `Enqueue` leaves the run in the default lane | `agentapi.TestFullBackgroundPoolNeverDelaysAChat` |
| `Dispatcher` without the pool's slots | `agentapi.TestFullBackgroundPoolNeverDelaysAChat` |
| `Route` ignores the placed pool | `pool.TestRouterSendsAPlacedRunBack` |
| `Route` takes a placed pool too weak for the agent | `pool.TestRouterSendsAPlacedRunBack` |
| `WithLimit` accepts 0 | `pool.TestRouterLimits` |
| `Run` drops the pool from the placement | `runner.TestRunKeepsItsPool` |
| resume drops the stored pool | `runner.TestRunKeepsItsPool`, `TestBackgroundRunWaitsForItsPoolsSlot` |
| memory store drops the pool | storetest `QueueOptionsAreStored` |
| SQLite stores no pool | storetest `QueueOptionsAreStored` |
| pool not checked as text | storetest `QueueOptionsAreStored` |
| API shows no pool | `agentapi.TestRunPoolIsStoredAndReturned` |
| config: `runs: chat` taken as background | `main.TestServeRoutesRunsByConfig` |
| config: sandbox pools accepted | `main.TestServeRejectsBadSetup` |
| config: unknown route pool accepted | `main.TestServeRejectsBadSetup` |
| config: chat-only pools dispatched | `main.TestServeRoutesRunsByConfig` |
| config: no startup check of agents' isolation | `main.TestServeRejectsBadSetup` |
| agents' isolation not passed to the server (survived) | equivalent while only process pools exist: startup already refuses any agent that needs more than process; T106 makes it observable |

## For T106 / T107

- A sandbox or VM pool is a `pool.Pool` with `Isolation()` above process; add it to `runnersConfig.router` (drop the "comes with T106/T107" refusal for that level) and give it a limit with `pool.WithLimit` like any pool. Its runs already get its slots, its lane and its stored pool.
- A resumed run comes back with `Placement.Pool` set: a VM pool can find the session's warm VM from it and `ConversationID`.
