package exa

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/pkg/clock"
)

// client calls Exa for one toolset instance.
type client struct {
	http    *http.Client
	base    string // no trailing slash
	key     string
	clock   clock.Clock
	log     *slog.Logger
	timeout time.Duration
}

// post sends body as JSON to path and returns the response body of a 200.
// It retries once on 429, after Retry-After (at most maxRetryWait), and maps
// every other outcome to the errors of E10.
func (c *client) post(ctx context.Context, path string, body any) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("%w: encode request: %w", ErrUpstream, err)
	}
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	for attempt := 1; ; attempt++ {
		status, header, respBody, err := c.send(callCtx, path, payload)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if callCtx.Err() != nil {
				return nil, fmt.Errorf("%w: no response within %s", ErrUpstream, c.timeout)
			}
			if errors.Is(err, ErrResponseTooLarge) {
				return nil, fmt.Errorf("%w: over %d bytes", ErrResponseTooLarge, maxResponseBytes)
			}
			c.log.Warn("exa request failed", "path", path, "attempt", attempt, "err", c.redact(err.Error()))
			return nil, fmt.Errorf("%w: could not reach Exa", ErrUpstream)
		}
		c.log.Debug("exa response", "path", path, "attempt", attempt, "status", status, "bytes", len(respBody))

		switch status {
		case http.StatusOK:
			return respBody, nil
		case http.StatusBadRequest, http.StatusUnprocessableEntity:
			if msg := c.errorMessage(respBody); msg != "" {
				return nil, invalidInput("exa: %s", msg)
			}
			return nil, invalidInput("exa: HTTP %d", status)
		case http.StatusUnauthorized, http.StatusForbidden:
			return nil, ErrKeyRejected
		case http.StatusTooManyRequests:
			wait, ok := retryAfter(header.Get("Retry-After"), c.clock.Now())
			if attempt > 1 || !ok {
				return nil, ErrRateLimited
			}
			if err := c.sleep(callCtx, wait); err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return nil, ErrRateLimited // the call's timeout ends before the wait
			}
		default:
			// No retry: a paid call may have run.
			return nil, fmt.Errorf("%w: HTTP %d", ErrUpstream, status)
		}
	}
}

// send makes one request. The key goes in the x-api-key header only.
func (c *client) send(ctx context.Context, path string, payload []byte) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("x-api-key", c.key)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	// readBody reads a body within the limit to EOF, so the connection is
	// reused; past the limit it stops at limit+1 bytes and the connection is
	// dropped rather than drained.
	defer func() { _ = resp.Body.Close() }()
	body, err := readBody(resp.Body)
	if err != nil {
		return 0, nil, nil, err
	}
	return resp.StatusCode, resp.Header, body, nil
}

// sleep waits d on the injected clock, or until ctx is done.
func (c *client) sleep(ctx context.Context, d time.Duration) error {
	t := c.clock.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// retryAfter parses a Retry-After header (seconds or an HTTP date). ok is
// false when the wait asked for is longer than maxRetryWait. A missing or
// unreadable header waits defaultRetryWait.
func retryAfter(h string, now time.Time) (time.Duration, bool) {
	h = strings.TrimSpace(h)
	if h == "" {
		return defaultRetryWait, true
	}
	if secs, err := strconv.ParseInt(h, 10, 64); err == nil {
		if secs < 0 {
			return defaultRetryWait, true
		}
		if secs > int64(maxRetryWait/time.Second) {
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(h); err == nil {
		d := max(t.Sub(now), 0)
		return d, d <= maxRetryWait
	}
	return defaultRetryWait, true
}

// errorMessage returns Exa's own message from an error body
// ({"error": "...", "tag": "..."}), redacted and capped, or "".
func (c *client) errorMessage(body []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	msg := strings.Join(strings.Fields(c.redact(e.Error)), " ")
	return truncate(msg, maxErrorMessageChars)
}

// redact replaces the key in text taken from a response. Exa has no reason
// to echo it; this is a defence in case it does.
func (c *client) redact(s string) string {
	if c.key == "" || !strings.Contains(s, c.key) {
		return s
	}
	return strings.ReplaceAll(s, c.key, redactedMarker)
}

// clean is what every string from a response goes through before it reaches
// the model: redacted, then capped at limit characters (runes).
func (c *client) clean(s string, limit int) string {
	return truncate(c.redact(s), limit)
}

// content is clean for a result's content: when it cuts, the marker says how
// much was cut and, in hint, how the agent can get more (E10).
func (c *client) content(s string, limit int, hint string) string {
	s, cut := cutRunes(c.redact(s), limit)
	if cut == 0 {
		return s
	}
	return s + "…[truncated " + groupThousands(cut) + " characters; " + hint + "]"
}

// truncate cuts s to at most limit runes, adding the short marker when it
// cuts; for titles, URLs and error messages, where no hint applies.
func truncate(s string, limit int) string {
	s, cut := cutRunes(s, limit)
	if cut == 0 {
		return s
	}
	return s + truncationMarker
}

// cutRunes returns s cut to at most limit runes and how many runes it cut.
// The result is valid UTF-8 (invalid bytes become U+FFFD).
func cutRunes(s string, limit int) (string, int) {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	total := utf8.RuneCountInString(s)
	if total <= limit {
		return s, 0
	}
	n := 0
	for i := range s {
		if n == limit {
			return s[:i], total - limit
		}
		n++
	}
	return s, 0
}

// groupThousands writes n with commas: 12400 is "12,400".
func groupThousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
