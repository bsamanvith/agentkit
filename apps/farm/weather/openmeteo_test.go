package weather_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/weather"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

const answer = `{"latitude":17.4,"longitude":78.5,"timezone":"Asia/Kolkata","daily":{
"time":["2026-11-09","2026-11-10","2026-11-11"],
"temperature_2m_max":[30.2,29.1,null],"temperature_2m_min":[21.4,20.9,null],
"precipitation_sum":[0,72.5,null],"precipitation_probability_max":[0,95,null],
"wind_speed_10m_max":[12.6,30.1,null],"wind_gusts_10m_max":[34.2,61,null],
"relative_humidity_2m_mean":[44,92,null],"et0_fao_evapotranspiration":[5.3,1.2,null]}}`

func server(t *testing.T, handle func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		handle(w, r)
	}))
	t.Cleanup(s.Close)
	return s, &n
}

// TestOpenMeteo: the answer is read day by day, the days past the models'
// horizon (nulls) are dropped, and plots in one grid cell share a call.
func TestOpenMeteo(t *testing.T) {
	t.Parallel()
	var query string
	srv, calls := server(t, func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		_, _ = fmt.Fprint(w, answer)
	})
	c, err := weather.OpenMeteo(srv.URL, weather.WithClock(clocktest.NewFake(time.Date(2026, 11, 9, 1, 0, 0, 0, time.UTC))))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	f, err := c.Forecast(ctx, geo.Point{Lat: 17.41, Lng: 78.52}, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Days) != 2 || f.Timezone != "Asia/Kolkata" {
		t.Fatalf("forecast %+v: want two days", f)
	}
	want := weather.Day{Date: "2026-11-10", TMaxC: 29.1, TMinC: 20.9, RainMM: 72.5, RainChance: 95, WindKmh: 30.1, GustKmh: 61, HumidityPct: 92, ET0MM: 1.2}
	if f.Days[1] != want {
		t.Errorf("day 2 = %+v, want %+v", f.Days[1], want)
	}
	for _, p := range []string{"latitude=17.4000", "longitude=78.5000", "forecast_days=16", "timezone=auto", "et0_fao_evapotranspiration"} {
		if !contains(query, p) {
			t.Errorf("query %q lacks %q", query, p)
		}
	}
	// A neighbour 2 km away, and a shorter ask: the cache.
	if f, err := c.Forecast(ctx, geo.Point{Lat: 17.39, Lng: 78.49}, 1); err != nil || len(f.Days) != 1 {
		t.Errorf("neighbour: %+v, %v", f, err)
	}
	if calls.Load() != 1 {
		t.Errorf("%d calls for one grid cell, want 1", calls.Load())
	}
	// Many plots of a village at once: one call.
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() { _, _ = c.Forecast(ctx, geo.Point{Lat: 16.0 + float64(i)*0.001, Lng: 80}, 7) })
	}
	wg.Wait()
	if calls.Load() != 2 {
		t.Errorf("%d calls after 20 plots of one village, want 2", calls.Load())
	}
	if _, err := c.Forecast(ctx, geo.Point{Lat: 95}, 7); err == nil {
		t.Error("a point off the globe passed")
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// TestRateLimit: a 429 pauses the client for Retry-After without asking
// again; after it, it asks.
func TestRateLimit(t *testing.T) {
	t.Parallel()
	limited := true
	srv, calls := server(t, func(w http.ResponseWriter, _ *http.Request) {
		if limited {
			w.Header().Set("Retry-After", "600")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = fmt.Fprint(w, answer)
	})
	clk := clocktest.NewFake(time.Date(2026, 11, 9, 1, 0, 0, 0, time.UTC))
	c, _ := weather.OpenMeteo(srv.URL, weather.WithClock(clk))
	ctx := context.Background()
	for range 3 {
		if _, err := c.Forecast(ctx, geo.Point{Lat: 17, Lng: 78}, 7); !errors.Is(err, weather.ErrUnavailable) {
			t.Fatalf("err %v, want ErrUnavailable", err)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("%d calls while paused, want 1", calls.Load())
	}
	limited = false
	clk.Advance(11 * time.Minute)
	if _, err := c.Forecast(ctx, geo.Point{Lat: 17, Lng: 78}, 7); err != nil {
		t.Errorf("after the pause: %v", err)
	}
}

// TestRetriesOnce: a 5xx is tried once more; a 4xx is not.
func TestRetriesOnce(t *testing.T) {
	t.Parallel()
	status := http.StatusBadGateway
	srv, calls := server(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
	clk := clocktest.NewFake(time.Time{})
	c, _ := weather.OpenMeteo(srv.URL, weather.WithClock(clk))
	ctx := context.Background()
	done := make(chan error)
	go func() { _, err := c.Forecast(ctx, geo.Point{Lat: 17, Lng: 78}, 7); done <- err }()
	if err := clk.WaitForTimers(ctx, 1); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Second)
	if err := <-done; !errors.Is(err, weather.ErrUnavailable) || calls.Load() != 2 {
		t.Errorf("5xx: %v after %d calls, want ErrUnavailable after 2", err, calls.Load())
	}
	status = http.StatusBadRequest
	if _, err := c.Forecast(ctx, geo.Point{Lat: 18, Lng: 78}, 7); !errors.Is(err, weather.ErrUnavailable) || calls.Load() != 3 {
		t.Errorf("4xx: %v after %d calls, want one more call", err, calls.Load())
	}
}

func TestSum(t *testing.T) {
	t.Parallel()
	got := weather.Sum([]weather.Day{{TMaxC: 30, TMinC: 20, RainMM: 2, ET0MM: 4, GustKmh: 20}, {TMaxC: 33, TMinC: 18, RainMM: 12, ET0MM: 3, GustKmh: 50}})
	want := weather.Totals{RainMM: 14, ET0MM: 7, MaxC: 33, MinC: 18, GustKmh: 50, RainDays: 1}
	if got != want {
		t.Errorf("Sum = %+v, want %+v", got, want)
	}
}
