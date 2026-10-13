---
id: T100
title: "runner: interactive chat runs stop with their user and are never recovered in the background"
epic: E05
status: done
depends_on: [T030, T088, T098]
---

# T100: interactive chat runs

## Context

**Today:** the recovery loop resumes every run whose lease expired or was released. That includes a chat run whose user closed the tab, or whose process died. The run then goes on in the background on any runner, with nobody watching.

**The owner's direction (2026-10-13):**
- **No background recovery.** A chat run with a user on the other end is never resumed in the background.
- **Stop means stop.** When the user presses stop or closes the tab, the run stops.
- **Interrupted runs are shown, not resumed.** The UI shows the run as interrupted, and the user can continue it.
- **Model errors.** A retryable model error is retried inline while the user waits, then the run fails.
- **A new message after an interruption** starts a new run. The owner asked that this follow what other harnesses do.

**What other harnesses do** (read in their source on 2026-10-13: opencode, Codex CLI, pi, Gemini CLI):
- A new message after an interruption always starts a fresh turn. None of them refuses it.
- None has a user-facing "continue" command.
- None sends the model a tool call without a result.
- Only opencode continues after a crash on its own. Codex shows the turn as Interrupted, and pi leaves the session as it was.

We keep the same rules, and "continue" is an extra: `Resume` sends the model exactly the requests that were never answered.

Background runs (webhooks, jobs, flows, E14) are unchanged: they keep the recovery loop and its retries.

## Scope

- **Store:**
  - Migration 0005 (SQLite and Postgres) adds `runs.interactive BOOLEAN NOT NULL DEFAULT false`; the memory store gets the same field.
  - `Create` takes variadic options (`store.CreateOption`, first `store.Interactive()`), so existing call sites don't change.
  - `Run.Interactive` reads it back.
  - `Resumable`, `ListResumable` and `ListResumableAll`, with their SQL predicate, never return an interactive run.
  - `Run.Interrupted(now)` reports a run that is interactive, `running`, and whose lease is free or expired. The API shows it as interrupted.
- **Runner:**
  - **`Interactive()` RunOption** on `Run` creates the run as interactive. It is stored on the run, so `Resume` keeps it.
  - **`Runner.Interrupt(ctx, runID)`** is the stop button:
    - **Live in this process:** the execution stops with the cause `ErrInterrupted`. It stores the turns it kept, releases its lease, and the run stays `running`, which makes it interrupted.
    - **Leased elsewhere:** `ErrLeased`. Routing the command to the owning replica is E18's job, as it is for `Cancel` today.
    - **Already free:** a no-op.
  - **Consumer stops reading:** an interruption, as today (store kept turns, release).
  - **A new message on an interrupted conversation:** `Run` on a conversation whose unfinished run is interactive and not live does two things in one call:
    - it cancels that run through the `Cancel` path, with the marker reply if the log ends on the user message;
    - it starts the new run.

    If the unfinished run is live, or is not interactive, `Run` returns `ErrRunUnfinished` as today.
  - **Continue** is `Resume(runID)` on an interrupted run. It already works; this task tests it for interactive runs.
  - **Inline retry:** a retryable model error (`llm.IsRetryable`) in an interactive run does not store `failed` with a `NextAttemptAt`. Instead the execution:
    - stores its kept turns;
    - emits `EventRetrying{Attempt, Delay}`;
    - waits on the runner's clock (an `Interrupt` or `ctx` ends the wait);
    - runs the harness again from its transcript, under the same lease.

    The policy is `WithInteractiveRetry(RetryPolicy)`, default 3 attempts, 1s base and 8s at most. After the last attempt the run is stored `failed` and not retryable. Inline retries write nothing beyond the kept turns.
- **Docs:** the runner's `doc.go` (Resuming, Leases and cancellation) and `store/doc.go` (what is resumable).

## Out of scope

- **The HTTP side (T037):**
  - the stop endpoint;
  - the reconnect grace after a dropped SSE connection;
  - showing `interrupted`;
  - no 409 for an interrupted conversation.
- **Commands to a run live on another replica** (E18).
- **Steering:** a message sent while the run is live is still `ErrRunUnfinished`.
- **Interactive flow runs** (`RunFlow`): flows stay background runs.

## Acceptance criteria

- [x] **storetest:** an interactive run whose lease expired, or which was released, is never listed or returned as resumable, and `Interrupted` reports it. A background run is listed as before. Covered on all three stores, Postgres in the integration tier. `storetest` `InteractiveRunsAreNeverResumable` and Admin's `SkipsInteractiveRuns`; run invariant 6 (checked after every run test) restates the rule, and both run simulations draw interactive runs and compare `ListResumable`, `ListResumableAll` and `Interrupted` with their model. Memory, SQLite and Postgres (Postgres 16, `-tags integration`, run locally).
- [x] **Recovery skips interactive runs.** A recovery pass after an interactive run's process crashes leaves it alone, and calls no model. `TestRecoveryLeavesInteractiveRunsAlone` (every kill point of the recovery tests, both flush modes).
- [x] **Interrupt:** turns kept so far are stored, the lease is released, the run is `running` and `Interrupted`, and the execution ends with `ErrInterrupted`. `TestInterruptStoresTurnsAndLeavesTheRunInterrupted` (also: a second Interrupt is a no-op, another process gets `ErrLeased`).
  - An interactive run whose consumer stops reading ends the same way. `TestInteractiveRunWhoseConsumerStopsIsInterrupted`.
- [x] **Continue:** `Resume` of an interrupted run sends exactly the requests an uninterrupted run would have sent from the stored point, and ends like one. `TestContinueSendsTheUnansweredRequests` (interrupted at every model turn).
- [x] **New message:** on an interrupted conversation it cancels the old run and starts a new one. The model sees a valid conversation, with the marker reply when the old run never answered. `TestNewMessageReplacesAnInterruptedRun` (interrupted before the answer, after a tool turn, process died, failed for good).
  - On a live run's conversation it returns `ErrRunUnfinished`. `TestNewMessageOnALiveOrBackgroundRunIsRefused` (live, an execution not iterated yet, and a background run).
  - Two racing new messages: exactly one run starts. `TestRacingNewMessagesStartOneRun` (the second message lands at each step of the first one's call).
- [x] **Inline retry:**
  - Retryable errors under the attempt limit end `completed`, with `EventRetrying` events and no `failed` transition in between. `TestRetryableErrorIsRetriedInline`.
  - At the limit the run is stored `failed`, not retryable, and recovery never resumes it. `TestInlineRetryStopsAtMaxAttempts`; a non-retryable error is not retried: `TestNonRetryableErrorFailsAnInteractiveRunAtOnce`.
  - An `Interrupt` during the wait ends the run interrupted at once. `TestInterruptDuringTheRetryWaitEndsTheRunAtOnce`.
- [x] **Migration 0005:** existing runs become background runs and stay claimable as before. `TestRunsFromBeforeInteractiveStayBackgroundRuns` (SQLite, and Postgres in the integration tier).
- [x] **Simulation:** the runner simulation draws interactive runs and the operations above (interrupt, new message, continue, crash), and recovery never touches an interactive run. `TestSimulation`, `FuzzSimulation`: every recovery pass is checked to leave each unfinished interactive run's status, failures, log and model requests unchanged; the case mix asserts each new case occurs.
- [x] **Mutation list** recorded (Notes).

- [x] **The CLI keeps working:** `agentkit chat`'s Ctrl-C relies on the run ending `interrupted` and on Enter resuming it with an empty message. `TestChatCtrlCCancelsRun`, `TestChatCtrlCAsTheRunStarts` and `TestE2EChat` (`cmd/agentkit`) pass unchanged, or are updated with the reason recorded (T078 session's handoff, 2026-10-13). They pass unchanged: `agentkit chat` talks to the v0 agent API, which does not use the runner yet (T037 moves it). `TestChatCtrlCAsTheRunStarts` does not exist on `main`; the chat tests that do (`TestChatCtrlCCancelsRun`, `TestChatCtrlCAtPromptQuits`, `TestE2EChat`) pass.

## Notes

- **Why keep the lease for chat runs.** The lease is how anyone tells a live run from an interrupted one:
  - after a page reload, the API needs to know whether to show the run as running or interrupted;
  - two "continue" clicks, or a continue while the run is still alive elsewhere, must not both run, and the second fails at `Claim`.

  A crashed interactive run shows as interrupted once its lease expires, at most one TTL (30s) later.
- **T099** builds on this migration: its columns go in 0006, and `ClaimNext` skips interactive runs too.

### Decisions

- **A new message also replaces a failed interactive run**, not only an interrupted one: "not live" is an interactive run whose lease nobody holds, running or failed. A failed chat run (inline retries used up, or a fatal model error) would otherwise block its conversation until someone cancels it, which is what the owner's direction rules out for interrupted runs.
- **The replacement goes through the store's lease.** `Run` cancels the old run by claiming it, as `Cancel` does, so a run that someone continues (or that is live in any process) refuses the claim (`store.ErrLeased`) and the new message gets `ErrRunUnfinished`. The held-lease check before it only saves the claim.
- **Racing messages.** Of two messages racing to replace a run, one starts. One narrow case: the second message can cancel the first one's new run between its `Create` and its `Claim` (the new run is interactive, with no lease yet); the second then starts, and the first gets an `UnfinishedError` naming the second's run. Exactly one run starts either way, and the canceled one has no entries.
- **A marker the replacing process did not write.** If the process replacing a run dies after the cancel and before the marker, the run is canceled without it, and the next run of the conversation appends it (the existing rule for `Cancel`). The simulation found this path (it sends one more message when a conversation ends on a user message).
- **Interrupt is any run's stop, of this runner only.** It stops a background run too, which the recovery loop then resumes. A run another process holds gives `store.ErrLeased` (E18 routes the command), and so does an execution of this runner made but not iterated yet: it holds its lease and is not live until iterated.
- **Inline retries count per execution.** `Run.Attempts` is not incremented between attempts (nothing is written but the turns); the final failure stores `Attempts` + 1, as one failure. Usage of the failed attempts is added to the run's usage when it ends.
- **`EventRetrying`** carries `Attempt` (the attempt about to run, 2 for the first retry), `Delay` and `Err`. A consumer that stops reading at it interrupts the run, like at any event.
- **`store.Create` takes options** (`store.CreateOption`, `store.Interactive()`); a retried `Create` returns the run as first stored, whatever its options. Backends read them with `store.NewCreateOptions`.

### For T037

- Start chat runs with `runner.Interactive()`. The stop endpoint is `Runner.Interrupt`; "continue" is `Runner.Resume`; a new message on an interrupted conversation is a plain `Runner.Run` (no 409). Show `interrupted` from `store.Run.Interrupted(now)`.
- `Interrupt` and `Cancel` only reach executions of the runner they are called on; until E18 a stop on another replica gets `store.ErrLeased`.
- The reconnect grace after a dropped SSE connection is not in the runner: a consumer that stops reading interrupts the run at once. The grace must keep reading (or buffer) for those 10 s, then stop.
- `EventRetrying` should reach the client (a "retrying in N s" line).

### For T099

- Migration 0005 is `runs.interactive` (SQLite `INTEGER`, Postgres `BOOLEAN`, default false); T099's columns go in 0006. The resumable predicate is `interactive = 0 AND (…)` / `NOT interactive AND (…)` in both stores (`backend.Resumable` in memory): `ClaimNext` must keep that conjunct. The `runs_resumable` indexes were left as they are (interactive runs are rare among running and failed ones); revisit with T099's index.

### Mutation list

Each plant was run against the packages named, then restored. All caught, except the two equivalent mutants noted.

| Plant | Caught by |
|---|---|
| `backend.Resumable` lists interactive runs (memory) | storetest `InteractiveRunsAreNeverResumable`, run `Simulation` |
| SQLite resumable SQL lists interactive runs | storetest `InteractiveRunsAreNeverResumable`, `Simulation` |
| Postgres resumable SQL lists interactive runs | storetest Admin `SkipsInteractiveRuns`, `Simulation` (integration) |
| SQLite `Create` drops the flag | storetest `InteractiveRunsAreNeverResumable`, `Simulation` |
| Migration 0005 defaults to interactive | `TestRunsFromBeforeInteractiveStayBackgroundRuns` |
| `Run.Interrupted` ignores `Interactive` | run invariant 6 (every run test) |
| Interrupt keeps the lease | `TestInterruptStoresTurnsAndLeavesTheRunInterrupted`, `TestNewMessageOnALiveOrBackgroundRunIsRefused`, `TestInterruptDuringTheRetryWait…` |
| Interrupt stops with Cancel's cause | `TestInterruptStoresTurns…`, `TestRacingNewMessagesStartOneRun`, … |
| Interrupt of a run held elsewhere succeeds | `TestInterruptStoresTurnsAndLeavesTheRunInterrupted` |
| `Interactive()` not stored | `TestInterruptStoresTurns…`, `TestInteractiveRunWhoseConsumerStops…`, `TestInlineRetryStopsAtMaxAttempts`, … |
| New message cancels a live run (lease check dropped and the cancel's claim ignored) | `TestNewMessageOnALiveOrBackgroundRunIsRefused/live` and `/not_yet_iterated`, `TestRacingNewMessagesStartOneRun` |
| Inline retry stores `failed` between attempts | `TestRetryableErrorIsRetriedInline`, `TestInlineRetryStopsAtMaxAttempts`, `TestInterruptDuringTheRetryWait…` |
| Retry wait ignores Interrupt | `TestInterruptDuringTheRetryWaitEndsTheRunAtOnce` (hangs until the test timeout: caught by the timeout) |
| A failed interactive run stored retryable | `TestInlineRetryStopsAtMaxAttempts` |
| Non-retryable errors retried inline | `TestNonRetryableErrorFailsAnInteractiveRunAtOnce` |
| One attempt too many (`>` for `>=`) | `TestInlineRetryStopsAtMaxAttempts` |
| Retry reruns the first input, not the stored transcript | `TestRetryableErrorIsRetriedInline`, `TestInlineRetryStopsAtMaxAttempts`, `TestNewMessageReplacesAnInterruptedRun/failed` |
| Equivalent: `abandoned` ignores the lease | survives: the cancel's `Claim` refuses a held run, so behavior is unchanged |
| Equivalent: `start` retries after any cancel error | survives: the retry is bounded and its `Create` gets the same `ErrRunUnfinished` |
