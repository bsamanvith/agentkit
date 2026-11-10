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

// add counts an event of each key at now.
func (l *limiter) add(now time.Time, keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.events) >= maxLimited {
		for k := range l.events {
			if len(l.recent(k, now)) == 0 {
				delete(l.events, k)
			}
		}
	}
	for _, k := range keys {
		l.events[k] = append(l.recent(k, now), now)
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
