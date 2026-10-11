---
id: T033
title: "harness: use pkg/clock instead of WithNow"
epic: E05
status: todo
depends_on: [T019, T022]
---

# T033: Harness on `pkg/clock`

## Context

The harness reads time only through `WithNow` because `pkg/clock` (T019) did not exist yet. It does now, and the runner (T030) passes one `clock.Clock` to everything it builds.

## Scope

From the T019 handoff: the clock API is `Now`, `NewTimer`, `NewTicker`, `After`. Keep `WithNow` working or remove it in the same change (not half-way). Test with `clocktest.Fake` (start the code, `WaitForTimers` before `Advance`, one interval at a time). This was deliberately not done in T019.

- `harness.WithClock(clock.Clock)`; `WithNow` kept as a thin deprecated wrapper (or removed if nothing outside the package uses it; check `cmd/`, `pkg/agentstep`, examples).
- Harness tests use `clocktest.Fake` where they control time.

## Acceptance criteria

- [ ] Record timings (message times, model and tool durations) come from the injected clock: with `clocktest.Fake`, advancing it by fixed amounts gives exactly those durations in the `Record`.
- [ ] All existing harness tests, invariants and the seeded simulation pass unchanged.
- [ ] archtest: `pkg/harness` may import `pkg/clock` (update the rule if needed).
