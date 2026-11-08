package elevation

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/pkg/clock"
)

// RetryAfter is the pause before the one retry of a failed request (a
// network error or a 5xx). A 429 is not retried: see [RateLimitPause].
const RetryAfter = 300 * time.Millisecond

// RateLimitPause is how long a client stops asking a service that answered
// 429 without a Retry-After header. Open-Meteo's free limits are per
// minute, hour and day, and its 429 does not say which.
const RateLimitPause = time.Hour

// Timeout is the default HTTP client's limit on one request: short enough
// that a hung service leaves the backup time to answer within a survey.
const Timeout = 15 * time.Second

var errRetry = errors.New("worth retrying")

// rateLimited is a 429 with how long to wait.
type rateLimited struct{ wait time.Duration }

func (e *rateLimited) Error() string { return "HTTP 429" }

// Option configures a [Client].
type Option func(*Client)

// WithHTTPClient sets the HTTP client (default: one with [Timeout]).
func WithHTTPClient(hc *http.Client) Option { return func(c *Client) { c.http = hc } }

// WithCache sets where heights are remembered (default: in memory, for this
// client only). One cache can serve several clients.
func WithCache(k *Cache) Option { return func(c *Client) { c.cache = k } }

// WithClock sets the clock of the pacing and the rate-limit pause.
func WithClock(k clock.Clock) Option { return func(c *Client) { c.clock = k } }

// Client is a Source on one HTTP elevation service: it asks for at most a
// batch of points a request, paces its requests, retries once, remembers
// the answers and pauses after a 429.
type Client struct {
	name     string
	tag      string // the cache's key prefix
	res      float64
	base     string
	batch    int
	interval time.Duration
	fetch    func(ctx context.Context, c *Client, ps []geo.Point) ([]float64, error)

	http  *http.Client
	cache *Cache
	clock clock.Clock

	pace  sync.Mutex // held while waiting for a request's turn
	last  time.Time
	mu    sync.Mutex
	until time.Time // no requests before this (after a 429)
}

func newClient(base string, c *Client, opts []Option) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("elevation: %q is not an http(s) URL", base)
	}
	c.base = base
	for _, o := range opts {
		o(c)
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: Timeout}
	}
	if c.cache == nil {
		c.cache = NewCache()
	}
	if c.clock == nil {
		c.clock = clock.Real()
	}
	return c, nil
}

// Name implements Source.
func (c *Client) Name() string { return c.name }

// Elevations implements Source.
func (c *Client) Elevations(ctx context.Context, ps []geo.Point) (Heights, error) {
	out := make([]float64, len(ps))
	var need []int
	for i, p := range ps {
		if !p.Valid() {
			return Heights{}, fmt.Errorf("elevation: point %d is not a position", i)
		}
		if h, ok := c.cache.get(c.tag, p); ok {
			out[i] = h
		} else {
			need = append(need, i)
		}
	}
	for len(need) > 0 {
		batch := need[:min(len(need), c.batch)]
		need = need[len(batch):]
		bp := make([]geo.Point, len(batch))
		for j, i := range batch {
			bp[j] = ps[i]
		}
		hs, err := c.ask(ctx, bp)
		if errors.Is(err, errRetry) {
			// Once more after a pause: the service, or the way to it, is
			// sometimes slow to shake hands.
			select {
			case <-ctx.Done():
				return Heights{}, ctx.Err()
			case <-c.clock.After(RetryAfter):
			}
			hs, err = c.ask(ctx, bp)
		}
		if err != nil {
			return Heights{}, err
		}
		if len(hs) != len(batch) {
			return Heights{}, fmt.Errorf("%w: %d heights for %d points", ErrUnavailable, len(hs), len(batch))
		}
		for j, i := range batch {
			out[i] = hs[j]
		}
		c.cache.put(c.tag, bp, hs)
	}
	return Heights{Metres: out, From: c.name, ResolutionM: c.res}, nil
}

// ask makes one request in its turn, unless the service is paused.
func (c *Client) ask(ctx context.Context, ps []geo.Point) ([]float64, error) {
	c.mu.Lock()
	until := c.until
	c.mu.Unlock()
	if now := c.clock.Now(); now.Before(until) {
		return nil, fmt.Errorf("%w: paused until %s", ErrRateLimited, until.UTC().Format(time.RFC3339))
	}
	if err := c.wait(ctx); err != nil {
		return nil, err
	}
	hs, err := c.fetch(ctx, c, ps)
	if rl, ok := errors.AsType[*rateLimited](err); ok {
		c.mu.Lock()
		c.until = c.clock.Now().Add(rl.wait)
		c.mu.Unlock()
		return nil, fmt.Errorf("%w: %w", ErrRateLimited, err)
	}
	return hs, err
}

// wait holds a request until interval has passed since the last one.
func (c *Client) wait(ctx context.Context) error {
	if c.interval <= 0 {
		return nil
	}
	c.pace.Lock()
	defer c.pace.Unlock()
	if d := c.last.Add(c.interval).Sub(c.clock.Now()); d > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.clock.After(d):
		}
	}
	c.last = c.clock.Now()
	return nil
}

// get does one GET and sorts its failures: a network error or 5xx is worth
// a retry, a 429 pauses the client, anything else but 200 is final.
func (c *Client) get(ctx context.Context, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		return nil, fmt.Errorf("%w: %w: %w", ErrUnavailable, errRetry, err)
	}
	switch {
	case resp.StatusCode == http.StatusOK:
		return resp, nil
	case resp.StatusCode == http.StatusTooManyRequests:
		resp.Body.Close()
		wait := RateLimitPause
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			wait = time.Duration(s) * time.Second
		}
		return nil, &rateLimited{wait: wait}
	case resp.StatusCode >= 500:
		resp.Body.Close()
		return nil, fmt.Errorf("%w: %w: HTTP %d", ErrUnavailable, errRetry, resp.StatusCode)
	default:
		resp.Body.Close()
		return nil, fmt.Errorf("%w: HTTP %d", ErrUnavailable, resp.StatusCode)
	}
}
