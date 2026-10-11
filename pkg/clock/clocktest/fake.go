package clocktest

import (
	"context"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
)

var _ clock.Clock = (*Fake)(nil)

// epoch is where a [Fake] starts when the caller gives no start time.
var epoch = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

// Fake is a [clock.Clock] whose time moves only when [Fake.Advance] is called.
// It is safe for concurrent use.
//
// Its timers and tickers behave like the real ones: the channel holds one
// value, so a ticker whose reader is slow misses ticks, and Stop and Reset
// discard a value that was not yet received.
type Fake struct {
	mu      sync.Mutex
	now     time.Time
	waiting []*entry // the active entries
	nextSeq int
	// changed is closed and replaced whenever the set of waiting timers
	// changes, which is how WaitForTimers wakes up.
	changed chan struct{}
}

// NewFake returns a fake clock showing start. A zero start means 1 January
// 2026, UTC.
func NewFake(start time.Time) *Fake {
	if start.IsZero() {
		start = epoch
	}
	return &Fake{now: start, changed: make(chan struct{})}
}

// entry is one timer or ticker. It is waiting while active.
type entry struct {
	f      *Fake
	seq    int // creation order, to order timers with the same deadline
	when   time.Time
	period time.Duration // zero for a timer
	active bool
	ch     chan time.Time
}

// Now implements [clock.Clock].
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// NewTimer implements [clock.Clock].
func (f *Fake) NewTimer(d time.Duration) clock.Timer {
	f.mu.Lock()
	defer f.mu.Unlock()
	e := f.newEntry(0)
	f.arm(e, d)
	return (*timer)(e)
}

// NewTicker implements [clock.Clock]. It panics if d is zero or less.
func (f *Fake) NewTicker(d time.Duration) clock.Ticker {
	if d <= 0 {
		panic("clocktest: non-positive interval for NewTicker")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	e := f.newEntry(d)
	f.arm(e, d)
	return (*ticker)(e)
}

// After implements [clock.Clock].
func (f *Fake) After(d time.Duration) <-chan time.Time { return f.NewTimer(d).C() }

// Advance moves the time forward by d. Every timer and tick that falls due on
// the way fires in deadline order (timers with the same deadline fire in the
// order they were created), and each channel receives the time its timer was
// set for, not the end of the advance.
//
// Advance is one step: it returns before the goroutines it woke have run, so a
// goroutine that re-arms a timer after waking does so at the end time. Code
// that waits in a loop is tested by advancing one interval at a time and
// calling [Fake.WaitForTimers] in between. Advance panics if d is negative.
func (f *Fake) Advance(d time.Duration) {
	if d < 0 {
		panic("clocktest: negative Advance")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	target := f.now.Add(d)
	for {
		next := f.nextDue(target)
		if next == nil {
			break
		}
		f.now = next.when
		next.fire()
	}
	f.now = target
}

// WaitForTimers blocks until at least n timers and tickers are waiting (not
// stopped, and for a timer not yet fired), or until ctx is done, in which case
// it returns the context's error. A test uses it to let the code under test
// arm its timers before the test advances the clock.
func (f *Fake) WaitForTimers(ctx context.Context, n int) error {
	for {
		f.mu.Lock()
		waiting := len(f.waiting)
		changed := f.changed
		f.mu.Unlock()
		if waiting >= n {
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// newEntry returns a new, inactive entry. The caller holds f.mu.
func (f *Fake) newEntry(period time.Duration) *entry {
	e := &entry{f: f, seq: f.nextSeq, period: period, ch: make(chan time.Time, 1)}
	f.nextSeq++
	return e
}

// nextDue returns the active entry with the earliest deadline at or before
// target, or nil. The caller holds f.mu.
func (f *Fake) nextDue(target time.Time) *entry {
	var best *entry
	for _, e := range f.waiting {
		if e.when.After(target) {
			continue
		}
		if best == nil || e.when.Before(best.when) || (e.when.Equal(best.when) && e.seq < best.seq) {
			best = e
		}
	}
	return best
}

// notify wakes the WaitForTimers calls. The caller holds f.mu.
func (f *Fake) notify() {
	close(f.changed)
	f.changed = make(chan struct{})
}

// arm makes e fire d from now, at once if d is zero or less. The caller holds
// f.mu.
func (f *Fake) arm(e *entry, d time.Duration) {
	e.drain()
	if !e.active {
		e.active = true
		f.waiting = append(f.waiting, e)
	}
	e.when = f.now.Add(max(d, 0))
	if d <= 0 {
		e.fire()
	}
	f.notify()
}

// deactivate takes e out of the waiting set. The caller holds f.mu.
func (f *Fake) deactivate(e *entry) {
	e.active = false
	for i, w := range f.waiting {
		if w == e {
			f.waiting = append(f.waiting[:i], f.waiting[i+1:]...)
			return
		}
	}
}

// fire delivers the entry's due time. A timer is done; a ticker is set for its
// next period. The value is dropped if the channel still holds an unread one.
// The caller holds f.mu.
func (e *entry) fire() {
	select {
	case e.ch <- e.when:
	default:
	}
	if e.period > 0 {
		e.when = e.when.Add(e.period)
	} else {
		e.f.deactivate(e)
	}
	e.f.notify()
}

// disarm stops e and discards an unread value. It reports whether e was
// waiting. The caller holds f.mu.
func (e *entry) disarm() bool {
	was := e.active
	e.f.deactivate(e)
	pending := e.drain()
	if was {
		e.f.notify()
	}
	// Like a real timer, one that fired but whose value nobody received still
	// counts as pending: Stop reports true and discards the value.
	return was || pending
}

// drain discards an unread value and reports whether there was one.
func (e *entry) drain() bool {
	select {
	case <-e.ch:
		return true
	default:
		return false
	}
}

type timer entry

func (t *timer) C() <-chan time.Time { return t.ch }

func (t *timer) Stop() bool {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	return (*entry)(t).disarm()
}

func (t *timer) Reset(d time.Duration) bool {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	was := t.active || len(t.ch) > 0
	t.f.arm((*entry)(t), d)
	return was
}

type ticker entry

func (t *ticker) C() <-chan time.Time { return t.ch }

func (t *ticker) Stop() {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	(*entry)(t).disarm()
}

func (t *ticker) Reset(d time.Duration) {
	if d <= 0 {
		panic("clocktest: non-positive interval for Ticker.Reset")
	}
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	t.period = d
	t.f.arm((*entry)(t), d)
}
