---
id: T072
title: "Triggers: schedules and inbound webhooks that start background runs"
epic: E14
status: done
depends_on: [T071]
---

# T072: triggers

## Context

Background agents need things that start them without a person: a schedule ("every weekday at 9, summarize the news on X") or an event from another system (a webhook). Decisions: [E14](E14-background-agents-messaging.md).

## Scope

- **Triggers:**
  - `/v1/triggers` (create, list, update, delete, pause; tenant-scoped, admin scope);
  - also in config for static ones.
  - A trigger holds: agent, prompt template, and target conversation (new each time, or one fixed conversation), plus either a schedule or a webhook.
- **Schedules:**
  - cron syntax with a time zone (`CRON_TZ`);
  - at most one firing in flight per trigger;
  - after downtime, missed firings run at most once (no stampede).
  - Firing is leased through the store, so with several replicas each firing happens once.
- **Inbound webhooks:**
  - each trigger gets a URL with a random ID and a secret; the request must be HMAC-signed (Standard Webhooks);
  - the body is passed to the run as a **data block** (labelled third-party data), never as instructions;
  - size limit; replay protection by timestamp and ID.
- Every firing is recorded (time, run ID, outcome) and visible through the API.

## References

- [Standard Webhooks](https://www.standardwebhooks.com/)
- [robfig/cron](https://github.com/robfig/cron) (parsing, `CRON_TZ`); evaluate a small dependency vs our own parser.
- Claude Code Routines and GitHub Actions `schedule` semantics (missed runs, time zones) for user expectations.

## Acceptance criteria

- [x] **Schedules:** a schedule fires at the right times across a DST change (fake clock); two replicas sharing a store fire it once; after a 3-hour outage an hourly trigger fires once, not three times.
- [x] **Webhooks:** a signed webhook starts a run with the payload as a data block. Unsigned, badly signed, replayed or oversized requests are refused, and no run starts.
- [x] **Overlap:** a trigger whose previous firing is still running does not start a second one (recorded as skipped).
- [x] **Mutation list** recorded.

## Notes

### What was built

- `pkg/trigger`: `Definition` (agent, prompt template with `{{trigger}}`, `{{time}}`, `{{date}}`, fixed conversation or a new one each time, schedule or webhook, `static`), `ParseSchedule` (five-field cron, `@hourly`... macros, names, ranges and steps, `CRON_TZ=`), `Schedule.Next` with cron's daylight-saving rules, `Message` (the prompt, then the webhook body as a separate `<third_party_data>` block, HTML-escaped so it cannot close the block), and `Scheduler` (a pass over `store.TriggerAdmin.DueTriggers` every 10s, firing through `Server.FireDue`).
- Store: `store.TriggerStore` (CAS on version, `StartFiring` claims a firing by its ID and, for a schedule, by the exact due time, `EndFiring`, `ListFirings` keeping the newest 200) and `store.TriggerAdmin` (`DueTriggers`, `TriggerByHook` across tenants), in memory, SQLite (migration 0016) and Postgres (migration 0017, row-level security like the other tables), shared rules in `pkg/store/internal/backend`, conformance in `storetest.RunTriggerStore`.
- `pkg/agentapi`: `/v1/triggers` (create, list, get with the webhook secret, PATCH with `version` and `rotate_webhook`, delete, pause, resume, firings), `POST /v1/hooks/{id}` (public route: the signature is the credential), `FireDue`, `SyncTriggers` for static triggers. Firings start tasks through T071's `startTask` (background, autonomous, the tenant's background limit, the agent's `notify`). OpenAPI updated and probed (every documented status).
- `pkg/trigger/signature.go`: `SignStandard`, `VerifyStandard`, `EncodeStandardSecret`/`DecodeStandardSecret` (Standard Webhooks, checked against the spec's test vector). In `pkg/trigger`, not `pkg/webhook` (completion webhooks): the runner and toolsets images import `pkg/agentapi` for its wire types, and `make deps-check` keeps `pkg/webhook` out of them.
- `cmd/agentkit`: `agents[].triggers` in the config (static, default tenant, stored at startup), the scheduler goroutine next to the notifier, the trigger store and admin from `storeopen.Opened.Triggers`, and webhook secrets derived from `AGENTKIT_WEBHOOK_SECRET` (`DeriveSecret(master, "trigger\x00"+tenant+"\x00"+hookID)`), all behind `!noagent`. Docs: QUICKSTART (config row and a Triggers paragraph), `docs/api/openapi.yaml`, a SIZES row.

### Decisions

- Our own cron parser (about 250 lines, 11 KB of symbols) instead of `robfig/cron`: no dependency, and the DST rules are ours to test. Rules: a time a spring-forward skips fires at the change, once; a time a fall-back repeats fires once, the first time; a schedule with `*` hours follows the clock's minutes (fires in both runs of a repeated hour, none in a skipped one). Default zone UTC.
- Missed firings after downtime: one firing, for the latest missed due time (its prompt's `{{time}}`), then the schedule goes on from now. Resuming a paused trigger skips what it missed.
- Overlap: a firing is skipped (recorded, with the reason) while the trigger's newest started run has not ended, while another of its firings is pending (claimed, run not started yet; one pending for more than 10 minutes is taken as lost and marked failed), or while its fixed conversation has a run going on. A run that failed and is retryable (recovery will resume it) counts as going on.
- Replay protection: the webhook-id is the firing's ID, unique per trigger in the store (kept for the newest 200 firings), plus the 5-minute timestamp tolerance. A delivery whose firing failed to start a run may be retried under the same ID. Paused trigger: 409, nothing recorded. Body limit 64 KiB (`Triggers.MaxBodyBytes`), JSON only.
- Webhook secrets are derived, not stored: rotating means a new hook ID (`rotate_webhook`), and the old URL is 404. Without `AGENTKIT_WEBHOOK_SECRET` schedules still work and webhook triggers are refused (400).
- Static triggers come from `agents[].triggers` (one place in the config, the agent is implied) and live in the default tenant; the API may pause and resume them only (409 `static_trigger` otherwise). A config change replaces the stored one (its schedule goes on from now); a removed one is deleted; an API trigger with the same ID is left alone and logged.
- Scope: every `/v1/triggers` route needs `agent:admin`.

### Tests per criterion

- Schedules: `TestScheduleAcrossDST` (agentapi, fake clock, New York spring and fall, prompts in the zone) and `TestScheduleAcrossDaylightSaving` (pkg/trigger); `TestScheduleTwoReplicasFireOnce` (two servers, one store and bus, concurrent passes for five hours: one firing and one run per hour); `TestScheduleOutageFiresOnce` (3h10m outage of an hourly trigger: one firing for 12:00, next 13:00). Store side: `storetest.RunTriggerStore` (concurrent `StartFiring` of one due time, due-time mismatch, paused) on all three backends (Postgres run here with `make integration STORE=postgres`'s deps).
- Webhooks: `TestWebhookStartsRun` (the prompt and a separate labelled, escaped data block; the firing records the run) and `TestWebhookRefusals` (unsigned, bad signature, stale, replayed, oversized, not JSON, unknown hook, paused: no run, no model call); `TestVerifyStandardRefuses`, `TestStandardVector`; `TestServeStaticTriggers` (cmd: config triggers stored, derived secret, signed delivery starts a run).
- Overlap: `TestTriggerOverlapSkips` (schedule, webhook and a shared fixed conversation: recorded as skipped with the reason, the next one after the run ends starts), `TestLostFiring`.
- Also: `TestTriggerCRUD`, `TestTriggersNeedTheirConfig`, `TestTriggerFixedConversation`, `TestWebhookFailedFiringRetries`, `TestStaticTriggers`, `TestOpenAPISpec`/`TestOpenAPIResponses`.

### Mutation list

Each planted alone, then the named tests run; all were killed.

1. `Schedule.Next` does not fire a skipped time at the change: `TestScheduleAcrossDST`.
2. `Schedule.Next` fires a repeated hour twice: `TestScheduleAcrossDST`.
3. The prompt's time rendered in UTC, not the schedule's zone: `TestScheduleAcrossDST`.
4. The store does not check the due time in `StartFiring`: `storetest.RunTriggerStore` (memory). (`TestScheduleTwoReplicasFireOnce` alone does not kill it: the firing ID, which is the due time, already refuses the second replica.)
5. Catch-up moves on from the due time, not now (each missed hour fires): `TestScheduleOutageFiresOnce`.
6. Catch-up fires the first missed time, not the latest: `TestScheduleOutageFiresOnce`.
7. `VerifyStandard` accepts any signature: `TestVerifyStandardRefuses`, `TestWebhookRefusals`.
8. A replayed firing ID is not a conflict: `TestWebhookRefusals`, `TestTriggerOverlapSkips`.
9. No body size limit: `TestWebhookRefusals`.
10. The body not HTML-escaped in the data block: `TestWebhookStartsRun`.
11. The body appended to the prompt's text, no data block of its own: `TestWebhookStartsRun`.
12. The overlap check ignores a run going on: `TestTriggerOverlapSkips`.
13. A stale timestamp accepted: `TestVerifyStandardRefuses`, `TestWebhookRefusals`.
14. A paused trigger is due: `storetest.RunTriggerStore` (memory).

### For T074

- The console's trigger list reads `GET /v1/triggers` (`next_fire_at`, `last_firing`, `paused`, `static`) and pauses with `POST /v1/triggers/{id}/pause|resume` (scope `agent:admin`); `GET /v1/triggers/{id}` shows the webhook path and secret.

### Follow-ups

- Static triggers for tenants other than the default (the API takes a `StaticTrigger.Tenant`, the config does not).
- A trigger's firings are kept for the newest 200; a longer history would need its own retention setting.
