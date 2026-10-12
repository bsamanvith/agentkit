---
id: T072
title: "Triggers: schedules and inbound webhooks that start background runs"
epic: E14
status: todo
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

- [ ] **Schedules:** a schedule fires at the right times across a DST change (fake clock); two replicas sharing a store fire it once; after a 3-hour outage an hourly trigger fires once, not three times.
- [ ] **Webhooks:** a signed webhook starts a run with the payload as a data block. Unsigned, badly signed, replayed or oversized requests are refused, and no run starts.
- [ ] **Overlap:** a trigger whose previous firing is still running does not start a second one (recorded as skipped).
- [ ] **Mutation list** recorded.
