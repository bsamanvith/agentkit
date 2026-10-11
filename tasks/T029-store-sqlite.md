---
id: T029
title: "pkg/store/sqlite: schema, migrations, all suites"
epic: E05
status: todo
depends_on: [T028]
---

# T029: `pkg/store/sqlite`

## Context

The local and single-node backend. It must keep the binary static (distroless image), so the driver is pure Go.

## Scope

- Driver: `modernc.org/sqlite` (pure Go, no CGO). Record the binary size it adds in Notes (the image budget is set in M5).
- Schema with embedded, numbered migrations run at open, each in a transaction:
  - `entries(tenant, conversation, seq, kind, messages, record, run_id, created_at, covers)`, primary key `(tenant, conversation, seq)`, which makes create-if-absent a plain `INSERT`;
  - `runs`, `leases`, `checkpoints`.
  - Messages are stored as `TEXT` holding the exact JSON bytes.
- WAL mode and a busy timeout. Concurrent writers from several goroutines and from two processes on one file.
- Cursors are opaque, encoding `(seq)` or `(created_at, seq)`.
- Schema written to port column for column to Postgres (no SQLite-only types in the contract).

## Acceptance criteria

- [ ] Passes `storetest.RunConversationStore`, `storetest.RunRunStore` and `flowtest.RunCheckpointer` on a temp-file database.
- [ ] Opening an old-version database applies the missing migrations; opening one newer than the binary fails clearly.
- [ ] Two processes appending to one conversation on the same file: no lost entries, conflicts reported (test via a helper subprocess or two connections; recorded how).
- [ ] Mutation list recorded in Notes (at least: `INSERT OR REPLACE` instead of create-if-absent, tenant filter dropped from one query, a cursor that skips an entry, messages re-encoded on write).

## Notes

- archtest allowlist: `pkg/store/sqlite` → `modernc.org/sqlite` (and what it needs).
