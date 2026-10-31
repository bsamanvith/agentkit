// Package nasapower gives the climate normals at any point on Earth from
// NASA POWER's climatology API (https://power.larc.nasa.gov): an
// [agro.ClimateSource].
//
// POWER is free, needs no key and covers the whole globe on a grid of about
// half a degree, from satellite and reanalysis data (MERRA-2) over the last
// twenty-odd years. The client asks for the agroclimatology community's
// monthly temperature (mean, high, low), rain and humidity, and caches the
// answer per grid cell: every plot of a village shares one.
package nasapower

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

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
)

// DefaultURL is the public API.
const DefaultURL = "https://power.larc.nasa.gov"

// Client is an agro.ClimateSource on POWER. It is safe for concurrent use.
type Client struct {
	base string
	http *http.Client

	mu    sync.Mutex
	cache map[[2]int64]agro.Climate
}

var _ agro.ClimateSource = (*Client)(nil)

// Option configures [New].
type Option func(*Client)

// WithURL sets the server (default [DefaultURL]).
func WithURL(u string) Option { return func(c *Client) { c.base = strings.TrimRight(u, "/") } }

// WithHTTPClient sets the HTTP client (default one with a 30s timeout:
// the API takes several seconds for a point).
func WithHTTPClient(hc *http.Client) Option { return func(c *Client) { c.http = hc } }

// New returns a client.
func New(opts ...Option) (*Client, error) {
	c := &Client{base: DefaultURL, http: &http.Client{Timeout: 30 * time.Second}, cache: map[[2]int64]agro.Climate{}}
	for _, o := range opts {
		o(c)
	}
	if _, err := url.Parse(c.base); err != nil {
		return nil, fmt.Errorf("nasapower: URL: %w", err)
	}
	return c, nil
}

// cell is the POWER grid cell of p: half a degree of latitude by five
// eighths of longitude.
func cell(p geo.Point) [2]int64 {
	return [2]int64{int64(math.Floor(p.Lat / 0.5)), int64(math.Floor(p.Lng / 0.625))}
}

var months = [12]string{"JAN", "FEB", "MAR", "APR", "MAY", "JUN", "JUL", "AUG", "SEP", "OCT", "NOV", "DEC"}

// daysIn is the days of each month in a year that is not a leap year, to
// turn POWER's mm per day into a month's total.
var daysIn = [12]float64{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}

// Climate implements agro.ClimateSource. A failure to reach the API, or
// an answer with gaps, matches agro.ErrNoClimate.
func (c *Client) Climate(ctx context.Context, p geo.Point) (agro.Climate, error) {
	if !p.Valid() {
		return agro.Climate{}, fmt.Errorf("%w: point %v", agro.ErrInvalid, p)
	}
	key := cell(p)
	c.mu.Lock()
	got, ok := c.cache[key]
	c.mu.Unlock()
	if ok {
		got.Point = p
		return got, nil
	}
	cl, err := c.fetch(ctx, p)
	if err != nil {
		return agro.Climate{}, fmt.Errorf("%w: nasapower: %w", agro.ErrNoClimate, err)
	}
	c.mu.Lock()
	c.cache[key] = cl
	c.mu.Unlock()
	return cl, nil
}

func (c *Client) fetch(ctx context.Context, p geo.Point) (agro.Climate, error) {
	q := url.Values{}
	q.Set("parameters", "T2M,T2M_MAX,T2M_MIN,PRECTOTCORR,RH2M")
	q.Set("community", "AG")
	q.Set("latitude", strconv.FormatFloat(p.Lat, 'f', 4, 64))
	q.Set("longitude", strconv.FormatFloat(p.Lng, 'f', 4, 64))
	q.Set("format", "JSON")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/temporal/climatology/point?"+q.Encode(), nil)
	if err != nil {
		return agro.Climate{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return agro.Climate{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return agro.Climate{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var body struct {
		Properties struct {
			Parameter map[string]map[string]float64 `json:"parameter"`
		} `json:"properties"`
		Header struct {
			Range     string  `json:"range"`
			FillValue float64 `json:"fill_value"`
		} `json:"header"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return agro.Climate{}, fmt.Errorf("decode: %w", err)
	}
	par := body.Properties.Parameter
	value := func(name string, m int) (float64, error) {
		v, ok := par[name][months[m]]
		if !ok || v == body.Header.FillValue {
			return 0, fmt.Errorf("no %s for %s", name, months[m])
		}
		return v, nil
	}
	cl := agro.Climate{Point: p, Source: "NASA POWER climatology"}
	if body.Header.Range != "" {
		cl.Source += ", " + body.Header.Range
	}
	var errs []error
	for m := range 12 {
		var err [5]error
		mo := &cl.Months[m]
		mo.Temp, err[0] = value("T2M", m)
		mo.TempMax, err[1] = value("T2M_MAX", m)
		mo.TempMin, err[2] = value("T2M_MIN", m)
		var rain float64
		rain, err[3] = value("PRECTOTCORR", m)
		mo.RainMM = math.Round(rain*daysIn[m]*10) / 10
		mo.Humidity, err[4] = value("RH2M", m)
		errs = append(errs, err[:]...)
	}
	if err := errors.Join(errs...); err != nil {
		return agro.Climate{}, err
	}
	if err := cl.Check(); err != nil {
		return agro.Climate{}, err
	}
	return cl, nil
}
