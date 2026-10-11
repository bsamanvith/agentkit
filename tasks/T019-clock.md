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

- **The fake behaves like Go's real timers (Go 1.23+).** A channel holds one value, so a ticker whose reader is slow misses ticks instead of receiving a backlog, `Stop`/`Reset` discard a value nobody received, and (as with a real `time.Timer`, checked against it on go1.26 and go1.27) they report `true` for a timer that fired but whose value was not received. Code tested on the fake therefore behaves the same in production, including the `if !t.Stop() { <-t.C() }` idiom, which does not block. Criterion 2 is read as: the schedule advances once per elapsed period, without drift, and at most one tick is pending (`TestTickerDropsTicksForASlowReaderWithoutDrifting`); a reader that keeps up gets one tick per period (`TestTickerFiresOncePerPeriodForAReaderThatKeepsUp`).
- `Advance` fires what is due in deadline order (ties in creation order), and each channel receives the deadline of its timer, not the end of the advance (`TestTimersReceiveTheirOwnDeadline`). `Advance` is one step under one lock: nobody can observe `Now` midway, and a goroutine woken by it re-arms at the end time. A test of a loop that waits advances one interval at a time and calls `WaitForTimers` in between (documented on `Advance`). The firing order itself is not observable through channels, so it has no test of its own.
- Timers that are armed and never fired or stopped (an `After` nobody waits for) stay in the waiting set, so they count for `WaitForTimers` and make `Advance` scan them; a real timer would be garbage collected. Fine for a test; do not use the fake in a long-running process.
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
| Timers fire with the end time of the `Advance` instead of their deadline (`Now` set to the end first, and the value taken from it) | `TestTimersReceiveTheirOwnDeadline` |
| `Stop` keeps an unread value | `TestStopAndResetDiscardAValueNobodyReceived`, `TestStoppedTickerNeverTicks` |
| `Reset` keeps an unread value | `TestStopAndResetDiscardAValueNobodyReceived`, `TestResetMovesTheDeadline` |
| `Stop` on a timer that fired but was not received reports false | `TestStopAndResetDiscardAValueNobodyReceived` |
| `Reset` on a timer that fired but was not received reports false | `TestStopAndResetDiscardAValueNobodyReceived` |
| `Real()` timers ignore the duration | `TestRealClockUsesTheMachineTime` |
| Timer with no delay does not fire at once | `TestTimerWithNoDelayFiresAtOnce` |
| Ticker `Reset` ignores the new period | `TestResetMovesTheDeadline` |
| Ticker keeps a backlog of missed ticks | `TestTickerDropsTicksForASlowReaderWithoutDrifting`, `TestResetMovesTheDeadline` |
| `WaitForTimers` does not wait | `TestWaitForTimersCountsOnlyWaitingTimers` |
| `WaitForTimers` counts a stopped timer | `TestWaitForTimersCountsOnlyWaitingTimers` |

### Extra tests beyond the criteria

`TestStopAndResetDiscardAValueNobodyReceived`, `TestStopAndResetAfterTheValueWasReceivedReportFalse` and `TestResetMovesTheDeadline` (the Go 1.23 semantics code will rely on), `TestTimerWithNoDelayFiresAtOnce`, `TestZeroStartIsAFixedTime`, `TestWaitForTimers*`, `TestNonPositiveTickerPeriodPanics`, `TestRealClockUsesTheMachineTime` (the one test of `Real`: 1 ms timers, blocking on channels, no sleep), `Example` and `ExampleFake`.

### Review fixes (an independent review after the first push)

- `Stop`/`Reset` returned `false` for a fired-but-unreceived timer where a real timer returns `true`, and a test locked that in (a drain idiom would have hung on the fake). Fixed, the doc on `clock.Timer` corrected, test flipped and extended.
- The `Advance` doc claimed `Now` shows the deadline while a timer fires, which no other goroutine can observe; reworded (see Design decisions).
- `TestWaitForTimersReturnsWhenTheCodeUnderTestHasArmedItsTimer` now ends through the test context instead of hanging if `WaitForTimers` returns early; `TestRealClockUsesTheMachineTime` now checks that the duration is honored (20 ms timers, no sleep).

### For T028, T030 and later

- Take a `clock.Clock` in the constructor (`WithClock` option, default `clock.Real()`), never `time.Now` or `time.After`. A lease's expiry is `clock.Now().Add(ttl)`.
- A test of a renewal loop: build the fake, start the runner, `WaitForTimers(ctx, 1)` so the loop has armed its ticker, then `Advance`. A loop that re-arms with `NewTimer` each time can do the same; it must not hold a stale timer's channel across a `Stop`.
- A ticker never holds more than one tick, so code must not count ticks to count elapsed time: compare `Now()` with a deadline instead.
- `pkg/harness` still has `WithNow`; switching it to the clock is a separate follow-up.
