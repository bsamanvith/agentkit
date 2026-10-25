package llmhttp

import (
	"context"
	"errors"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

const (
	// maxRetryAfter is the longest Retry-After a retry waits for; a longer
	// one returns the error instead.
	maxRetryAfter = 2 * time.Minute
	// maxBackoff caps the backoff between retries without a Retry-After.
	maxBackoff = 8 * time.Second
	// maxErrorBody is how much of an error response is read.
	maxErrorBody = 64 << 10
)

// RetryDelayer is an error that says how long to wait before a retry, as
// Google's RetryInfo does in the error body. Send waits for it when the
// response has no Retry-After header.
type RetryDelayer interface {
	error
	RetryDelay() time.Duration
}

// Send sends a request with post and returns the response once it is a 2xx,
// retrying connection failures and retryable statuses up to maxRetries
// times. Any other response is read (its first 64 KiB) and returned as the
// error failed makes of it; a connection failure as the client's error.
func Send(ctx context.Context, maxRetries int, post func(context.Context) (*http.Response, error), failed func(*http.Response, []byte) error) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := post(ctx)
		if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp, nil
		}
		var wait time.Duration
		retry := attempt < maxRetries && ctx.Err() == nil
		if err == nil {
			data, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
			_ = resp.Body.Close()
			err = failed(resp, data)
			var hint time.Duration
			if d, ok := errors.AsType[RetryDelayer](err); ok {
				hint = d.RetryDelay()
			}
			wait, retry = retryAfter(resp, attempt, retry, hint)
		} else {
			wait = backoff(attempt)
		}
		if !retry {
			return nil, err
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, err
		case <-t.C:
		}
	}
}

// retryAfter says whether a failed response is retried and after how long.
// A retry is allowed when the attempts are not used up (retry); it happens
// for 408, 409, 429 and 5xx, or as an X-Should-Retry header says, after the
// wait the server asks for (Retry-After-Ms, then Retry-After, in seconds or
// as a date, or else hint, when it is positive), unless that is longer than
// maxRetryAfter.
func retryAfter(resp *http.Response, attempt int, retry bool, hint time.Duration) (time.Duration, bool) {
	if !retry {
		return 0, false
	}
	switch resp.Header.Get("X-Should-Retry") {
	case "true":
	case "false":
		return 0, false
	default:
		s := resp.StatusCode
		if s != http.StatusRequestTimeout && s != http.StatusConflict && s != http.StatusTooManyRequests && s < 500 {
			return 0, false
		}
	}
	for _, h := range []struct {
		name string
		unit time.Duration
	}{{"Retry-After-Ms", time.Millisecond}, {"Retry-After", time.Second}} {
		v := resp.Header.Get(h.name)
		if v == "" {
			continue
		}
		var d time.Duration
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			if math.IsNaN(n) || n < 0 {
				continue
			}
			if n > float64(maxRetryAfter/h.unit) {
				return 0, false
			}
			d = time.Duration(n * float64(h.unit))
		} else if t, err := http.ParseTime(v); err == nil && h.unit == time.Second {
			d = max(time.Until(t), 0)
			if d > maxRetryAfter {
				return 0, false
			}
		} else {
			continue
		}
		return d, true
	}
	if hint > maxRetryAfter {
		return 0, false
	}
	if hint > 0 {
		return hint, true
	}
	return backoff(attempt), true
}

// backoff is the wait before retry attempt+1 when the server names none:
// half a second doubling up to maxBackoff, less up to a quarter at random.
func backoff(attempt int) time.Duration {
	d := maxBackoff
	if attempt < 5 {
		d = min(time.Duration(float64(500*time.Millisecond)*math.Pow(2, float64(attempt))), maxBackoff)
	}
	return d - rand.N(d/4+1)
}
