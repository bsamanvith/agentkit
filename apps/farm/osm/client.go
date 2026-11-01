package osm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
)

// Public servers.
const (
	// DefaultOverpass are public Overpass instances, tried in order.
	DefaultOverpass = "https://overpass-api.de/api/interpreter,https://maps.mail.ru/osm/tools/overpass/api/interpreter"
	// DefaultAPI is the OpenStreetMap API.
	DefaultAPI = "https://api.openstreetmap.org"
	// MaxAPIBox is the biggest box, in square degrees, the client asks the
	// map call for: about 2.2 km a side in India. The server's own limit is
	// 0.25.
	MaxAPIBox = 0.0004
	// MaxBody caps an answer: a dense town's square kilometre is a few MB.
	MaxBody = 32 << 20
	// CacheSize is how many answers a client keeps.
	CacheSize = 64
)

// Option configures a client.
type Option func(*client)

// WithHTTPClient sets the HTTP client (default: one with a 30 s timeout).
func WithHTTPClient(hc *http.Client) Option { return func(c *client) { c.http = hc } }

// WithClock sets the clock of the request interval and the cache.
func WithClock(clk clock.Clock) Option { return func(c *client) { c.clock = clk } }

// WithInterval sets the least time between two requests (default 1 s).
func WithInterval(d time.Duration) Option { return func(c *client) { c.interval = d } }

// WithCacheTTL sets how long an answer is kept (default 24 h).
func WithCacheTTL(d time.Duration) Option { return func(c *client) { c.ttl = d } }

type client struct {
	name      string
	userAgent string
	http      *http.Client
	clock     clock.Clock
	interval  time.Duration
	ttl       time.Duration
	request   func(ctx context.Context, box BBox) ([]*http.Request, error)
	maxBox    float64

	turn  chan struct{}
	last  time.Time // guarded by turn
	mu    sync.Mutex
	cache map[[4]int64]cached
	order [][4]int64
}

type cached struct {
	fs []Feature
	at time.Time
}

func newClient(name, userAgent string, opts []Option) (*client, error) {
	if strings.TrimSpace(userAgent) == "" {
		return nil, errors.New("osm: a User-Agent that names the app is required")
	}
	c := &client{
		name: name, userAgent: userAgent, http: &http.Client{Timeout: 30 * time.Second},
		clock: clock.Real(), interval: time.Second, ttl: 24 * time.Hour,
		turn: make(chan struct{}, 1), cache: map[[4]int64]cached{},
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// Overpass returns a source on Overpass instances (their interpreter
// URLs), tried in order.
func Overpass(userAgent string, urls []string, opts ...Option) (Source, error) {
	if len(urls) == 0 {
		return nil, errors.New("osm: Overpass needs at least one URL")
	}
	for _, u := range urls {
		if err := checkURL(u); err != nil {
			return nil, err
		}
	}
	c, err := newClient("OpenStreetMap (Overpass)", userAgent, opts)
	if err != nil {
		return nil, err
	}
	c.maxBox = 0.01
	c.request = func(ctx context.Context, box BBox) ([]*http.Request, error) {
		form := url.Values{"data": {overpassQuery(box)}}.Encode()
		var reqs []*http.Request
		for _, u := range urls {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form))
			if err != nil {
				return nil, err
			}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			reqs = append(reqs, req)
		}
		return reqs, nil
	}
	return c, nil
}

// API returns a source on the OpenStreetMap API's map call at base (such
// as [DefaultAPI]).
func API(userAgent, base string, opts ...Option) (Source, error) {
	if err := checkURL(base); err != nil {
		return nil, err
	}
	base = strings.TrimRight(base, "/")
	c, err := newClient("OpenStreetMap", userAgent, opts)
	if err != nil {
		return nil, err
	}
	c.maxBox = MaxAPIBox
	c.request = func(ctx context.Context, box BBox) ([]*http.Request, error) {
		f := func(x float64) string { return strconv.FormatFloat(x, 'f', 6, 64) }
		u := base + "/api/0.6/map.json?bbox=" + f(box.West) + "," + f(box.South) + "," + f(box.East) + "," + f(box.North)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		return []*http.Request{req}, nil
	}
	return c, nil
}

func checkURL(u string) error {
	p, err := url.Parse(u)
	if err != nil || (p.Scheme != "https" && p.Scheme != "http") || p.Host == "" {
		return fmt.Errorf("osm: %q is not an http(s) URL", u)
	}
	return nil
}

// overpassQuery asks for the tagged features this package reads, with
// their geometry.
func overpassQuery(b BBox) string {
	f := func(x float64) string { return strconv.FormatFloat(x, 'f', 6, 64) }
	bb := "(" + f(b.South) + "," + f(b.West) + "," + f(b.North) + "," + f(b.East) + ")"
	parts := []string{
		`way["highway"]`, `way["railway"]`, `way["waterway"]`, `way["natural"~"^(water|wetland|wood|scrub)$"]`,
		`way["water"]`, `way["landuse"]`, `way["building"]`, `way["power"~"^(line|minor_line)$"]`,
		`relation["natural"="water"]`, `relation["landuse"]`, `relation["waterway"="riverbank"]`,
		`node["man_made"~"^(water_well|water_tower)$"]`, `node["amenity"]`, `node["place"]`,
	}
	var q strings.Builder
	q.WriteString("[out:json][timeout:25];(")
	for _, p := range parts {
		q.WriteString(p + bb + ";")
	}
	q.WriteString(");out geom;")
	return q.String()
}

func (c *client) Name() string { return c.name }

// Features implements Source.
func (c *client) Features(ctx context.Context, box BBox) ([]Feature, error) {
	if !box.Valid() || box.Area() > c.maxBox {
		return nil, fmt.Errorf("%w: %.5f square degrees, at most %.5f", ErrBox, box.Area(), c.maxBox)
	}
	k := box.key()
	c.mu.Lock()
	hit, ok := c.cache[k]
	c.mu.Unlock()
	if ok && c.clock.Now().Sub(hit.at) < c.ttl {
		return hit.fs, nil
	}
	reqs, err := c.request(ctx, box)
	if err != nil {
		return nil, err
	}
	var errs []error
	for _, req := range reqs {
		fs, err := c.do(ctx, req)
		if err == nil {
			c.mu.Lock()
			if _, ok := c.cache[k]; !ok {
				c.order = append(c.order, k)
			}
			c.cache[k] = cached{fs, c.clock.Now()}
			for len(c.order) > CacheSize {
				delete(c.cache, c.order[0])
				c.order = c.order[1:]
			}
			c.mu.Unlock()
			return fs, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

func (c *client) do(ctx context.Context, req *http.Request) ([]Feature, error) {
	select {
	case c.turn <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-c.turn }()
	if wait := c.interval - c.clock.Now().Sub(c.last); !c.last.IsZero() && wait > 0 {
		t := c.clock.NewTimer(wait)
		select {
		case <-t.C():
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		}
	}
	c.last = c.clock.Now()
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrUnavailable, req.URL.Host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return nil, fmt.Errorf("%w: %s: HTTP %d", ErrUnavailable, req.URL.Host, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrUnavailable, req.URL.Host, err)
	}
	if len(data) > MaxBody {
		return nil, fmt.Errorf("%w: %s: answer larger than %d bytes", ErrUnavailable, req.URL.Host, MaxBody)
	}
	return Parse(data)
}
