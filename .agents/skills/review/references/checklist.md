# Review checklist

Compact form of the `review` skill, for agents and people. Each line names the
rule; the reason is in [ENGINEERING.md](../../../../docs/ENGINEERING.md).
**B** = blocking when broken, **O** = optional.

## Correctness

- **B** Errors are returned or handled, wrapped with `%w`, classified with the package's sentinels; a cancelled `ctx` is reported as `ctx.Err()`, not as a failure.
- **B** Every goroutine has an owner that stops it and waits for it; nothing runs after the call that started it returns (unless documented).
- **B** Nothing returned to a caller shares memory with internal state, and nothing a caller passed in is kept and changed later (unless documented).
- **B** Limits, budgets and indexes: off by one, not enforced, enforced after the fact.
- **B** Locks: held across IO or a callback, missing on one path, or dropped between a check and its use.

## Boundaries and shape (§1)

- **B** `go test -count=1 ./internal/archtest/` passes (cached passes miss new imports).
- **B** No `pkg/` or `internal/` import of `cmd/`; no parent importing its own subpackage; no `*test` package in non-test code (§1.4).
- **B** A new third-party import has a `rules.go` line with its reason, and a `docs/SIZES.md` row if it moves a binary or image (AGENTS.md, Sizes).
- **B** No `init()`, no package-level mutable state, no global registry; `New(...) (*T, error)` validates (§1.3).
- **B** A second implementation of a seam goes through the one picker and passes the conformance suite (AGENTS.md, Seams; §2.4).
- **O** Interfaces small and defined by the caller; `doc.go` says what and why; a runnable `Example`.

## Tests (§2)

- **B** Each acceptance criterion has a test that exists and checks that criterion.
- **B** No `time.Sleep`, and no wall-clock deadline that a slow machine can miss; concurrency is driven from the test's side (§2.5, §2.6).
- **B** No assertion on an error or log message string; `errors.Is` / `errors.As` (§2.5).
- **B** Golden files only for wire formats, rewritten with `make golden`.
- **B** Planted bugs (3 to 5) are each caught by a test; claims in the task's mutation list hold when re-planted.
- **B** Changed tests pass `-race -count=50` (`-count=300` for timing or goroutine order).
- **B** Code with many paths checks its invariants after every run through one helper (§2.6).
- **O** Black box (`package foo_test`), fakes not mocks, `t.Parallel()`, table-driven only when inputs vary; no test that duplicates a lower tier.

## Task file (§5)

- **B** Ticked criteria name their tests; unticked ones say what they need; `status` is `done` only with every box ticked.
- **B** The epic checklist matches the status (`scripts/lint-skills.sh`).
- **O** Notes record decisions, the mutation list, and "For T0xx" sections for the tasks that build on it.
