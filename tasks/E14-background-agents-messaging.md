---
id: E14
title: "Background agents and agent-to-agent messaging"
status: todo
---

# E14: background agents and agent messaging

Owner request (2026-10-12): agents that **run in the background** (not tied to an open browser tab or a CLI waiting on a stream), and agents that can **message other agents**.

Today a run lives as long as its HTTP request: closing the stream cancels it (T036). Making runs outlive requests needs runs, events and conversations stored and owned by a process that can resume them. That is the **runner** (T030) on the **SQLite store** (T029), so this epic starts after them, and they move up the plan.

## Decisions (made in planning; change them in the task that finds them wrong)

| Question | Decision |
|---|---|
| What "background" means | A run started with `"background": true` returns `202` with its IDs immediately. The runner owns it: closing a client connection never cancels it (only `POST /v1/runs/{id}/cancel` does), it survives a restart (recovery, T030), and it can run for hours within its budgets. Foreground runs keep today's behaviour. |
| Watching a background run | Every run's events are **stored** (an append-only event log per run). `GET /v1/runs/{id}/events` streams them as SSE from the start, or from `Last-Event-ID`, then follows live ones. Any number of clients can watch, and they can come and go. `GET /v1/runs?status=running&background=true` lists them. |
| Being told when it finishes | Optional per run: a **webhook** (`notify: {url}`; signed with an HMAC header, retried with backoff), and the console's notifications. Later: email or Slack through connectors. |
| What starts background runs | API calls, **schedules** (cron, per agent, with a fixed prompt) and **inbound webhooks** (a signed URL per trigger that starts a run with the payload as data) (T072). Plus **messages from other agents** (T073). |
| Agent messaging model | **Mailboxes, not shared memory.** Each agent instance has an inbox. An agent sends with a tool, `agent_message(to, text, thread?, wait?)`: <br>- `wait: true` blocks until the recipient replies, like delegating to a sub-agent within one run; <br>- `wait: false` returns at once with a thread ID, and the recipient's reply arrives later as a new message that **wakes** the sender (a new background turn in the sender's conversation). <br>`agents_list()` shows whom it may message. |
| Addresses | `agent:<name>` for our agents now. `a2a:<card URL>` for remote A2A agents later (E13), so one tool reaches both. Internally a message thread maps onto A2A's task and context model, so the two stay compatible. |
| Delivery | Messages are stored in a **message log** (store, tenant-scoped), delivered **at least once** and processed **exactly once** per message ID (idempotency key = message ID). Ordering is per thread. A recipient that's busy queues the message; the runner delivers it when the conversation is free (one unfinished run per conversation, E05). |
| Safety limits | **Who:** `agents[].can_message: [...]` lists the recipients an agent may message; anything else is refused. **Loops:** a thread has a hop limit (default 8) and a total token and time budget, and the runner stops a thread that exceeds them, with a clear error to the sender. **Storms:** per-agent rate limits on sends. **Trust:** a message from another agent is shown to the recipient as data from that agent (labelled sender), never as system or user instructions. |
| Flow vs messaging | `flow` (E04) stays the way to write a **fixed** multi-step or multi-agent process (sequence, parallel, approve). Messaging is for **dynamic** collaboration the agents decide on. Both run on the runner, and a flow step can send and await a message. |
| Visibility | Every background run and message thread is visible in the console: what's running, who messaged whom, the thread's messages, cost so far, and a cancel button (T074; laid out in T070). |

## Tasks

- [ ] [T071](T071-background-runs.md) Background runs: detached runs, stored event log, resumable event stream, listing, cancel, completion webhooks
- [ ] [T072](T072-triggers.md) Triggers: schedules and inbound webhooks that start background runs
- [ ] [T073](T073-agent-messaging.md) Agent messaging: mailboxes, `agent_message`/`agents_list` tools, wake on reply, permissions and loop limits
- [ ] [T074](T074-console-background-view.md) Console: background runs and agent threads

Order:
1. **T029 (SQLite store) and T030 (runner) first.** They are now on the critical path: start them now.
2. Then T071.
3. T072 and T073 in parallel after T071.
4. T074 after T071 and the console (T055), laid out by T070.
