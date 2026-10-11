---
id: T028
title: "pkg/store: interfaces, in-memory store, conformance suites"
epic: E05
status: todo
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
- **`RunStore`**: `Create`, `Get`, `List(convID, page)`, `Finish(status, usage, error)`, plus a lease: `Claim(runID, owner, ttl)`, `Renew`, `Release`. A claim held by another owner that has not expired is `ErrLeased`.
- **Checkpoints**: backends implement `flow.Checkpointer` directly.
- Every method is scoped by `tenancy.From(ctx)`; no tenant is `ErrNoTenant`. One tenant never sees another's data.
- `store/memory`: an in-memory implementation of all three, for tests and single-process use.
- `store/storetest`: `RunConversationStore`, `RunRunStore`, plus reuse of `flowtest.RunCheckpointer`.

## Acceptance criteria

- [ ] Message JSON (with Opaque blocks) read back from `History` and `Context` is byte-identical to what was appended.
- [ ] Append is create-if-absent: an identical retry is a no-op; a different entry at a taken `Seq` is `ErrConflict`; a gap is an error.
- [ ] Two writers racing for the same `Seq`: exactly one wins and the other gets `ErrConflict` (driven from the test, not by scheduling luck; §2.6).
- [ ] `History` pages cover every entry exactly once in both orders, with any limit, while entries are appended between pages.
- [ ] `Context` returns the latest compaction plus later entries; with no compaction, everything.
- [ ] Leases: a second owner cannot claim an unexpired lease; it can after expiry (with an injected clock); `Renew` by a non-owner fails.
- [ ] Tenant isolation for every method; no tenant is `ErrNoTenant`.
- [ ] Invariants named and checked after every suite run through one helper: sequence numbers are dense from 1, stored bytes are unchanged, `Context` is a suffix of `History`.
- [ ] Seeded simulation (random interleavings of appends, retries, conflicts, compactions and reads against the in-memory store) plus a fuzz target over the seed.
- [ ] `store/memory` passes every suite, plus `flowtest.RunCheckpointer`. Mutation list recorded in Notes.

## Notes

- archtest: `pkg/store` may import `pkg/llm`, `pkg/flow`, `pkg/tenancy`; never `pkg/harness`.
