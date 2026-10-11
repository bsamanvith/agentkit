package clock_test

import (
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
)

// Real forwards to the time package. The timers here are short; the test
// blocks on their channels instead of sleeping, so it takes as long as they do
// and no longer.
func TestRealClockUsesTheMachineTime(t *testing.T) {
	t.Parallel()
	c := clock.Real()

	before := time.Now()
	now := c.Now()
	if after := time.Now(); now.Before(before) || now.After(after) {
		t.Errorf("Now() = %v, outside [%v, %v]", now, before, after)
	}

	const d = 20 * time.Millisecond
	began := time.Now()
	<-c.NewTimer(d).C()
	<-c.After(d)
	if elapsed := time.Since(began); elapsed < 2*d {
		t.Errorf("two timers of %v fired after %v, the duration was not honored", d, elapsed)
	}

	tk := c.NewTicker(time.Millisecond)
	defer tk.Stop()
	<-tk.C()
	<-tk.C()

	stopped := c.NewTimer(time.Hour)
	if !stopped.Stop() {
		t.Error("Stop on a waiting timer returned false")
	}
}
