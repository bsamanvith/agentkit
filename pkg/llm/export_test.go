package llm

import (
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
)

// WithIdleTimeoutOn is [WithIdleTimeout] on clk, so a test controls the time.
func WithIdleTimeoutOn(m Model, d time.Duration, clk clock.Clock) Model {
	return withIdleTimeout(m, d, clk)
}
