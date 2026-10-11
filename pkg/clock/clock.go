package clock

import "time"

// Clock tells the time and makes timers. It is the part of the time package
// that agentkit code uses, as an interface.
//
// Timers and tickers follow the semantics of Go 1.23 and later: after Stop or
// Reset returns, no value from before the call is received from C.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
	// NewTimer returns a timer that sends the time on its channel once, d
	// after now. A d of zero or less fires at once.
	NewTimer(d time.Duration) Timer
	// NewTicker returns a ticker that sends the time on its channel every d.
	// Like [time.NewTicker] it panics if d is zero or less.
	NewTicker(d time.Duration) Ticker
	// After is NewTimer(d).C() for callers that never stop the timer.
	After(d time.Duration) <-chan time.Time
}

// Timer is one future event. See [time.Timer].
type Timer interface {
	// C is the channel the time is sent on. It holds at most one value.
	C() <-chan time.Time
	// Stop prevents the timer from firing. It reports whether it did: false
	// means the timer had already fired or been stopped.
	Stop() bool
	// Reset makes the timer fire d from now, whether or not it had fired. It
	// reports whether the timer was still waiting.
	Reset(d time.Duration) bool
}

// Ticker is a repeating event. See [time.Ticker].
type Ticker interface {
	// C is the channel the ticks are sent on. It holds at most one tick: a
	// reader that is slow misses ticks instead of receiving a backlog.
	C() <-chan time.Time
	// Stop turns the ticker off. No more ticks arrive.
	Stop()
	// Reset changes the period to d, counting from now. It panics if d is zero
	// or less.
	Reset(d time.Duration)
}

// Real returns the clock of the machine: the time package, unchanged.
func Real() Clock { return realClock{} }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }

func (realClock) NewTicker(d time.Duration) Ticker { return realTicker{time.NewTicker(d)} }

func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

type realTimer struct{ t *time.Timer }

func (r realTimer) C() <-chan time.Time        { return r.t.C }
func (r realTimer) Stop() bool                 { return r.t.Stop() }
func (r realTimer) Reset(d time.Duration) bool { return r.t.Reset(d) }

type realTicker struct{ t *time.Ticker }

func (r realTicker) C() <-chan time.Time   { return r.t.C }
func (r realTicker) Stop()                 { r.t.Stop() }
func (r realTicker) Reset(d time.Duration) { r.t.Reset(d) }
