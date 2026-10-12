---
id: T086
title: "Steering a running task"
epic: E17
status: todo
depends_on: [T071]
---

# T086: steering

## Context

A person watching a task's transcript can nudge it ("focus on 2026 data", "skip the PDF"), without stopping it. Decisions: [E17](E17-tool-safety-control.md) ("Steering").

## Scope

- **Queueing:** `POST /v1/tasks/{id}/messages {text}` queues a steering message. The runner delivers it at the next turn boundary as a user message marked `steer`, and emits a `steered` event.
- **Turn boundaries:**
  - several queued messages are delivered together, in order;
  - a message sent after the final turn is refused with a clear status (the task finished).
- **Pause and resume:** `POST /v1/tasks/{id}/pause` stops at the next boundary (status `paused`); `resume` continues; pause never interrupts a tool call in flight.
- **Clients:** `agentclient` and the OpenAPI spec.

## References

- Claude Code's message queueing while a turn runs (what users expect of steering)
- E05's turn atomicity and one-unfinished-run rule (steering must not break them)

## Acceptance criteria

- [ ] **Delivery:** a steering message sent during a tool call is delivered after that call's results and before the next model call (event order checked); the transcript stays valid.
- [ ] **Ordering:** two messages are delivered together, in order; a message after completion is refused.
- [ ] **Pause and resume:** pause stops at the boundary, resume continues; a restart while paused stays paused.
- [ ] **Mutation list** recorded.
