package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/connectors/internal/httpx"
	"github.com/bsamanvith/agentkit/pkg/clock"
)

const (
	// MaxBodyBytes is the largest response body the client reads.
	MaxBodyBytes = httpx.MaxBody

	// DefaultTimeout limits one request when [Config.Timeout] is zero.
	DefaultTimeout = 30 * time.Second

	// maxMessageChars caps text taken from a provider's error bodies.
	maxMessageChars = 300
)

// Config configures [New].
type Config struct {
	// Service names the API in logs and error messages: "gmail", "hubspot".
	Service string
	// Account names what the user connects, in messages that say to
	// connect or reconnect it: "Google account".
	Account string
	// BaseURL is the API's address, without a trailing slash.
	BaseURL string
	Tokens  TokenSource
	// Timeout limits one request (zero is [DefaultTimeout]).
	Timeout time.Duration

	HTTP   *http.Client // nil: a new one
	Clock  clock.Clock  // nil: the real clock
	Logger *slog.Logger // nil: discard

	// ParseError reads the provider's error body: its message (shown to
	// the model, redacted and capped) and machine-readable reasons, which
	// RateLimited reads. Nil shows no message.
	ParseError func(body []byte) (message string, reasons []string)
	// RateLimited says whether an error response means "slow down" (Google
	// says so with a 403 and a reason). A 429 always does.
	RateLimited func(status int, reasons []string) bool
}

// Client sends requests to one API. It is shared by a toolset's tools and is
// safe for concurrent use.
type Client struct {
	cfg Config
}

// New returns a client for cfg.
func New(cfg Config) *Client {
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{}
	}
	if cfg.Clock == nil {
		cfg.Clock = clock.Real()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.Account == "" {
		cfg.Account = "account"
	}
	cfg.BaseURL = strings.TrimSuffix(cfg.BaseURL, "/")
	return &Client{cfg: cfg}
}

// With returns a copy of c that sends to base with tokens: for an API whose
// address depends on the user's account (Salesforce's instance_url), read
// with the token at the start of a call.
func (c *Client) With(base string, tokens TokenSource) *Client {
	cfg := c.cfg
	cfg.BaseURL, cfg.Tokens = strings.TrimSuffix(base, "/"), tokens
	return &Client{cfg: cfg}
}

// Request is one call to the API.
type Request struct {
	Method string // default GET
	// Path starts with "/" and is appended to the base URL. Build it with
	// [Path] so that ids are escaped.
	Path  string
	Query url.Values
	// Body, if not nil, is sent as JSON.
	Body any
	// Raw, if not nil, is sent as it is instead of Body (a multipart upload,
	// say). Set its Content-Type in Header.
	Raw []byte
	// Header adds request headers. A Content-Type or Accept given here is
	// kept; otherwise they are application/json (Content-Type only when
	// there is a body).
	Header http.Header
}

// Path joins path segments under a prefix with each segment escaped, so an id
// from the model cannot reach another endpoint: Path("/crm/v3/objects", kind, id).
func Path(prefix string, segments ...string) string {
	var b strings.Builder
	b.WriteString(prefix)
	for _, s := range segments {
		b.WriteByte('/')
		b.WriteString(url.PathEscape(s))
	}
	return b.String()
}

// DoJSON sends r and decodes the JSON response into out (not decoded when out
// is nil or the body is empty).
func (c *Client) DoJSON(ctx context.Context, r Request, out any) error {
	body, err := c.Do(ctx, r)
	if err != nil {
		return err
	}
	if out == nil || len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%s returned a response that is not valid JSON", c.cfg.Service)
	}
	return nil
}

// Do sends r and returns the body of a 2xx. See the package documentation for
// how failures and retries work.
func (c *Client) Do(ctx context.Context, r Request) ([]byte, error) {
	var payload []byte
	if r.Raw != nil && r.Body != nil {
		return nil, errors.New("request has both Body and Raw")
	}
	payload = r.Raw
	if r.Body != nil {
		var err error
		if payload, err = json.Marshal(r.Body); err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
	}
	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	status, header, body, err := c.attempt(ctx, r, payload, token)
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized {
		if inv, ok := c.cfg.Tokens.(Invalidator); ok {
			inv.Invalidate(token)
			if token, err = c.token(ctx); err != nil {
				return nil, err
			}
			if status, header, body, err = c.attempt(ctx, r, payload, token); err != nil {
				return nil, err
			}
		}
	}
	// A success body can be megabytes and is never an error: only an error's
	// is parsed.
	if status >= 300 && c.rateLimited(status, body) {
		wait, ok := httpx.RetryAfter(ctx, header.Get("Retry-After"), c.cfg.Clock.Now())
		if !ok {
			return nil, c.statusError(status, body, token)
		}
		c.cfg.Logger.DebugContext(ctx, c.cfg.Service+" rate limited, retrying", "path", r.Path, "wait", wait)
		if err := httpx.Sleep(ctx, c.cfg.Clock, wait); err != nil {
			return nil, err
		}
		if status, _, body, err = c.attempt(ctx, r, payload, token); err != nil {
			return nil, err
		}
	}
	if status >= 200 && status < 300 {
		return body, nil
	}
	return nil, c.statusError(status, body, token)
}

// noToken is [ErrNoToken] as the model sees it.
type noToken struct{ service, account string }

func (e noToken) Error() string {
	return fmt.Sprintf("%s: no %s is connected; connect one and try again", e.service, e.account)
}

func (noToken) Is(target error) bool { return target == ErrNoToken }

func (c *Client) token(ctx context.Context) (string, error) {
	t, err := c.cfg.Tokens.Token(ctx)
	switch {
	case errors.Is(err, ErrNoToken):
		return "", noToken{c.cfg.Service, c.cfg.Account}
	case errors.Is(err, ErrNotOAuth):
		return "", fmt.Errorf("%s: %w", c.cfg.Service, err)
	case err != nil && ctx.Err() != nil:
		return "", ctx.Err()
	case err != nil:
		c.cfg.Logger.WarnContext(ctx, c.cfg.Service+" token source failed", "err", err)
		return "", fmt.Errorf("%s: could not get an access token", c.cfg.Service)
	case strings.TrimSpace(t) == "":
		return "", noToken{c.cfg.Service, c.cfg.Account}
	}
	return t, nil
}

// attempt sends one request. It returns an error only when no status was
// received (transport failure, timeout, a body over the limit).
func (c *Client) attempt(ctx context.Context, r Request, payload []byte, token string) (int, http.Header, []byte, error) {
	actx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	method := r.Method
	if method == "" {
		method = http.MethodGet
	}
	u := c.cfg.BaseURL + r.Path
	if len(r.Query) > 0 {
		u += "?" + r.Query.Encode()
	}
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(actx, method, u, body)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("build request: %w", err)
	}
	for k, v := range r.Header {
		req.Header[k] = slices.Clone(v)
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	if payload != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)

	start := time.Now()
	resp, err := c.cfg.HTTP.Do(req)
	if err != nil {
		return 0, nil, nil, c.transportError(ctx, actx, token, r.Path, err)
	}
	defer resp.Body.Close()
	data, err := httpx.ReadBody(resp.Body, MaxBodyBytes)
	switch {
	case errors.Is(err, httpx.ErrTooLarge):
		return 0, nil, nil, fmt.Errorf("response from %s is over the 8 MB limit", c.cfg.Service)
	case err != nil:
		return 0, nil, nil, c.transportError(ctx, actx, token, r.Path, err)
	}
	c.cfg.Logger.DebugContext(ctx, c.cfg.Service+" request", "method", method, "path", r.Path, "status", resp.StatusCode,
		"bytes", len(data), "duration", time.Since(start))
	return resp.StatusCode, resp.Header, data, nil
}

// transportError reports a request that got no complete response. The cause
// is logged, redacted, not shown: it is the client's text and says nothing the
// model can act on. The caller's own cancellation is returned as such.
func (c *Client) transportError(ctx, actx context.Context, token, path string, cause error) error {
	if ctx.Err() == nil {
		c.cfg.Logger.WarnContext(ctx, c.cfg.Service+" request failed", "path", path, "err", httpx.Redact(cause.Error(), token))
	}
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case actx.Err() != nil:
		return fmt.Errorf("%s did not answer within %v", c.cfg.Service, c.cfg.Timeout)
	default:
		return fmt.Errorf("could not reach %s", c.cfg.Service)
	}
}

func (c *Client) parse(body []byte) (string, []string) {
	if c.cfg.ParseError == nil {
		return "", nil
	}
	return c.cfg.ParseError(body)
}

func (c *Client) rateLimited(status int, body []byte) bool {
	if status == http.StatusTooManyRequests {
		return true
	}
	if c.cfg.RateLimited == nil {
		return false
	}
	_, reasons := c.parse(body)
	return c.cfg.RateLimited(status, reasons)
}

// statusError maps a non-2xx response to the error the model sees.
func (c *Client) statusError(status int, body []byte, token string) error {
	msg, reasons := c.parse(body)
	msg = Truncate(httpx.Redact(msg, token), maxMessageChars)
	service := c.cfg.Service
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == http.StatusBadRequest || status == http.StatusConflict || status == http.StatusUnprocessableEntity:
		if msg == "" {
			msg = "no details"
		}
		return Invalid("%s rejected the request (status %d): %s", service, status, msg)
	case status == http.StatusUnauthorized:
		return fmt.Errorf("%s rejected the access token (status %d); the %s must be reconnected", service, status, c.cfg.Account)
	case status == http.StatusTooManyRequests || (c.cfg.RateLimited != nil && c.cfg.RateLimited(status, reasons)):
		return fmt.Errorf("%s rate limited the request", service)
	case status == http.StatusForbidden:
		// The provider's reason is what tells "scope not granted" from
		// "not yours", and holds nothing secret.
		return fmt.Errorf("%s denied access (status %d)%s", service, status, detail(msg))
	case status == http.StatusNotFound:
		return fmt.Errorf("%s: not found (status %d)%s", service, status, detail(msg))
	default:
		return fmt.Errorf("%s returned status %d", service, status)
	}
}

func detail(msg string) string {
	if msg == "" {
		return ""
	}
	return ": " + strings.TrimSpace(msg)
}
