---
id: T071
title: "Tasks: detached runs, stored event log, resumable event stream, completion webhooks"
epic: E14
status: done
depends_on: [T030, T029, T037, T090]
---

# T071: background runs

## Context

Runs that outlive the request that started them. Decisions: [E14](E14-background-agents-messaging.md).

## Scope

- **Starting a task:** `POST /v1/tasks {agent, input, interaction?, policy?}` returns `202` with the task ID. *Task* is the owner's name for every run that isn't a chat; a task has its own transcript, not a chat conversation. The runner owns it: a client disconnect never cancels it. Chats keep `POST /v1/runs`.
- **Cancel:** `POST /v1/runs/{id}/cancel`. It is idempotent, and the run ends `canceled` with the E05 marker rule.
- **Event log** (built on E18's T090: durable events in the store's event log, live events on the bus):
  - every run's events (both modes) are appended to a stored log;
  - `GET /v1/runs/{id}/events` replays them from the start or from `Last-Event-ID`, then follows live events until the terminal one;
  - many watchers at once;
  - the log is kept by the retention policy (initially as long as the conversation).
- **Listing:** `GET /v1/runs?status=&background=&agent=` with cursor paging.
- **Completion webhooks:**
  - `notify: {"url": ...}` per run (or the agent's default);
  - the POST body carries the run summary, signed `Agentkit-Signature: t=<ts>,v1=<hmac-sha256>` with a per-tenant secret;
  - retries with backoff for 24 h;
  - delivery attempts are recorded.
- **Limits:** per-tenant concurrent background runs (`429 too_many_background_runs`), plus per-run budgets and a wall-clock limit (default 2 h).
- `agentclient` (T053) and the OpenAPI spec (T062) gain all of the above.

## References

- [OpenAI Responses API background mode](https://platform.openai.com/docs/guides/background) and the Claude API Batches/async patterns (find current docs with the `claude-api` skill): how others expose detached runs and polling.
- [WHATWG SSE `Last-Event-ID`](https://html.spec.whatwg.org/multipage/server-sent-events.html) for resuming streams.
- [Standard Webhooks](https://www.standardwebhooks.com/) (signing, retries, idempotency headers); Stripe's webhook signatures for the `t=,v1=` scheme.
- Temporal and Inngest docs on durable execution, for what users expect of long-running jobs (visibility, cancel, retries).

## Acceptance criteria

- [x] **Detached:** a background run continues after the starting client disconnects, and finishes. Two watchers attached at different times both see every event in order, with no gaps or duplicates (`Last-Event-ID` resume tested mid-stream).
- [x] **Restart:** killing the process mid-run and restarting it resumes the run (T030 recovery), and watchers reconnecting with `Last-Event-ID` continue seamlessly (replay e2e with the real binary).
- [x] **Cancel:** cancel works and is idempotent. A foreground chat run is still stopped by a disconnect: it is interrupted, not canceled, after T037's reconnect grace (T100), so the user can continue it.
- [x] **Webhook delivery:** signed and verified by a test receiver; retried after a 500 (fake clock); never sent twice for one completion (idempotency key).
- [x] **Limits:** the concurrent-background limit gives 429; the wall-clock limit ends the run `failed` with a clear code.
- [x] **Mutation list** recorded.

## Notes

### What was built

- `POST /v1/tasks` (`pkg/agentapi/tasks.go`): a background run (no `runner.Interactive()`) in a conversation of its own, `202` with the run, `Location` and the run headers. `interaction` accepts only `autonomous` (or none), and every task runs with T084's `autonomous` mode whatever its agent's, so it never suspends; `policy` is the run's tool policy as for chats.
- Watching: `GET /v1/runs/{id}/events` on a task replays the stored log after `Last-Event-ID` and follows the bus; a task's streamed `shutdown`/`interrupted` ending (its process stopped) is skipped (`goesOn`), and the watcher waits for the process that takes the run up (`awaitsPickup`, `pickup`, `awaitHeld`), so a watch of a task ends only at `run_done` or a stored final error. No reconnect grace applies to tasks; the run timeout is for chats only.
- Store: `store.RunIndex` (`ListRuns` with status, kind and agent filters, cursor pages) and `store.NotificationLog` (one notification per run, its attempts, lease-claimed delivery) in memory, SQLite (migration 0013) and Postgres (0014, with RLS), in `storetest`.
- Listing: `GET /v1/runs` without `conversation_id` lists the tenant's runs (`status`, `background`, `agent`); the run gains `background` and `notify`.
- Limits: `WithMaxBackgroundRuns` (default 10, `429 too_many_background_runs`, `Retry-After: 30`), `WithTaskTimeLimit` (default 2h, `runner.WithTimeLimit`, from the run's start across resumes, `failed` with `wall_clock_limit`, not retryable).
- Pools (T105, merged first): a task is routed as a background run, takes a slot of its pool until it ends (`429 over_capacity` when the pool is full), and is put in that pool's lane, so the pool's dispatcher is the one that resumes it after a crash (`TestTaskTakesAPoolSlot`). The dispatchers take `AGENTKIT_RECOVERY_INTERVAL`.
- `pkg/webhook`: `Sign`/`Verify` (`Agentkit-Signature: t=,v1=`, HMAC-SHA256 of `t.body`), `DeriveSecret` per tenant, the notifier (claims due notifications, posts through `pkg/netguard`, `Agentkit-Webhook-Id` = run ID, retries 5s doubling to 1h for 24h, redirects are failures). `GET /v1/webhooks/secret` (admin) gives the tenant's secret.
- serve: `AGENTKIT_WEBHOOK_SECRET` (32+ bytes), `AGENTKIT_WEBHOOK_ALLOW_HOSTS`, `AGENTKIT_RECOVERY_INTERVAL`; the notifier runs beside recovery as agent-component wiring (`webhooks_agent.go`, behind `!noagent`; `make deps-check` keeps `pkg/webhook` out of the runner and toolsets images); agents' `notify` and `agent_api.max_background_runs`/`task_time_limit` config. `agentclient`: `StartTask`, `WebhookSecret`, `RunsQuery.Background`/`Agent`, `ErrTooManyBackgroundRuns`. OpenAPI updated.

### Decisions

- Migrations are SQLite 0013 and Postgres 0014, after T044's credential reconnect and T105's pool (SQLite needs dense numbers).
- A task's per-run budget is its agent's harness budgets (`budget_exceeded`) plus the wall-clock limit; per-request budget overrides are left to the owner (below).
- The background limit is counted in the store under a per-server lock: exact on one replica, best-effort across replicas (two replicas may each admit the last place at once).
- A watcher that follows a task across a hand-over may miss `text_delta` events the new process streamed before it followed; the `turn_done` has the text. Every other event arrives once, in order.
- `supervised` tasks (T084's unticked "Supervised" criterion) are not served: a suspended task needs a "waiting for input" state its watchers end on and a notification when it suspends (T084's hook: the runner's suspended outcome, `store.Run.Pending`), and an input endpoint (`Runner.Answer`). Left to E14 notifications.
- Size: +172 KB on agentkit, api and default; local and dev +143 and +147 KB; runner 0 and toolsets +8 KB (`noagent`). Under every budget (docs/SIZES.md).
- Webhooks are configured by environment (secret, allowed private hosts), not per agent file, so the secret stays out of config.

### Tests per criterion

- Detached: `TestTaskRunsDetached` (grace 0, two watchers, `Last-Event-ID` mid-stream), `TestTaskFinishesUnwatched`.
- Restart: `TestE2ETaskSurvivesARestart` (real binary, replay model, kill, restart, reconnect with `Last-Event-ID`, one signed delivery), `TestTaskWatchedAcrossARestart` (replicas, shared bus).
- Cancel: `TestTaskCancel`; the foreground disconnect: `TestClientDisconnect` (unchanged, still interrupted after the grace).
- Webhooks: `pkg/webhook` `TestDeliveryIsSignedAndVerified`, `TestRetriedAfterA500` (fake clock), `TestNeverSentTwice`, `TestGivesUpAfterTheWindow`, `TestWaitsForTheRunToEnd`; storetest `TestNotifications`; `TestTaskNotify`.
- Limits: `TestTaskLimit` (9 at once, limit 3, other tenant), `TestTaskLimitCountAndStartAreOneStep` (the count-and-start race, deterministic), `TestTaskLimitCountsTasksOnly`, `TestTaskWallClockLimit`, `TestTaskLimitsLeaveChats`, runner `timelimit_test.go`. Listing: `TestListTasks`, agentclient `TestTasks`.

### Mutation list

Caught: the run timeout applied to tasks (TestTaskLimitsLeaveChats hangs to the suite timeout); the reconnect grace applied to tasks (TestTaskRunsDetached, also by hang); no `goesOn` skip in the watch (TestTaskWatchedAcrossARestart); limit `>` for `>=` (TestTaskLimit); no `wall_clock_limit` classification, live or stored (TestTaskWallClockLimit); the limit counting chats (TestTaskLimitCountsTasksOnly, added for it); a wrong signature (three webhook tests); notifications not idempotent, delivery ignoring the lease (storetest TestNotifications).

Caught in review: no lock around count-and-start (TestTaskLimitCountAndStartAreOneStep, a store that holds the first count until a second has read).

Survived (40 and 20 runs): `WebhookPayload` not waiting for the stored ending (the race the wait covers is rare in-process); the `goesOn` skip in `pending` (reached only when a release is seen before the bus event). Kept as defense; follow-up below.

### Owner questions

- Supervised tasks: a `waiting_for_input` status (or `suspended` with a task's `interrupts`) and `POST /v1/runs/{id}/input` for tasks, in T072/T074 or a T071 follow-up?

- Per-request budgets on `POST /v1/tasks` (tokens, tool calls), or only the agent's?
- Should the background limit be exact across replicas (a store-side counter)?

### For T072/T073/T074

- Start background work with `StartOptions{Background: true}` through `Runs.Start`; it gets recovery, the wall-clock limit and webhooks for free. `Server.WebhookPayload` is the body for any run-ended hook.
- Follow-ups: a test of `WebhookPayload` against a run whose ending is stored late.
