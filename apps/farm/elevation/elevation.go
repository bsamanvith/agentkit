// Package elevation looks up the height of the ground at points, for the
// slope of a plot: which corner is lowest (where water collects, where a
// farm pond goes) and how much the land falls.
//
// [OpenMeteo] asks Open-Meteo's elevation API
// (https://open-meteo.com/en/docs/elevation-api), which reads the
// Copernicus DEM GLO-90: heights on a 90 m grid, to about a metre or two.
// A small plot can sit inside one or two cells, so a fall of less than a
// couple of metres is "flat at this resolution", not a measured slope.
package elevation

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

	"github.com/bsamanvith/agentkit/apps/farm/geo"
)

// DefaultURL is Open-Meteo's elevation API.
const DefaultURL = "https://api.open-meteo.com/v1/elevation"

// MaxPoints is the most points one request asks for (the API's limit).
const MaxPoints = 100

// ResolutionM is the grid of the elevation model, in metres.
const ResolutionM = 90

// ErrUnavailable is a lookup the server could not answer.
var ErrUnavailable = errors.New("elevation: the server could not answer")

// Source gives the ground's height in metres above sea level at points.
// Implementations are safe for concurrent use.
type Source interface {
	Elevations(ctx context.Context, ps []geo.Point) ([]float64, error)
	// Name says where the heights come from, for the farmer.
	Name() string
}

// Func is a Source from a function, for tests and offline use.
type Func func(geo.Point) float64

// Elevations implements Source.
func (f Func) Elevations(_ context.Context, ps []geo.Point) ([]float64, error) {
	out := make([]float64, len(ps))
	for i, p := range ps {
		out[i] = f(p)
	}
	return out, nil
}

// Name implements Source.
func (Func) Name() string { return "fixed heights" }

// Client is a Source on Open-Meteo. It caches heights per point to about a
// metre.
type Client struct {
	base string
	http *http.Client
	mu   sync.Mutex
	seen map[[2]int64]float64
}

// OpenMeteo returns a client of the elevation API at base (such as
// [DefaultURL]); hc nil is a client with a 30 s timeout.
func OpenMeteo(base string, hc *http.Client) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("elevation: %q is not an http(s) URL", base)
	}
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{base: base, http: hc, seen: map[[2]int64]float64{}}, nil
}

// RetryAfter is the pause before the one retry of a failed request
// (a network error, 429 or 5xx).
const RetryAfter = 300 * time.Millisecond

var errRetry = errors.New("worth retrying")

// Name implements Source.
func (c *Client) Name() string { return "Copernicus DEM GLO-90 via Open-Meteo" }

func key(p geo.Point) [2]int64 {
	return [2]int64{int64(math.Round(p.Lat * 1e5)), int64(math.Round(p.Lng * 1e5))}
}

// Elevations implements Source.
func (c *Client) Elevations(ctx context.Context, ps []geo.Point) ([]float64, error) {
	out := make([]float64, len(ps))
	var need []int
	c.mu.Lock()
	for i, p := range ps {
		if h, ok := c.seen[key(p)]; ok {
			out[i] = h
		} else {
			need = append(need, i)
		}
	}
	c.mu.Unlock()
	for len(need) > 0 {
		batch := need[:min(len(need), MaxPoints)]
		need = need[len(batch):]
		lat, lng := make([]string, len(batch)), make([]string, len(batch))
		for j, i := range batch {
			if !ps[i].Valid() {
				return nil, fmt.Errorf("elevation: point %d is not a position", i)
			}
			lat[j] = strconv.FormatFloat(ps[i].Lat, 'f', 6, 64)
			lng[j] = strconv.FormatFloat(ps[i].Lng, 'f', 6, 64)
		}
		hs, err := c.fetch(ctx, strings.Join(lat, ","), strings.Join(lng, ","))
		if errors.Is(err, errRetry) {
			// Once more after a pause: the service, or the way to it, is
			// sometimes slow to shake hands.
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(RetryAfter):
			}
			hs, err = c.fetch(ctx, strings.Join(lat, ","), strings.Join(lng, ","))
		}
		if err != nil {
			return nil, err
		}
		if len(hs) != len(batch) {
			return nil, fmt.Errorf("%w: %d heights for %d points", ErrUnavailable, len(hs), len(batch))
		}
		c.mu.Lock()
		for j, i := range batch {
			out[i] = hs[j]
			c.seen[key(ps[i])] = hs[j]
		}
		c.mu.Unlock()
	}
	return out, nil
}

func (c *Client) fetch(ctx context.Context, lat, lng string) ([]float64, error) {
	q := url.Values{"latitude": {lat}, "longitude": {lng}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"?"+q.Encode(), nil)
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
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, fmt.Errorf("%w: %w: HTTP %d", ErrUnavailable, errRetry, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: HTTP %d", ErrUnavailable, resp.StatusCode)
	}
	var body struct {
		Elevation []float64 `json:"elevation"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return body.Elevation, nil
}
