package weather_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/weather"
	"github.com/bsamanvith/agentkit/apps/farm/weather/weathertest"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

// metSeries is hourly from 18:30 UTC on 7 Nov (midnight in India) for 36
// hours, then six-hourly to 18:30 UTC on 9 Nov: two whole Indian days, 8
// and 9 November. 1 mm of rain an hour on the 8th, 6 mm per six hours on
// the 9th; 22 to 32 °C; wind 5 m/s, 10 m/s at noon on the 9th.
func metSeries() string {
	var b strings.Builder
	b.WriteString(`{"properties":{"timeseries":[`)
	start := time.Date(2026, 11, 7, 18, 30, 0, 0, time.UTC)
	first := true
	add := func(t time.Time, temp, wind float64, period string) {
		if !first {
			b.WriteString(",")
		}
		first = false
		fmt.Fprintf(&b, `{"time":%q,"data":{"instant":{"details":{"air_temperature":%g,"relative_humidity":80,"wind_speed":%g}}%s}}`,
			t.Format(time.RFC3339), temp, wind, period)
	}
	for h := range 36 {
		t := start.Add(time.Duration(h) * time.Hour)
		temp := 22 + 10*math.Sin(math.Pi*float64(t.In(weather.India).Hour())/24)
		add(t, math.Round(temp), 5, `,"next_1_hours":{"details":{"precipitation_amount":1}},"next_6_hours":{"details":{"precipitation_amount":6}}`)
	}
	for h := 36; h <= 48; h += 6 {
		t := start.Add(time.Duration(h) * time.Hour)
		wind := 5.0
		if h == 42 {
			wind = 10
		}
		add(t, 30, wind, `,"next_6_hours":{"details":{"precipitation_amount":6,"air_temperature_max":33,"air_temperature_min":21}}`)
	}
	b.WriteString("]}}")
	return b.String()
}

func TestMETNorway(t *testing.T) {
	t.Parallel()
	var agent string
	srv, calls := server(t, func(w http.ResponseWriter, r *http.Request) {
		agent = r.Header.Get("User-Agent")
		_, _ = fmt.Fprint(w, metSeries())
	})
	if _, err := weather.NewMETNorway(srv.URL, ""); err == nil {
		t.Error("no User-Agent passed")
	}
	m, err := weather.NewMETNorway(srv.URL, "farm-test/1", weather.WithMETClient(nil, clocktest.NewFake(time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC))))
	if err != nil {
		t.Fatal(err)
	}
	f, err := m.Forecast(context.Background(), geo.Point{Lat: 8.73, Lng: 77.70}, 7)
	if err != nil {
		t.Fatal(err)
	}
	if agent != "farm-test/1" || calls.Load() != 1 {
		t.Errorf("agent %q, %d calls", agent, calls.Load())
	}
	// The series ends at midnight starting the 10th: two whole days.
	if len(f.Days) != 2 || f.Days[0].Date != "2026-11-08" || f.Days[1].Date != "2026-11-09" {
		t.Fatalf("days %+v", f.Days)
	}
	d8, d9 := f.Days[0], f.Days[1]
	if d8.RainMM != 24 || d8.TMaxC != 32 || d8.TMinC != 22 || d8.HumidityPct != 80 || d8.GustKmh != 25.2 {
		t.Errorf("8 Nov %+v: want 24 mm (hourly), 22 to 32 °C, gusts 18 km/h x 1.4", d8)
	}
	// The 9th: 12 hourly mm, then two six-hourly periods (12 mm): 24.
	if d9.RainMM != 24 || d9.TMaxC != 33 || d9.TMinC != 21 || d9.WindKmh != 36 {
		t.Errorf("9 Nov %+v", d9)
	}
	if d8.ET0MM < 3 || d8.ET0MM > 6 {
		t.Errorf("ET0 %v mm, want a tropical November's 3 to 6", d8.ET0MM)
	}
	if _, err := m.Forecast(context.Background(), geo.Point{Lat: 8.74, Lng: 77.71}, 1); err != nil || calls.Load() != 1 {
		t.Errorf("a neighbour asked again: %v, %d calls", err, calls.Load())
	}
}

// TestFallback: the first provider that answers; when all fail, an error
// that names each and matches ErrUnavailable.
func TestFallback(t *testing.T) {
	t.Parallel()
	down := weathertest.New()
	down.Set(errors.New("connection refused"))
	up := weathertest.New(weathertest.Week("2026-11-08")...)
	fb := weather.Fallback(nil, down, up)
	if fb.Name() != "fake, then fake" {
		t.Errorf("name %q", fb.Name())
	}
	f, err := fb.Forecast(context.Background(), geo.Point{Lat: 17, Lng: 78}, 7)
	if err != nil || len(f.Days) != 7 || down.Calls() != 1 || up.Calls() != 1 {
		t.Errorf("%v, %d days; calls %d, %d", err, len(f.Days), down.Calls(), up.Calls())
	}
	up.Set(errors.New("429"))
	if _, err := fb.Forecast(context.Background(), geo.Point{Lat: 17, Lng: 78}, 7); !errors.Is(err, weather.ErrUnavailable) || !strings.Contains(err.Error(), "connection refused") || !strings.Contains(err.Error(), "429") {
		t.Errorf("both down: %v", err)
	}
	if weather.Fallback(nil, up) != weather.Source(up) {
		t.Error("one source is not used as it is")
	}
}

func TestHargreaves(t *testing.T) {
	t.Parallel()
	// A mid-latitude May day of FAO-56: about 3.5 mm.
	if v := weather.Hargreaves(-22.9, 135, 26.6, 14.8); math.Abs(v-3.5) > 0.6 {
		t.Errorf("Hargreaves = %.2f", v)
	}
}
