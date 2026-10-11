package clocktest_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

var start = time.Date(2030, time.May, 1, 12, 0, 0, 0, time.UTC)

// received reports whether a value is waiting on ch, and takes it.
func received(ch <-chan time.Time) (time.Time, bool) {
	select {
	case v := <-ch:
		return v, true
	default:
		return time.Time{}, false
	}
}

func mustNotFire(t *testing.T, ch <-chan time.Time, when string) {
	t.Helper()
	if v, ok := received(ch); ok {
		t.Fatalf("fired %s, with %v", when, v)
	}
}

func mustFire(t *testing.T, ch <-chan time.Time, when string) time.Time {
	t.Helper()
	v, ok := received(ch)
	if !ok {
		t.Fatalf("did not fire %s", when)
	}
	return v
}

func TestTimerFiresOnceAtItsDeadline(t *testing.T) {
	t.Parallel()
	f := clocktest.NewFake(start)
	tm := f.NewTimer(5 * time.Second)

	f.Advance(4 * time.Second)
	mustNotFire(t, tm.C(), "after 4s of 5s")

	f.Advance(time.Second)
	if got := mustFire(t, tm.C(), "after 5s"); !got.Equal(start.Add(5 * time.Second)) {
		t.Errorf("fired with %v, want the deadline %v", got, start.Add(5*time.Second))
	}

	f.Advance(time.Hour)
	mustNotFire(t, tm.C(), "a second time")
}

func TestTickerFiresOncePerPeriodForAReaderThatKeepsUp(t *testing.T) {
	t.Parallel()
	f := clocktest.NewFake(start)
	tk := f.NewTicker(time.Second)
	defer tk.Stop()

	for i := 1; i <= 10; i++ {
		f.Advance(time.Second)
		got := mustFire(t, tk.C(), "once the period elapsed")
		if want := start.Add(time.Duration(i) * time.Second); !got.Equal(want) {
			t.Fatalf("tick %d at %v, want %v", i, got, want)
		}
		mustNotFire(t, tk.C(), "twice in one period")
	}
}

// A real ticker has room for one tick, so a reader that is slow misses ticks
// and never receives a backlog. The schedule itself does not move: the tick
// after a long advance is still due at the next multiple of the period.
func TestTickerDropsTicksForASlowReaderWithoutDrifting(t *testing.T) {
	t.Parallel()
	f := clocktest.NewFake(start)
	tk := f.NewTicker(time.Second)
	defer tk.Stop()

	f.Advance(10*time.Second + 500*time.Millisecond) // ends between two ticks
	mustFire(t, tk.C(), "during a long advance")
	mustNotFire(t, tk.C(), "with a backlog of the ticks it missed")

	f.Advance(400 * time.Millisecond)
	mustNotFire(t, tk.C(), "before the next period boundary")
	f.Advance(100 * time.Millisecond)
	if got := mustFire(t, tk.C(), "at the next period boundary"); !got.Equal(start.Add(11 * time.Second)) {
		t.Errorf("next tick at %v, want %v", got, start.Add(11*time.Second))
	}
}

func TestStoppedTimerNeverFires(t *testing.T) {
	t.Parallel()
	f := clocktest.NewFake(start)
	tm := f.NewTimer(time.Second)

	if !tm.Stop() {
		t.Error("Stop on a waiting timer returned false")
	}
	if tm.Stop() {
		t.Error("Stop on a stopped timer returned true")
	}
	f.Advance(time.Hour)
	mustNotFire(t, tm.C(), "after Stop")
}

func TestStoppedTickerNeverTicks(t *testing.T) {
	t.Parallel()
	f := clocktest.NewFake(start)
	tk := f.NewTicker(time.Second)

	f.Advance(time.Second)
	tk.Stop() // the tick that already fired is discarded too
	f.Advance(time.Hour)
	mustNotFire(t, tk.C(), "after Stop")
}

func TestStopAndResetDiscardAValueNobodyReceived(t *testing.T) {
	t.Parallel()
	f := clocktest.NewFake(start)

	// As with a real timer, a value nobody received still counts as pending.
	stopped := f.NewTimer(time.Second)
	f.Advance(time.Second)
	if !stopped.Stop() {
		t.Error("Stop on a timer that fired but was not received returned false")
	}
	mustNotFire(t, stopped.C(), "a stale value after Stop")

	reset := f.NewTimer(time.Second)
	f.Advance(time.Second)
	if !reset.Reset(2 * time.Second) {
		t.Error("Reset on a timer that fired but was not received returned false")
	}
	mustNotFire(t, reset.C(), "a stale value after Reset")
	f.Advance(time.Second)
	mustNotFire(t, reset.C(), "before the new deadline")
	f.Advance(time.Second)
	mustFire(t, reset.C(), "at the new deadline")
}

// Once the value was received, the timer is no longer pending.
func TestStopAndResetAfterTheValueWasReceivedReportFalse(t *testing.T) {
	t.Parallel()
	f := clocktest.NewFake(start)
	tm := f.NewTimer(time.Second)
	f.Advance(time.Second)
	mustFire(t, tm.C(), "at its deadline")

	if tm.Stop() {
		t.Error("Stop after the value was received returned true")
	}
	if tm.Reset(time.Second) {
		t.Error("Reset after the value was received returned true")
	}
	if !tm.Reset(time.Second) {
		t.Error("Reset of a waiting timer returned false")
	}
}

func TestResetMovesTheDeadline(t *testing.T) {
	t.Parallel()
	f := clocktest.NewFake(start)
	tm := f.NewTimer(10 * time.Second)
	tk := f.NewTicker(time.Second)
	defer tk.Stop()

	f.Advance(9 * time.Second)
	if !tm.Reset(10 * time.Second) {
		t.Error("Reset on a waiting timer returned false")
	}
	f.Advance(9 * time.Second)
	mustNotFire(t, tm.C(), "at the old deadline")
	f.Advance(time.Second)
	mustFire(t, tm.C(), "at the new deadline")

	tk.Reset(3 * time.Second)
	mustNotFire(t, tk.C(), "a stale tick after Reset")
	f.Advance(2 * time.Second)
	mustNotFire(t, tk.C(), "before the new period")
	f.Advance(time.Second)
	mustFire(t, tk.C(), "after the new period")
	f.Advance(2 * time.Second)
	mustNotFire(t, tk.C(), "at the old period")
	f.Advance(time.Second)
	mustFire(t, tk.C(), "after a second new period")
}

// Each channel receives the time its timer was set for, not the time at the
// end of the advance.
func TestTimersReceiveTheirOwnDeadline(t *testing.T) {
	t.Parallel()
	f := clocktest.NewFake(start)
	late := f.After(9 * time.Second)
	early := f.After(2 * time.Second)
	mid := f.NewTimer(5 * time.Second)

	f.Advance(time.Minute)

	for _, c := range []struct {
		name string
		ch   <-chan time.Time
		at   time.Duration
	}{{"early", early, 2 * time.Second}, {"mid", mid.C(), 5 * time.Second}, {"late", late, 9 * time.Second}} {
		if got := mustFire(t, c.ch, c.name); !got.Equal(start.Add(c.at)) {
			t.Errorf("%s fired with %v, want %v", c.name, got, start.Add(c.at))
		}
	}
	if got, want := f.Now(), start.Add(time.Minute); !got.Equal(want) {
		t.Errorf("Now after Advance = %v, want %v", got, want)
	}
}

func TestTimerWithNoDelayFiresAtOnce(t *testing.T) {
	t.Parallel()
	f := clocktest.NewFake(start)
	for _, d := range []time.Duration{0, -time.Second} {
		if got := mustFire(t, f.After(d), "at once"); !got.Equal(start) {
			t.Errorf("After(%v) fired with %v, want now (%v)", d, got, start)
		}
	}
}

func TestZeroStartIsAFixedTime(t *testing.T) {
	t.Parallel()
	a, b := clocktest.NewFake(time.Time{}), clocktest.NewFake(time.Time{})
	if a.Now().IsZero() || !a.Now().Equal(b.Now()) {
		t.Errorf("fakes with no start show %v and %v, want the same non-zero time", a.Now(), b.Now())
	}
}

func TestWaitForTimersReturnsWhenTheCodeUnderTestHasArmedItsTimer(t *testing.T) {
	t.Parallel()
	f := clocktest.NewFake(start)
	woke := make(chan time.Time)
	go func() { woke <- <-f.After(30 * time.Second) }()

	if err := f.WaitForTimers(t.Context(), 1); err != nil {
		t.Fatalf("WaitForTimers: %v", err)
	}
	// The goroutine has armed its timer, so this Advance cannot miss it. A
	// WaitForTimers that returned too early would leave it waiting forever; the
	// test's context ends the wait instead of the test hanging.
	f.Advance(30 * time.Second)
	select {
	case got := <-woke:
		if !got.Equal(start.Add(30 * time.Second)) {
			t.Errorf("woke at %v, want %v", got, start.Add(30*time.Second))
		}
	case <-t.Context().Done():
		t.Fatal("the goroutine never woke")
	}
}

func TestWaitForTimersCountsOnlyWaitingTimers(t *testing.T) {
	t.Parallel()
	f := clocktest.NewFake(start)
	stopped := f.NewTimer(time.Second)
	stopped.Stop()
	f.After(time.Second)
	f.Advance(time.Second) // fired: no longer waiting
	waiting := f.NewTicker(time.Second)
	defer waiting.Stop()

	if err := f.WaitForTimers(t.Context(), 1); err != nil {
		t.Fatalf("one ticker is waiting: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := f.WaitForTimers(ctx, 2); !errors.Is(err, context.Canceled) {
		t.Errorf("WaitForTimers(2) with one waiting = %v, want context.Canceled", err)
	}
}

func TestNonPositiveTickerPeriodPanics(t *testing.T) {
	t.Parallel()
	f := clocktest.NewFake(start)
	for name, fn := range map[string]func(){
		"NewTicker": func() { f.NewTicker(0) },
		"Reset":     func() { f.NewTicker(time.Second).Reset(-time.Second) },
		"Advance":   func() { f.Advance(-time.Second) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s did not panic", name)
				}
			}()
			fn()
		}()
	}
}

var _ clock.Clock = (*clocktest.Fake)(nil)
