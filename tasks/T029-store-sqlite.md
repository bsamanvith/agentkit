---
id: T029
title: "pkg/store/sqlite: schema, migrations, all suites"
epic: E05
status: done
depends_on: [T028, T038]
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
  - `checkpoints` has `shape TEXT NOT NULL DEFAULT ''` for `flow.Checkpoint.Shape` (an empty value stays empty).
  - `tenant` is part of every primary key and every query (see the E05 contracts section); tenant IDs are case-sensitive, so compare them as stored, never case-folded.
- WAL mode and a busy timeout. Concurrent writers from several goroutines and from two processes on one file.
- Cursors are opaque, encoding `(seq)` or `(created_at, seq)`.
- Schema written to port column for column to Postgres (no SQLite-only types in the contract).
- **`store.Admin`** (T038, planner decision, see E05): implement the one cross-tenant read for SQLite and pass `storetest.RunAdmin`.
- Read T028's "For T029 (SQLite)" Notes first: they give the exact transaction and query shapes (create-if-absent `Append`, keyset cursors, conditional lease and transition updates, the partial unique index for one unfinished run per conversation).

## Acceptance criteria

Suite tests are subtests of `storetest.RunConversationStore` (C), `RunRunStore` (R), `RunCheckpointer` (K) and `RunAdmin` (A), each with its seeded simulation; `pkg/store/sqlite` runs them as `TestConversationStore`, `TestRunStore`, `TestCheckpointer` and `TestAdmin`.

- [x] Passes `storetest.RunConversationStore`, `storetest.RunRunStore`, `storetest.RunCheckpointer` and `storetest.RunAdmin` on a temp-file database. (`TestConversationStore`, `TestRunStore`, `TestCheckpointer` (K `Flow` is `flowtest.RunCheckpointer`), `TestAdmin`: a new file in `t.TempDir()` per suite test; the seed corpora of `FuzzConversationSimulation`, `FuzzRunSimulation`, `FuzzAdminSimulation`; `TestAdminIsASeparateValue` (memory's, with its reflection check). All four suites again in `TestTwoHandles`, with every call sent in turn to one of two `Store`s open on one file, so the suites' races run between two writer connections that only SQLite's lock serializes)
- [x] Opening an old-version database applies the missing migrations; opening one newer than the binary fails clearly. (`TestOpenUpgradesAnOlderSchema`: a file written by a binary that knew only migration 1 (entries; `Create` fails on it) keeps its entries after a full `Open`, and runs and checkpoints work; `TestOpenRefusesANewerSchema`: binaries that know 1 and 2 of the 3 migrations get `ErrSchemaTooNew`, naming both versions, and the file is unchanged; `TestConcurrentOpensMigrateOnce`: four opens of a new file at once all succeed)
- [x] Two processes appending to one conversation on the same file: no lost entries, conflicts reported (test via a helper subprocess or two connections; recorded how). (`TestTwoProcessesAppendToOneConversation`: the test binary runs itself twice as `TestAppendHelperProcess`, two real processes with POSIX locks between them, each appending 150 entries the runner's way (read `Context`, append at `Seq+1`, on `ErrConflict` reload). The log has all 300, dense, each process's in its own order; a typical run reports about 30 conflicts. Also `TestRacesAcrossHandlesHaveOneWinner`: 16 goroutines over two `Store`s released by one closed channel race for a conversation's unfinished run, a lease, a transition and `Seq` 1; exactly one wins each and the rest get `ErrUnfinished`, `ErrLeased`, `ErrConflict`, `ErrConflict`)
- [x] Mutation list recorded in Notes (at least: `INSERT OR REPLACE` instead of create-if-absent, tenant filter dropped from one query, a cursor that skips an entry, messages re-encoded on write). (24 plants below, none survived)

## Notes

### API (package `sqlite`)

- `func Open(ctx context.Context, path string, opts ...Option) (*Store, error)`: opens or creates the file and migrates it. `func (*Store) Close() error`.
- `func WithClock(clock.Clock) Option` (default `clock.Real()`), `func WithReaders(n int) Option` (read connections, default `max(2, GOMAXPROCS)`).
- `*Store` implements `store.ConversationStore`, `store.RunStore` and `flow.Checkpointer`. `func Admin(s *Store) store.Admin` is a package function over an unexported `admin{s}`, as in memory.
- `var ErrSchemaTooNew`: the file has migrations this binary does not know.

### Schema

Migrations are `pkg/store/sqlite/migrations/NNNN_name.sql`, embedded, numbered densely from 0001 (checked at open), each applied in its own `BEGIN IMMEDIATE` transaction and recorded in `schema_migrations(version, name, applied_at)`. That table is used instead of `PRAGMA user_version`, so the scheme ports to Postgres.

```sql
-- 0001_entries.sql
CREATE TABLE entries (
    tenant TEXT NOT NULL, conversation TEXT NOT NULL, seq INTEGER NOT NULL,
    kind TEXT NOT NULL, messages TEXT NOT NULL, record TEXT NOT NULL DEFAULT '',
    run_id TEXT NOT NULL DEFAULT '', covers INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (tenant, conversation, seq));
CREATE INDEX entries_compactions ON entries (tenant, conversation, seq) WHERE kind = 'compaction';
CREATE INDEX entries_created ON entries (tenant, conversation, created_at, seq);

-- 0002_runs.sql
CREATE TABLE runs (
    tenant TEXT NOT NULL, id TEXT NOT NULL, conversation TEXT NOT NULL, status TEXT NOT NULL,
    input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens INTEGER NOT NULL DEFAULT 0, cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT '', retryable INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER, attempts INTEGER NOT NULL DEFAULT 0, pending TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL, finished_at INTEGER,
    lease_owner TEXT NOT NULL DEFAULT '', lease_expires INTEGER,
    PRIMARY KEY (tenant, id));
CREATE UNIQUE INDEX runs_unfinished ON runs (tenant, conversation)
    WHERE status NOT IN ('completed', 'rejected', 'canceled');
CREATE INDEX runs_conversation ON runs (tenant, conversation, created_at, id);
CREATE INDEX runs_suspended ON runs (tenant, created_at, id) WHERE status = 'suspended';
CREATE INDEX runs_resumable ON runs (tenant, created_at, id) WHERE status IN ('running', 'failed');
CREATE INDEX runs_resumable_all ON runs (created_at, tenant, id) WHERE status IN ('running', 'failed');

-- 0003_checkpoints.sql
CREATE TABLE checkpoints (
    tenant TEXT NOT NULL, run_id TEXT NOT NULL,
    shape TEXT NOT NULL DEFAULT '', body TEXT NOT NULL,
    PRIMARY KEY (tenant, run_id));
```

Every query plan was checked with `EXPLAIN QUERY PLAN`: each list walks its index in output order with no sort (`ListResumable` → `runs_resumable`, `ListResumableAll` → `runs_resumable_all`, `ListSuspended` → `runs_suspended`, `List` → `runs_conversation`, `Unfinished` → `runs_unfinished`, `Context`'s compaction lookup → covering `entries_compactions`, a `History` seek by time → `entries_created`).

### Design decisions

- **The constraints decide, not a read in Go.** `Append` reads the last entry (for `ErrGap` and the time), then does a plain `INSERT`; only when the insert fails does it read the row at that position, to tell a retry (`Same`, nil) from `ErrConflict`. `Create` inserts first; on failure the stored rows tell the same run (returned), the id in another conversation (`ErrConflict`) and another unfinished run (`ErrUnfinished`, from the partial unique index). `Transition` is `UPDATE … WHERE tenant = ? AND id = ? AND status = ?` and reads the run only when no row changed, to tell `ErrNotFound`, `ErrFinal`, `ErrInvalid` and `ErrConflict` apart in the contract's order. `Claim`, `Renew` and `Release` are one conditional `UPDATE` each. So the mutations "unique index dropped", "`INSERT OR REPLACE`", "no `WHERE status`" and "lease check skipped" each break a suite rather than being covered by a redundant check.
- **The lease is on the run's row**, not in a `leases` table as Scope lists: T028's and T038's binding SQL (`lease_owner = '' OR lease_expires <= :now`) and the resumable indexes assume it, a claim is one `UPDATE`, and a run has at most one lease.
- **Connections**: two pools per `Store`. Writes go through one writer connection whose transactions begin `IMMEDIATE` (`_txlock=immediate`), so a read-then-write takes the write lock first and never fails half way with `SQLITE_BUSY_SNAPSHOT`. Reads use a pool of `query_only` connections; in WAL mode they never block the writer. `busy_timeout` is 30 s and `foreign_keys` is on (no foreign keys yet), set in the DSN so that every pooled connection gets them. Durability is SQLite's default (`synchronous=FULL`).
- **WAL is switched on at `Open`, with a retry while the file is busy.** The first version set `journal_mode(WAL)` in the DSN; `TestConcurrentOpensMigrateOnce` then failed about one run in five with `SQLITE_BUSY` at once, because changing the journal mode does not go through the busy handler. `schema_migrations` is likewise created inside the migration's IMMEDIATE transaction, not before it.
- **Times** are `INTEGER` Unix nanoseconds, full precision, and NULL is the zero time (`NextAttemptAt`, `FinishedAt`, an empty lease). The resumable query therefore reads `next_attempt_at IS NULL OR next_attempt_at <= :now` (a retryable failure with no time is due at once). The suites' fake clock starts in 2026 (`clocktest.NewFake(time.Time{})`), not year 1 as T038's note feared. A time the caller asks the store to keep outside 1678–2262 (a `NextAttemptAt`, or a lease ttl that ends there) is `ErrInvalid`; the memory store accepts any. A time a read only compares with (`now`, `FromTime`) may be anything: it is clamped just past the stored range, which compares the same.
- **`Context` is one statement** (`seq >= COALESCE((SELECT MAX(seq) … kind = 'compaction'), 1)`), so it reads one snapshot without a transaction; `view.Seq` is its last entry's `seq`.
- **Pages** fetch `limit + 1` rows; the extra row says whether there is a next page, so a page with a `Next` holds exactly `limit`. Cursors are `backend.HistoryCursor` / `backend.RunCursor`, as in memory. A `History` seek by time orders by `(created_at, seq)`: the same order, since times never go backwards along a log, read from `entries_created` without a sort.
- **Checkpoints** are one JSON document (`flow.Checkpoint` "is plain JSON, so a store can keep it as one document") plus `shape` in its own column; an empty shape stays `''`. `flowtest` compares `Initial` and `Steps` as JSON values, so re-marshalling `RawMessage` (which compacts it) is fine.
- **Messages and records** are bound as `TEXT` with the exact bytes `backend.CheckEntry` produced; `record` is `''` when empty and read back as nil.
- **Tenant and other ids** use the default `BINARY` collation (bytewise, like `strings.Compare`), so the admin order matches Go's.
- **Driver errors** (`SQLITE_BUSY` after the busy timeout, a full disk, a closed store) come back wrapped with the operation, matching no `store` sentinel. `busy` names the driver's `*sqlite.Error` (hence the archtest line for `modernc.org/sqlite` itself, not `.../lib`).

### Suite gaps found (for the owner of `storetest`)

Two plants survived the four suites and are now caught by tests in this package only; both are rules of the contract that a later backend could break the same way:

- Times never going backwards along a log, when **writers' clocks disagree** (no suite moves a clock backwards): `TestEntryTimesNeverGoBackwardsAcrossWriters`.
- A failed run with **`Retryable` and no `NextAttemptAt`** is resumable at once (no suite fails a run that way): `TestRetryableFailureWithNoNextAttemptIsResumable`. A NULL-encoding backend misses it easily.

Both fit `storetest` (a clock wrapper that can step back for the first; one more case in `ListResumableFindsInterruptedAndDueRuns` for the second). `pkg/store/storetest` was outside this task's files.

### Gates

- `make check` exit 0.
- `go test -race -shuffle=on -count=20 -timeout 60m ./pkg/store/...` (the 10 m default is too short: `sqlite` takes about 12 min here): clean: `pkg/store` 1.6 s, `memory` 71 s, `sqlite` 730 s.
- `make fuzz FUZZTIME=30s`: clean, including the three new targets here (33 targets; here, in 30 s on 14 workers: `FuzzConversationSimulation` 6,925 runs, `FuzzRunSimulation` 8,071, `FuzzAdminSimulation` 4,574, each a whole simulation on a new file).
- The package's tests take about 4 s, or 36 s under `-race` (modernc's C-to-Go translation is slow under the race detector).

### Performance (Apple M4 Pro, macOS, APFS SSD; `go test -run '^$' -bench . -benchtime=1000x ./pkg/store/sqlite/`)

| Benchmark | Result |
|---|---|
| `BenchmarkAppend`: one entry per op, one conversation, each a committed transaction | 107 µs/op, 2672 B/op, 67 allocs/op: **1k entries in about 107 ms** |
| `BenchmarkListResumable/mixed`: a page of 100 from 10k runs, a quarter each unclaimed, leased, suspended, completed | `ListResumable` 0.21 ms per page; `ListResumableAll` 0.21 ms |
| `BenchmarkListResumable/mostly-leased`: 10k running runs, all leased but the newest 100, so the query walks the whole index | `ListResumable` 3.2 ms per page; `ListResumableAll` 2.2 ms (the lease condition cannot be indexed, so the scan skips 9,900 leased rows) |

### Image size

Nothing in `cmd/` imports the store yet, so the shipped image is unchanged. Measured in a copy of the tree with a blank use of `sqlite.Open` added to `cmd/agentkit` (Dockerfile flags: `CGO_ENABLED=0 -trimpath -ldflags "-s -w"`):

| | binary linux/amd64 | binary linux/arm64 | image (arm64, `make image-size`) |
|---|---|---|---|
| `cmd/agentkit` today | 15.00 MB | 13.96 MB | 17.31 MB |
| with the SQLite store | 18.77 MB | 17.63 MB | 20.98 MB |
| added | +3.78 MB | +3.67 MB | +3.67 MB |

**With the store wired in, the image is 1 MB over `IMAGE_BUDGET_MB=20`.** See "For T037". (`make image` failed on `main` until 0f22246, because `.dockerignore` kept out `connectors/`, which `cmd/agentkit` has imported since T046–T048. The measurement was taken with that fix.)

### Mutation check

Each bug was planted alone in `pkg/store/sqlite` by a script, then `go test -count=1 ./pkg/store/sqlite/` was run (`-count=20` for the one marked timing) and the file restored. 24 plants, all compiled, none survived. Suite subtests are named as above; every suite failure also failed `TestTwoHandles` and the matching fuzz seed corpus.

| Planted bug | Failing tests |
|---|---|
| `Append` uses `INSERT OR REPLACE` | C `AppendIsCreateIfAbsent`, `RacingWritersExactlyOneWins`, `Simulation`; `TestRacesAcrossHandlesHaveOneWinner` |
| Tenant filter dropped: `Context`'s compaction subquery | C `TenantIsolation`, `Simulation` |
| Tenant filter dropped: the run lookup (`Get`, and inside `Transition`, `Claim`, `Release`) | every R and A test (run invariant 1) |
| Tenant filter dropped: `ListResumable` | every R and A test (run invariant 6, A invariant 7) |
| Tenant filter dropped: checkpoint `Load` | K `TenantIsolation` |
| Tenant filter dropped: `Append`'s read of the last entry | C `TenantIsolation`, `Simulation` |
| `History` cursor skips an entry (`seq > after + 1`) | C `HistoryPagesVisitEveryEntryOnce`, `ContextStartsAtTheLatestCompaction`, `HistoryLimitAboveMaximumIsLowered`, `Simulation`, … |
| Messages re-encoded on write (through `any`: keys sorted) | C `AppendIsCreateIfAbsent` (an identical retry conflicts), `MessageJSONRoundTripsByteForByte`, `Simulation`, … |
| `Claim` skips the lease check | R `Leases`, `Simulation`; A `Simulation`; `TestRacesAcrossHandlesHaveOneWinner` |
| Unique index on unfinished runs dropped | R `OneUnfinishedRunPerConversation`, `Simulation`; A `Simulation`; `TestRacesAcrossHandlesHaveOneWinner` |
| `Transition` without `WHERE status` | R `Lifecycle`, `Simulation`; A `Simulation`; `TestRacesAcrossHandlesHaveOneWinner` |
| Entry times may go backwards (no clamp to the previous entry's time) | `TestEntryTimesNeverGoBackwardsAcrossWriters` (survived the suites) |
| Retryable failure with no next attempt time not resumable (`IS NULL` dropped) | `TestRetryableFailureWithNoNextAttemptIsResumable` (survived the suites) |
| Admin order without the tenant tie-break | A `OrderIsStableAndLimitIsHonored`, `Concurrently`, `LimitAboveTheMaximumIsLowered`, `Simulation` |
| A newer schema not refused | `TestOpenRefusesANewerSchema` |
| `Release` ignores the owner | R `Leases`, `Simulation`; A `Simulation` |
| Run cursor repeats the last run (`>=`) | A `Simulation` (the suite then loops on the repeated page until the test times out) |
| `Renew` accepts an expired lease | R `Leases`, `Simulation`; A `Simulation`; `FuzzRunSimulation` seed 5 |
| `Context` ignores compactions | C `ContextStartsAtTheLatestCompaction`, `HistoryPagesVisitEveryEntryOnce`, `TenantIsolation`, `Simulation`, … |
| `Transition` does not count failures | R `Lifecycle`, `Leases`, `ListResumableFindsInterruptedAndDueRuns`, `Simulation`; A `ListsTheResumableRunsOfEveryTenant`, `Simulation`; `TestRacesAcrossHandlesHaveOneWinner` |
| WAL switch not retried when busy | `TestConcurrentOpensMigrateOnce` (timing: about one run in five, so certain at `-count=20`, not at `-count=1`) |
| Descending `History` by time ordered `created_at, seq DESC` | C `HistorySeeksBySeqAndTime` |
| Query times not clamped (overflowing `UnixNano`) | `TestTimesOutsideTheStoredRange` |
| Stored times not range-checked | `TestTimesOutsideTheStoredRange` |

Not planted: `Context` read in two statements without a transaction (a torn view needs a writer between the two reads; no test can force that point from outside the store, so this is kept by construction, as one statement).

### For T030 (runner)

- Open one `*sqlite.Store` per process (`sqlite.Open(ctx, path, sqlite.WithClock(clk))`) and use it as the `ConversationStore`, `RunStore` and `flow.Checkpointer`; give `sqlite.Admin(s)` to the recovery loop only. `Close` it on shutdown. Several processes on one file are safe: every rule is held by SQL, and the store passes the same suites as memory, so code that works on memory works here.
- Errors match memory's sentinels exactly. Anything else (a busy file past the 30 s timeout, a full disk) is a driver error that matches none: treat it as an unknown result. An `Append` can be retried as is; after a failed `Transition`, `Get` the run before trying again (T028's rule).
- `NextAttemptAt` and lease ttls must end between 1678 and 2262, or the store returns `ErrInvalid`. A retry policy that computes "never" as a far-future time must use `Retryable: false` instead.
- An `Append` is one committed transaction (about 0.1 ms here). Writes in one process share one connection, so the store never holds a transaction across calls, and neither should a wrapper.

### For T037 (agent API v1: wiring the store into `serve`)

- **Image budget**: linking the store brings the agentkit image from 17.3 MB to 21.0 MB, over `IMAGE_BUDGET_MB=20` (`make image-size` fails). Decide in T037: raise the budget (PLAN.md targets 10–20 MB; the driver is the price of a static binary with SQLite), or keep SQLite behind a build tag for an image variant.
- The database path is configuration (flag or env). WAL keeps `-wal` and `-shm` files next to the database, so the **directory** must be writable by the distroless `nonroot` user, on a local disk (WAL needs shared memory: no network file systems). One node only; several processes on that node are fine.
- `Open` migrates. A binary older than the file refuses it with `ErrSchemaTooNew`, so rolling back a deploy that added a migration means restoring the file too. Say so in the operations docs.
- The tenant comes from the auth middleware into `ctx`; the store refuses calls without one (`tenancy.ErrNoTenant`).

### For T071 and T073 (event log, message log: E14)

- **A new table is a new migration file**, `migrations/0004_<name>.sql`, the next number (numbers are dense from 0001; `Open` refuses a gap). Never edit an applied migration. The file runs in one transaction, so it may hold several statements. Add the store methods, then a test that a file at the previous version upgrades (`TestOpenUpgradesAnOlderSchema` uses the test-only `WithMigrations(n)` to open as an older binary).
- Keep the conventions: `tenant TEXT NOT NULL` first in every primary key and index, and `tenant = ?` in every query (the mutation list plants one dropped filter per query); times as `INTEGER` Unix nanoseconds through `storeTime`/`argTime`/`loadTime` (NULL for zero); JSON as `TEXT` with the exact bytes; ids with the default `BINARY` collation; writes through `s.write` (IMMEDIATE), reads through `s.r`.
- **Event log** (T071): `run_events(tenant, run_id, seq, kind, data TEXT, created_at, PRIMARY KEY (tenant, run_id, seq))`, appended like entries (create-if-absent `INSERT`, `ErrGap`/`ErrConflict` from the stored row), read with `seq > ?` (`Last-Event-ID` is the `seq`). SQLite has no `LISTEN/NOTIFY`: a watcher in another process polls with the keyset query; in one process, wake watchers from the writer.
- **Message log** (T073): `agent_messages(tenant, id, thread_id, …, PRIMARY KEY (tenant, id))` with an index `(tenant, thread_id, created_at, id)` for per-thread order. Exactly-once processing is a conditional update, as `Claim` is: `UPDATE … SET processed_at = ? WHERE tenant = ? AND id = ? AND processed_at IS NULL`; one row changed means this worker owns the message. Delivery retries can lease a message the way runs are leased.
- Add the cases to `storetest` first, run them on memory, then here; and re-run this task's mutation script idea on the new queries (tenant filter, `INSERT OR REPLACE`, the conditional update's `WHERE`).
