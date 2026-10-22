---
id: T056
title: "Test speed: make test back under its 30 s budget"
epic: E01
status: review
depends_on: []
---

# T056: test speed

## Context

`docs/ENGINEERING.md` §2.2 budgets the unit tier at "whole repo < 30 s". On 2026-10-12 `go test -race -shuffle=on ./...` takes **52 s** wall on 4 cores (planner measurement, clean test cache). The slowest:

| Package / test | Time |
|---|---|
| `cmd/agentkit` `TestE2EFlowKilledResumedApproved`, `TestE2ERun`, `TestE2EServeTavilyOptional`, `TestE2EServeAgent` | ~18–20 s each (in parallel; the package takes 21.9 s) |
| `connectors/exa`, `connectors/parallel`, `connectors/tavily` `TestConformance` | 16.2 s, 11.3 s, 11.0 s |
| `connectors/parallel` `TestBodyLimit` | 10.7 s |
| `pkg/store/memory` | 14.6 s (package) |
| `pkg/harness` `TestSimulatedRuns` | 5.1 s |

Coverage-guided fuzzing is already separate (`make fuzz`, its own CI job since this plan). Seed corpora run as ordinary tests and stay.

## Scope

- **Find where the time goes**: a `make test-timing` target (or script) that prints the slowest packages and top-level tests from `go test -json`. CI prints the top 10 after `make check`.
- **Fix what is waste, not coverage**:
  - e2e tests: build the binary once per package (check whether the 20 s is the `-race` build; consider building the e2e binary without `-race` while the test process keeps it, and say why it is safe);
  - `RunConnector`: run independent checks as parallel subtests where they don't share state; use a small `MaxBodyBytes` in body-limit checks (the limit is a parameter);
  - `TestBodyLimit`-style tests: a small limit instead of real 8 MB bodies under `-race`;
  - `pkg/store/memory`: find the slow part (simulation size? fuzz seeds?) and size it.
- **No check may be dropped** to save time. A long-running check that is truly needed moves behind `-short`-skip into an explicit tier with a reason, not deleted.
- Record before and after numbers (same machine, clean cache) in Notes.

## Acceptance criteria

- [ ] `go test -race -shuffle=on ./...` under 30 s wall on a 4-core machine with a clean test cache (before/after in Notes).
- [x] `make test-timing` prints the slowest packages and tests; CI prints the top 10.
- [x] Every test that existed still runs (count of tests before and after, from `go test -json`), and every mutation list in the touched packages still kills its mutants (re-run the planted bugs listed in their task files for the packages changed).

## Notes

Status `review`: the first criterion is not met and could not be measured
here. The repository has grown since the 52 s measurement (2026-10-12): under
`-race` the SQLite store alone was over 10 minutes of test-binary time, and
this machine ran at a load of 20-30 on 4 cores (many sessions at once), so no
wall number from it means "a 4-core machine". What was done instead is the
long pole of `make check`, `pkg/store/sqlite`, which was at the edge of go
test's 10-minute default timeout.

### Where the time went (profile, `-race`)

`go test -race -cpuprofile` of `pkg/store/sqlite` (T029's suites, run once by
one store and once by two handles on one file):

- **SQL parsing was a third of all CPU.** `database/sql` with
  modernc.org/sqlite parses and plans every query on every call
  (`sqlite3Prepare`, `yy_reduce`), and under `-race` the transpiled C pays
  checkptr on every pointer step. `TestAdmin/LimitAboveTheMaximumIsLowered`
  (1001 runs, then the rig's invariant check: about 16 000 reads) spent 53 %
  of its time in the parser.
- **A bare `LIMIT ?` re-prepares itself on every run**, even from a kept
  statement: SQLite plans with the value bound to a LIMIT parameter and marks
  the statement expired when it is bound again (`sqlite3Reprepare` was 40 %
  of that test after the first fix). `LIMIT CAST(? AS INTEGER)` is planned
  once; measured on a `List` query, 3.2 ms against 0.18 ms per call kept.
- **Migrating a new file was a fifth of all CPU**: every subtest opens a new
  store and `Open` runs the ten migrations' DDL through the parser.

### What changed

- `pkg/store/sqlite/stmts.go`: both pools keep each query's prepared
  statement (`stmtDB`, bounded at 256 texts, past which a query runs
  unprepared). Writes run in a `txn` that uses the writer pool's kept
  statements through `Tx.StmtContext` (database/sql reuses the one already
  prepared on the writer's connection). The writer has one connection, held
  by the transaction, so a query seen for the first time inside one runs
  unprepared and is prepared for keeping right after the transaction ends.
  Production code, not only tests: every read and write parses its SQL once
  per connection instead of once per call. SQLite re-prepares a kept
  statement itself when another handle changes the schema
  (`TestKeptStatementsSurviveASchemaChange`).
- Every `LIMIT ?` in the store is `LIMIT CAST(? AS INTEGER)` (same result:
  the store always binds an integer).
- `pkg/store/sqlite/sqlite_test.go`: the suites' stores open a copy of one
  file that `sqlite.Open` created and migrated once per test binary
  (`migrated`, `TestMain` removes it). Opening and migrating a new file stays
  tested by the tests that open their own (`migrate_test.go`, `Example`,
  `TestTwoProcessesAppendToOneConversation`).
- `scripts/test-timing.sh` reads `go test -json`: the slowest packages (go
  test's Elapsed) and top-level tests (wall from the test's last start or
  continue, since go test's Elapsed for a parallel test stops when it
  pauses), and the test count. `--output` prints what go test without
  `-json` would have (build errors, result lines, failing tests' output).
  `make test TEST_JSON=file` (and so `make check TEST_JSON=file`) keeps the
  events while printing that; `make test-timing` runs the tests and prints
  the timing. CI's check job runs `make check TEST_JSON=$RUNNER_TEMP/test.json`
  and then a "slowest tests" step (also after a failure).

Nothing was skipped, moved behind `-short` or made smaller: the suites, seed
counts and sizes are as they were.

### Numbers (this machine, load 20-30 on 4 cores, `-count=1`)

| Run | Before | After |
|---|---|---|
| `pkg/store/sqlite` + `pkg/credentials/sqlite`, `-race -shuffle=on`, run side by side at the same time from origin/main and this branch | store 810.7 s, credentials 82.2 s (13 m 45 s wall) | store 290.9 s, credentials 76.1 s (4 m 56 s wall) |
| `pkg/store/sqlite` alone, `-race` | 371.5 s (reads kept only) | 209.4 s (reads and writes kept, migrated once) |
| `TestAdmin/LimitAboveTheMaximumIsLowered` alone, `-race` | 58.5 s wall, 36.5 s CPU | 25.7 s wall, 18.6 s CPU (reads kept only) |
| whole repo `go test -race -shuffle=on ./...` | 34 m 41 s wall, `pkg/store/sqlite` 647.4 s; `pkg/mcp/server` hung in `TestMaxInFlight` until the 30 m timeout | `make check` 8 m 30 s wall including lint; slowest packages `pkg/store/sqlite` 274.1 s, `pkg/runner` 97.9 s, `cmd/agentkit` 64.9 s, `pkg/store/memory` 49.3 s |

Tests (from `go test -json`, every `run` event): 5 837 before (the
`pkg/mcp/server` run was cut short by its hang), 5 847 after. Every test of
the before run runs after; the 10 new ones are 9 `pkg/mcp/server` tests the
hung run never reached and `TestKeptStatementsSurviveASchemaChange`.

### Mutation check

T029's planted bugs, re-planted by a script in the changed files and
`go test -count=1 ./pkg/store/sqlite/` (all killed), plus the new code's:

| Planted bug | Failing tests |
|---|---|
| `Append` uses `INSERT OR REPLACE` | `TestConversationStore`, `TestTwoHandles`, `TestConversationIndex`, `TestRacesAcrossHandlesHaveOneWinner`, `TestTwoProcessesAppendToOneConversation`, `FuzzConversationSimulation` |
| `History` cursor skips an entry | `TestConversationStore`, `TestTwoHandles`, `FuzzConversationSimulation` |
| `Claim` skips the lease check | `TestRunStore`, `TestAdmin`, `TestTwoHandles`, `TestRacesAcrossHandlesHaveOneWinner`, both run fuzz seeds |
| `Transition` without `WHERE status` | `TestRunStore`, `TestAdmin`, `TestTwoHandles`, `TestRacesAcrossHandlesHaveOneWinner`, both run fuzz seeds |
| `Release` ignores the owner | `TestRunStore`, `TestAdmin`, `TestTwoHandles`, both run fuzz seeds |
| Admin order without the tenant tie-break | `TestAdmin`, `TestTwoHandles`, `FuzzAdminSimulation` |
| Tenant filter dropped in `Get` | `TestRunStore`, `TestAdmin`, `TestCommandLog`, `TestTwoHandles`, both run fuzz seeds |
| Run cursor repeats the last run (`>=`) | `TestAdmin` (loops until the timeout, as in T029) |
| `ListResumable`'s kept `LIMIT CAST(?3 …)` plus one | `TestRunStore`, `TestTwoHandles` |
| A kept statement looked up by the query's length, not its text | `TestAdmin`, `TestTwoHandles`, `FuzzAdminSimulation` |
| A write uses the kept statement itself, not `Tx.StmtContext` (outside the transaction) | every suite: the writer's one connection is held, so the package times out |
| `migrated` returns the shared template, not a copy | `TestEventLog`, `TestCommandLog`, `TestToolsetStore`, `TestConversationIndex`, `TestRacesAcrossHandlesHaveOneWinner`, … |

Not killable by a behavior test, by design: preparing after the transaction
(`txn.keep`) and the `CAST` in a `LIMIT` change only how often SQLite parses,
not what a call returns; the numbers above are their check.
`TestKeptStatementsSurviveASchemaChange` pins SQLite's own re-prepare on a
schema change, which no plant in this package can break.

### Review (PR #69)

- A kept statement is one SQLite statement on the writer's connection, and
  modernc.org/sqlite steps it in place: a transaction that ran a text again
  while rows of the same text were still open reset it under them, and the
  outer loop started over forever (no store method does this today). A text
  whose rows a transaction opened from its kept statement now runs
  unprepared for the rest of that transaction
  (`TestTxnSameQueryWhileItsRowsAreOpen`, which loops without the fix).
- `--output` hid the output of a test that never ended: a timeout's panic
  and stack, a panic in another goroutine, an `os.Exit`, since go test
  writes no `fail` for that test. It now hides only tests that passed or
  were skipped, and the timing lists such a test as `(unfinished)`, timed
  to its package's end. `scripts/test-timing.sh --test` checks both modes on
  `scripts/testdata/test-timing/run.json` and runs in `make lint`.

### Findings outside this task

- `pkg/mcp/server` `TestMaxInFlight` hung for the whole 30 m timeout once in
  the before run (passed in the gate run): a flaky hang worth a task.
- `pkg/credentials/sqlite` runs its own SQL on `sqlite.Store.DB()`'s pools, so
  it does not get the kept statements; its time is mostly `bit_flips`
  (one Put and Get per bit of a sealed blob).

### For whoever continues (connectors, e2e, memory)

The rest of the Scope above was not done here, since none of it is on
`make check`'s critical path while `pkg/store/sqlite` is: `RunConnector`
subtests in parallel and a small `MaxBodyBytes` in body-limit checks, the
connectors' own 8 MB `TestBodyLimit`s, and `pkg/store/memory`'s
`LimitAboveTheMaximumIsLowered` (its rig's invariant check reads every
conversation of every tenant; the memory store's `List` scans all runs). The
e2e binary is already built once per package and without `-race`
(`cmd/agentkit/e2e_test.go`). Use `make test-timing` to pick the next one.
