---
id: T073
title: "Agent messaging: mailboxes, agent_message and agents_list tools, wake on reply, permissions and loop limits"
epic: E14
status: todo
depends_on: [T071]
---

# T073: agent messaging

## Context

Agents that work together by sending each other messages, synchronously (delegate and wait) or asynchronously (send, carry on, get woken by the reply). Decisions: [E14](E14-background-agents-messaging.md).

## Scope

- **Message log** in the store (tenant-scoped):
  - `{id, thread_id, from, to, text, data?, in_reply_to?, hop, created_at, delivered_at, processed_at}`;
  - at-least-once delivery, and exactly-once processing per message ID;
  - ordered per thread.
- **Tools** (offered to agents whose config has `can_message`):
  - `agents_list()`: the agents this one may message, with their descriptions;
  - `agent_message(to, text, thread?, wait?=false, timeout?)`:
    - `wait: true` returns the reply, or a timeout with the thread ID;
    - `wait: false` returns the thread ID at once;
  - `thread_read(thread)`.
- **Delivery and wake-up:**
  - a message to an agent starts or continues a **background run** of the recipient, in a conversation per thread;
  - the recipient's final answer is sent back as the reply;
  - a reply to a sender that isn't waiting **wakes** it: a new background turn in the sender's conversation, containing the reply labelled with its sender.
- **Limits:**
  - `can_message` allowlist;
  - hop limit per thread (default 8);
  - per-thread token and time budget;
  - per-agent send rate.
  - A thread over a limit is stopped, and both sides get a clear error.
- **Addresses:** `agent:<name>` only; `a2a:` addresses are refused with a clear error until E13's T068.
- **API:** `GET /v1/threads` and `GET /v1/threads/{id}`.

## References

- Claude Code's sessions and teammates messaging (`SendMessage`, mailboxes, a peer's message treated as data rather than an instruction) for the user model.
- [A2A specification](https://a2a-protocol.org/latest/specification/): tasks, contexts and `input-required`, so threads map onto A2A later.
- The actor model and mailboxes (Erlang/OTP, Akka) for delivery and ordering semantics.
- AutoGen and CrewAI multi-agent conversation patterns, and their known failure modes (loops, runaway cost), for the limits.

## Acceptance criteria

- [ ] **Sync delegation:** A sends with `wait: true`, B answers, and A's run continues with the reply in one run.
- [ ] **Async:** A sends with `wait: false` and finishes; B's reply later wakes A with a new turn that contains it, labelled as from B.
- [ ] **Exactly-once processing:** a message delivered twice (simulated crash after delivery, before ack) is processed once.
- [ ] **Ordering:** a thread's messages are processed in order, including when the recipient was busy (queued).
- [ ] **Limits:** a message to an agent outside `can_message` is refused. A ping-pong loop stops at the hop limit with errors to both. The budget stop and the rate limit each have a test.
- [ ] **Trust:** a message's text never appears as system or user instructions to the recipient (request snapshot).
- [ ] **Simulation:** a seeded simulation (§2.6) of several agents messaging, with crashes and restarts, checks the log invariants after every step.
- [ ] **Mutation list** recorded.
