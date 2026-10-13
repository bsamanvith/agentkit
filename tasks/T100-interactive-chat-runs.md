---
id: T100
title: "runner: interactive chat runs stop with their user and are never recovered in the background"
epic: E05
status: todo
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

- [ ] **storetest:** an interactive run whose lease expired, or which was released, is never listed or returned as resumable, and `Interrupted` reports it. A background run is listed as before. Covered on all three stores, Postgres in the integration tier.
- [ ] **Recovery skips interactive runs.** A recovery pass after an interactive run's process crashes leaves it alone, and calls no model.
- [ ] **Interrupt:** turns kept so far are stored, the lease is released, the run is `running` and `Interrupted`, and the execution ends with `ErrInterrupted`.
  - An interactive run whose consumer stops reading ends the same way.
- [ ] **Continue:** `Resume` of an interrupted run sends exactly the requests an uninterrupted run would have sent from the stored point, and ends like one.
- [ ] **New message:** on an interrupted conversation it cancels the old run and starts a new one. The model sees a valid conversation, with the marker reply when the old run never answered.
  - On a live run's conversation it returns `ErrRunUnfinished`.
  - Two racing new messages: exactly one run starts.
- [ ] **Inline retry:**
  - Retryable errors under the attempt limit end `completed`, with `EventRetrying` events and no `failed` transition in between.
  - At the limit the run is stored `failed`, not retryable, and recovery never resumes it.
  - An `Interrupt` during the wait ends the run interrupted at once.
- [ ] **Migration 0005:** existing runs become background runs and stay claimable as before.
- [ ] **Simulation:** the runner simulation draws interactive runs and the operations above (interrupt, new message, continue, crash), and recovery never touches an interactive run.
- [ ] **Mutation list** recorded.

## Notes

- **Why keep the lease for chat runs.** The lease is how anyone tells a live run from an interrupted one:
  - after a page reload, the API needs to know whether to show the run as running or interrupted;
  - two "continue" clicks, or a continue while the run is still alive elsewhere, must not both run, and the second fails at `Claim`.

  A crashed interactive run shows as interrupted once its lease expires, at most one TTL (30s) later.
- **T099** builds on this migration: its columns go in 0006, and `ClaimNext` skips interactive runs too.
