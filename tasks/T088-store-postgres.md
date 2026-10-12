---
id: T088
title: "pkg/store/postgres: the server store, same suites as SQLite, plus the durable event log"
epic: E18
status: done
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

- [x] **Suite gaps closed first:** both T029 gaps and the time-range rule are `storetest` cases; each was seen failing on a planted bug in the memory store.
- [x] **Suites:** all existing store suites and simulations pass against Postgres, including T029's two-handle race tests ported to two pools.
- [x] **Event log:** append and read-after pass the new suite for memory, SQLite and Postgres. Concurrent appends to one run get distinct, dense sequence numbers or a conflict, never a gap.
- [x] **Byte-exact messages:** a message with an Opaque block and odd whitespace reads back byte for byte.
- [x] **Migrations:** four concurrent opens migrate once; a newer schema is refused.
- [x] **CI:** the Postgres job runs the integration tier and is required.
- [x] **Retries:** a forced `40001` on a write is retried and succeeds without a duplicate entry.
- [x] **Mutation list** recorded.

## Notes

- From T029: the race gate needs `go test -race -count=20 -timeout 30m`; SQLite alone takes about 12 minutes there, over Go's 10-minute default.

### API

Package `store` (all backends):
- `type Event struct { Seq int64; Type string; Data json.RawMessage; CreatedAt time.Time }`.
- `type EventLog interface`, with three methods:
  - `AppendEvents(ctx, runID, []Event) error`: all or none, consecutive seqs; an identical retry (or partly stored batch) is a no-op; a different event at a taken seq is `ErrConflict`; a first seq past the end is `ErrGap`.
  - `EventsAfter(ctx, runID, after int64, limit int) ([]Event, error)`: `limit` is read like `Page.Limit`.
  - `LastEventSeq(ctx, runID) (int64, error)`: 0 for an empty log.
- `storetest.RunEventLog` and `storetest.SimulateEvents`. Memory, SQLite and Postgres implement `EventLog` and run both.
- **Text rule (contract, every backend):** what a store keeps as text must be UTF-8 with no NUL byte, or the call is `ErrInvalid`. That covers conversation and run ids, lease owners, `Details.Error`, pending paths, an entry's `Record` and `RunID`, and an event's `Type` and `Data`. Looking a run up by such an id (`Get`, `Transition`, `Claim`, `Renew`, `Release`, checkpoint `Load`) is `ErrNotFound`: no such run can exist. Messages need no check, because `llm` refuses to marshal Opaque data that is not UTF-8 JSON. Postgres cannot hold such text at all; the rule makes the other backends refuse the same values. Cases: `TextAStoreCannotHold` in the conversation and run suites.

Package `postgres`:
- `Open(ctx, url string, opts ...Option) (*Store, error)`: `url` is a `postgres://` URL or a key=value string, as pgx reads it (`pool_max_conns` and the other `pool_*` parameters work). `(*Store).Close()`.
- `Migrate(ctx, url, opts...) error`: the one-off job.
- `WithClock(clock.Clock)` (default `clock.Real()`).
- `WithMigrate(on bool)`: default true; false checks the schema only, and an older one is `ErrSchemaTooOld`.
- `*Store` implements `store.ConversationStore`, `store.RunStore`, `store.EventLog` and `flow.Checkpointer`. `func Admin(*Store) store.Admin` is a separate value, as in memory and sqlite.
- `ErrSchemaTooNew` (a newer binary migrated the database) and `ErrSchemaTooOld`.

`agentkit migrate` (cmd/agentkit, `migrate.go`):
- Reads `AGENTKIT_STORE` and refuses anything but a `postgres://` or `postgresql://` URL, without echoing it.
- Runs `postgres.Migrate` and prints nothing on success.
- Safe while servers run, and to repeat.

### Schema

`pkg/store/postgres/migrations/0001_entries.sql` … `0004_events.sql` are SQLite's four migrations, ported column for column:
- Tenant, id, conversation, run_id and lease_owner are `TEXT COLLATE "C"`, so ordering and cursors compare bytes, as in SQLite and Go.
- Integers and times are `BIGINT`; times are Unix ns, NULL for zero. `retryable` is `BOOLEAN`.
- Messages, record, event data and checkpoint body are `TEXT`, never `JSONB`.
- The same primary keys and partial indexes, including `runs_unfinished` (unique on `(tenant, conversation)` where the status is not final). Leases are columns on `runs`.

Events (0004, also new in SQLite):
```sql
CREATE TABLE events (
    tenant TEXT COLLATE "C" NOT NULL, run_id TEXT COLLATE "C" NOT NULL, seq BIGINT NOT NULL,
    type TEXT NOT NULL, data TEXT NOT NULL DEFAULT '', created_at BIGINT NOT NULL,
    PRIMARY KEY (tenant, run_id, seq));
```

Migrations:
- They are recorded in `schema_migrations(version, name, applied_at)`, each in its own transaction (DDL is transactional), under `pg_advisory_lock(hashtext('agentkit.store.migrate'), hashtext(current_schema()))`. The lock is held on one connection.
- The tables live in the connection's current schema, so one database can hold several stores. The tests use that: one schema per test.

### Design decisions

- **The database keeps every rule; Go never checks and then writes.**
  - `Append` is one `INSERT … SELECT` that inserts only if entry seq−1 exists. It stamps the time as `GREATEST(now, previous entry's time)`. When nothing is inserted, the stored row decides the result: the same entry is nil, a different one `ErrConflict`, a missing predecessor `ErrGap`; otherwise the whole statement runs again.
  - `Create` is a plain `INSERT`; a 23505 on `runs_pkey` or `runs_unfinished` is resolved from the stored rows.
  - `Transition`, `Claim`, `Renew` and `Release` are each one conditional `UPDATE … RETURNING`. When nothing changes, `getRun` says why.
  - `AppendEvents` is one READ COMMITTED transaction: read the last event and the taken rows the batch overlaps, decide (`backend.NewEvents`, shared with memory and SQLite), then insert from `unnest`. A 23505 on `events_pkey` means another writer took a position in between, and the whole append runs again from fresh reads.
- **Retries:** `retry` runs the whole statement or transaction again, up to 8 attempts with a 1, 2, … ms pause. It retries on 40001, on 40P01, and when a write found its reads overtaken (`errRetry`). After 8 attempts the database's error is returned, wrapped ("gave up after 8 attempts"), so callers can still read its SQLSTATE.
- **Never `now()` in SQL.** Times come from the injected clock, so a fake clock drives the suites and simulations on Postgres exactly as on memory.
- **Runner-chosen event seqs** (agreed with the owner). The writer passes seq = `LastEventSeq` + 1 and counts on. This makes a retry an idempotent no-op. It also fences a stale lease holder: its event at a taken seq is `ErrConflict`. And T090 can use the seq as the bus ID and the SSE `Last-Event-ID`.
- **Postgres refuses text it cannot hold (22021, 22P05) as `ErrInvalid`.** Most values are refused before SQL by the text rule. A checkpoint is not covered (see Findings).

### Gates

All runs were against a local Postgres 17.11 (Homebrew), started with `max_connections=100`, CI's default.

- `make check`: exit 0.
- `go test -tags integration -race -count=20 -timeout 30m ./pkg/store/...` with `AGENTKIT_PG_URL` and `AGENTKIT_REQUIRE_INTEGRATION=1`: clean.
  - `pkg/store` 1.7 s, `memory` 147 s, `postgres` 448 s, `sqlite` 1,247 s.
  - SQLite now takes 21 of the 30 minutes on this machine. A slower runner needs `-timeout 45m`.
- Coverage-guided fuzzing of the four Postgres simulation targets, 30 s each with `-parallel 4`, each run a whole simulation in a new schema: clean.
  - `FuzzConversationSimulation` 1,767 execs, `FuzzRunSimulation` 1,948, `FuzzAdminSimulation` 1,974, `FuzzEventSimulation` 2,037.
- Postgres package tests: 576 suite subtests, the same count as SQLite, plus the Postgres tests (migrations, cross-pool races, byte-exact storage, forced retries, checkpoint text). Every test drops its schema; none are left after a run. About 12 s, or 22 s under `-race`.
- `golangci-lint run --build-tags integration ./pkg/store/... ./cmd/agentkit/... ./pkg/bus/...`: 0 issues.
- Not measured: Postgres benchmarks. T029's `bench_test.go` shapes port directly if T090 needs numbers.

### Mutation check

Each bug was planted alone by a script, the package's tests were run (`-tags integration` against Postgres 17 for the `postgres` plants), and the file was restored. 41 plants, 39 caught on the first run. The two survivors (G13, G17) led to new tests and are caught now. Plants that broke the build were re-planted in a form that compiles (G11, G18). Suite names: C conversation, R run, A admin, K checkpointer, E event log.

**Suite gaps, in the memory store:**

| Planted bug | Failing tests |
|---|---|
| P1 entry-time clamp to the previous entry removed | C `EntryTimesNeverGoBackwards` |
| P2 a retryable failure with no `NextAttemptAt` never due | R `RetryableFailureWithNoNextAttemptIsDueAtOnce`; A tests |
| P3a next-attempt time not range-checked | R `TimesOutsideTheStoredRange` |
| P3b lease expiry not range-checked | R `TimesOutsideTheStoredRange` |

**Event log, in the memory store:**

| Planted bug | Failing tests |
|---|---|
| E1 no time clamp | E `TimesNeverGoBackwards`, `Simulation` |
| E2 a different event at a taken seq accepted | E `AppendRules`, `ConcurrentAppendsAreDenseOrConflict` |
| E3 no gap check | E `AppendRules`, `TenantIsolation` |
| E4 a partly stored batch appended whole | E `DataRoundTripsByteForByte`, `RetriesAreNoOps` |
| E5 reads share the stored slice | E `KeepsCopies` |
| E6 no UTF-8 check on data | E `AppendRules`, `Simulation` |
| E7 limit ignored | E `EventsAfterLimits`, `Simulation` |
| E8 consecutive seqs not checked | E `AppendRules`, `Simulation` |
| E9 writes keep the caller's slice | E `KeepsCopies` |
| E10 empty type accepted | E `AppendRules` |
| E11 an identical retry moves the stored time | E `RetriesAreNoOps` |

**Text rule, in `backend`:** T1 (ids), T2 (owner), T3 (`Details.Error`), T4 (pending paths), T8 (NUL) were caught by R `TextAStoreCannotHold`. T6 (`Record`) and T7 (entry `RunID`) were caught by C `TextAStoreCannotHold`. A check on messages (T5) was dropped as unreachable: `llm` will not marshal Opaque data that is not UTF-8 JSON.

**Postgres, in `pkg/store/postgres`** (every suite failure also fails the same suite under `TestTwoPools`):

| Planted bug | Failing tests |
|---|---|
| G1 40001 not retried | `TestSerializationFailuresAreRetried/*/40001/once` |
| G2 retries unbounded by one | `TestSerializationFailuresAreRetried/*/always` |
| G3 no advisory lock around migrations | `TestConcurrentOpensMigrateOnce` (concurrent `CREATE TABLE`: 23505 on `pg_type`) |
| G4 migrate accepts a newer schema | `TestOpenRefusesANewerSchema` |
| G5 `WithMigrate(false)` accepts an older schema | `TestWithMigrateOffRefusesAnOldSchema` |
| G6 messages re-encoded through `jsonb` | C `AppendIsCreateIfAbsent`, `MessageJSONRoundTripsByteForByte`, …; `TestJSONIsStoredByteForByte` |
| G7 `Append` without the time clamp | C `EntryTimesNeverGoBackwards` |
| G8 `AppendEvents` without the time clamp | E `TimesNeverGoBackwards`, `Simulation` |
| G9 `Claim` takes a held lease | R `Leases`, `Simulation`; `TestRacesAcrossPoolsHaveOneWinner`; `TestSerializationFailuresAreRetried/Claim` |
| G10 `runs_unfinished` not unique | R `OneUnfinishedRunPerConversation`, `Simulation`; A `Simulation` |
| G11 a 23505 on `events_pkey` surfaced instead of re-read | `TestRacesAcrossPoolsHaveOneWinner` (AppendEvents) |
| G12 tenant filter dropped from `EventsAfter` | E `DataRoundTripsByteForByte`, `RetriesAreNoOps`, `TenantIsolation`, … |
| G13 run ids with a language collation (`en-US-x-icu`) instead of `"C"` | survived; caught by the new R `IDsBreakTiesAsBytes` |
| G14 `Append` without the gap check | C `AppendIsCreateIfAbsent`, `TenantIsolation`, … |
| G15 `Transition` ignores `from` | R `Lifecycle`, `Simulation` |
| G16 `getRun` without the text short-circuit (22021 → `ErrInvalid`) | R `TextAStoreCannotHold` |
| G17 22021 not mapped to `ErrInvalid` | survived; caught by the new `TestCheckpointTextPostgresCannotHold` |
| G18 an identical `Append` retry is a conflict | C `AppendIsCreateIfAbsent/IdenticalRetryIsANoOp`, `Simulation` |
| G19 `Release` ignores the owner | R `Leases`, `Simulation`; A `Simulation` |

### Image size

The binary is `cmd/agentkit`, built `CGO_ENABLED=0 GOOS=linux -trimpath -ldflags "-s -w"` (amd64): 14.88 MB on main, 18.74 MB with the Postgres store linked in through `agentkit migrate`. That is +3.87 MB for pgx and its dependencies, including `golang.org/x/text`. T029 measured the image at the binary plus about 2.3 MB, which puts it near 21 MB, under `IMAGE_BUDGET_MB=30`. CI's `image` job checks this on push.

### Findings (not fixed: outside this task's files)

- **Checkpoints escape the text rule.**
  - `flow.Checkpointer.Save` checks neither the run id nor the body. `json.Marshal` passes a `json.RawMessage`'s bytes through, so a checkpoint whose state holds bytes that are not UTF-8 can be saved.
  - Memory and SQLite keep it. Postgres refuses it as `store.ErrInvalid` (`TestCheckpointTextPostgresCannotHold`).
  - Deciding whether `Save` should refuse such text everywhere is a contract change in `pkg/flow` (`Checkpointer` docs and `flowtest`).
- **archtest does not see integration-tagged files.** `go list` in `archtest_test.go` runs without `-tags integration`. I allowlisted the tagged files' imports anyway (`internal/testkit` for `pkg/store`, pgx for `cmd/agentkit`). Adding `-tags integration` to that `go list` would check them.
- **golangci-lint does not see integration-tagged files** (T089 reported this too): `.golangci.yml` needs `run.build-tags: [integration]`. I linted with `--build-tags integration` by hand: 0 issues.
- **`make fuzz` skips the Postgres fuzz targets.** It lists targets without the tag. Their seed corpora run in the CI `integration` job. Coverage-guided fuzzing of Postgres was run by hand (see Gates).
- **`scripts/dev.sh` does not exist.** `testkit.IntegrationEnv`'s skip message points to it (T006). Locally I used a Postgres 17 from Homebrew.
- **A flaky Redis bus test (T089's code).** The first CI run of the merged `integration` job (1525201, run 38314136107) failed `pkg/bus/redis` `TestExpiredStreamRestarts`: "Close did not return in 10s", in the test's cleanup, 10 s into the package. Every store package passed in that run. The rerun of the job was green, and locally the test passed 30 times under `-race` and in three runs of the full CI command at `GOMAXPROCS=4`. So `Bus.Close` can hang for more than 10 s, and it seems more likely now that bus and store packages share the 4-core runner. Owner: T089 or T090, which closes buses on shutdown.
- **Branch protection is off on `main`.** "Required" holds by convention: a red job turns the commit red, as for `fuzz`.

### For T090 (stream API)

- **One store value.** The `*postgres.Store` the runner uses is the `store.EventLog`. Events are scoped by the tenant in ctx like everything else. The run id is a label: the log does not check that the run exists.
- **Write the event to Postgres first, then publish it on the bus with the same seq** (`bus.ID{Seq: uint64(e.Seq)}`; seqs start at 1). The log is the source of truth and the bus the live tail. A crash between the two loses only the live copy, and the replay below brings it back.
- **The runner chooses seqs.**
  - On taking a lease, read `LastEventSeq` once, then number events from there. Batch events of one step into one `AppendEvents` call (all or none).
  - On a failed or unknown append, call it again with the same batch: identical events at taken seqs are a no-op.
  - `ErrConflict` means another process wrote that seq: this runner lost the lease. Stop, don't renumber.
  - `ErrGap` is a bug in the numbering.
- **Stream handler:**
  - Replay `EventsAfter(run, lastEventID, page)` in pages until a short page, then `Subscribe(topic, lastSentSeq)` on the bus.
  - `Last-Event-ID` is the seq, or `<seq>.<n>` for a delta; replay after its seq.
  - On `ErrGap` from the bus, replay from the log again after the last seq sent.
  - An event's `CreatedAt` never decreases along a run's log; it is the store's time, not the runner's.
- **Text deltas are live-only** (E18) and never in the log. A durable event carries the state, so a client that missed deltas loses nothing it needs.
- `EventsAfter` is a keyset read on the primary key: one index range scan per page, whatever the log's length.

### For T037 (wiring the store into `serve`)

- **The switch:** `AGENTKIT_STORE=postgres://…` (or `postgresql://`) selects `postgres.Open(ctx, url, postgres.WithClock(clk))`. `migrate.go` defines `envStore`; move it next to serve's env consts when you add the switch.
- **Migrations:** `AGENTKIT_STORE_MIGRATE=off` passes `postgres.WithMigrate(false)`, and serve then exits on `postgres.ErrSchemaTooOld` with a message that names `agentkit migrate`. Any other value, or unset, migrates at startup (the default). Both modes refuse `ErrSchemaTooNew`.
- **Pool size:** the pool is pgx's default (max(4, NumCPU) connections). Size it with `pool_max_conns` in the URL; document that rather than adding a knob.
- **The recovery loop** gets `postgres.Admin(s)`, never the request path (archtest keeps backends out of `pkg/agentapi`).
- **Shutdown:** `Close` waits for connections in use, so call it after the runner has stopped.
- **The schema follows `search_path`:** set it in the URL to keep agentkit's tables out of `public`.
