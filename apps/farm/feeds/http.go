package feeds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Timeout is the limit on one request of [NewHTTPClient]'s client.
const Timeout = 20 * time.Second

// MaxBody bounds a response [GetJSON] and [GetBody] read: 8 MiB.
const MaxBody = 8 << 20

// NewHTTPClient returns an HTTP client with [Timeout].
func NewHTTPClient() *http.Client { return &http.Client{Timeout: Timeout} }

// GetBody does one GET and returns the body of a 200. It sorts failures
// the way a [Chain] wants them: a network error or a 5xx is an
// [ErrUnavailable], a 429 a [RateLimitError] (with Retry-After in seconds
// when sent), a 404 an [ErrNoData], and any other status an error that
// names it. header (may be nil) is added to the request.
func GetBody(ctx context.Context, hc *http.Client, url string, header http.Header) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusTooManyRequests:
		var wait time.Duration
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			wait = time.Duration(s) * time.Second
		}
		return nil, &RateLimitError{RetryAfter: wait}
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%w: HTTP 404", ErrNoData)
	case resp.StatusCode >= 500, resp.StatusCode == http.StatusForbidden:
		// A 403 here is most often a firewall refusing the server's
		// address (the Indian government's sites refuse many clouds),
		// not a bad request: another provider may still answer.
		return nil, fmt.Errorf("%w: HTTP %d", ErrUnavailable, resp.StatusCode)
	default:
		return nil, fmt.Errorf("feeds: HTTP %d from %s", resp.StatusCode, req.URL.Host)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody+1))
	if err != nil {
		return nil, fmt.Errorf("%w: reading the answer: %w", ErrUnavailable, err)
	}
	if len(b) > MaxBody {
		return nil, fmt.Errorf("feeds: answer from %s over %d bytes", req.URL.Host, MaxBody)
	}
	return b, nil
}

// GetJSON does [GetBody] and decodes the body into v.
func GetJSON(ctx context.Context, hc *http.Client, url string, header http.Header, v any) error {
	b, err := GetBody(ctx, hc, url, header)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%w: not the JSON expected: %w", ErrUnavailable, err)
	}
	return nil
}
