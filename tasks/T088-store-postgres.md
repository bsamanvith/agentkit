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

- **Driver:** `pkg/store/postgres` on `github.com/jackc/pgx/v5` (`pgxpool`). Archtest allowlist for this package only.
- **Migrations:** embedded, numbered, run at open under an advisory lock (one migrator at a time). An older binary refuses a newer schema (the same rule as T029).
- **Schema:** T029's schema ported column for column. Messages are `JSONB` stored from the exact bytes (verify byte-exact round-trip, or keep `TEXT` if JSONB normalizes; T020's Opaque blocks must come back unchanged). Tenant in every key and query; one unfinished run per conversation by a partial unique index; leases by conditional `UPDATE`.
- **Event log** (new for E18):
  - an `events` table: tenant, run ID, seq, type, JSON, created_at;
  - primary key `(tenant, run_id, seq)`; append-only;
  - `Append(run, events)` and `After(run, seq, limit)`;
  - added to `pkg/store` as a small `EventLog` interface with a `storetest` suite, implemented for memory and SQLite too.
- **Suites:** pass `RunConversationStore`, `RunRunStore`, `RunCheckpointer`, `RunAdmin`, the new event-log suite, and the simulations, against a real Postgres. Locally that's `scripts/dev.sh up deps` (T006) or `docker run postgres:17`. Tests read `AGENTKIT_PG_URL`; they skip when it's unset and fail when it's unset under `AGENTKIT_REQUIRE_INTEGRATION=1` (the integration tier, ENGINEERING §2.2).
- **CI:** a job with a Postgres service container runs the integration tier.
- **Later, not in this task:** row-level security as a second isolation layer (PLAN `tenancy`).

## References

- [pgx v5](https://github.com/jackc/pgx) and `pgxpool`; [Postgres partial indexes](https://www.postgresql.org/docs/current/indexes-partial.html); [advisory locks](https://www.postgresql.org/docs/current/explicit-locking.html#ADVISORY-LOCKS)
- [JSONB vs JSON storage](https://www.postgresql.org/docs/current/datatype-json.html) (JSONB normalizes key order and whitespace: why byte-exact messages may need `TEXT` or `JSON`)
- T029's Notes "For T088"-equivalent: its query shapes, cursors and the two-handle race tests to port
- [GitHub Actions service containers](https://docs.github.com/en/actions/use-cases-and-examples/using-containerized-services/creating-postgresql-service-containers)

## Acceptance criteria

- [ ] **Suites:** all existing store suites and simulations pass against Postgres, including T029's two-handle race tests ported to two pools.
- [ ] **Event log:** append and read-after pass the new suite for memory, SQLite and Postgres. Concurrent appends to one run get distinct, dense sequence numbers or a conflict, never a gap.
- [ ] **Byte-exact messages:** a message with an Opaque block and odd whitespace reads back byte for byte.
- [ ] **Migrations:** four concurrent opens migrate once; a newer schema is refused.
- [ ] **CI:** the Postgres job runs the integration tier and is required.
- [ ] **Mutation list** recorded.
