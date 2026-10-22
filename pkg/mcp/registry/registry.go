package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
)

// DefaultURL is the official MCP Registry.
const DefaultURL = "https://registry.modelcontextprotocol.io"

// Defaults and limits.
const (
	// DefaultCacheTTL is how long a search's result is kept.
	DefaultCacheTTL = 5 * time.Minute
	// DefaultCacheSize caps the searches kept.
	DefaultCacheSize = 256
	// DefaultTimeout bounds one search, every page of it.
	DefaultTimeout = 10 * time.Second
	// MaxLimit is the most servers a search returns.
	MaxLimit = 100
	// MaxQueryLength caps a query, in bytes.
	MaxQueryLength = 200
	// maxPages caps the pages one search reads: a page can hold few
	// servers with remotes, so the next is read until there are enough.
	maxPages = 3
	// maxBodyBytes caps a page of the registry's answer.
	maxBodyBytes = 4 << 20
)

// The remote transports a [Remote] has, as the registry names them.
const (
	TransportStreamableHTTP = "streamable-http"
	TransportSSE            = "sse"
)

// Errors of [Client.Search], checked with [errors.Is].
var (
	// ErrInvalid: the query or the limit is out of range.
	ErrInvalid = errors.New("registry: invalid search")
	// ErrUnavailable: the registry could not be reached, or did not answer
	// as the API says.
	ErrUnavailable = errors.New("registry: unavailable")
)

// Server is a server the registry lists, with at least one remote.
type Server struct {
	// Name is the registry's name for it ("app.linear/linear").
	Name        string
	Title       string
	Description string
	Version     string
	WebsiteURL  string
	// RepositoryURL is its source repository, when the registry has one.
	RepositoryURL string
	// Status is the registry's: "active" or "deprecated".
	Status string
	// Remotes are its endpoints over Streamable HTTP or SSE, in the
	// registry's order.
	Remotes []Remote
}

// Remote is an endpoint a server is hosted at.
type Remote struct {
	// Transport is [TransportStreamableHTTP] or [TransportSSE].
	Transport string
	URL       string
	// Variables names the "{variables}" in URL, which whoever adds the
	// server fills in (a tenant's ID, a region); none for most.
	Variables []string
	// Headers are the headers the server says it takes.
	Headers []Header
}

// Header is a header a remote takes.
type Header struct {
	Name        string
	Description string
	Required    bool
	// Secret marks a credential (an API key).
	Secret bool
}

// Option configures [New].
type Option func(*Client)

// WithHTTPClient sets the client that calls the registry (default: one
// with [DefaultTimeout] over http.DefaultTransport, which uses the
// environment's proxy).
func WithHTTPClient(hc *http.Client) Option { return func(c *Client) { c.hc = hc } }

// WithClock sets the clock of the cache (default: the real one).
func WithClock(clk clock.Clock) Option { return func(c *Client) { c.clk = clk } }

// WithCacheTTL sets how long a result is kept (default [DefaultCacheTTL]);
// 0 keeps none.
func WithCacheTTL(d time.Duration) Option { return func(c *Client) { c.ttl = d } }

// WithCacheSize caps the results kept (default [DefaultCacheSize]).
func WithCacheSize(n int) Option { return func(c *Client) { c.size = n } }

// Client searches one registry. It is safe for concurrent use.
type Client struct {
	base *url.URL
	hc   *http.Client
	clk  clock.Clock
	ttl  time.Duration
	size int

	mu    sync.Mutex
	cache map[cacheKey]cached
}

type cacheKey struct {
	query string
	limit int
}

type cached struct {
	at      time.Time
	servers []Server
}

// New returns a client of the registry at base: [DefaultURL], or a
// subregistry's URL, under which the API's paths (/v0.1/servers) are.
func New(base string, opts ...Option) (*Client, error) {
	u, err := url.Parse(base)
	switch {
	case err != nil:
		return nil, fmt.Errorf("registry: url %q: %w", base, err)
	case u.Scheme != "https" && u.Scheme != "http", u.Host == "", u.User != nil, u.RawQuery != "", u.Fragment != "":
		return nil, fmt.Errorf("registry: url %q: want http(s)://host[/path], with no user, query or fragment", base)
	}
	c := &Client{base: u, clk: clock.Real(), ttl: DefaultCacheTTL, size: DefaultCacheSize, cache: map[cacheKey]cached{}}
	for _, o := range opts {
		o(c)
	}
	if c.ttl < 0 || c.size <= 0 {
		return nil, errors.New("registry: the cache's TTL must not be negative and its size must be positive")
	}
	if c.hc == nil {
		c.hc = &http.Client{Timeout: DefaultTimeout}
	}
	return c, nil
}

// URL returns the registry's URL.
func (c *Client) URL() string { return c.base.String() }

// Search returns up to limit servers matching query (the registry matches
// it against server names) that have a remote agentkit can add: Streamable
// HTTP or SSE. Servers with only packages, and deleted ones, are left out.
// Only each server's latest version is listed.
func (c *Client) Search(ctx context.Context, query string, limit int) ([]Server, error) {
	query = strings.TrimSpace(query)
	switch {
	case query == "" || len(query) > MaxQueryLength:
		return nil, fmt.Errorf("%w: the query must have 1 to %d bytes", ErrInvalid, MaxQueryLength)
	case limit < 1 || limit > MaxLimit:
		return nil, fmt.Errorf("%w: the limit must be 1 to %d", ErrInvalid, MaxLimit)
	}
	k := cacheKey{strings.ToLower(query), limit}
	if s, ok := c.cached(k); ok {
		return s, nil
	}
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()
	var out []Server
	seen := map[string]bool{}
	cursor := ""
	for range maxPages {
		page, err := c.page(ctx, query, limit, cursor)
		if err != nil {
			return nil, err
		}
		for _, e := range page.Servers {
			if s, ok := e.server(); ok && !seen[s.Name] {
				seen[s.Name] = true
				out = append(out, s)
			}
		}
		cursor = page.Metadata.NextCursor
		if len(out) >= limit || cursor == "" {
			break
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	if out == nil {
		out = []Server{}
	}
	c.keep(k, out)
	return clone(out), nil
}

func (c *Client) cached(k cacheKey) ([]Server, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cache[k]
	if !ok {
		return nil, false
	}
	if c.clk.Now().Sub(e.at) >= c.ttl {
		delete(c.cache, k)
		return nil, false
	}
	return clone(e.servers), true
}

func (c *Client) keep(k cacheKey, s []Server) {
	if c.ttl == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clk.Now()
	for len(c.cache) >= c.size {
		var oldest cacheKey
		first := true
		for key, e := range c.cache {
			if first || e.at.Before(c.cache[oldest].at) {
				oldest, first = key, false
			}
		}
		delete(c.cache, oldest)
	}
	c.cache[k] = cached{at: now, servers: clone(s)}
}

// page reads one page of the registry's list.
func (c *Client) page(ctx context.Context, query string, limit int, cursor string) (listResponse, error) {
	u := c.base.JoinPath("v0.1", "servers")
	q := url.Values{"search": {query}, "limit": {fmt.Sprint(limit)}, "version": {"latest"}}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return listResponse{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return listResponse{}, ctx.Err()
		}
		return listResponse{}, fmt.Errorf("%w: %s: %w", ErrUnavailable, c.base.Host, unwrapURL(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return listResponse{}, fmt.Errorf("%w: %s answered %s", ErrUnavailable, c.base.Host, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	switch {
	case err != nil:
		return listResponse{}, fmt.Errorf("%w: %s: reading the answer: %w", ErrUnavailable, c.base.Host, err)
	case len(body) > maxBodyBytes:
		return listResponse{}, fmt.Errorf("%w: %s: the answer is over %d bytes", ErrUnavailable, c.base.Host, maxBodyBytes)
	}
	var lr listResponse
	if err := json.Unmarshal(body, &lr); err != nil || lr.Servers == nil {
		return listResponse{}, fmt.Errorf("%w: %s: the answer is not a list of servers", ErrUnavailable, c.base.Host)
	}
	return lr, nil
}

// unwrapURL drops the *url.Error's URL, which repeats the query.
func unwrapURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// listResponse is GET /v0.1/servers.
type listResponse struct {
	Servers  []entry `json:"servers"`
	Metadata struct {
		NextCursor string `json:"nextCursor"`
	} `json:"metadata"`
}

type entry struct {
	Server struct {
		Name        string `json:"name"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Version     string `json:"version"`
		WebsiteURL  string `json:"websiteUrl"`
		Repository  struct {
			URL string `json:"url"`
		} `json:"repository"`
		Remotes []struct {
			Type    string `json:"type"`
			URL     string `json:"url"`
			Headers []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				IsRequired  bool   `json:"isRequired"`
				IsSecret    bool   `json:"isSecret"`
			} `json:"headers"`
		} `json:"remotes"`
	} `json:"server"`
	Meta struct {
		Official struct {
			Status string `json:"status"`
		} `json:"io.modelcontextprotocol.registry/official"`
	} `json:"_meta"`
}

var variable = regexp.MustCompile(`\{([^{}]+)\}`)

// server is the entry as a [Server], false when agentkit cannot add it.
func (e entry) server() (Server, bool) {
	status := e.Meta.Official.Status
	if status == "deleted" || e.Server.Name == "" {
		return Server{}, false
	}
	if status == "" {
		status = "active"
	}
	s := Server{
		Name: e.Server.Name, Title: e.Server.Title, Description: e.Server.Description, Version: e.Server.Version,
		WebsiteURL: e.Server.WebsiteURL, RepositoryURL: e.Server.Repository.URL, Status: status,
	}
	for _, r := range e.Server.Remotes {
		if r.Type != TransportStreamableHTTP && r.Type != TransportSSE {
			continue
		}
		if u, err := url.Parse(variable.ReplaceAllString(r.URL, "x")); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			continue
		}
		rm := Remote{Transport: r.Type, URL: r.URL}
		for _, m := range variable.FindAllStringSubmatch(r.URL, -1) {
			if !slices.Contains(rm.Variables, m[1]) {
				rm.Variables = append(rm.Variables, m[1])
			}
		}
		for _, h := range r.Headers {
			if h.Name != "" {
				rm.Headers = append(rm.Headers, Header{Name: h.Name, Description: h.Description, Required: h.IsRequired, Secret: h.IsSecret})
			}
		}
		s.Remotes = append(s.Remotes, rm)
	}
	return s, len(s.Remotes) > 0
}

// clone copies servers deeply, so the cache never shares memory with a
// caller.
func clone(in []Server) []Server {
	out := slices.Clone(in)
	for i := range out {
		out[i].Remotes = slices.Clone(out[i].Remotes)
		for j := range out[i].Remotes {
			out[i].Remotes[j].Variables = slices.Clone(out[i].Remotes[j].Variables)
			out[i].Remotes[j].Headers = slices.Clone(out[i].Remotes[j].Headers)
		}
	}
	return out
}
