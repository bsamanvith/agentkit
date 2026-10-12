---
id: T056
title: "Test speed: make test back under its 30 s budget"
epic: E01
status: todo
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
- [ ] `make test-timing` prints the slowest packages and tests; CI prints the top 10.
- [ ] Every test that existed still runs (count of tests before and after, from `go test -json`), and every mutation list in the touched packages still kills its mutants (re-run the planted bugs listed in their task files for the packages changed).
