package nasapower_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/nasapower"
)

// server serves body (a recorded answer) and counts the requests.
func server(t *testing.T, status int, body string) (*nasapower.Client, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		if r.URL.Path != "/api/temporal/climatology/point" || r.URL.Query().Get("community") != "AG" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c, err := nasapower.New(nasapower.WithURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	return c, &n
}

func recorded(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/hyderabad.json")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var farooqnagar = geo.Point{Lat: 17.0716, Lng: 78.204}

func TestClimate(t *testing.T) {
	t.Parallel()
	c, _ := server(t, http.StatusOK, recorded(t))
	cl, err := c.Climate(context.Background(), farooqnagar)
	if err != nil {
		t.Fatal(err)
	}
	// The recorded answer: May is the hottest month, July's rain is
	// 4.9 mm a day.
	if got := cl.Months[4].Temp; got != 32.63 {
		t.Errorf("May mean %v, want 32.63", got)
	}
	if got := cl.Months[6].RainMM; got != 151.9 {
		t.Errorf("July rain %v mm, want 4.9 × 31 = 151.9", got)
	}
	if total := cl.RainMM(); total < 700 || total > 800 {
		t.Errorf("year's rain %v mm, want about 770", total)
	}
	for i, m := range cl.Months {
		if m.TempMin > m.Temp || m.Temp > m.TempMax {
			t.Errorf("month %d: low %v, mean %v, high %v out of order", i+1, m.TempMin, m.Temp, m.TempMax)
		}
	}
	if cl.Point != farooqnagar || cl.Source == "" {
		t.Errorf("point %v, source %q", cl.Point, cl.Source)
	}
}

func TestCachesPerGridCell(t *testing.T) {
	t.Parallel()
	c, n := server(t, http.StatusOK, recorded(t))
	ctx := context.Background()
	for _, p := range []geo.Point{farooqnagar, {Lat: 17.08, Lng: 78.21}, farooqnagar} {
		if _, err := c.Climate(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if got := n.Load(); got != 1 {
		t.Errorf("%d requests for one cell, want 1", got)
	}
	if _, err := c.Climate(ctx, geo.Point{Lat: 18.5, Lng: 78.2}); err != nil {
		t.Fatal(err)
	}
	if got := n.Load(); got != 2 {
		t.Errorf("%d requests for two cells, want 2", got)
	}
}

func TestFailures(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"down":       {http.StatusServiceUnavailable, ""},
		"not json":   {http.StatusOK, "<html>"},
		"fill value": {http.StatusOK, strings.Replace(recorded(t), `"T2M":{"JAN":21.9`, `"T2M":{"JAN":-999.0`, 1)},
		"gap":        {http.StatusOK, strings.Replace(recorded(t), `"RH2M":{"JAN":51.83,`, `"RH2M":{`, 1)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c, n := server(t, tc.status, tc.body)
			for range 2 {
				if _, err := c.Climate(context.Background(), farooqnagar); !errors.Is(err, agro.ErrNoClimate) {
					t.Fatalf("err %v, want ErrNoClimate", err)
				}
			}
			if n.Load() != 2 {
				t.Errorf("a failure was cached: %d requests", n.Load())
			}
		})
	}
}

func TestRefusesABadPoint(t *testing.T) {
	t.Parallel()
	c, n := server(t, http.StatusOK, recorded(t))
	if _, err := c.Climate(context.Background(), geo.Point{Lat: 95, Lng: 0}); !errors.Is(err, agro.ErrInvalid) {
		t.Errorf("err %v, want ErrInvalid", err)
	}
	if n.Load() != 0 {
		t.Error("asked the API for a bad point")
	}
}
