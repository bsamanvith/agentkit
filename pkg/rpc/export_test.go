package rpc

import "time"

// IdleTimer is the timer of a client's idle timeout.
type IdleTimer = idleTimer

// WithIdleTimers makes the client's idle timer with f instead of
// time.AfterFunc: a test's fake, so that a test of the timeout does not
// depend on how its goroutines are scheduled. The package knows no clock
// package (internal/archtest), so the seam is for its own tests only.
func WithIdleTimers(f func(time.Duration, func()) IdleTimer) DialOption {
	return func(c *Client) { c.afterFunc = f }
}
