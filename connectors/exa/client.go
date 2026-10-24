package exa

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

	"github.com/bsamanvith/agentkit/connectors/internal/apikey"
	"github.com/bsamanvith/agentkit/connectors/internal/httpx"
	"github.com/bsamanvith/agentkit/connectors/internal/trunc"
	"github.com/bsamanvith/agentkit/pkg/clock"
)

// client calls Exa for one toolset instance. Each call uses its own copy
// ([client.forCall]), which holds the caller's key.
type client struct {
	http    *http.Client
	base    string // no trailing slash
	keys    apikey.Key
	key     string // the call's, set by forCall
	clock   clock.Clock
	log     *slog.Logger
	timeout time.Duration
}

// forCall returns a copy of c with the key of ctx's caller: the one from
// the environment, or the caller's own from the toolset's credential slot.
func (c *client) forCall(ctx context.Context) (*client, error) {
	key, err := c.keys.Get(ctx)
	if err != nil {
		return nil, err
	}
	cc := *c
	cc.key = key
	return &cc, nil
}

// post sends body as JSON to path and returns the response body of a 200.
// It retries once on 429, after Retry-After (at most httpx.MaxRetryWait), and maps
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
			wait, ok := httpx.RetryAfter(callCtx, header.Get("Retry-After"), c.clock.Now())
			if attempt > 1 || !ok {
				return nil, ErrRateLimited
			}
			if err := httpx.Sleep(callCtx, c.clock, wait); err != nil {
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
	defer func() { _ = resp.Body.Close() }()
	body, err := httpx.ReadBody(resp.Body, maxResponseBytes)
	if errors.Is(err, httpx.ErrTooLarge) {
		err = ErrResponseTooLarge
	}
	if err != nil {
		return 0, nil, nil, err
	}
	return resp.StatusCode, resp.Header, body, nil
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
func (c *client) redact(s string) string { return httpx.Redact(s, c.key) }

// clean is what every string from a response goes through before it reaches
// the model: redacted, then capped at limit characters (runes).
func (c *client) clean(s string, limit int) string {
	return truncate(c.redact(s), limit)
}

// content is clean for a result's content: when it cuts, the marker says how
// much was cut and, in hint, how the agent can get more (E10).
func (c *client) content(s string, limit int, hint string) string {
	return trunc.Content(c.redact(s), limit, hint)
}

// truncate cuts s to at most limit runes, adding the short marker when it
// cuts; for titles, URLs and error messages, where no hint applies.
func truncate(s string, limit int) string { return trunc.Short(s, limit, trunc.Marker) }
