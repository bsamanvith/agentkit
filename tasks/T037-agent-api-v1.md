---
id: T037
title: "agent component v1: on the runner (persist, resume, approve, cancel, list)"
epic: E09
status: done
depends_on: [T036, T030, T100]
---

# T037: Agent API v1

## Context

Swap T036's in-memory runs for `pkg/runner` (T030), keeping the API, so runs survive restarts and the run lifecycle (E05 decisions) is reachable over HTTP.

## Scope

- Runs and conversations go through the runner and a store chosen by config. The tenant comes from the auth middleware (the static token maps to one tenant until E07).
- **Store by config:** `AGENTKIT_STORE` selects the backend:
  - `memory` is the default, so the quickstart is unchanged;
  - `sqlite:/path/agentkit.db` selects SQLite (T029);
  - `postgres://…` selects Postgres (T088). Whichever of T037 and T088 lands second wires it.
  - An unknown scheme fails at startup.
  - Read T029's "For T037" and T030's notes first.
- **Image size:** linking the SQLite driver takes the image from about 17.4 to 21 MB. The budget is now 30 MB (`IMAGE_BUDGET_MB`, planner 2026-10-12), with every driver in the one default binary and no build tags. Record the new size in the Notes.
- New endpoints: `GET /v1/runs?conversation_id=&status=` (paged), `POST /v1/runs/{id}/resume`, `POST /v1/runs/{id}/decisions` (approve or reject a pending step), `POST /v1/runs/{id}/cancel`, `GET /v1/runs/{id}/events` (replay a run's stored events, then follow it live if still running).
- `POST /v1/runs` on a conversation with a live unfinished run → 409 with the run ID (`ErrRunUnfinished`).
- The runner's recovery loop runs inside `agentkit serve`.
- **Chat runs stop with their user** (owner, 2026-10-13, T100; this replaces the planner's earlier "a dropped client detaches" note). Runs started by `POST /v1/runs` are interactive (`runner.Interactive()`):
  - **Stop button:** `POST /v1/runs/{id}/stop` calls `Runner.Interrupt`.
  - **Dropped SSE connection:** a server-side goroutine keeps draining the execution for a reconnect grace of 10s. A client that reconnects within the grace (`GET /v1/runs/{id}/events`) follows the run on. Otherwise the run is interrupted, so a closed tab stops it.
  - **Showing the run:** a run that `Run.Interrupted` reports is shown as `interrupted`, and `POST /v1/runs/{id}/resume` continues it.
  - **No 409 after an interruption:** `POST /v1/runs` on an interrupted conversation starts a new run, which cancels the old one.
  - **Background runs:** runs started for background work (E14) keep detaching, and the recovery loop resumes them.
- **Conversations without messages (T092):** `POST /v1/conversations` creates an empty conversation (id, tenant, agent, created, empty title until its first run). `store.ConversationStore` only knows a conversation once it has an entry, so the store needs a conversation record of its own (id, tenant, agent, title, created/updated) for creation, listing and the agent check; keep both creation paths (explicit, and a run without `conversation_id`).

## Acceptance criteria

- [x] The T036 acceptance tests pass unchanged against v1 (same API).
- [x] Kill the server mid-run (replay e2e, separate process): after restart the chat run is shown `interrupted` and nothing resumes it; `POST /v1/runs/{id}/resume` finishes it, and `GET /v1/runs/{id}/events` shows the whole run.
- [x] Approve and reject through the API on a flow with `Approve`; cancel; 409 on an unfinished conversation.
- [x] `AGENTKIT_STORE` selects memory or SQLite (and Postgres once T088 is on `main`); an unknown scheme fails startup; the T036 acceptance tests pass on each.
- [x] **Closing the SSE connection mid-run:**
  - reconnecting within the grace shows the run finish;
  - staying away past the grace leaves it `interrupted`, with its kept turns stored;
  - `POST /v1/runs/{id}/stop` interrupts it at once.
- [x] Mutation list recorded.

## Notes

### Files

- **`pkg/agentapi`**: `runs.go` (the run endpoints, the run view, `GET /v1/runs`), `hub.go` (a run's events in memory, its clients, the reconnect grace), `replay.go` (a run's events rebuilt from its entries); `server.go` now starts runs on a `runner.Runner` and `drive`s each execution into its hub. Tests: `runs_test.go` (new), `helpers_test.go` (`testStore`, `rigTenant`), probes of every new status in `openapi_test.go`.
- **`pkg/store/scoped`** (new): the tenant-scoped `Store` a serving component gets, from `Memory` or a spec (`Open`), never a backend's `Admin`. `pkg/agentapi` and its tests take their store from here.
- **`pkg/store`**: the conversation record index (`store.ConversationIndex`, T092), in the memory store and migration **0006** of SQLite and Postgres; `storetest/index.go`. Postgres serializes a conversation's appends, create and delete on a transaction-scoped advisory lock of its tenant and id (`lockConversation`).
- **`pkg/agentclient`**: `runs.go` (`Runs`, `Events`, `Resume`, `Stop`, `Cancel`, `Decide`); `ErrRunState`, and `RunID` on `*APIError`.
- **`cmd/agentkit`**: `store.go` (`AGENTKIT_STORE`, `AGENTKIT_STORE_MIGRATE`); `serve.go` (`AGENTKIT_RUN_LEASE`, the recovery loop); `chat.go` (Ctrl-C stops the run); `kill_e2e_test.go` and `testdata/e2e/serve-kill/`.
- **`internal/archtest`**: `pkg/agentapi` may import `pkg/store`, `pkg/store/scoped` (tests), `pkg/runner`, `pkg/flow` and `pkg/tenancy`; no backend. `go list -deps ./pkg/agentapi` shows `pkg/store` and nothing under it.
- **CI**: the `integration` job runs the `pkg/agentapi` suite with `AGENTAPI_TEST_STORE=sqlite` and on its Postgres service.

### Environment

| Variable | Values | Default |
|---|---|---|
| `AGENTKIT_STORE` | `memory`, `sqlite:/path/agentkit.db`, `postgres://…` (or `postgresql://`); anything else fails startup, without echoing the value | `memory` |
| `AGENTKIT_STORE_MIGRATE` | `on`, `off` (Postgres: `off` leaves the schema to `agentkit migrate`) | `on` |
| `AGENTKIT_RUN_LEASE` | a duration, at least `1s` | `30s` |

**Image size:** 26.8 MB uncompressed (`make image-size`, 2026-10-14), within `IMAGE_BUDGET_MB=30`, with the SQLite and Postgres drivers in the one binary.

### Decisions

- **The T100 direction replaces T036's disconnect and 409 criteria.** A client that goes away no longer cancels its run: the run goes on for the reconnect grace, then is interrupted. A new message to an interrupted conversation starts a new run (no 409 `conversation_interrupted`; the code stays in the wire, never returned). The T036 tests that said otherwise were changed with this note.
- **Lease loss reads as interrupted.** A run whose lease this process could not renew in time, or that another process claimed, ends its stream with `run_error` `interrupted`; it is resumable.
- **Stop is a no-op on a run that is not going on** (200 with the run as it is), including a canceled one. Stopping a run another process holds is 409 `run_leased`.
- **A run's slot is freed before its last event reaches its clients**, so a client that starts its next run at once is not refused over capacity (`TestSlotFreeBeforeRunDone`).
- **`GET /v1/runs/{id}` between the store's end of a run and its execution's last event** reads the run as stored, with its error (`TestRunViewBeforeItsEnding`); before, it said `interrupted` with no error.
- **Retention bounds only the events kept in memory.** Runs and conversations live in the store; an evicted run's events are replayed from it (`TestEventsOfAnEvictedRun`).
- **A replayed `run_start` has its prompt's time**; the live one has the harness's start time. They agree when the harness and the store share a clock, as in the rig; the OpenAPI probe harnesses run on the rig's clock for that reason.
- **Read-only turns are redone after a crash.** Chat runs store their turns in batches (T098), except a turn that called a tool not declared read-only, which is stored at once. A process killed mid-run loses the read-only turns since the last batch, and the resumed run makes them again (the kill e2e asserts this: 4 model calls, the resumed run's first the same as the killed one's).
- **The recovery loop** runs in `serve` beside the API, with the backend's `Admin`, which nothing else gets. Every run the API starts is interactive, so today the loop resumes only runs other processes started without a user (E14).

### For T090

- The SSE `id` is the event's `seq`; a run's events have one seq order. When a stopped run is resumed, its continuation's first event takes the seq of the `run_error` that ended the stopped stream (that ending is replaced, not kept). A client following with `Last-Event-ID` across a resume must expect the seq of the last ending it saw to come again, with another event.
- `GET /v1/runs/{id}/events` is single-replica: it follows a run this server executes, and reads a run another server executes from what was stored (its events up to now, no tail). Tailing across replicas is T090.
- Migration 0006 is T037's; T090's is 0007.
- The wire grew only: `retrying` events (`attempt`, `delay_ms`), `Error.run_id`, the run statuses `interrupted`, `suspended`, `canceled`, `rejected`, and the codes `run_final`, `run_leased`, `awaiting_decision`, `not_pending`, `shape_changed`, `run_failed`.

### For T099

- Migration 0006 is now T037's conversation record index, and 0007 is T090's; T099's columns, planned for 0006, take the next free number.

### Mutation list

Each plant alone, against the test named, then restored (by a script). 11 of 12 caught; the survivor is explained below.

| Plant | Caught by |
|---|---|
| The run's slot freed after its last event | `TestSlotFreeBeforeRunDone` (was timing-only in `TestAskRetriesOverCapacity`; made deterministic with a logger that holds the server) |
| The run view drops the stored error while the run has no ending | `TestRunViewBeforeItsEnding` (same) |
| Lease loss not mapped to interrupted | `TestRunLosesItsLease` |
| 409 `conversation_busy` without `run_id` | `TestRequestErrors` |
| `GET /v1/runs` ignores an unknown status | `TestListRuns` |
| `Last-Event-ID` ignored | `TestEventsOfAnEvictedRun`, agentclient `Events` tests |
| Cancel only interrupts | `TestCancelRun` |
| A decision's approval inverted | `TestDecisions` |
| The reconnect grace never interrupts | `TestClientDisconnect/past_the_grace_*` |
| An unknown `AGENTKIT_STORE` falls back to memory | `TestServeRejectsBadSetup/agent:_unknown_store` |
| `agentkit chat` closes the stream on Ctrl-C without stopping the run | `TestChatCtrlCCancelsRun` (now counts the stop) |
| **Survived:** `serve` does not start the recovery loop | Nothing in `cmd/agentkit`: the API starts only interactive runs, which the loop never resumes, so no serve test has a run for it. `runner.Recovery` has its own tests; the wiring is three lines in `serve.go`. E14 (background runs over the API) is where a serve-level test belongs. |

The flakes the repeated runs found were fixed in the tests: `TestShutdownThroughTheRuntime` and `TestDrainEndsRunsWithShutdown` wait for two timers (the drain's and the lease renewal's), and the kill e2e waits until its client has the first turn before the kill.
