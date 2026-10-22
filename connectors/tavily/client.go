package tavily

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

// maxMessageChars caps text taken from Tavily's error bodies.
const maxMessageChars = 300

// client sends requests to Tavily. It is shared by a toolset's tools and is
// safe for concurrent use.
type client struct {
	http    *http.Client
	baseURL string
	key     string // "" in keyless mode
	keyEnv  string
	clock   clock.Clock
	log     *slog.Logger
}

// inputError is an error the model can fix by changing its input. The tools
// turn it into [tool.ErrInvalidInput].
type inputError struct{ msg string }

func (e *inputError) Error() string { return e.msg }

func invalid(format string, args ...any) error {
	return &inputError{msg: fmt.Sprintf(format, args...)}
}

// errTooLarge is a response over httpx.MaxBody.
var errTooLarge = errors.New("response from tavily is over the 8 MB limit")

// post sends body to endpoint and returns the response body of a 2xx. A 429
// is retried once; every other failure is returned as it is.
func (c *client) post(ctx context.Context, endpoint string, body any, timeout time.Duration) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	status, header, resp, err := c.attempt(ctx, endpoint, payload, timeout)
	if err != nil {
		return nil, err
	}
	if status == http.StatusTooManyRequests {
		wait, ok := httpx.RetryAfter(ctx, header.Get("Retry-After"), c.clock.Now())
		if !ok {
			return nil, c.rateLimited()
		}
		c.log.DebugContext(ctx, "tavily rate limited, retrying", "endpoint", endpoint, "wait", wait)
		if err := httpx.Sleep(ctx, c.clock, wait); err != nil {
			return nil, err
		}
		if status, _, resp, err = c.attempt(ctx, endpoint, payload, timeout); err != nil {
			return nil, err
		}
	}
	return resp, c.statusError(status, resp)
}

// attempt sends one request. It returns an error only when no status was
// received (transport failure, timeout, a body over the limit).
func (c *client) attempt(ctx context.Context, endpoint string, payload []byte, timeout time.Duration) (int, http.Header, []byte, error) {
	actx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(actx, http.MethodPost, c.baseURL+endpoint, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	} else {
		req.Header.Set("X-Tavily-Access-Mode", "keyless")
	}

	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, c.transportError(ctx, actx, endpoint, timeout, err)
	}
	defer resp.Body.Close()
	body, err := httpx.ReadBody(resp.Body, httpx.MaxBody)
	switch {
	case errors.Is(err, httpx.ErrTooLarge):
		return 0, nil, nil, errTooLarge
	case err != nil:
		return 0, nil, nil, c.transportError(ctx, actx, endpoint, timeout, err)
	}
	c.log.DebugContext(ctx, "tavily request", "endpoint", endpoint, "status", resp.StatusCode,
		"bytes", len(body), "duration", time.Since(start))
	return resp.StatusCode, resp.Header, body, nil
}

// transportError reports a request that got no complete response. The
// cause is not included: it is the client's text, not Tavily's, and says
// nothing the model can act on. It is logged instead, redacted, so that a
// DNS, TLS or proxy failure can be told apart from Tavily being down. The
// caller's own cancellation is returned as such, and not logged.
func (c *client) transportError(ctx, actx context.Context, endpoint string, timeout time.Duration, cause error) error {
	if ctx.Err() == nil {
		c.log.WarnContext(ctx, "tavily request failed", "endpoint", endpoint, "err", c.redact(cause.Error()))
	}
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case actx.Err() != nil:
		return fmt.Errorf("tavily did not answer within %v", timeout)
	default:
		return errors.New("could not reach tavily")
	}
}

// statusError maps a non-2xx status to the error the model sees.
func (c *client) statusError(status int, body []byte) error {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == http.StatusBadRequest || status == http.StatusUnprocessableEntity:
		msg := c.clean(errorMessage(body), maxMessageChars)
		if msg == "" {
			msg = "no details"
		}
		return invalid("tavily rejected the request (status %d): %s", status, msg)
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return fmt.Errorf("tavily rejected the API key (status %d)", status)
	case status == http.StatusTooManyRequests:
		return c.rateLimited()
	default:
		return fmt.Errorf("tavily returned status %d", status)
	}
}

func (c *client) rateLimited() error {
	if c.key == "" {
		return fmt.Errorf("tavily rate limited the request; set %s for higher limits. Until then, %s", c.keyEnv, httpx.RateLimitAdvice)
	}
	return errors.New("tavily rate limited the request; " + httpx.RateLimitAdvice)
}

// clean makes text taken from a response safe to show: the key replaced,
// then the text cut to limit runes.
func (c *client) clean(s string, limit int) string {
	return truncate(c.redact(s), limit)
}

// cleanContent is clean for a result's content: the marker says how to get
// more.
func (c *client) cleanContent(s string, limit int, more string) string {
	return truncateContent(c.redact(s), limit, more)
}

func (c *client) redact(s string) string { return httpx.Redact(s, c.key) }

// errorMessage extracts Tavily's message from an error body:
// {"detail": {"error": "..."}} (400 and others), {"detail": [{"loc", "msg"}]}
// (422), or {"detail": "..."}. An extract 400 may also list failed URLs.
func errorMessage(body []byte) string {
	var e struct {
		Detail json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(body, &e) != nil || len(e.Detail) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(e.Detail, &text) == nil {
		return text
	}
	var obj struct {
		Error  string           `json:"error"`
		Failed []failedUpstream `json:"failed_results"`
	}
	if json.Unmarshal(e.Detail, &obj) == nil {
		msg := obj.Error
		for i, f := range obj.Failed {
			if i == 3 {
				msg += "; …"
				break
			}
			msg += fmt.Sprintf("; %s: %s", f.URL, f.Error)
		}
		return msg
	}
	var list []struct {
		Loc []any  `json:"loc"`
		Msg string `json:"msg"`
	}
	if json.Unmarshal(e.Detail, &list) == nil {
		var parts []string
		for _, v := range list {
			var loc []string
			for _, l := range v.Loc {
				loc = append(loc, fmt.Sprint(l))
			}
			parts = append(parts, strings.Join(loc, ".")+": "+v.Msg)
		}
		return strings.Join(parts, "; ")
	}
	return ""
}
