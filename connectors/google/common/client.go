package common

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
)

// Env is what a Google connector's factory options set, and what it builds a
// [Client] from. The connector's options (WithHTTPClient, WithBaseURL, ...)
// fill it in; [Env.Defaults] fills the rest.
type Env struct {
	HTTPClient *http.Client
	BaseURL    string // "" is the connector's default
	LookupEnv  func(string) (string, bool)
	Clock      clock.Clock
	Logger     *slog.Logger
	// Tokens, if set, supplies the access token of the user the tools act
	// for. Without it a connector reads the token from the environment.
	Tokens TokenSource
}

// Defaults fills in what the options left out, so a nil option means the
// default rather than a nil that a toolset would dereference.
func (e *Env) Defaults() {
	if e.HTTPClient == nil {
		e.HTTPClient = &http.Client{}
	}
	if e.LookupEnv == nil {
		e.LookupEnv = func(string) (string, bool) { return "", false }
	}
	if e.Clock == nil {
		e.Clock = clock.Real()
	}
	if e.Logger == nil {
		e.Logger = slog.New(slog.DiscardHandler)
	}
}

// TokensFor returns the source for a toolset: the injected one, else the
// environment variable envName. It logs which, never the token.
func (e *Env) TokensFor(service, envName string) (TokenSource, error) {
	if e.Tokens != nil {
		e.Logger.Info(service+" toolset auth", "mode", "token source")
		return e.Tokens, nil
	}
	src, err := EnvTokens(e.LookupEnv, envName)
	if err != nil {
		return nil, err
	}
	e.Logger.Info(service+" toolset auth", "mode", "environment", "access_token_env", envName)
	return src, nil
}

// Client sends requests to one Google API. It is shared by a toolset's tools
// and is safe for concurrent use.
type Client struct {
	service string
	http    *http.Client
	base    string
	tokens  TokenSource
	timeout time.Duration
	clock   clock.Clock
	log     *slog.Logger
}

// ClientConfig configures [NewClient].
type ClientConfig struct {
	// Service names the API in logs and error messages: "gmail".
	Service string
	// BaseURL is the API's address, without a trailing slash.
	BaseURL string
	Tokens  TokenSource
	// Timeout limits one request (zero is 30 s).
	Timeout time.Duration
	Env     Env
}

// NewClient returns a client for cfg. The env must have had its defaults set.
func NewClient(cfg ClientConfig) *Client {
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultTimeout
	}
	return &Client{
		service: cfg.Service,
		http:    cfg.Env.HTTPClient,
		base:    strings.TrimSuffix(cfg.BaseURL, "/"),
		tokens:  cfg.Tokens,
		timeout: cfg.Timeout,
		clock:   cfg.Env.Clock,
		log:     cfg.Env.Logger,
	}
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
// from the model cannot reach another endpoint: Path("/gmail/v1/users/me/messages", id).
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
		return fmt.Errorf("%s returned a response that is not valid JSON", c.service)
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
		if inv, ok := c.tokens.(Invalidator); ok {
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
	if status >= 300 && isRateLimited(status, errorReasons(body)) {
		wait, ok := httpx.RetryAfter(ctx, header.Get("Retry-After"), c.clock.Now())
		if !ok {
			return nil, c.statusError(status, body, token)
		}
		c.log.DebugContext(ctx, c.service+" rate limited, retrying", "path", r.Path, "wait", wait)
		if err := httpx.Sleep(ctx, c.clock, wait); err != nil {
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

// clean makes text taken from a response safe to show: the access token
// replaced, then cut to limit characters.
func clean(s, token string, limit int) string {
	return Truncate(httpx.Redact(s, token), limit)
}

func (c *Client) token(ctx context.Context) (string, error) {
	t, err := c.tokens.Token(ctx)
	switch {
	case errors.Is(err, ErrNoToken):
		return "", fmt.Errorf("%s: %w; connect one and try again", c.service, ErrNoToken)
	case err != nil && ctx.Err() != nil:
		return "", ctx.Err()
	case err != nil:
		c.log.WarnContext(ctx, c.service+" token source failed", "err", err)
		return "", fmt.Errorf("%s: could not get an access token", c.service)
	case strings.TrimSpace(t) == "":
		return "", fmt.Errorf("%s: %w; connect one and try again", c.service, ErrNoToken)
	}
	return t, nil
}

// attempt sends one request. It returns an error only when no status was
// received (transport failure, timeout, a body over the limit).
func (c *Client) attempt(ctx context.Context, r Request, payload []byte, token string) (int, http.Header, []byte, error) {
	actx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	method := r.Method
	if method == "" {
		method = http.MethodGet
	}
	u := c.base + r.Path
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
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, c.transportError(ctx, actx, token, r.Path, err)
	}
	defer resp.Body.Close()
	data, err := httpx.ReadBody(resp.Body, MaxBodyBytes)
	switch {
	case errors.Is(err, httpx.ErrTooLarge):
		return 0, nil, nil, fmt.Errorf("response from %s is over the 8 MB limit", c.service)
	case err != nil:
		return 0, nil, nil, c.transportError(ctx, actx, token, r.Path, err)
	}
	c.log.DebugContext(ctx, c.service+" request", "method", method, "path", r.Path, "status", resp.StatusCode,
		"bytes", len(data), "duration", time.Since(start))
	return resp.StatusCode, resp.Header, data, nil
}

// transportError reports a request that got no complete response. The cause
// is logged, redacted, not shown: it is the client's text and says nothing the
// model can act on. The caller's own cancellation is returned as such.
func (c *Client) transportError(ctx, actx context.Context, token, path string, cause error) error {
	if ctx.Err() == nil {
		c.log.WarnContext(ctx, c.service+" request failed", "path", path, "err", httpx.Redact(cause.Error(), token))
	}
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case actx.Err() != nil:
		return fmt.Errorf("%s did not answer within %v", c.service, c.timeout)
	default:
		return fmt.Errorf("could not reach %s", c.service)
	}
}
