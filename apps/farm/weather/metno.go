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
	"github.com/bsamanvith/agentkit/apps/farm/sun"
	"github.com/bsamanvith/agentkit/pkg/clock"
)

// METNorwayURL is the Norwegian Meteorological Institute's free
// Locationforecast API (ECMWF's global model outside the Nordics): no key,
// but a User-Agent that names the app, and at most 20 requests a second.
const METNorwayURL = "https://api.met.no/weatherapi/locationforecast/2.0/complete"

// India is UTC+5:30, without the time zone database: the day boundaries
// MET Norway's hourly forecast is summed over (Open-Meteo works the zone
// out itself).
var India = time.FixedZone("IST", 5*3600+1800)

// GustFactor turns a sustained wind into a gust when the provider gives
// none: the WMO's factor over open land is 1.3 to 1.5.
const GustFactor = 1.4

// METNorway is a [Source] on MET Norway's Locationforecast. It sums the
// hourly (then six-hourly) forecast into days in its zone; reference
// evapotranspiration, which it does not give, is Hargreaves' (FAO-56 eq.
// 52) from the day's high and low. Forecasts are kept per 0.1° cell, like
// Open-Meteo's.
type METNorway struct {
	url, agent string
	zone       *time.Location
	http       *http.Client
	clock      clock.Clock
	ttl        time.Duration

	mu    sync.Mutex
	cache map[[2]int64]entry
}

// METOption configures [NewMETNorway].
type METOption func(*METNorway)

// WithZone sets the zone days are counted in (default India).
func WithZone(l *time.Location) METOption { return func(m *METNorway) { m.zone = l } }

// WithMETClient sets the HTTP client and clock.
func WithMETClient(hc *http.Client, k clock.Clock) METOption {
	return func(m *METNorway) {
		if hc != nil {
			m.http = hc
		}
		if k != nil {
			m.clock = k
		}
	}
}

// NewMETNorway returns a client of the API at u ([METNorwayURL] when
// empty), identifying itself as agent, as MET Norway's terms ask.
func NewMETNorway(u, agent string, opts ...METOption) (*METNorway, error) {
	if u == "" {
		u = METNorwayURL
	}
	if p, err := url.Parse(u); err != nil || (p.Scheme != "http" && p.Scheme != "https") {
		return nil, fmt.Errorf("weather: bad URL %q", u)
	}
	if strings.TrimSpace(agent) == "" {
		return nil, fmt.Errorf("weather: MET Norway needs a User-Agent naming the app")
	}
	m := &METNorway{
		url: u, agent: agent, zone: India, http: &http.Client{Timeout: 20 * time.Second}, clock: clock.Real(),
		ttl: 3 * time.Hour, cache: map[[2]int64]entry{},
	}
	for _, o := range opts {
		o(m)
	}
	return m, nil
}

// Name implements Source.
func (m *METNorway) Name() string { return "MET Norway" }

type metAnswer struct {
	Properties struct {
		Timeseries []struct {
			Time time.Time `json:"time"`
			Data struct {
				Instant struct {
					Details struct {
						Temp *float64 `json:"air_temperature"`
						RH   *float64 `json:"relative_humidity"`
						Wind *float64 `json:"wind_speed"`
						Gust *float64 `json:"wind_speed_of_gust"`
					} `json:"details"`
				} `json:"instant"`
				Next1 *metPeriod `json:"next_1_hours"`
				Next6 *metPeriod `json:"next_6_hours"`
			} `json:"data"`
		} `json:"timeseries"`
	} `json:"properties"`
}

type metPeriod struct {
	Details struct {
		Rain   *float64 `json:"precipitation_amount"`
		Max    *float64 `json:"air_temperature_max"`
		Min    *float64 `json:"air_temperature_min"`
		Chance *float64 `json:"probability_of_precipitation"`
	} `json:"details"`
}

// Forecast implements Source.
func (m *METNorway) Forecast(ctx context.Context, p geo.Point, days int) (Forecast, error) {
	if days < 1 || days > 16 {
		return Forecast{}, fmt.Errorf("weather: %d days: want 1 to 16", days)
	}
	k := key(p)
	m.mu.Lock()
	if e, ok := m.cache[k]; ok && m.clock.Now().Sub(e.at) < m.ttl {
		m.mu.Unlock()
		return trim(e.f, days), nil
	}
	m.mu.Unlock()
	center := geo.Point{Lat: float64(k[0]) * cell, Lng: float64(k[1]) * cell}
	q := url.Values{}
	q.Set("lat", strconv.FormatFloat(center.Lat, 'f', 4, 64))
	q.Set("lon", strconv.FormatFloat(center.Lng, 'f', 4, 64))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.url+"?"+q.Encode(), nil)
	if err != nil {
		return Forecast{}, err
	}
	req.Header.Set("User-Agent", m.agent)
	resp, err := m.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Forecast{}, ctx.Err()
		}
		return Forecast{}, fmt.Errorf("%w: MET Norway: %w", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return Forecast{}, fmt.Errorf("%w: reading MET Norway's answer: %w", ErrUnavailable, err)
	}
	if resp.StatusCode != http.StatusOK {
		return Forecast{}, fmt.Errorf("%w: MET Norway answered %s", ErrUnavailable, resp.Status)
	}
	var a metAnswer
	if err := json.Unmarshal(body, &a); err != nil {
		return Forecast{}, fmt.Errorf("%w: reading MET Norway's answer: %w", ErrUnavailable, err)
	}
	f := m.days(a, center)
	if len(f.Days) == 0 {
		return Forecast{}, fmt.Errorf("%w: MET Norway's answer has no whole days", ErrUnavailable)
	}
	m.mu.Lock()
	m.cache[k] = entry{f: f, at: m.clock.Now(), days: len(f.Days)}
	if len(m.cache) > 50_000 {
		m.cache = map[[2]int64]entry{}
	}
	m.mu.Unlock()
	return trim(f, days), nil
}

type metDay struct {
	date               string
	max, min           float64
	temps              int
	rain, chance, wind float64
	gust               float64
	rhSum              float64
	rhN                int
	gusty              bool
	end                time.Time // the day's end, in the zone
}

// days sums the series into days. Rain is counted once: from the hourly
// amounts while there are some, then the six-hourly ones. The last day
// is kept only if the series covers it to its end.
func (m *METNorway) days(a metAnswer, p geo.Point) Forecast {
	var out []*metDay
	var covered time.Time
	get := func(t time.Time) *metDay {
		date := t.In(m.zone).Format(DateLayout)
		if n := len(out); n > 0 && out[n-1].date == date {
			return out[n-1]
		}
		lt := t.In(m.zone)
		d := &metDay{
			date: date, max: math.Inf(-1), min: math.Inf(1),
			end: time.Date(lt.Year(), lt.Month(), lt.Day()+1, 0, 0, 0, 0, m.zone),
		}
		out = append(out, d)
		return d
	}
	for _, s := range a.Properties.Timeseries {
		d := get(s.Time)
		in := s.Data.Instant.Details
		if in.Temp != nil {
			d.max, d.min = math.Max(d.max, *in.Temp), math.Min(d.min, *in.Temp)
			d.temps++
		}
		if in.RH != nil {
			d.rhSum += *in.RH
			d.rhN++
		}
		if in.Wind != nil {
			d.wind = math.Max(d.wind, *in.Wind*3.6)
		}
		if in.Gust != nil {
			d.gust, d.gusty = math.Max(d.gust, *in.Gust*3.6), true
		}
		if s.Time.Before(covered) {
			continue
		}
		per, span := s.Data.Next1, time.Hour
		if per == nil || per.Details.Rain == nil {
			per, span = s.Data.Next6, 6*time.Hour
		}
		if per == nil || per.Details.Rain == nil {
			continue
		}
		d.rain += *per.Details.Rain
		if c := per.Details.Chance; c != nil {
			d.chance = math.Max(d.chance, *c)
		}
		if span == 6*time.Hour {
			if per.Details.Max != nil {
				d.max = math.Max(d.max, *per.Details.Max)
			}
			if per.Details.Min != nil {
				d.min = math.Min(d.min, *per.Details.Min)
			}
		}
		covered = s.Time.Add(span)
	}
	f := Forecast{Point: p, Timezone: m.zone.String(), Source: "MET Norway (ECMWF)", Fetched: m.clock.Now().UTC()}
	for i, d := range out {
		if d.temps == 0 || (i == len(out)-1 && covered.Before(d.end)) {
			continue
		}
		day := Day{
			Date: d.date, TMaxC: round1(d.max), TMinC: round1(d.min), RainMM: round1(d.rain), RainChance: d.chance,
			WindKmh: round1(d.wind), GustKmh: round1(d.gust),
		}
		if !d.gusty {
			day.GustKmh = round1(d.wind * GustFactor)
		}
		if d.rhN > 0 {
			day.HumidityPct = math.Round(d.rhSum / float64(d.rhN))
		}
		t, _ := time.Parse(DateLayout, d.date)
		day.ET0MM = round1(Hargreaves(p.Lat, t.YearDay(), day.TMaxC, day.TMinC))
		f.Days = append(f.Days, day)
	}
	return f
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// Hargreaves is FAO-56's reference evapotranspiration from temperature
// alone (eq. 52), mm a day, for a provider that does not give it.
func Hargreaves(lat float64, doy int, tmax, tmin float64) float64 {
	ra := sun.Extraterrestrial(lat, doy) * 0.408 // MJ/m² to mm of water
	return math.Max(0, 0.0023*((tmax+tmin)/2+17.8)*math.Sqrt(math.Max(0, tmax-tmin))*ra)
}

var _ Source = (*METNorway)(nil)
