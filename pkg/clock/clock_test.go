package clock_test

import (
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
)

// Real forwards to the time package. The timers here are 1 ms long; the test
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

	<-c.NewTimer(time.Millisecond).C()
	<-c.After(time.Millisecond)

	tk := c.NewTicker(time.Millisecond)
	defer tk.Stop()
	<-tk.C()
	<-tk.C()

	stopped := c.NewTimer(time.Hour)
	if !stopped.Stop() {
		t.Error("Stop on a waiting timer returned false")
	}
}
