---
id: T073
title: "Agent messaging: mailboxes, agent_message and agents_list tools, wake on reply, permissions and loop limits"
epic: E14
status: done
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

- [x] **Sync delegation:** A sends with `wait: true`, B answers, and A's run continues with the reply in one run.
- [x] **Async:** A sends with `wait: false` and finishes; B's reply later wakes A with a new turn that contains it, labelled as from B.
- [x] **Exactly-once processing:** a message delivered twice (simulated crash after delivery, before ack) is processed once.
- [x] **Ordering:** a thread's messages are processed in order, including when the recipient was busy (queued).
- [x] **Limits:** a message to an agent outside `can_message` is refused. A ping-pong loop stops at the hop limit with errors to both. The budget stop and the rate limit each have a test.
- [x] **Trust:** a message's text never appears as system or user instructions to the recipient (request snapshot).
- [x] **Simulation:** a seeded simulation (§2.6) of several agents messaging, with crashes and restarts, checks the log invariants after every step.
- [x] **Mutation list** recorded.

## Notes

### What was built

- **Message log** (`store.MessageLog`, `store.MessageAdmin` in `pkg/store/messages.go`): messages `{id, thread, from, to, from_conversation, to_conversation, kind (request, reply, notice), text, data, in_reply_to, hop, state (queued, running, done), run_id, attempts, created/delivered/processed_at}` and threads `{status (open, stopped), stop code and reason, hops, tokens}`, in memory, SQLite (migration 0015) and Postgres (0016, with RLS), checked by `storetest.RunMessageLog`. A send is idempotent by message ID; delivery is a lease claim (`ClaimMessage`, `ClaimDueMessage` across tenants); a queued message is due only when no lower hop of its thread to the same conversation is queued (the mailbox order).
- **`pkg/agentmsg`**: `Messenger` (agents, `can_message`, limits, a per-tenant token bucket per agent) with the tools `agents_list`, `agent_message(to, text, thread?, wait?, timeout?)` and `thread_read(thread)`; and `Dispatcher`, which claims the next due message, starts a background run of the recipient through `Runs` (`agentapi.Server.Deliver`), and when a request's run ends sends its answer back as the reply. A sender that waits takes the reply in its tool call (it claims it before the dispatcher does); one that does not is woken by the reply as a new background run in its conversation. A run's input is a fixed label and the message as one JSON line (`agentmsg.Envelope`), never the system prompt or the user's words.
- **Limits**: `can_message` (`not_allowed`), hop limit (default 8), thread token budget (500k, counted from the usage of every run a message of the thread starts, when it ends) and time budget (1h from the first message), per-agent send rate (burst 10, then one per 2s). A send over a thread limit stops the thread: the sender gets the error (`hop_limit`, `token_budget`, `time_budget`), every other conversation in it a notice; a stopped thread takes notices only (`thread_stopped`). Addresses: `agent:<name>`; `a2a:` is `a2a_unsupported`.
- **Agent API**: `GET /v1/threads` (newest first, cursor pages) and `GET /v1/threads/{id}` (with messages) in the OpenAPI spec; `WithRunContext` (every execution of a run's agent runs in a context made from the run's agent and conversation: how the tools learn the sender); `Server.Deliver` and `Server.RunResult` for the dispatcher.
- **serve**: `agents[].can_message`, `agent_api.messaging.{max_hops, max_tokens, max_duration, send_every, send_burst}`; one dispatcher per process beside recovery (`cmd/agentkit/messaging_agent.go`, behind `!noagent`), only when an agent has `can_message`. Docs in QUICKSTART.

### Decisions

- Migrations SQLite 0015 and Postgres 0016, the next free on main at push time (`11eec57`): T075 (bsamanvith/agentkit#82, `items`) merged first with 0014 and 0015, so T073 renumbered.
- One conversation per thread and recipient (`conv_<thread>_<agent>`), made on first delivery; a sender's replies go to the conversation it sent from (its task or chat). A conversation runs one run at a time: a message for a busy one is retried after `WithRetry` (1s) and stays behind in hop order.
- Exactly-once: before starting a run the dispatcher looks in the recipient conversation's history (since the message was first delivered) for a prompt carrying the message's ID, and takes that run if there is one; so a dispatcher that died between starting the run and recording it does not start a second.
- Replies and notices get no reply of their own: a thread ends unless an agent sends again. The recipient's answer to a request is its reply, also when its run failed (the reply's `data` has the error).
- The token budget is checked at each send (and at each reply, with the tokens about to be added), not mid-run: a run in a thread can overrun it by its own usage, bounded by the agent's own `max_total_tokens`. Likewise the time budget.
- The rate limit is per process (an in-memory bucket per tenant and agent): with N replicas an agent may send up to N times as fast. Good enough against storms; a store-backed limiter is a follow-up if it matters.
- `GET /v1/threads` needs `agent:read`, as `GET /v1/runs` does: threads are the tenant's, as runs are.
- An agent on a remote runner (`AGENTKIT_RUNNER_URL`) cannot have `can_message` yet (the sender does not cross to the runner): startup fails saying so. It can still be messaged.
- The `wait` default is 2 minutes, at most 10 (`WithWait`); a timeout returns the thread, and the reply then wakes the sender later.

### Tests per criterion

- Sync delegation: `TestSyncDelegation` (pkg/agentmsg, real agent API, fake models), and `TestServeAgentMessaging` (cmd/agentkit, through serve's config and wiring).
- Async: `TestAsyncReplyWakesTheSender` (the wake-up's request carries the reply, labelled from B, in A's conversation).
- Exactly-once: `TestDeliveredOnceAfterACrash` (the first dispatcher dies after starting B's run; the second takes over after the lease; B runs once, its model sees the message once); storetest `ClaimAndUpdate`, `ConcurrentClaimsNeverShare`.
- Ordering: `TestMessagesOfAThreadAreProcessedInOrder` (recipient busy, messages queued); storetest `DueInMailboxOrder`.
- Limits: `TestRecipientsOutsideCanMessageAreRefused` (and `a2a:`), `TestPingPongStopsAtTheHopLimit` (errors to both), `TestThreadBudgets` (token and time), `TestSendsAreRateLimited`; serve config `TestServeAgentMessagingRefused`.
- Trust: `TestAMessageIsDataNotInstructions` (request snapshot `testdata/delivered_request.txt`: the system prompt is the recipient's own, the text only inside the JSON line).
- Simulation: `TestSimulation` / `FuzzSimulation` (`sim_test.go`): 60 seeds of three agents messaging each other, two dispatchers crashing before or after starting a run or after any write, runs ending and sending in their thread, the clock moving; the log invariants (dense hops, hop limit, notices last, replies to their request's sender once and only after its run ended, at most one run per message and the one it names, per-conversation hop order) after every step; after the drain every message done and every request of an open thread answered. It checks that every crash kind and every stop code is reached.
- API: `TestThreads`, `TestOpenAPISpec`, `TestOpenAPIResponses`.

### Mutation list

Each planted alone; the named tests fail.

| # | Bug planted | Caught by |
|---|---|---|
| M1 | the dispatcher's exactly-once look in the history never matches | TestDeliveredOnceAfterACrash, TestSimulation |
| M2 | memory `ClaimDueMessage` ignores the mailbox order | TestSimulation, storetest DueInMailboxOrder (memory) |
| M3 | no hop limit | TestPingPongStopsAtTheHopLimit, TestSimulation |
| M4 | `can_message` not checked | TestRecipientsOutsideCanMessageAreRefused |
| M5 | no rate limit | TestSendsAreRateLimited, TestSimulation |
| M6 | the message text also outside the JSON line of the prompt | TestAMessageIsDataNotInstructions |
| M7 | a request's run's tokens not added to the thread | TestSimulation (no token_budget stop reached) |
| M8 | no stop notices | TestPingPongStopsAtTheHopLimit |
| M9 | a waiting sender ignores its reply | TestSyncDelegation |
| M10 | no reply sent when a request's run ends | TestSyncDelegation, TestAsyncReplyWakesTheSender, TestSimulation |
| M11 | `WithRunContext` not applied | TestSyncDelegation, TestServeAgentMessaging |
| M12 | the time budget never stops a thread | TestThreadBudgets |
| M13 | `thread_read` of a thread the agent is not part of | TestThreadReadShowsOnlyItsParts |
| M14 | SQLite `ClaimDueMessage` ignores the mailbox order | storetest DueInMailboxOrder (sqlite) |
| M15 | `GET /v1/threads` oldest first | TestThreads |
| M16 | a run a message started may start a fresh thread (`triggeredBy` returns "") | TestALoopOfNewThreadsStopsAtTheHopLimit |
| M17 | a reply's or notice's run is done at delivery, its tokens not counted | TestAWakeUpCountsInTheThreadsTokens |
| M18 | `can_message` to an agent with a toolset the sender lacks | TestNoMessagingToMoreToolsets, TestServeAgentMessagingRefused |
| M19 | the exactly-once look stops at the first delivery's time, no skew allowed | TestDeliveredOnceAcrossSkewedClocks |
| M20 | a waiting sender takes any notice of the thread, not only its conversation's | TestAWaitTakesOnlyItsOwnNotice |

### Review fixes

Review of PR #83 (each fix has a test that failed before it):

- **A loop could dodge the hop limit by starting a new thread per send.** A run woken by a reply (or started by a request) that called `agent_message` without `thread` got a fresh thread, so an A-B ping-pong never stopped (rate limit only slowed it). Now a run that a message started sends in that message's thread when it names none (`triggeredBy`: the conversation's latest prompt, if it carries a message of the log sent to that conversation). A run that no message started (a user's chat or task) still starts new threads. Test: `TestALoopOfNewThreadsStopsAtTheHopLimit`.
- **Wake-up runs were free.** A reply's or notice's run was marked done at delivery, so its usage never reached the thread's token budget. Every delivered message now stays `running` until its run ends and adds the run's tokens; only a request sends a reply. Test: `TestAWakeUpCountsInTheThreadsTokens`.
- **`can_message` lent toolsets.** A key may run an agent only if it may use all of the agent's toolsets (`access.agent`), but a key that may run A could use B's toolsets through A's messages. `agentmsg.New` now refuses `can_message` to an agent with a toolset (or runtime toolset) the sender does not have. Tests: `TestNoMessagingToMoreToolsets`, `TestServeAgentMessagingRefused` ("toolsets").
- **Exactly-once under clock skew.** `delivered` stopped looking at entries older than the message's first delivery, stamped by possibly another replica's clock; a prompt stamped by a clock behind it was missed and the message ran twice. It now looks back `maxClockSkew` (1 minute) further. Test: `TestDeliveredOnceAcrossSkewedClocks`.
- **Stored agents (T075, merged first) and `can_message`.** The messenger knows only the config file's agents, so a stored agent with `can_message` would have got no tools without a word; the registry now refuses the field, as it refuses memory and UI. Test: `TestStoredAgentRefused` ("can_message").
- **Test gap:** no test caught a waiting sender taking another conversation's stop notice (M20). Test: `TestAWaitTakesOnlyItsOwnNotice`.

Considered and left as they are:

- Per-user visibility of threads: conversations, runs and their histories are tenant-wide (`GET /v1/conversations`, `/v1/runs`), and a thread holds nothing its conversations' histories do not, so threads being tenant-wide leaks nothing new. Per-user threads need per-user conversations first (and the thread's origin, above).
- The hop check and the send are not one transaction: concurrent sends in one thread can overshoot the limit by the number of sends in flight, after which every send is refused. Bounded; a store-side limit would need the limit in the store API.
- Rate limit per process: as decided above.
- Prompt injection: the text is only ever inside the JSON line (newlines and quotes escaped), and in tool results; a user can forge an envelope only in their own conversation, where `triggeredBy` and `delivered` also check the message's conversation in the log.
- Tenant isolation: both tables have RLS forced like the others, and `TestRowLevelSecurity` covers them; the cross-tenant claim goes through the admin pool as `ClaimDue` does.
- A message to an agent the server no longer runs is retried every second for ever (and logged); worth a follow-up that answers such a request with an error.

### For T074 (console) and later

- The Activity view's Threads: `GET /v1/threads` and `/v1/threads/{id}` give participants (`from`/`to`), hops (`hops` of `max_hops`), tokens, each message's sender and its `run_id`. The limits are not in the API yet; add them to `GET /v1/threads/{id}` if the view shows "3 of 8".
- A thread's origin (which user's task or chat started it) is not recorded; per-user filtering of threads needs it.
- An operator stop (`StopThread` exists in the store) has no endpoint yet.
- E13 (T068 merged as a client): `a2a:` addresses could route to a remote agent through `pkg/a2a/client`; `parseAddress` is the one place to change.
