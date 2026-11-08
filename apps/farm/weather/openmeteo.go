package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/pkg/clock"
)

// DefaultURL is Open-Meteo's public forecast API: free for
// non-commercial use, no key, 10,000 calls a day.
const DefaultURL = "https://api.open-meteo.com/v1/forecast"

// cell is how coarse the cache is: 0.1° (about 11 km), finer than the
// weather models' own grids (9 to 13 km for the global ones).
const cell = 0.1

// Client is a [Source] on Open-Meteo. It is safe for concurrent use.
type Client struct {
	url   string
	http  *http.Client
	clock clock.Clock
	ttl   time.Duration

	mu      sync.Mutex
	cache   map[[2]int64]entry
	flights map[[2]int64]*flight
	paused  time.Time // after a 429, until then
}

type entry struct {
	f    Forecast
	at   time.Time
	days int
}

type flight struct {
	done chan struct{}
	f    Forecast
	err  error
}

// Option configures [OpenMeteo].
type Option func(*Client)

// WithHTTPClient sets the HTTP client (default one with a 20s timeout).
func WithHTTPClient(hc *http.Client) Option { return func(c *Client) { c.http = hc } }

// WithClock sets the clock the cache and the pause after a rate limit go
// by.
func WithClock(k clock.Clock) Option { return func(c *Client) { c.clock = k } }

// WithTTL sets how long a forecast is reused (default 3 hours: the global
// models run every 6).
func WithTTL(d time.Duration) Option { return func(c *Client) { c.ttl = d } }

// OpenMeteo returns a client of the forecast API at u ([DefaultURL] when
// empty).
func OpenMeteo(u string, opts ...Option) (*Client, error) {
	if u == "" {
		u = DefaultURL
	}
	if p, err := url.Parse(u); err != nil || (p.Scheme != "http" && p.Scheme != "https") {
		return nil, fmt.Errorf("weather: bad URL %q", u)
	}
	c := &Client{
		url: u, http: &http.Client{Timeout: 20 * time.Second}, clock: clock.Real(), ttl: 3 * time.Hour,
		cache: map[[2]int64]entry{}, flights: map[[2]int64]*flight{},
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

func key(p geo.Point) [2]int64 {
	return [2]int64{int64(math.Round(p.Lat / cell)), int64(math.Round(p.Lng / cell))}
}

// Forecast implements [Source]. Requests for one grid cell at once share
// one call.
func (c *Client) Forecast(ctx context.Context, p geo.Point, days int) (Forecast, error) {
	if days < 1 || days > 16 {
		return Forecast{}, fmt.Errorf("weather: %d days: want 1 to 16", days)
	}
	if p.Lat < -90 || p.Lat > 90 || p.Lng < -180 || p.Lng > 180 {
		return Forecast{}, fmt.Errorf("weather: point %v is off the globe", p)
	}
	k := key(p)
	c.mu.Lock()
	now := c.clock.Now()
	if e, ok := c.cache[k]; ok && now.Sub(e.at) < c.ttl && e.days >= days {
		c.mu.Unlock()
		return trim(e.f, days), nil
	}
	if now.Before(c.paused) {
		until := c.paused
		c.mu.Unlock()
		return Forecast{}, fmt.Errorf("%w: Open-Meteo's rate limit, paused until %s", ErrUnavailable, until.Format(time.RFC3339))
	}
	if fl, ok := c.flights[k]; ok {
		c.mu.Unlock()
		select {
		case <-fl.done:
			if fl.err != nil {
				return Forecast{}, fl.err
			}
			return trim(fl.f, days), nil
		case <-ctx.Done():
			return Forecast{}, ctx.Err()
		}
	}
	fl := &flight{done: make(chan struct{})}
	c.flights[k] = fl
	c.mu.Unlock()

	// Ask for the whole fortnight once: later, shorter asks are served
	// from the cache.
	const ask = 16
	center := geo.Point{Lat: float64(k[0]) * cell, Lng: float64(k[1]) * cell}
	fl.f, fl.err = c.fetch(ctx, center, ask)
	c.mu.Lock()
	delete(c.flights, k)
	if fl.err == nil {
		c.cache[k] = entry{f: fl.f, at: c.clock.Now(), days: len(fl.f.Days)}
		if len(c.cache) > 50_000 {
			c.cache = map[[2]int64]entry{} // bounded; a full restart of the cache is cheap
		}
	}
	c.mu.Unlock()
	close(fl.done)
	if fl.err != nil {
		return Forecast{}, fl.err
	}
	return trim(fl.f, days), nil
}

func trim(f Forecast, days int) Forecast {
	if len(f.Days) > days {
		f.Days = f.Days[:days]
	}
	f.Days = append([]Day(nil), f.Days...)
	return f
}

var daily = []string{
	"temperature_2m_max", "temperature_2m_min", "precipitation_sum", "precipitation_probability_max",
	"wind_speed_10m_max", "wind_gusts_10m_max", "relative_humidity_2m_mean", "et0_fao_evapotranspiration",
}

type answer struct {
	Timezone string `json:"timezone"`
	Daily    struct {
		Time   []string   `json:"time"`
		TMax   []*float64 `json:"temperature_2m_max"`
		TMin   []*float64 `json:"temperature_2m_min"`
		Rain   []*float64 `json:"precipitation_sum"`
		Chance []*float64 `json:"precipitation_probability_max"`
		Wind   []*float64 `json:"wind_speed_10m_max"`
		Gust   []*float64 `json:"wind_gusts_10m_max"`
		Humid  []*float64 `json:"relative_humidity_2m_mean"`
		ET0    []*float64 `json:"et0_fao_evapotranspiration"`
	} `json:"daily"`
}

func (c *Client) fetch(ctx context.Context, p geo.Point, days int) (Forecast, error) {
	q := url.Values{}
	q.Set("latitude", strconv.FormatFloat(p.Lat, 'f', 4, 64))
	q.Set("longitude", strconv.FormatFloat(p.Lng, 'f', 4, 64))
	q.Set("daily", strings.Join(daily, ","))
	q.Set("timezone", "auto")
	q.Set("forecast_days", strconv.Itoa(days))
	var lastErr error
	for attempt := range 2 {
		if attempt > 0 {
			select {
			case <-c.clock.After(time.Second):
			case <-ctx.Done():
				return Forecast{}, ctx.Err()
			}
		}
		f, retry, err := c.once(ctx, c.url+"?"+q.Encode(), p)
		if err == nil {
			return f, nil
		}
		lastErr = err
		if !retry || ctx.Err() != nil {
			break
		}
	}
	return Forecast{}, lastErr
}

func (c *Client) once(ctx context.Context, u string, p geo.Point) (Forecast, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Forecast{}, false, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Forecast{}, false, ctx.Err()
		}
		return Forecast{}, true, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Forecast{}, true, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		wait := time.Hour
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			wait = time.Duration(s) * time.Second
		}
		c.mu.Lock()
		c.paused = c.clock.Now().Add(wait)
		c.mu.Unlock()
		return Forecast{}, false, fmt.Errorf("%w: Open-Meteo answered 429 (rate limit)", ErrUnavailable)
	case resp.StatusCode >= 500:
		return Forecast{}, true, fmt.Errorf("%w: Open-Meteo answered %s", ErrUnavailable, resp.Status)
	case resp.StatusCode != http.StatusOK:
		return Forecast{}, false, fmt.Errorf("%w: Open-Meteo answered %s: %s", ErrUnavailable, resp.Status, snippet(body))
	}
	var a answer
	if err := json.Unmarshal(body, &a); err != nil {
		return Forecast{}, false, fmt.Errorf("%w: reading Open-Meteo's answer: %w", ErrUnavailable, err)
	}
	f := Forecast{Point: p, Timezone: a.Timezone, Source: "Open-Meteo (best model for the place)", Fetched: c.clock.Now().UTC()}
	d := a.Daily
	for i, date := range d.Time {
		day := Day{Date: date}
		ok := true
		for _, v := range []struct {
			to  *float64
			col []*float64
			req bool
		}{
			{&day.TMaxC, d.TMax, true},
			{&day.TMinC, d.TMin, true},
			{&day.RainMM, d.Rain, true},
			{&day.RainChance, d.Chance, false},
			{&day.WindKmh, d.Wind, false},
			{&day.GustKmh, d.Gust, false},
			{&day.HumidityPct, d.Humid, false},
			{&day.ET0MM, d.ET0, false},
		} {
			if i < len(v.col) && v.col[i] != nil {
				*v.to = *v.col[i]
			} else if v.req {
				ok = false
			}
		}
		if !ok {
			break // the models' horizon: later days have no numbers
		}
		f.Days = append(f.Days, day)
	}
	if len(f.Days) == 0 {
		return Forecast{}, false, fmt.Errorf("%w: Open-Meteo's answer has no days", ErrUnavailable)
	}
	return f, false, nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

var _ Source = (*Client)(nil)

// Name implements Source.
func (c *Client) Name() string { return "Open-Meteo" }
