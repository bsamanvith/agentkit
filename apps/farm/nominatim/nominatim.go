// Package nominatim finds where a point is with OpenStreetMap's Nominatim
// reverse geocoder: a [farm.Locator].
//
// The public server (https://nominatim.openstreetmap.org) allows one request
// a second and needs an identifying User-Agent
// (https://operations.osmfoundation.org/policies/nominatim/); the client
// keeps to both and caches answers per ~100 m cell, since a farmer
// verifies the same plot again and again while drawing it. For more than
// light use, run your own Nominatim and point the client at it.
package nominatim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/pkg/clock"
)

// DefaultURL is the public server.
const DefaultURL = "https://nominatim.openstreetmap.org"

// Client is a farm.Locator on Nominatim. It is safe for concurrent use.
type Client struct {
	base      string
	userAgent string
	http      *http.Client
	clock     clock.Clock
	interval  time.Duration

	turn  chan struct{} // held by the one request in flight
	last  time.Time     // when the last request was sent; guarded by turn
	mu    sync.Mutex
	cache map[[2]int64]result
}

type result struct {
	loc farm.Location
	err error // nil or farm.ErrNoLocation
}

var _ farm.Locator = (*Client)(nil)

// Option configures [New].
type Option func(*Client)

// WithURL sets the server (default [DefaultURL]).
func WithURL(u string) Option { return func(c *Client) { c.base = strings.TrimRight(u, "/") } }

// WithHTTPClient sets the HTTP client (default one with a 10s timeout).
func WithHTTPClient(hc *http.Client) Option { return func(c *Client) { c.http = hc } }

// WithInterval sets the least time between two requests (default 1s, the
// public server's limit).
func WithInterval(d time.Duration) Option { return func(c *Client) { c.interval = d } }

// WithClock sets the clock the interval is kept on.
func WithClock(clk clock.Clock) Option { return func(c *Client) { c.clock = clk } }

// New returns a client. userAgent identifies the application, as the
// server's policy requires: "agentkit-farm/0.1 (you@example.com)".
func New(userAgent string, opts ...Option) (*Client, error) {
	if strings.TrimSpace(userAgent) == "" {
		return nil, errors.New("nominatim: a User-Agent is required")
	}
	c := &Client{
		base: DefaultURL, userAgent: userAgent, interval: time.Second,
		http: &http.Client{Timeout: 10 * time.Second}, clock: clock.Real(),
		turn: make(chan struct{}, 1), cache: map[[2]int64]result{},
	}
	for _, o := range opts {
		o(c)
	}
	if _, err := url.Parse(c.base); err != nil {
		return nil, fmt.Errorf("nominatim: URL: %w", err)
	}
	return c, nil
}

// cell is the ~100 m cache cell of p (3 decimal places).
func cell(p geo.Point) [2]int64 {
	return [2]int64{int64(math.Round(p.Lat * 1000)), int64(math.Round(p.Lng * 1000))}
}

// Locate implements farm.Locator.
func (c *Client) Locate(ctx context.Context, p geo.Point) (farm.Location, error) {
	key := cell(p)
	c.mu.Lock()
	r, ok := c.cache[key]
	c.mu.Unlock()
	if ok {
		return r.loc, r.err
	}
	loc, err := c.fetch(ctx, p)
	if err == nil || errors.Is(err, farm.ErrNoLocation) {
		c.mu.Lock()
		c.cache[key] = result{loc, err}
		c.mu.Unlock()
	}
	return loc, err
}

// wait takes the client's turn, keeping the interval since the last
// request; the caller calls the returned release.
func (c *Client) wait(ctx context.Context) (func(), error) {
	select {
	case c.turn <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	release := func() { <-c.turn }
	if wait := c.interval - c.clock.Now().Sub(c.last); !c.last.IsZero() && wait > 0 {
		t := c.clock.NewTimer(wait)
		select {
		case <-t.C():
		case <-ctx.Done():
			t.Stop()
			release()
			return nil, ctx.Err()
		}
	}
	c.last = c.clock.Now()
	return release, nil
}

// get sends a GET of path with q, in turn, and returns the body of a 200.
func (c *Client) get(ctx context.Context, path string, q url.Values) (io.ReadCloser, error) {
	release, err := c.wait(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path+"?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("nominatim: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept-Language", "en")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("nominatim: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("nominatim: HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}

func (c *Client) fetch(ctx context.Context, p geo.Point) (farm.Location, error) {
	q := url.Values{}
	q.Set("format", "jsonv2")
	q.Set("lat", strconv.FormatFloat(p.Lat, 'f', 6, 64))
	q.Set("lon", strconv.FormatFloat(p.Lng, 'f', 6, 64))
	q.Set("zoom", "14") // village or suburb
	q.Set("addressdetails", "1")
	rc, err := c.get(ctx, "/reverse", q)
	if err != nil {
		return farm.Location{}, err
	}
	defer rc.Close()
	var body struct {
		Error       string            `json:"error"`
		DisplayName string            `json:"display_name"`
		Address     map[string]string `json:"address"`
	}
	if err := json.NewDecoder(io.LimitReader(rc, 1<<20)).Decode(&body); err != nil {
		return farm.Location{}, fmt.Errorf("nominatim: decode: %w", err)
	}
	if body.Error != "" || len(body.Address) == 0 {
		// "Unable to geocode": nothing there, as in the open sea.
		return farm.Location{}, farm.ErrNoLocation
	}
	a := body.Address
	first := func(keys ...string) string {
		for _, k := range keys {
			if v := a[k]; v != "" {
				return v
			}
		}
		return ""
	}
	return farm.Location{
		Village:     first("village", "hamlet", "town", "suburb", "neighbourhood", "city"),
		SubDistrict: first("subdistrict", "municipality", "county"),
		District:    first("state_district", "district", "city_district", "county"),
		State:       first("state", "region"),
		Country:     a["country"],
		CountryCode: strings.ToUpper(a["country_code"]),
		DisplayName: body.DisplayName,
	}, nil
}

// Place is a named place found by [Client.Search].
type Place struct {
	Name  string    `json:"name"`
	Kind  string    `json:"kind"` // village, town, river, ...
	Point geo.Point `json:"point"`
	// Address is the full name, from the village up to the country.
	Address string `json:"address"`
}

// MaxResults caps [Client.Search].
const MaxResults = 10

// Search finds places by name ("Kothapeta, Andhra Pradesh", "Rythu Bazar
// Amalapuram"), best match first; near biases the search to around a
// point when it is not nil; country limits it to a country (ISO code, or
// ""). Searches are not cached.
func (c *Client) Search(ctx context.Context, query string, near *geo.Point, country string, limit int) ([]Place, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(query) > 300 {
		return nil, errors.New("nominatim: a search needs a name of at most 300 bytes")
	}
	if limit <= 0 || limit > MaxResults {
		limit = 5
	}
	q := url.Values{}
	q.Set("format", "jsonv2")
	q.Set("q", query)
	q.Set("limit", strconv.Itoa(limit))
	if country != "" {
		q.Set("countrycodes", strings.ToLower(country))
	}
	if near != nil {
		// A box of about 50 km around the point, preferred but not binding.
		f := func(x float64) string { return strconv.FormatFloat(x, 'f', 4, 64) }
		q.Set("viewbox", f(near.Lng-0.25)+","+f(near.Lat+0.25)+","+f(near.Lng+0.25)+","+f(near.Lat-0.25))
	}
	body, err := c.get(ctx, "/search", q)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	var rows []struct {
		Name        string `json:"name"`
		Type        string `json:"type"`
		AddressType string `json:"addresstype"`
		Lat         string `json:"lat"`
		Lon         string `json:"lon"`
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&rows); err != nil {
		return nil, fmt.Errorf("nominatim: decode: %w", err)
	}
	out := []Place{}
	for _, r := range rows {
		lat, err1 := strconv.ParseFloat(r.Lat, 64)
		lng, err2 := strconv.ParseFloat(r.Lon, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		kind := r.AddressType
		if kind == "" {
			kind = r.Type
		}
		name := r.Name
		if name == "" {
			name, _, _ = strings.Cut(r.DisplayName, ",")
		}
		out = append(out, Place{Name: name, Kind: kind, Point: geo.Point{Lat: lat, Lng: lng}, Address: r.DisplayName})
	}
	return out, nil
}
