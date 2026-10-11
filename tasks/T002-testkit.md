---
id: T002
title: Test helpers (testkit)
epic: E01
status: done
depends_on: [T001]
---

# T002: Test helpers (testkit)

## Context

A few shared helpers make the rules in [ENGINEERING.md §2.5](../docs/ENGINEERING.md#25-rules-that-keep-tests-from-being-flaky-or-brittle) easy to follow. Keep this package tiny; add helpers only when a second caller needs them. The fake clock moved to [T019](T019-clock.md); nothing needs it yet.

## Scope

`internal/testkit`:
- `Golden(t, path, got []byte)`; `make golden` (env `AGENTKIT_UPDATE_GOLDEN=1`) rewrites the files.
- `IntegrationEnv(t, key) string`: returns the env value; skips if unset, fails if `AGENTKIT_REQUIRE_INTEGRATION=1`.
- `Eventually(t, timeout, cond)`: poll with a deadline, the only sanctioned way to wait.

## Out of scope

- Assertion libraries. Use the standard `testing` package; `go-cmp` is allowed for diffs.

## Acceptance criteria

- [x] `Golden` passes on a match, fails with a line diff on a mismatch, and rewrites the file in update mode.
- [x] `Golden` fails clearly when the golden file is missing (and update mode creates it).
- [x] `IntegrationEnv` returns the value when set, skips when unset, and fails when unset with `AGENTKIT_REQUIRE_INTEGRATION=1`.
- [x] `Eventually` returns as soon as the condition holds and fails after the timeout when it never does.

## Notes

- Review fix: the documented `go test ./... -update` failed with "flag provided but not defined" in packages that do not import testkit. Golden updates now use `AGENTKIT_UPDATE_GOLDEN=1` behind `make golden`, which works across `./...`.
- Tests are white-box (`package testkit`) so they can drive the unexported update switch instead of the global `-update` flag; they use a recording `testing.TB` that stops on `Fatalf`/`Skipf` with `runtime.Goexit`, like the real one.
