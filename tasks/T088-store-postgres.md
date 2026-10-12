---
id: T088
title: "pkg/store/postgres: the server store, same suites as SQLite, plus the durable event log"
epic: E18
status: todo
depends_on: [T029]
---

# T088: Postgres store

## Context

The owner chose Postgres + Redis for the server (E18). SQLite (T029) stays for local and single-node use. Decisions: [E18](E18-streaming-backend.md), [E05](E05-store-runner.md).

## Scope

**First, before the backend** (T029 handoff; a third backend must not inherit the gaps):
- **Close two `storetest` gaps** that T029 found only its own package tests catch:
  - (a) entry times never go backwards when two writers' clocks disagree;
  - (b) a retryable failure with no `NextAttemptAt` is due at once.
  - Plant each bug in the memory store, see the suite fail, then remove the plant.
- **Time range is part of the contract:** a time the caller asks the store to keep outside 1678–2262 (int64 Unix ns) is `ErrInvalid` in every backend, the memory store included. Add a `storetest` case.

- **Driver:** `pkg/store/postgres` on `github.com/jackc/pgx/v5` (`pgxpool`). Archtest allowlist for this package only.
- **Migrations:** embedded, numbered, recorded in `schema_migrations` (T029's scheme), run at open under an advisory lock (one migrator at a time). An older binary refuses a newer schema (`ErrSchemaTooNew`, as in T029).
  - Migrating at startup is the default.
  - `agentkit migrate` (store URL from `AGENTKIT_STORE`) runs them as a one-off job for deployments that want that.
  - `AGENTKIT_STORE_MIGRATE=off` makes `serve` refuse to start on an old schema instead of migrating it.
- **Schema:** T029's schema ported column for column.
  - Times are `BIGINT` Unix ns.
  - Messages and checkpoints are `TEXT`, not `JSONB`: JSONB normalizes key order and whitespace, and T020's Opaque blocks must come back byte for byte.
  - Tenant in every key and query.
  - One unfinished run per conversation, by a partial unique index.
  - Leases are columns on `runs`, claimed by one conditional `UPDATE` (T029's shape).
  - The tables that land on SQLite before this task does (T037's conversation record, and T071, T073, T058 if they're first) are ported here.
- **Errors:**
  - `23505` (unique violation) maps to the store's conflict errors.
  - `40001` and `40P01` (serialization failure, deadlock) are retried a bounded number of times inside the store, never surfaced as a 500.
- **Event log** (new for E18):
  - an `events` table: tenant, run ID, seq, type, JSON, created_at;
  - primary key `(tenant, run_id, seq)`; append-only;
  - `Append(run, events)` and `After(run, seq, limit)`;
  - added to `pkg/store` as a small `EventLog` interface with a `storetest` suite, implemented for memory and SQLite too.
- **Suites:** pass `RunConversationStore`, `RunRunStore`, `RunCheckpointer`, `RunAdmin`, the new event-log suite, and the simulations, against a real Postgres. Locally that's `scripts/dev.sh up deps` (T006) or `docker run postgres:17`. Tests read `AGENTKIT_PG_URL`; they skip when it's unset and fail when it's unset under `AGENTKIT_REQUIRE_INTEGRATION=1` (the integration tier, ENGINEERING §2.2).
- **CI:** a job with a Postgres service container runs the integration tier.
- **Wiring:** `AGENTKIT_STORE=postgres://…` selects this store in `serve`. T037 adds the `AGENTKIT_STORE` switch: whichever of T037 and T088 lands second wires Postgres into it.
- **Later, not in this task:** row-level security as a second isolation layer ([T093](T093-postgres-rls.md)).

## References

- [pgx v5](https://github.com/jackc/pgx) and `pgxpool`; [Postgres partial indexes](https://www.postgresql.org/docs/current/indexes-partial.html); [advisory locks](https://www.postgresql.org/docs/current/explicit-locking.html#ADVISORY-LOCKS)
- [JSONB vs JSON storage](https://www.postgresql.org/docs/current/datatype-json.html) (JSONB normalizes key order and whitespace: why byte-exact messages may need `TEXT` or `JSON`)
- [T029](T029-store-sqlite.md) Notes ("For T030", "For T037", "For T071 and T073"): its query shapes, cursors, lease columns and the two-handle race tests to port
- [GitHub Actions service containers](https://docs.github.com/en/actions/use-cases-and-examples/using-containerized-services/creating-postgresql-service-containers)

## Acceptance criteria

- [ ] **Suite gaps closed first:** both T029 gaps and the time-range rule are `storetest` cases; each was seen failing on a planted bug in the memory store.
- [ ] **Suites:** all existing store suites and simulations pass against Postgres, including T029's two-handle race tests ported to two pools.
- [ ] **Event log:** append and read-after pass the new suite for memory, SQLite and Postgres. Concurrent appends to one run get distinct, dense sequence numbers or a conflict, never a gap.
- [ ] **Byte-exact messages:** a message with an Opaque block and odd whitespace reads back byte for byte.
- [ ] **Migrations:** four concurrent opens migrate once; a newer schema is refused.
- [ ] **CI:** the Postgres job runs the integration tier and is required.
- [ ] **Retries:** a forced `40001` on a write is retried and succeeds without a duplicate entry.
- [ ] **Mutation list** recorded.

## Notes

- From T029: the race gate needs `go test -race -count=20 -timeout 30m`; SQLite alone takes about 12 minutes there, over Go's 10-minute default.
