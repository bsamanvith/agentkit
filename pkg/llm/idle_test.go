package llm_test

import (
	"context"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
)

// Time the caller spends on an event is not idle time.
func TestIdleTimeoutIgnoresTheCallersOwnTime(t *testing.T) {
	t.Parallel()
	const idle = time.Second
	clk := clocktest.NewFake(time.Time{})
	m := llm.WithIdleTimeoutOn(llmtest.NewFake(t, llmtest.Text("one two three four five six seven")), idle, clk)
	// An already-done context: WaitForTimers reports at once whether a
	// watchdog is armed, without waiting for one.
	done, cancel := context.WithCancel(t.Context())
	cancel()
	n := 0
	for _, err := range m.Generate(t.Context(), llm.Request{}) {
		if err != nil {
			t.Fatalf("after %d events: %v", n, err)
		}
		n++
		// While the caller holds an event, no watchdog may be running.
		if clk.WaitForTimers(done, 1) == nil {
			t.Fatalf("after %d events: the idle watchdog runs while the caller handles the event", n)
		}
		clk.Advance(2 * idle)
	}
	if n < 2 {
		t.Errorf("got %d events", n)
	}
}

func TestIdleTimeoutOffReturnsTheModel(t *testing.T) {
	t.Parallel()
	fake := llmtest.NewFake(t)
	if got := llm.WithIdleTimeout(fake, 0); got != llm.Model(fake) {
		t.Errorf("WithIdleTimeout(m, 0) = %T, want m itself", got)
	}
}
