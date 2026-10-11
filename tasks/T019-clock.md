---
id: T019
title: clock package and fake clock
epic: E05
status: todo
depends_on: [T001]
---

# T019: `clock` package and fake clock

## Context

Split out of T002, now part of E05: the runner and the store's leases need it, and the harness switches from `WithNow` to it. Also used later for token refresh, rate limits and retry backoff.

## Scope

- `pkg/clock`: `Clock` interface (`Now`, `NewTimer`, `NewTicker`, `After`) and `clock.Real()`.
- `pkg/clock/clocktest`: `Fake` clock with `Advance(d)` that fires due timers deterministically.

## Acceptance criteria

- [ ] A timer set for 5s does not fire after `Advance(4s)` and fires exactly once after `Advance(1s)`.
- [ ] Tickers fire once per elapsed period across a large `Advance`.
- [ ] A stopped timer never fires.
- [ ] `clocktest.Fake` and `clock.Real()` satisfy `Clock` (compile-time assertion, no test).

## Notes

-
