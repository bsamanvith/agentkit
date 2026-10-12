package agentclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
)

// DefaultMaxResponseBytes is the default of [WithMaxResponseBytes].
const DefaultMaxResponseBytes = 16 << 20

// DefaultUserAgent is the User-Agent sent without [WithUserAgent].
const DefaultUserAgent = "agentkit-agentclient"

// ErrInvalidConfig reports an unusable argument to [New].
var ErrInvalidConfig = errors.New("invalid agent client configuration")

// RunRequest is the body of POST /v1/runs: the message, and the conversation
// to continue (empty starts a new one). The message may be empty only to
// resume an interrupted conversation.
type RunRequest = wire.RunRequest

// Client talks to one agent API server. It is safe for concurrent use.
type Client struct {
	base     string // scheme, host and path prefix, without a trailing slash
	token    string
	ua       string
	hc       *http.Client
	maxBytes int
}

// Option configures [New].
type Option func(*Client)

// WithToken sends "Authorization: Bearer <token>" with every request.
func WithToken(token string) Option { return func(c *Client) { c.token = token } }

// WithHTTPClient sets the HTTP client (default: a client with no timeout).
// A Timeout on it also limits how long a stream can run; bound requests with
// their context instead.
func WithHTTPClient(hc *http.Client) Option { return func(c *Client) { c.hc = hc } }

// WithUserAgent sets the User-Agent header (default [DefaultUserAgent]).
func WithUserAgent(ua string) Option { return func(c *Client) { c.ua = ua } }

// WithMaxResponseBytes limits the size of one SSE line, one event and one
// JSON response body the client reads (default [DefaultMaxResponseBytes]);
// past it, reading fails with [ErrResponseTooLarge].
func WithMaxResponseBytes(n int) Option { return func(c *Client) { c.maxBytes = n } }

// New returns a client of the agent API at baseURL: the URL the API's /v1 is
// under, such as "http://localhost:8080" (or "https://host/prefix" behind a
// proxy that mounts it there). It fails with [ErrInvalidConfig] for a URL
// that is not absolute http(s) or has a query, or an option with an unusable
// value.
func New(baseURL string, opts ...Option) (*Client, error) {
	u, err := url.Parse(baseURL)
	switch {
	case err != nil:
		return nil, fmt.Errorf("%w: base URL: %w", ErrInvalidConfig, err)
	case u.Scheme != "http" && u.Scheme != "https", u.Host == "":
		return nil, fmt.Errorf("%w: base URL %q is not an absolute http or https URL", ErrInvalidConfig, baseURL)
	case u.RawQuery != "" || u.Fragment != "":
		return nil, fmt.Errorf("%w: base URL %q has a query or fragment", ErrInvalidConfig, baseURL)
	}
	c := &Client{base: strings.TrimRight(u.String(), "/"), ua: DefaultUserAgent, hc: &http.Client{}, maxBytes: DefaultMaxResponseBytes}
	for _, o := range opts {
		o(c)
	}
	switch {
	case c.hc == nil:
		return nil, fmt.Errorf("%w: nil HTTP client", ErrInvalidConfig)
	case c.maxBytes < 1:
		return nil, fmt.Errorf("%w: the response limit must be at least 1 byte", ErrInvalidConfig)
	}
	return c, nil
}

// Stream starts a run and returns its event stream, open: read it with
// [Stream.Next], and close it. The run goes on until its last event, until
// the stream is closed or ctx is done (either cancels the run on the server).
// An HTTP error before the run starts is an [*APIError].
func (c *Client) Stream(ctx context.Context, req RunRequest) (*Stream, error) {
	ctx, cancel := context.WithCancel(ctx)
	resp, err := c.postRun(ctx, req, "text/event-stream")
	if err != nil {
		cancel()
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer cancel()
		return nil, c.apiError(resp)
	}
	if !hasMediaType(resp.Header.Get("Content-Type"), "text/event-stream") {
		cancel()
		resp.Body.Close()
		return nil, fmt.Errorf("%w: POST /v1/runs answered %q, not an event stream", ErrBadResponse, resp.Header.Get("Content-Type"))
	}
	return newStream(resp, cancel, c.maxBytes), nil
}

// Run starts a run and waits for it to end, then returns it (JSON mode). A
// run that failed is not a Go error: its Status is [wire.StatusFailed] and
// its Error says why. An HTTP error before the run starts is an [*APIError].
// If ctx is done before the run ends, the run is cancelled on the server.
func (c *Client) Run(ctx context.Context, req RunRequest) (wire.Run, error) {
	var run wire.Run
	resp, err := c.postRun(ctx, req, "application/json")
	if err != nil {
		return run, err
	}
	return run, c.decode(resp, "POST /v1/runs", &run)
}

// GetRun returns a recent run (GET /v1/runs/{id}). An unknown or evicted run
// matches [ErrNotFound].
func (c *Client) GetRun(ctx context.Context, id string) (wire.Run, error) {
	var run wire.Run
	return run, c.get(ctx, "/v1/runs/"+url.PathEscape(id), &run)
}

// GetConversation returns a conversation and its messages
// (GET /v1/conversations/{id}). An unknown or evicted conversation matches
// [ErrNotFound].
func (c *Client) GetConversation(ctx context.Context, id string) (wire.Conversation, error) {
	var conv wire.Conversation
	return conv, c.get(ctx, "/v1/conversations/"+url.PathEscape(id), &conv)
}

func (c *Client) postRun(ctx context.Context, req RunRequest, accept string) (*http.Response, error) {
	return c.postJSON(ctx, "/v1/runs", req, accept)
}

// postJSON posts v as JSON to path.
func (c *Client) postJSON(ctx context.Context, path string, v any, accept string) (*http.Response, error) {
	body, err := json.Marshal(v)
	if err != nil { // cannot happen: the request types are plain strings
		return nil, err
	}
	r, err := c.request(ctx, http.MethodPost, path, bytes.NewReader(body), accept)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	return c.hc.Do(r)
}

func (c *Client) get(ctx context.Context, path string, v any) error {
	r, err := c.request(ctx, http.MethodGet, path, nil, "application/json")
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(r)
	if err != nil {
		return err
	}
	return c.decode(resp, "GET "+path, v)
}

func (c *Client) request(ctx context.Context, method, path string, body io.Reader, accept string) (*http.Request, error) {
	r, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Accept", accept)
	r.Header.Set("User-Agent", c.ua)
	if c.token != "" {
		r.Header.Set("Authorization", "Bearer "+c.token)
	}
	return r, nil
}

// decode reads a JSON response into v, or the API error it is, and closes
// the body.
func (c *Client) decode(resp *http.Response, what string, v any) error {
	return c.decodeStatus(resp, http.StatusOK, what, v)
}

// decodeStatus is decode for a response whose success is status.
func (c *Client) decodeStatus(resp *http.Response, status int, what string, v any) error {
	if resp.StatusCode != status {
		return c.apiError(resp)
	}
	defer resp.Body.Close()
	if !hasMediaType(resp.Header.Get("Content-Type"), "application/json") {
		return fmt.Errorf("%w: %s answered %q, not JSON", ErrBadResponse, what, resp.Header.Get("Content-Type"))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(c.maxBytes)+1))
	if err != nil {
		return fmt.Errorf("agentclient: reading the response to %s: %w", what, err)
	}
	if len(body) > c.maxBytes {
		return fmt.Errorf("%w: the response to %s is larger than %d bytes", ErrResponseTooLarge, what, c.maxBytes)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("%w: the response to %s: %w", ErrBadResponse, what, err)
	}
	return nil
}

func hasMediaType(header, want string) bool {
	mt, _, err := mime.ParseMediaType(header)
	return err == nil && mt == want
}
