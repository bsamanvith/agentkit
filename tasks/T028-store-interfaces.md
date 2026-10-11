---
id: T028
title: "pkg/store: interfaces, in-memory store, conformance suites"
epic: E05
status: done
depends_on: [T020, T023, T027]
---

# T028: `pkg/store`

## Context

The storage contract every backend implements, written once as conformance suites so SQLite (T029), Postgres later, and any key-value or document backend behave the same. Direction: [PLAN.md `store`](../docs/PLAN.md#store) and the E05 decisions table. The interfaces must not assume SQL.

## Design

- **`ConversationStore`**, an append-only log of entries per conversation:
  - `Entry`: `Seq` (position in the conversation, chosen by the writer), `Kind` (`prompt`, `turn`, `compaction`), `Messages []llm.Message` (stored as `llm.Message` JSON, byte for byte), `Record json.RawMessage` (times, usage and timings, opaque to the store), `RunID`, `CreatedAt`, and for a compaction `Covers` (the last sequence number it summarizes).
  - `Append(ctx, convID, entry)`: **create if absent** at `Seq`. Appending an identical entry again is a no-op, so a retry after a timeout is safe. A different entry at a taken `Seq` is `ErrConflict`. A gap (`Seq` beyond the next free one) is an error.
  - `History(ctx, convID, Page{Cursor, Limit, Order})`: every entry, for people, paged with opaque cursors (a cursor may only be valid for the backend that issued it). Supports seeking to a sequence number or a time.
  - `Context(ctx, convID)`: the latest compaction entry plus the entries after it, with the sequence number it was built from.
- **`RunStore`**: `Create`, `Get`, `List(convID, page)`, plus a lease: `Claim(runID, owner, ttl)`, `Renew`, `Release`. A claim held by another owner that has not expired is `ErrLeased`.
  - **Run lifecycle** (planner decision; see the E05 decisions on when a stopped run continues): statuses `running`, `suspended` (waiting for an approval: the pending step paths are stored), `failed` (with `Retryable`, the attempt count and `NextAttemptAt`), and the final ones `completed`, `rejected`, `canceled`. A final status never changes again (`ErrFinal`). Transitions go through one method (`Transition(ctx, runID, from, to, details)`) that fails with `ErrConflict` if the current status is not `from`, so two processes cannot both move a run.
  - "Interrupted" is not stored: it is `running` with an expired lease.
  - `ListResumable(ctx, now, limit)`: runs that should continue now: `running` with an expired lease, and `failed` + `Retryable` with `NextAttemptAt <= now`. `ListSuspended(ctx, page)`: runs waiting for a decision.
  - `Unfinished(ctx, convID)`: the conversation's unfinished run, if any (at most one).
- **Checkpoints**: backends implement `flow.Checkpointer` directly.
- Every method is scoped by `tenancy.From(ctx)`; no tenant is `ErrNoTenant`. One tenant never sees another's data.
- `store/memory`: an in-memory implementation of all three, for tests and single-process use.
- `store/storetest`: `RunConversationStore`, `RunRunStore`, plus reuse of `flowtest.RunCheckpointer`.

## Acceptance criteria

Suite tests are subtests of `storetest.RunConversationStore` (C), `storetest.RunRunStore` (R) and `storetest.RunCheckpointer` (K); `pkg/store/memory` runs them as `TestConversationStore`, `TestRunStore` and `TestCheckpointer`.

- [x] Message JSON (with Opaque blocks) read back from `History` and `Context` is byte-identical to what was appended. (C `MessageJSONRoundTripsByteForByte`: Opaque data with whitespace, ToolUse input with unsorted keys and `1.50`/`1e2`, HTML characters, invalid UTF-8; C `KeepsCopies`; and conversation invariant 2 after every test)
- [x] Append is create-if-absent: an identical retry is a no-op; a different entry at a taken `Seq` is `ErrConflict`; a gap is an error. (C `AppendIsCreateIfAbsent/IdenticalRetryIsANoOp`, `/DifferentEntryIsErrConflict`, `/GapIsErrGap` (`ErrGap`); C `InvalidEntryIsErrInvalid`)
- [x] Two writers racing for the same `Seq`: exactly one wins and the other gets `ErrConflict` (driven from the test, not by scheduling luck; §2.6). (C `RacingWritersExactlyOneWins/FromTheSameView`: both build from one `Context` read and the test fixes the order, both ways round; `/Concurrently`: 8 writers released by one gate, 10 rounds, asserting what holds for every schedule; identical racers all succeed)
- [x] `History` pages cover every entry exactly once in both orders, with any limit, while entries are appended between pages. (C `HistoryPagesVisitEveryEntryOnce`: 2 orders × limits 1, 2, 3, 7 and the default over 205 entries; C `HistorySeeksBySeqAndTime`, `HistoryLimitAboveMaximumIsLowered`, `HistoryRefusesBadPages`; the simulation's interleaved history reads; R `ListPagesVisitEveryRunOnce`, `ListSeeksByTime`, `ListSuspendedPages`)
- [x] `Context` returns the latest compaction plus later entries; with no compaction, everything. (C `ContextStartsAtTheLatestCompaction`; conversation invariant 5)
- [x] Leases: a second owner cannot claim an unexpired lease; it can after expiry (with an injected clock); `Renew` by a non-owner fails. (R `Leases/SecondOwnerCannotClaimUntilExpiry` (refused at expiry − 1ns, granted at expiry), `/RenewByNonOwnerFails`, `/RenewExtendsUntilExpiry`, `/ReleaseLetsAnotherOwnerClaim`, `/OnlyUnfinishedRunsCanBeLeased`, `/ArgumentsAreChecked`, `/RacingClaimsExactlyOneWins`; R `Simulation`)
- [x] Run lifecycle: allowed transitions succeed; a transition from the wrong status is `ErrConflict`; a final status cannot change (`ErrFinal`); two writers moving the same run concurrently: exactly one wins. (R `Lifecycle/AllowedTransitionsSucceed`: all 36 pairs of statuses, `ErrInvalid` for the moves not allowed, `ErrFinal` from every final status; `/WrongFromIsErrConflict`, `/DetailsMustFitTheStatus`, `/FieldsAccumulate`, `/RacingTransitionsExactlyOneWins` (8 writers released at once, 10 rounds); R `OneUnfinishedRunPerConversation`; R `Simulation`)
- [x] `ListResumable` returns exactly the interrupted runs (running, lease expired, via an injected clock) and the retryable failed runs that are due; not running runs with a live lease, suspended runs, final runs or failed runs not yet due. (R `ListResumableFindsInterruptedAndDueRuns`: 13 runs, one per case, both sides of each boundary, the limit, and the `now` argument deciding rather than the store's clock; run invariant 6 and R `Simulation` check it against a model after every test and at random steps)
- [x] Tenant isolation for every method; no tenant is `ErrNoTenant`. (C, R and K `TenantIsolation`: `tenancy.ErrNoTenant` from all 3 + 10 + 2 methods, the same ids in two tenants never conflict; conversation invariant 6 and run invariant 1 read everything a test touched through every tenant it used plus one that never wrote)
- [x] Invariants named and checked after every suite run through one helper: sequence numbers are dense from 1, stored bytes are unchanged, `Context` is a suffix of `History`. (`convRig` and `runRig` in `storetest`, registered with `t.Cleanup`; listed below)
- [x] Seeded simulation (random interleavings of appends, retries, conflicts, compactions and reads against the in-memory store) plus a fuzz target over the seed. (`storetest.SimulateConversations`, 48 seeds in C `Simulation`, `FuzzConversationSimulation`; `storetest.SimulateRuns` for leases and the lifecycle, 48 seeds in R `Simulation`, `FuzzRunSimulation`)
- [x] `store/memory` passes every suite, plus `flowtest.RunCheckpointer`. Mutation list recorded in Notes. (K `Flow` runs `flowtest.RunCheckpointer` inside one tenant)

## Notes

### API (package `store`)

- `ConversationStore`: `Append(ctx, id, Entry) error`, `History(ctx, id, Page) (EntryPage, error)`, `Context(ctx, id) (ContextView, error)`.
- `Entry{Seq, Kind, Messages []llm.Message, Record json.RawMessage, RunID, Covers, CreatedAt}`; kinds `KindPrompt`, `KindTurn`, `KindCompaction`.
- `Page{Cursor, Limit, Order, FromSeq, FromTime}`, `Ascending`/`Descending`, `DefaultPageLimit` (100), `MaxPageLimit` (1000); `EntryPage{Entries, Next}`; `ContextView{Entries, Seq}`.
- `RunStore`: `Create(ctx, id, conversationID) (Run, error)`, `Get`, `List(ctx, conversationID, Page) (RunPage, error)`, `Transition(ctx, id, from, to Status, Details) (Run, error)`, `ListResumable(ctx, now, limit) ([]Run, error)`, `ListSuspended(ctx, Page) (RunPage, error)`, `Unfinished(ctx, conversationID) (Run, error)`, and the embedded `Leaser`: `Claim(ctx, runID, owner, ttl) (Lease, error)`, `Renew(...) (Lease, error)`, `Release(ctx, runID, owner) error`.
- `Run{ID, ConversationID, Status, Usage, Error, Retryable, NextAttemptAt, Attempts, Pending, CreatedAt, FinishedAt, Lease}`; `Status` (`running`, `suspended`, `failed`, and the final `completed`, `rejected`, `canceled`) with `Final()` and `CanMoveTo(to)`; `Details{Usage, Error, Retryable, NextAttemptAt, Pending}`; `Lease{Owner, Expires}`.
- Sentinels: `ErrNotFound`, `ErrInvalid`, `ErrConflict`, `ErrGap`, `ErrLeased`, `ErrLeaseLost`, `ErrFinal`, `ErrUnfinished`. No tenant is `tenancy.ErrNoTenant`; a missing checkpoint is `flow.ErrNotFound`; a done context is the context's error.
- Checkpoints: backends implement `flow.Checkpointer` (not redefined).
- `store/memory`: `New(...Option) *Store`, `WithClock(clock.Clock)`; one value implements all three interfaces.
- `store/storetest`: `RunConversationStore(t, func(t, clock.Clock) store.ConversationStore)`, `RunRunStore(t, func(t, clock.Clock) store.RunStore)`, `RunCheckpointer(t, func(t) flow.Checkpointer)`, `SimulateConversations(t, newStore, seed)`, `SimulateRuns(t, newStore, seed)`.
- `store/internal/backend` (not public API): argument checks, message encoding, the identical-entry comparison and the cursor format, shared by every backend under `pkg/store` so they cannot drift on a rule the suite only samples.

### Design decisions

- **Sequence numbers** are chosen by the writer: the `ContextView.Seq` (or last `History` entry) it read, plus one. The store never assigns them; that is what makes concurrent writers conflict instead of interleaving.
- **Identical** means same `Kind`, same messages JSON bytes (what `json.Marshal(e.Messages)` gives), same `Record` bytes, `RunID` and `Covers`. `CreatedAt` is not compared: it is **set by the store** at the first write (the value passed in is ignored) and never decreases along a log (a backend clamps it to the previous entry's), so `History` can seek by time with a binary search.
- **A gap is its own sentinel, `ErrGap`**, not `ErrConflict`: a gap means the writer is confused (never retry), a conflict means it is stale (reload and decide again).
- **Validation (`ErrInvalid`)**: `Seq ≥ 1`; a known kind; at least one message, each marshals; `Record` empty or valid JSON; `Covers` is 0 except for a compaction, where it must be exactly `Seq-1`. That makes `Covers` redundant today but explicit for readers of history, and it keeps "Context is a suffix of History" true: a compaction built from a context that grew since conflicts at its `Seq` rather than silently dropping the newer entries. If T031 wants "summarize all but the last K turns", relax this rule there and change `Context` (and invariant 5) together.
- **Messages are stored as the bytes `json.Marshal([]llm.Message)` produces** and decoded on every read; a backend never decodes and re-encodes them. `Record` is kept byte for byte too (both are text to the store; Postgres must use `text`/`bytea`, not `jsonb`, which reorders keys). Empty `Record` reads back as nil.
- **An empty conversation is not an error**: `History` gives an empty page and `Context` `{Seq: 0}`. A conversation exists once it has an entry, so key-value backends need no separate conversation record, and another tenant's conversation looks exactly like a missing one.
- **Runs**: `Create` is create-if-absent too (same id and conversation returns the stored run, whatever its status; another conversation is `ErrConflict`). A conversation has at most one unfinished (not final) run: `Create` of another is `ErrUnfinished`, enforced by the store so two processes cannot both start one; `Unfinished` returns it or `ErrNotFound`. `List` and `ListSuspended` order by `(CreatedAt, ID)`, so ties are ordered by id, and support `FromTime` but not `FromSeq` (`ErrInvalid`).
- **`Finish` became `Transition`.** The first version had `Finish(status, usage, error)` from the original design; the planner's run lifecycle landed on `main` while this task was in progress, and it replaced `Finish` before anything was pushed, so no caller ever saw it.
- **Lifecycle** (the planner's design, made concrete): the allowed moves are `running → suspended | failed | completed | rejected | canceled`, `suspended → running | rejected | canceled`, `failed → running | canceled` (`Status.CanMoveTo`); the rest are `ErrInvalid`. `Transition` checks, in this order: `ErrNotFound`; `ErrFinal` if the run is final (whatever `from` and `to`); `ErrInvalid` for a move not allowed or details that do not fit; `ErrConflict` if the status is not `from`. Details must fit the new status: `Pending` (required, sorted, no empty path) only for suspended; `Retryable` and `NextAttemptAt` only for failed, and `NextAttemptAt` needs `Retryable`; `Error` only for failed, rejected and canceled. Each transition replaces `Error`, `Retryable`, `NextAttemptAt` and `Pending` (so they are cleared when they no longer apply), **adds** `Details.Usage` to `Run.Usage` (pass what was spent since the last transition), increments `Attempts` on every move to failed (the store counts, so the count cannot be lost), and sets `FinishedAt` on a final status. Transitions neither check nor change the lease. A failed run that is not retryable is "failed for good" until an operator moves it to running.
- **`ListResumable(ctx, now, limit)`** returns, ordered by `(CreatedAt, ID)` and capped like a page limit, the running runs whose lease is not held at `now` (expired, released or never claimed) and the failed runs with `Retryable` and `NextAttemptAt <= now`. It uses the `now` passed in, not the store's clock, so the caller decides the time. It is scoped by the tenant in `ctx` like everything else: there is no cross-tenant listing (see T030 below).
- **Leases** (all times from the injected clock): held while `now < Expires`; at `Expires` anyone can claim. `Claim` by the holder extends (a retry is safe). `Renew` needs an unexpired lease of the same owner: another owner's live lease is `ErrLeased`, an expired, released or never-claimed one is `ErrLeaseLost` (even if nobody else took it since, because the owner cannot know that). `Release` clears the owner's lease, expired or not; another owner's live lease is `ErrLeased`; anything else is a no-op. A final run cannot be claimed or renewed (`ErrFinal`); a suspended or failed one can (it is claimed before it is resumed). No fencing token yet: if the runner ever needs one (a writer that lost its lease writing elsewhere than the log), add a counter to `Lease` that `Claim` bumps for a new owner.
- **Cursors** are opaque (base64 of list, order and position), valid only for the backend, conversation and order that issued them; one the backend cannot read, or one used with the other order, is `ErrInvalid`. A cursor carries a position (the last `Seq`, or the last run's `(CreatedAt, ID)`), never an offset, so appends between pages cannot shift it.
- **archtest**: a new rule, `ModuleImports` / `module-import-allowlist`: the `pkg/store` tree (its own packages aside) may import only `pkg/llm`, `pkg/flow`, `pkg/flow/flowtest`, `pkg/tenancy`, `pkg/clock` and `pkg/clock/clocktest`, so it can never import `pkg/harness`. `TestRules` has a "store imports the harness" case. No other package needed a change.

### Invariants (checked when every suite test ends)

`convRig` (conversations), for every conversation the test touched, read through every tenant it used plus `initech`, which never writes:

1. **Dense**: entries are numbered 1..n, and the appends the store accepted are exactly those positions.
2. **Unchanged**: every entry reads back as it was first accepted (messages JSON bytes, `Record` bytes, `Kind`, `RunID`, `Covers`), whatever retries, conflicting appends and scribbling on passed-in or returned values happened.
3. **Times**: `CreatedAt` is set and never decreases along the log.
4. **History** in pages of 7 oldest first and pages of 3 newest first gives the same log, reversed; a page with a `Next` is full.
5. **Context is a suffix of History**: from the latest compaction (or the first entry) to the end, with `Seq` the log's length.
6. **Tenants**: each tenant sees only its own accepted appends (the unused tenant sees nothing).

`runRig` (runs), for every run id and conversation the test touched, through every tenant plus `initech`:

1. **Tenants**: `Get` finds exactly the runs the tenant created; others are `ErrNotFound`.
2. **Records**: a run keeps its conversation; its status and lifecycle fields (usage summed over transitions, error, retry, attempts counting the failures, pending paths) are those of the last transition the store accepted; `FinishedAt` is set exactly when the status is final; a lease has an owner exactly when it has an expiry.
3. **List** in pages of 2 and 3, both orders, gives each conversation's runs once, ordered by `(CreatedAt, ID)`, newest first the reverse.
4. **One unfinished run**: a conversation has at most one run that is not final, and `Unfinished` returns it (`ErrNotFound` when there is none).
5. **Suspended**: `ListSuspended`, paged, gives exactly the suspended runs, in order.
6. **Resumable**: `ListResumable` at the clock's time gives exactly the running runs whose lease is not held and the retryable failed runs that are due, in order (judged by a restatement of the rule in the suite, not the backend's code).

The simulations check every call against independent models: `SimulateConversations` (writers with stale cached contexts, retries of accepted and of lost entries, gaps, invalid entries, refreshing a cache from `History` the way the runner will and comparing it with `Context`, up to three history reads advancing a page at a time between other calls, no-tenant and cancelled calls, two tenants with the same conversation ids) and `SimulateRuns` (runs created and retried in three conversations of two tenants, claims, renewals and releases by three owners, transitions from the right and the wrong status with valid and invalid details, clock moves, and `ListResumable` (at times around now) and `Unfinished` reads; the stored lease is compared after every lease step).

### Mutation check

Each bug was planted alone in `pkg/store/memory`, `pkg/store/internal/backend` or `pkg/store` (every plant compiled; a first try of the `List` tenant filter did not and was rewritten), `go test -race ./pkg/store/...` was run, and the files were restored and diffed against a backup. 43 plants, none survived. C = `TestConversationStore`, R = `TestRunStore`, K = `TestCheckpointer`; "Sim" means the seeded subtests and the fuzz targets' seed corpora.

| Planted bug | Failing tests |
|---|---|
| An identical retry reported as a conflict | C `AppendIsCreateIfAbsent/IdenticalRetryIsANoOp`, `RacingWritersExactlyOneWins/Concurrently`, Sim, store `Example` |
| A different entry overwrites an existing one | C `DifferentEntryIsErrConflict`, `RacingWritersExactlyOneWins/FromTheSameView` and `/Concurrently`, Sim, store `Example` |
| A gap accepted | C `GapIsErrGap`, `TenantIsolation`, Sim |
| A cursor that skips entries when appends happen between pages (ascending cursor counting from the end) | C `HistoryPagesVisitEveryEntryOnce`, Sim |
| Descending cursor off by one (skips an entry) | C `HistoryPagesVisitEveryEntryOnce`, `HistorySeeksBySeqAndTime`, `MessageJSONRoundTripsByteForByte` and 2 more (invariant 4), Sim |
| `Context` ignores the latest compaction (takes the first) | C `ContextStartsAtTheLatestCompaction`, `HistoryPagesVisitEveryEntryOnce`, `HistoryLimitAboveMaximumIsLowered` (invariant 5), Sim |
| `Context` ignores compactions altogether | C `ContextStartsAtTheLatestCompaction` and 5 more (invariant 5), Sim |
| `ContextView.Seq` is the view's length, not the log's | C `ContextStartsAtTheLatestCompaction` and 5 more (invariant 5), Sim |
| Message bytes re-encoded on write (decoded to `any` and marshaled again) | every C test that stores a ToolUse or record (invariant 2), Sim |
| `Record` compacted on write | every C test (invariant 2), Sim |
| The stored `Record` shares the caller's bytes | C `KeepsCopies` |
| A returned `Record` shares the stored bytes | C `KeepsCopies` |
| A compaction may cover less than everything before it | C `InvalidEntryIsErrInvalid`, Sim |
| A retry is compared on its messages only | C `AppendIsCreateIfAbsent/DifferentEntryIsErrConflict` |
| `History` seek by time skips the entries created at that time | C `HistorySeeksBySeqAndTime` |
| The tenant dropped from `Context`'s lookup | C `TenantIsolation` and every other C test (invariant 6), Sim |
| A lease claimable before expiry (one second early) | R `Leases/SecondOwnerCannotClaimUntilExpiry`, `/RenewExtendsUntilExpiry`, `ListResumableFindsInterruptedAndDueRuns`, Sim |
| A lease still held at its expiry instant | R `Leases/SecondOwnerCannotClaimUntilExpiry`, `/RenewExtendsUntilExpiry`, `ListResumableFindsInterruptedAndDueRuns`, memory `Example` |
| `Renew` by a non-owner accepted | R `Leases/RenewByNonOwnerFails`, `/SecondOwnerCannotClaimUntilExpiry`, Sim |
| `Renew` of the owner's expired lease accepted | R `Leases/RenewExtendsUntilExpiry`, Sim |
| `Release` by a non-owner clears the lease | R `Leases/ReleaseLetsAnotherOwnerClaim`, Sim |
| A final run can be claimed | R `Leases/OnlyUnfinishedRunsCanBeLeased`, Sim |
| A final status can change | R `Lifecycle/AllowedTransitionsSucceed`, `/WrongFromIsErrConflict`, Sim |
| A transition from the wrong status accepted | R `Lifecycle/WrongFromIsErrConflict`, `/RacingTransitionsExactlyOneWins`, Sim |
| A move the lifecycle forbids accepted (failed → completed) | R `Lifecycle/AllowedTransitionsSucceed`, Sim |
| Details that do not fit accepted (an error on completed) | R `Lifecycle/DetailsMustFitTheStatus` |
| Usage replaced instead of added | R `Lifecycle/AllowedTransitionsSucceed`, `/FieldsAccumulate`, `OneUnfinishedRunPerConversation` (run invariant 2), Sim |
| Failures not counted in `Attempts` | R `Lifecycle/FieldsAccumulate` and 5 more (run invariant 2) |
| An old error kept after leaving failed | R `Lifecycle/AllowedTransitionsSucceed` (run invariant 2), Sim |
| Pending paths stored unsorted, sharing the caller's slice | R `Lifecycle/FieldsAccumulate`, `/AllowedTransitionsSucceed` and 3 more, Sim |
| `FinishedAt` not set | every R test that finishes a run (run invariant 2) |
| `ListResumable` returns running runs with a live lease | R `ListResumableFindsInterruptedAndDueRuns` and every lease test (run invariant 6) |
| `ListResumable` ignores `Retryable` | R `ListResumableFindsInterruptedAndDueRuns`, `Lifecycle/FieldsAccumulate` (run invariant 6), Sim |
| `ListResumable` returns failed runs not yet due | R `ListResumableFindsInterruptedAndDueRuns`, `Lifecycle/RacingTransitionsExactlyOneWins` (run invariant 6), Sim |
| `ListResumable` returns suspended runs | R `ListResumableFindsInterruptedAndDueRuns`, `ListSuspendedPages` and 3 more (run invariant 6), Sim |
| `ListResumable` reads the store's clock instead of `now` | R `ListResumableFindsInterruptedAndDueRuns`, Sim |
| `Create` ignores the conversation's unfinished run | R `OneUnfinishedRunPerConversation`, Sim |
| A failed run counted as finished | R `OneUnfinishedRunPerConversation` and 5 more (run invariant 4) |
| `Create` in another conversation accepted | R `CreateIsCreateIfAbsent`, Sim |
| The tenant filter dropped from the run lists | R `TenantIsolation` and every other R test (run invariants 3, 5, 6 through the other tenants) |
| `List` cursor ignores the id among runs created at the same time | R `ListPagesVisitEveryRunOnce`, `ListSuspendedPages`, `Lifecycle/RacingTransitionsExactlyOneWins` (run invariant 3), Sim |
| The tenant dropped from checkpoint `Load` | K `TenantIsolation` |
| No `ctx` check | C, R and K (`Flow/`) `CancelledContextIsReported`, C Sim |

Not plantable with these tests: the clamp that keeps `CreatedAt` from going backwards when the clock does. `clocktest.Fake` cannot go backwards, so a store without the clamp passes; conversation invariant 3 would catch it against a clock that can.

### For T029 (SQLite)

- Run all three suites: `storetest.RunConversationStore`, `storetest.RunRunStore` and `storetest.RunCheckpointer` (not `flowtest.RunCheckpointer` directly: the store needs a tenant in `ctx`, and the storetest wrapper adds one and the checkpoint isolation test). Factories get a `clock.Clock` (a fake); the store must read every time it records (entry and run `CreatedAt`, `FinishedAt`, lease expiry) from it. Consider a fuzz target over `storetest.SimulateConversations` as well; the sequence-race tests use goroutines on one store value, so give the factory a connection pool, not a single connection.
- Import `pkg/store/internal/backend` for `CheckEntry` (validation + the exact bytes to store), `Stored.Same` (retry vs conflict), `Stored.Entry` (decode), `CheckID`, `CheckLease`, `CheckTransition`, `Held`, `Resumable`, `ParseHistoryPage`/`HistoryCursor`, `ParseRunPage`/`RunCursor`. The cursor encodes a position (`seq`, or `(created_at, id)` for runs), so queries are keyset: `seq > ?` / `seq < ?` and `(created_at, id) > (?, ?)`.
- `Append` is one transaction: insert at `(tenant, conversation, seq)` only if `seq = max(seq)+1` (else `ErrGap`); on a primary-key collision, read the row and return nil if `Same`, else `ErrConflict`. Never `INSERT OR REPLACE`. `created_at = max(now, previous entry's created_at)`. Store `created_at` with full precision (integer nanoseconds), since cursors and seeks compare it.
- `Context` is one read: the max `seq` with `kind = 'compaction'`, then `seq >= that`, plus the log's max `seq`, in one transaction (the view must be of one moment).
- Leases are a conditional update in one statement or transaction (claim only where `owner = ? OR owner = '' OR expires <= now`); compare times as the memory store does (`held` while `now < expires`).
- Keep `messages` and `record` as `TEXT` with the exact bytes (for Postgres later: `text`, never `jsonb`).
- `Transition` is one conditional update (`UPDATE runs SET ... WHERE tenant = ? AND id = ? AND status = ?`); when it changes no row, read the run to tell `ErrNotFound`, `ErrFinal` and `ErrConflict` apart. `Create` checks for an unfinished run of the conversation in the same transaction as the insert (or a partial unique index on `(tenant, conversation) WHERE status NOT IN ('completed','rejected','canceled')`, which makes it race-free; map its violation to `ErrUnfinished`). `backend.CheckTransition`, `backend.Held` and `backend.Resumable` hold the rules; `ListResumable` in SQL is `(status = 'running' AND (lease_owner = '' OR lease_expires <= ?)) OR (status = 'failed' AND retryable AND next_attempt_at <= ?)`.
- `Pending` is a list: store it as a JSON array in a `TEXT` column (or a child table) and return a fresh sorted slice.
- Every query filters by tenant, including the run lookups inside `Claim`, `Transition`, `List`, `ListResumable`, `ListSuspended` and `Unfinished`, and the checkpoint `Load`.

### For T030 (runner)

- **Sequence assignment**: load `Context`; the next entry goes at `view.Seq + 1`. Write the prompt first, then one `KindTurn` entry per `EventTurnDone`, each at the next `Seq`. A retry after a timeout or an unknown result is safe: append the same entry again; nil means it is stored. `ErrConflict` means another writer took that position: stop, reload, and do not write the turn again blindly. `ErrGap` is a bug in the runner.
- **Cache**: keep the decoded context keyed by `view.Seq`. To bring it up to date, `History` with `FromSeq: cached+1`, `Ascending`, and apply entries in order (a compaction replaces the cache); the simulation does exactly this and checks it equals `Context`.
- **Compaction** (T031): append `KindCompaction` at `view.Seq+1` with `Covers = view.Seq` (anything else is `ErrInvalid`); a conflict means the context grew, so compact again from the new view.
- **Leases**: `Create` (create-if-absent; `ErrUnfinished` is the runner's `ErrRunUnfinished`, with `Unfinished` giving the run ID), then `Claim(runID, owner, ttl)`; `ErrLeased` means another process is running it; `ErrFinal` means there is nothing to resume. Renew well inside the ttl from a clock timer; on `ErrLeaseLost` or `ErrLeased` stop writing, because another owner may have taken over. `Release` on every exit path (releasing a lease you no longer hold is harmless unless someone else holds it, which is `ErrLeased`).
- **Lifecycle**: move the run with `Transition(id, from, to, details)`, always naming the status you believe it is in; `ErrConflict` means another process moved it (reload, do not retry as is), `ErrFinal` that it is over. A transition is not idempotent: after a timeout, `Get` the run to see whether it happened before trying again. Pass in `Details.Usage` only what was spent since the last transition (it is added). Suspend with the pending paths (`flow`'s `Checkpoint.Pending`), fail with `Retryable`/`NextAttemptAt` from the retry policy, and read `Attempts` (the store's count of failures) to decide when to stop retrying. Recovery: `ListResumable(ctx, clock.Now(), limit)` then `Claim` each run (the claim, not the list, decides who resumes it); both are per tenant, so `Recover` needs the list of tenants to visit (or a later cross-tenant admin method).
- **Errors**: `store.ErrNotFound` for runs, `flow.ErrNotFound` for checkpoints, `tenancy.ErrNoTenant` when `ctx` has no tenant: the runner must be called with the tenant in `ctx`.
- `Entry.RunID` is a label the store does not check; `Entry.Record` is the runner's JSON (times, usage, timings), stored and returned byte for byte. `CreatedAt` is the store's write time, not the turn's time: keep the turn's own times in `Record`.
