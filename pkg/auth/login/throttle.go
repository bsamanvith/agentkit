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

// throttle counts failed sign-ins by key ("user:<name>", "addr:<ip>").
type throttle struct {
	mu   sync.Mutex
	keys map[string]failures
}

// wait returns how long key must wait before its next attempt, 0 if none.
func (t *throttle) wait(key string, threshold int, now time.Time) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	f, ok := t.keys[key]
	if !ok || f.n < threshold {
		return 0
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

// fail counts a failure of each key.
func (t *throttle) fail(now time.Time, keys ...string) {
	t.mu.Lock()
	defer t.mu.Unlock()
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
// none was, the oldest.
func (t *throttle) sweep(now time.Time) {
	oldest := ""
	for k, f := range t.keys {
		if now.Sub(f.last) >= forgetAfter {
			delete(t.keys, k)
			continue
		}
		if oldest == "" || f.last.Before(t.keys[oldest].last) {
			oldest = k
		}
	}
	if len(t.keys) >= maxThrottled {
		delete(t.keys, oldest)
	}
}
