package parallel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
)

const (
	// maxBodyBytes bounds every response body read.
	maxBodyBytes = 8 << 20
	// maxErrorChars caps a provider message passed on to the model.
	maxErrorChars = 300
	// maxRetryWait is the longest Retry-After honored; a longer one fails.
	maxRetryWait = 10 * time.Second
	// defaultRetryWait is the wait when a 429 has no usable Retry-After.
	defaultRetryWait = time.Second
)

// inputError is a request the caller can fix: the tool reports it as
// tool.ErrInvalidInput rather than ErrToolFailed.
type inputError struct{ msg string }

func (e *inputError) Error() string { return e.msg }

func badInput(format string, args ...any) error {
	return &inputError{msg: fmt.Sprintf(format, args...)}
}

// client calls Parallel's API with the key.
type client struct {
	http    *http.Client
	baseURL string
	key     string
	timeout time.Duration
	clock   clock.Clock
	log     *slog.Logger
}

// post sends body as JSON to path and decodes a 2xx response into out. It
// retries once on 429; every other failure is returned as is.
func (c *client) post(ctx context.Context, path string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("parallel: encode request: %w", err)
	}
	for attempt := 1; ; attempt++ {
		status, header, data, err := c.do(ctx, path, payload)
		if err != nil {
			return err
		}
		c.log.Debug("parallel response", "path", path, "status", status, "attempt", attempt, "bytes", len(data))
		if status >= 200 && status < 300 {
			if err := json.Unmarshal(data, out); err != nil {
				return errors.New("parallel: unreadable response (not the documented JSON)")
			}
			return nil
		}
		if status == http.StatusTooManyRequests && attempt == 1 {
			if wait, ok := retryAfter(header.Get("Retry-After"), c.clock.Now()); ok {
				if err := c.sleep(ctx, wait); err != nil {
					return err
				}
				continue
			}
		}
		return c.statusError(status, data)
	}
}

// do makes one attempt, bounded by the timeout setting.
func (c *client) do(ctx context.Context, path string, payload []byte) (int, http.Header, []byte, error) {
	actx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(actx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("parallel: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", c.key)
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, nil, ctx.Err()
		}
		if actx.Err() != nil {
			return 0, nil, nil, fmt.Errorf("parallel: no response within %v", c.timeout)
		}
		return 0, nil, nil, errors.New("parallel: request failed: " + c.redact(err.Error()))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, nil, ctx.Err()
		}
		return 0, nil, nil, fmt.Errorf("parallel: reading the response (HTTP %d) failed", resp.StatusCode)
	}
	if len(data) > maxBodyBytes {
		return 0, nil, nil, fmt.Errorf("parallel: response larger than %d MiB (HTTP %d)", maxBodyBytes>>20, resp.StatusCode)
	}
	return resp.StatusCode, resp.Header, data, nil
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

// retryAfter reads a Retry-After header (whole seconds or an HTTP date); an
// absent or unreadable one means defaultRetryWait. It reports false when the
// wait is longer than maxRetryWait, so the call fails at once instead.
func retryAfter(v string, now time.Time) (time.Duration, bool) {
	d := defaultRetryWait
	v = strings.TrimSpace(v)
	if secs, err := strconv.Atoi(v); err == nil {
		if secs > int(maxRetryWait/time.Second) {
			return 0, false
		}
		if secs >= 0 {
			d = time.Duration(secs) * time.Second
		}
	} else if t, err := http.ParseTime(v); err == nil {
		d = max(t.Sub(now), 0)
	}
	if d > maxRetryWait {
		return 0, false
	}
	return d, true
}

// statusError maps a non-2xx response to the connector's errors.
func (c *client) statusError(status int, body []byte) error {
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		msg := c.providerMessage(body)
		if msg == "" {
			return badInput("parallel rejected the request (HTTP %d)", status)
		}
		return badInput("parallel rejected the request (HTTP %d): %s", status, msg)
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("parallel: rejected the API key (HTTP %d)", status)
	case http.StatusTooManyRequests:
		return errors.New("parallel: rate limited (HTTP 429)")
	default:
		return fmt.Errorf("parallel: upstream error (HTTP %d)", status)
	}
}

// providerMessage extracts Parallel's error message from an error body:
// {"type": "error", "error": {"message": ...}}, or a FastAPI-style
// {"detail": ...}. It is redacted, on one line and capped.
func (c *client) providerMessage(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Detail json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	msg := e.Error.Message
	if msg == "" && len(e.Detail) > 0 {
		var s string
		var list []struct {
			Msg string `json:"msg"`
		}
		switch {
		case json.Unmarshal(e.Detail, &s) == nil:
			msg = s
		case json.Unmarshal(e.Detail, &list) == nil && len(list) > 0:
			msg = list[0].Msg
		}
	}
	return truncate(strings.Join(strings.Fields(c.redact(msg)), " "), maxErrorChars)
}

// redact replaces the key in text taken from a response.
func (c *client) redact(s string) string {
	if c.key == "" {
		return s
	}
	return strings.ReplaceAll(s, c.key, "[redacted]")
}
