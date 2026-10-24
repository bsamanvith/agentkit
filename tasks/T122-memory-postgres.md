---
id: T122
title: "Context store backend postgres, in the store's database"
epic: E23
status: done
depends_on: [T058, T093, T121]
---

# T122: memory in Postgres

## Context

Compose and Kubernetes run on the Postgres store; memory had only the
in-process and SQLite backends, so a deployed agent's memory was lost on
restart or not shared across replicas.

## Scope

- `pkg/memory/postgres`: a `memory.Store` on the Postgres store's pool
  (`storepg.Store.Pool`), as `pkg/credentials/postgres` is.
- Store migration `memory_files` (tenant, user_id, path, content, size,
  updated), with the store's row-level security policy.
- A command is one transaction holding a per-scope transaction advisory lock,
  so its checks (limits, what exists) and its writes see one state across
  replicas.
- `cmd/agentkit`: `memory.store.backend: "postgres"` (the database
  `$AGENTKIT_STORE` names; `nopostgres` builds refuse it, naming the tag).

## Out of scope

- The search store in Postgres (pgvector): RAG later.
- The runner component (T123).

## Acceptance criteria

- [x] Passes `memorytest.RunStore`, including `Concurrent` and `Text`, on one pool and on two (replicas). — test: `postgres.TestConformance`, `postgres.TestTwoReplicas` (integration)
- [x] Two stores on one database (two replicas) see each other's writes, and concurrent creates in one scope never pass the file limit. — test: `postgres.TestReplicasShareLimits` (integration)
- [x] Row-level security holds: a query with another tenant's `app.tenant` sees no row. — test: `postgres.TestRowLevelSecurity` (integration)
- [x] `backend: "postgres"` needs a Postgres `$AGENTKIT_STORE` and no path; a `nopostgres` build names the tag; through `cmd/agentkit` it migrates (or, with migrations off, refuses an old schema) and a second replica's tool reads what the first wrote, per user. — test: `TestMemoryPostgresBackendConfig`, `TestMemoryPostgresNotBuilt` (`-tags nopostgres`), `TestMemoryPostgres` (integration)

## Notes

- Mutation check: replacing the advisory lock with a no-op fails `TestReplicasShareLimits` (6 and 7 files made past a limit of 5) and `Concurrent` in both suites.
- Size, measured with `go build -trimpath -ldflags='-s -w'` and each variant's tags: dev +32 KB (28,823,712 to 28,856,480 bytes), api +28 KB, local unchanged (`nopostgres`). Under 100 KB, so no SIZES.md row.
- Each agent with `backend: "postgres"` opens its own small pool, as the credential vault does. Sharing the agent API's store pool is a later tidy-up if agents multiply.
