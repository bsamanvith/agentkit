package llm_test

import (
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
)

// Time the caller spends on an event is not idle time.
func TestIdleTimeoutIgnoresTheCallersOwnTime(t *testing.T) {
	t.Parallel()
	m := llm.WithIdleTimeout(llmtest.NewFake(t, llmtest.Text("one two three four five six seven")), 30*time.Millisecond)
	n := 0
	for _, err := range m.Generate(t.Context(), llm.Request{}) {
		if err != nil {
			t.Fatalf("after %d events: %v", n, err)
		}
		n++
		time.Sleep(60 * time.Millisecond)
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
