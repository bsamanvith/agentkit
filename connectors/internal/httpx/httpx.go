// Package httpx holds the HTTP rules every connector follows
// (docs/CONNECTORS.md §2): bounded response bodies, one retry of a 429 after
// Retry-After on the injected clock, and secrets kept out of what a response
// or an error shows. Each connector keeps its own request building and error
// mapping; this is the part that must not drift between them.
package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
)

const (
	// MaxBody is the largest response body a connector reads: 8 MiB.
	MaxBody = 8 << 20
	// MaxRetryWait is the longest Retry-After honored; a longer one fails
	// the call at once.
	MaxRetryWait = 10 * time.Second
	// DefaultRetryWait is the wait when a 429 has no usable Retry-After.
	DefaultRetryWait = time.Second
	// Redacted replaces a secret in text taken from a response.
	Redacted = "[redacted]"
)

// ErrTooLarge is a response body over the limit passed to [ReadBody].
var ErrTooLarge = errors.New("response body over the size limit")

// ReadBody reads r to EOF, or returns [ErrTooLarge] once it passes limit
// bytes. A body within the limit is read to the end, so the connection is
// reused; one past it is not drained.
func ReadBody(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, ErrTooLarge
	}
	return b, nil
}

// RetryAfter is how long a 429 asks to wait, read from a Retry-After value
// (whole seconds or an HTTP date, relative to now), and whether to retry at
// all. A missing or unreadable value waits [DefaultRetryWait]. It reports
// false when the wait is over [MaxRetryWait] or would outlast ctx's deadline,
// so the call fails at once instead of sleeping into a timeout.
func RetryAfter(ctx context.Context, v string, now time.Time) (time.Duration, bool) {
	d := DefaultRetryWait
	v = strings.TrimSpace(v)
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		if secs > int64(MaxRetryWait/time.Second) {
			return 0, false
		}
		if secs >= 0 {
			d = time.Duration(secs) * time.Second
		}
	} else if t, err := http.ParseTime(v); err == nil {
		d = max(t.Sub(now), 0)
	}
	if d > MaxRetryWait {
		return 0, false
	}
	// A context's deadline is in real time, whatever clock now came from.
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < d {
		return 0, false
	}
	return d, true
}

// Sleep waits d on clk, or until ctx is done.
func Sleep(ctx context.Context, clk clock.Clock, d time.Duration) error {
	t := clk.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Redact replaces every secret in s with [Redacted]. Empty secrets are
// skipped, so a keyless client can pass its empty key.
func Redact(s string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, Redacted)
		}
	}
	return s
}
