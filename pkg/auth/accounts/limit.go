package accounts

import (
	"sync"
	"time"
)

// limiter counts events (failed attempts, codes sent) per key within a
// window: past max events a key waits until its oldest event leaves the
// window. Keys are forgotten when their window passes. It is in memory, so
// each replica counts its own: the codes' own attempt limits are in the
// store.
type limiter struct {
	max    int
	window time.Duration

	mu     sync.Mutex
	events map[string][]time.Time
}

// maxLimited bounds the keys a limiter remembers; past it, the oldest
// windows are swept.
const maxLimited = 50_000

func newLimiter(maxEvents int, window time.Duration) *limiter {
	return &limiter{max: maxEvents, window: window, events: map[string][]time.Time{}}
}

// wait returns how long key must wait before its next event, 0 if none.
func (l *limiter) wait(key string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	ev := l.recent(key, now)
	if len(ev) < l.max {
		return 0
	}
	return ev[len(ev)-l.max].Add(l.window).Sub(now)
}

// take counts an event of key at now if key may have one, and returns 0;
// otherwise how long key must wait, counting nothing. Checking and
// counting under one lock is what keeps a burst within the limit.
func (l *limiter) take(key string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	ev := l.recent(key, now)
	if len(ev) >= l.max {
		return ev[len(ev)-l.max].Add(l.window).Sub(now)
	}
	l.sweep(now)
	l.events[key] = append(ev, now)
	return 0
}

// undo takes back key's latest event (an attempt that did not fail).
func (l *limiter) undo(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if ev := l.events[key]; len(ev) > 1 {
		l.events[key] = ev[:len(ev)-1]
	} else {
		delete(l.events, key)
	}
}

// add counts an event of each key at now.
func (l *limiter) add(now time.Time, keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)
	for _, k := range keys {
		l.events[k] = append(l.recent(k, now), now)
	}
}

// sweep drops keys whose window passed once there are too many; l.mu is
// held.
func (l *limiter) sweep(now time.Time) {
	if len(l.events) < maxLimited {
		return
	}
	for k := range l.events {
		if len(l.recent(k, now)) == 0 {
			delete(l.events, k)
		}
	}
}

// forget drops key's events (a success).
func (l *limiter) forget(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.events, key)
}

// recent returns key's events within the window, dropping older ones.
// l.mu is held.
func (l *limiter) recent(key string, now time.Time) []time.Time {
	ev := l.events[key]
	i := 0
	for i < len(ev) && !ev[i].After(now.Add(-l.window)) {
		i++
	}
	if i == len(ev) {
		delete(l.events, key)
		return nil
	}
	ev = ev[i:]
	if len(ev) > l.max {
		ev = ev[len(ev)-l.max:]
	}
	return ev
}
