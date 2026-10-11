---
id: T019
title: clock package and fake clock
epic: E05
status: done
depends_on: [T001]
---

# T019: `clock` package and fake clock

## Context

Split out of T002, now part of E05: the runner and the store's leases need it, and the harness switches from `WithNow` to it. Also used later for token refresh, rate limits and retry backoff.

## Scope

- `pkg/clock`: `Clock` interface (`Now`, `NewTimer`, `NewTicker`, `After`) and `clock.Real()`.
- `pkg/clock/clocktest`: `Fake` clock with `Advance(d)` that fires due timers deterministically.

## Acceptance criteria

- [x] A timer set for 5s does not fire after `Advance(4s)` and fires exactly once after `Advance(1s)`. (`TestTimerFiresOnceAtItsDeadline`)
- [x] Tickers fire once per elapsed period across a large `Advance`. (`TestTickerFiresOncePerPeriodForAReaderThatKeepsUp`, `TestTickerDropsTicksForASlowReaderWithoutDrifting`; reworded, see Design decisions)
- [x] A stopped timer never fires. (`TestStoppedTimerNeverFires`, `TestStoppedTickerNeverTicks`)
- [x] `clocktest.Fake` and `clock.Real()` satisfy `Clock` (compile-time assertion, no test). (`var _ clock.Clock = …` in `clocktest/fake.go`; `Real` returns the `Clock` interface, so its assertion is its return type)

## Notes

### API

- `clock.Clock`: `Now()`, `NewTimer(d) Timer`, `NewTicker(d) Ticker`, `After(d) <-chan time.Time`. `clock.Timer`: `C()`, `Stop() bool`, `Reset(d) bool`. `clock.Ticker`: `C()`, `Stop()`, `Reset(d)`. `clock.Real()` forwards to the time package.
- `clocktest.NewFake(start) *Fake` (a zero start is 1 Jan 2026 UTC), `Fake.Advance(d)`, `Fake.WaitForTimers(ctx, n) error`.
- Standard library only; no archtest change.

### Design decisions

- **The fake behaves like Go's real timers (Go 1.23+).** A channel holds one value, so a ticker whose reader is slow misses ticks instead of receiving a backlog, and `Stop`/`Reset` discard a value nobody received. Code tested on the fake therefore behaves the same in production. Criterion 2 is read as: the schedule advances once per elapsed period, without drift, and at most one tick is pending (`TestTickerDropsTicksForASlowReaderWithoutDrifting`); a reader that keeps up gets one tick per period (`TestTickerFiresOncePerPeriodForAReaderThatKeepsUp`).
- `Advance` fires what is due in deadline order (ties in creation order). While one fires, `Now` shows its deadline and the channel receives the deadline, not the end of the advance (`TestTimersReceiveTheirOwnDeadline`). The order itself is not observable through channels, so it has no test of its own.
- A timer of zero or negative length fires at once; `NewTicker`/`Reset` with a non-positive period and `Advance` of a negative duration panic, like the time package.
- `WaitForTimers` is how a test lets the code under test arm its timers before advancing, with no sleep. It counts timers and tickers that are waiting (not stopped, not fired).
- There is no `AfterFunc`: nothing needs it yet.

### Mutation check

Each bug was planted alone (the code compiled), `go test -race ./pkg/clock/...` was run, and the code was restored afterwards. Two mutants survived the first run (a ticker that counts its next period from the end of an `Advance`, and a ticker `Reset` that ignores the new period); the tests were tightened (`Advance` ending between ticks; a second period after `Reset`) and all now fail.

| Planted bug | Failing tests |
|---|---|
| Timer fires late (a deadline equal to the target is not due) | `TestTimerFiresOnceAtItsDeadline`, `TestTickerFiresOncePerPeriodForAReaderThatKeepsUp` and 4 more |
| Timer fires early (a second before its deadline) | `TestTimerFiresOnceAtItsDeadline`, `TestStopAndResetDiscardAValueNobodyReceived` and 3 more |
| Timer fires a second time an hour later | `TestTimerFiresOnceAtItsDeadline`, `TestStopAndResetDiscardAValueNobodyReceived`, `TestWaitForTimersCountsOnlyWaitingTimers` |
| `Stop` ignored | `TestStoppedTimerNeverFires`, `TestStoppedTickerNeverTicks` |
| Ticker counts its next period from the end of the `Advance` (drift) | `TestTickerDropsTicksForASlowReaderWithoutDrifting` |
| `Now` jumps to the end before timers fire | `TestTimersReceiveTheirOwnDeadline` |
| `Stop` keeps an unread value | `TestStopAndResetDiscardAValueNobodyReceived`, `TestStoppedTickerNeverTicks` |
| `Reset` keeps an unread value | `TestStopAndResetDiscardAValueNobodyReceived`, `TestResetMovesTheDeadline` |
| Timer with no delay does not fire at once | `TestTimerWithNoDelayFiresAtOnce` |
| Ticker `Reset` ignores the new period | `TestResetMovesTheDeadline` |
| Ticker keeps a backlog of missed ticks | `TestTickerDropsTicksForASlowReaderWithoutDrifting`, `TestResetMovesTheDeadline` |
| `WaitForTimers` does not wait | `TestWaitForTimersCountsOnlyWaitingTimers` |
| `WaitForTimers` counts a stopped timer | `TestWaitForTimersCountsOnlyWaitingTimers` |

### Extra tests beyond the criteria

`TestStopAndResetDiscardAValueNobodyReceived` and `TestResetMovesTheDeadline` (the Go 1.23 semantics code will rely on), `TestTimerWithNoDelayFiresAtOnce`, `TestZeroStartIsAFixedTime`, `TestWaitForTimers*`, `TestNonPositiveTickerPeriodPanics`, `TestRealClockUsesTheMachineTime` (the one test of `Real`: 1 ms timers, blocking on channels, no sleep), `Example` and `ExampleFake`.

### For T028, T030 and later

- Take a `clock.Clock` in the constructor (`WithClock` option, default `clock.Real()`), never `time.Now` or `time.After`. A lease's expiry is `clock.Now().Add(ttl)`.
- A test of a renewal loop: build the fake, start the runner, `WaitForTimers(ctx, 1)` so the loop has armed its ticker, then `Advance`. A loop that re-arms with `NewTimer` each time can do the same; it must not hold a stale timer's channel across a `Stop`.
- A ticker never holds more than one tick, so code must not count ticks to count elapsed time: compare `Now()` with a deadline instead.
- `pkg/harness` still has `WithNow`; switching it to the clock is a separate follow-up.
