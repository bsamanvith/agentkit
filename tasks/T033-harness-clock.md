---
id: T033
title: "harness: use pkg/clock instead of WithNow"
epic: E05
status: done
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

- [x] Record timings (message times, model and tool durations) come from the injected clock: with `clocktest.Fake`, advancing it by fixed amounts gives exactly those durations in the `Record`.
- [x] All existing harness tests, invariants and the seeded simulation pass unchanged.
- [x] archtest: `pkg/harness` may import `pkg/clock` (update the rule if needed).

## Notes

**Done (2026-10-21).**

- `harness.WithClock(clock.Clock)` replaces `WithNow`; the default is `clock.Real()`, and a nil clock is `ErrInvalidConfig` as before. `WithNow` is **removed**, not kept as a deprecated wrapper: its callers outside the package were `cmd/agentkit` and tests in `pkg/runner`, `pkg/runner/runnerrpc` and `pkg/agentapi`, all of which already held a `clock.Clock`, so they moved to `WithClock` in this change (one way to set it, and no `SA1019` on a deprecated name). The harness reads only `Now`; it arms no timers.
- Tests: `TestRecordTimingsComeFromTheClock` (clock_test.go) runs on `clocktest.Fake`. The harness waits on no timer, so `WaitForTimers` does not apply; the model wrapper and the tool advance the fake by fixed amounts inside their calls, on the run's goroutine (one tool call per turn, so nothing races), and the record holds exactly those times: requested/first event/done per turn, message times, tool start and duration, run start and finish. The other harness tests keep their millisecond-per-read `fakeClock`, now a `clock.Clock` (it embeds `clock.Real()` for the timer methods the harness never calls); their assertions are unchanged.
- archtest: `pkg/harness` has no module-import row, so it may import `pkg/clock` already; the rule run passes with the import. `pkg/runner/runnerrpc` gained `pkg/clock/clocktest` (its test's fixed clock).

**Mutation self-check** (each made a test fail):

1. Tool duration halved (`tools.go`): TestRecordTimingsComeFromTheClock.
2. `run.now` reads `time.Now`: TestRecordTimingsComeFromTheClock (events outside the fake's time).
3. `DoneAt` set to `FirstEventAt`: TestRecordTimingsComeFromTheClock.
4. Tool results message stamped at the reply's time instead of when tools finished: TestRecordTimingsComeFromTheClock.
5. Tool start read 1ns early: TestRecordTimingsComeFromTheClock.
6. Nil clock accepted: TestNewRejectsBadConfig.

### For later tasks

- Pass the same `clock.Clock` to `harness.WithClock` and `agentapi.WithClock` (cmd/agentkit does) so that times in events and resources agree.
