---
id: T002
title: Test helpers (testkit and fake clock)
epic: E01
status: todo
depends_on: [T001]
---

# T002: Test helpers (testkit and fake clock)

## Context

A few shared helpers make the rules in [ENGINEERING.md §2.5](../docs/ENGINEERING.md#25-rules-that-keep-tests-from-being-flaky-or-brittle) easy to follow. Keep this package tiny; add helpers only when a second caller needs them.

## Scope

- `pkg/clock`: `Clock` interface (`Now`, `NewTimer`, `NewTicker`, `After`) and `clock.Real()`.
- `pkg/clock/clocktest`: `Fake` clock with `Advance(d)` that fires due timers deterministically.
- `internal/testkit`:
  - `Golden(t, path, got []byte)` with a package-level `-update` flag.
  - `IntegrationEnv(t, key) string`: returns the env value; skips if unset, fails if `AGENTKIT_REQUIRE_INTEGRATION=1`.
  - `Eventually(t, timeout, cond)`: poll-with-deadline, the only sanctioned way to wait.

## Out of scope

- Assertion libraries. Use the standard `testing` package; `go-cmp` is allowed for diffs.

## Acceptance criteria

- [ ] Fake clock: a timer set for 5s does not fire after `Advance(4s)` and fires exactly once after `Advance(1s)`.
- [ ] Fake clock: tickers fire once per elapsed period across a large `Advance`.
- [ ] `Golden` fails with a readable diff on mismatch and rewrites the file with `-update`.
- [ ] `IntegrationEnv` skips when unset and fails when unset with `AGENTKIT_REQUIRE_INTEGRATION=1`.
- [ ] `clock.Fake` and `clock.Real()` satisfy the same interface (compile-time assertion, no test needed).

## Notes

-
