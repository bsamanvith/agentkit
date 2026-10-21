package login

import (
	"fmt"
	"math"
	"sync"
	"time"
)

// Throttling of failed sign-ins: past a threshold of failures, an attempt
// must wait base, then twice as long after each further failure, up to max.
// A key's count is forgotten after forget without a failure, or (for a
// username) at a successful sign-in.
const (
	userThreshold = 10
	// A client address is often shared (an office behind NAT), so it gets
	// more room than a username before it is held back.
	addrThreshold = 100
	backoffBase   = time.Second
	backoffMax    = 15 * time.Minute
	forgetAfter   = time.Hour
	maxThrottled  = 10_000
)

// ThrottledError is a sign-in refused, without checking the password,
// because of earlier failures for the username or the client's address.
type ThrottledError struct {
	// RetryAfter is how long until the next attempt is allowed.
	RetryAfter time.Duration
}

func (e *ThrottledError) Error() string {
	return fmt.Sprintf("too many failed sign-ins: try again in %s", e.RetryAfter.Round(time.Second))
}

// RetryAfterSeconds is RetryAfter in whole seconds, rounded up: the value of
// a Retry-After header.
func (e *ThrottledError) RetryAfterSeconds() int {
	return int(math.Ceil(e.RetryAfter.Seconds()))
}

type failures struct {
	n    int
	last time.Time
}

// throttle counts failed sign-ins by key ("user:<name>", "addr:<ip>"), and
// the attempts still checking a password, so that attempts sent at once
// cannot all pass before the first of them fails.
type throttle struct {
	mu       sync.Mutex
	keys     map[string]failures
	inflight map[string]int
}

// limit is a key and the failures it may have before it is held back.
type limit struct {
	key       string
	threshold int
}

// attempt returns how long the attempt must wait, the longest of its keys';
// when it is 0 the attempt is let through and counted as running until
// [throttle.finish].
func (t *throttle) attempt(now time.Time, limits ...limit) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	var d time.Duration
	for _, l := range limits {
		d = max(d, t.wait(l.key, l.threshold, now))
	}
	if d > 0 {
		return d
	}
	if t.inflight == nil {
		t.inflight = map[string]int{}
	}
	for _, l := range limits {
		t.inflight[l.key]++
	}
	return 0
}

// finish ends an attempt [throttle.attempt] let through, counting a failure
// of each key if failed.
func (t *throttle) finish(now time.Time, failed bool, keys ...string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, k := range keys {
		if t.inflight[k]--; t.inflight[k] <= 0 {
			delete(t.inflight, k)
		}
	}
	if failed {
		t.fail(now, keys...)
	}
}

// wait returns how long key must wait before its next attempt, 0 if none.
// Once its failures and running attempts reach the threshold, attempts run
// one at a time. t.mu is held.
func (t *throttle) wait(key string, threshold int, now time.Time) time.Duration {
	f := t.keys[key]
	running := t.inflight[key]
	switch {
	case f.n+running < threshold:
		return 0
	case running > 0:
		return backoff(max(f.n-threshold, 0))
	}
	until := f.last.Add(backoff(f.n - threshold))
	if !now.Before(until) {
		return 0
	}
	return until.Sub(now)
}

// backoff is the wait after k failures past the threshold: base·2^k, at
// most max.
func backoff(k int) time.Duration {
	if k >= 30 {
		return backoffMax
	}
	return min(backoffBase<<k, backoffMax)
}

// fail counts a failure of each key. t.mu is held.
func (t *throttle) fail(now time.Time, keys ...string) {
	if t.keys == nil {
		t.keys = map[string]failures{}
	}
	for _, k := range keys {
		f, ok := t.keys[k]
		if ok && now.Sub(f.last) >= forgetAfter {
			f = failures{}
		}
		if !ok && len(t.keys) >= maxThrottled {
			t.sweep(now)
		}
		t.keys[k] = failures{n: f.n + 1, last: now}
	}
}

// forget drops key's count.
func (t *throttle) forget(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.keys, key)
}

// sweep makes room: it drops the counts not failed for forgetAfter, and if
// none was, the one with the fewest failures (the oldest of those), so that
// failures sprayed over many new names or addresses cannot push out the
// count of one being guessed at.
func (t *throttle) sweep(now time.Time) {
	victim := ""
	for k, f := range t.keys {
		if now.Sub(f.last) >= forgetAfter {
			delete(t.keys, k)
			continue
		}
		if v, ok := t.keys[victim]; !ok || f.n < v.n || f.n == v.n && f.last.Before(v.last) {
			victim = k
		}
	}
	if len(t.keys) >= maxThrottled {
		delete(t.keys, victim)
	}
}
