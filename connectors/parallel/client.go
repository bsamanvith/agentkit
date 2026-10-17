package parallel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/connectors/internal/httpx"
	"github.com/bsamanvith/agentkit/pkg/clock"
)

// maxErrorChars caps a provider message passed on to the model.
const maxErrorChars = 300

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
			if wait, ok := httpx.RetryAfter(ctx, header.Get("Retry-After"), c.clock.Now()); ok {
				if err := httpx.Sleep(ctx, c.clock, wait); err != nil {
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
	data, err := httpx.ReadBody(resp.Body, httpx.MaxBody)
	switch {
	case errors.Is(err, httpx.ErrTooLarge):
		return 0, nil, nil, fmt.Errorf("parallel: response larger than %d MiB (HTTP %d)", httpx.MaxBody>>20, resp.StatusCode)
	case err != nil && ctx.Err() != nil:
		return 0, nil, nil, ctx.Err()
	case err != nil:
		return 0, nil, nil, fmt.Errorf("parallel: reading the response (HTTP %d) failed", resp.StatusCode)
	}
	return resp.StatusCode, resp.Header, data, nil
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
func (c *client) redact(s string) string { return httpx.Redact(s, c.key) }
