---
id: T071
title: "Tasks: detached runs, stored event log, resumable event stream, completion webhooks"
epic: E14
status: todo
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

- [ ] **Detached:** a background run continues after the starting client disconnects, and finishes. Two watchers attached at different times both see every event in order, with no gaps or duplicates (`Last-Event-ID` resume tested mid-stream).
- [ ] **Restart:** killing the process mid-run and restarting it resumes the run (T030 recovery), and watchers reconnecting with `Last-Event-ID` continue seamlessly (replay e2e with the real binary).
- [ ] **Cancel:** cancel works and is idempotent. A foreground chat run is still stopped by a disconnect: it is interrupted, not canceled, after T037's reconnect grace (T100), so the user can continue it.
- [ ] **Webhook delivery:** signed and verified by a test receiver; retried after a 500 (fake clock); never sent twice for one completion (idempotency key).
- [ ] **Limits:** the concurrent-background limit gives 429; the wall-clock limit ends the run `failed` with a clear code.
- [ ] **Mutation list** recorded.
