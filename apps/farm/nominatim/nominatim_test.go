package nominatim_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/nominatim"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

// A reply recorded from the public server for 17.385, 78.4867 (trimmed).
const hyderabad = `{"place_id":1,"display_name":"Nampally, Hyderabad, Telangana, 500001, India",
"address":{"suburb":"Nampally","city":"Hyderabad","state_district":"Hyderabad","state":"Telangana",
"ISO3166-2-lvl4":"IN-TG","postcode":"500001","country":"India","country_code":"in"}}`

func server(t *testing.T, body string, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		if r.URL.Path != "/reverse" || r.URL.Query().Get("format") != "jsonv2" || r.Header.Get("User-Agent") != "farm-test/1" {
			t.Errorf("request %s, User-Agent %q", r.URL, r.Header.Get("User-Agent"))
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func client(t *testing.T, url string, opts ...nominatim.Option) *nominatim.Client {
	t.Helper()
	c, err := nominatim.New("farm-test/1", append([]nominatim.Option{nominatim.WithURL(url), nominatim.WithInterval(0)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLocate(t *testing.T) {
	t.Parallel()
	srv, n := server(t, hyderabad, http.StatusOK)
	c := client(t, srv.URL)
	got, err := c.Locate(context.Background(), geo.Point{Lat: 17.385, Lng: 78.4867})
	if err != nil {
		t.Fatal(err)
	}
	want := farm.Location{
		Village: "Nampally", District: "Hyderabad", State: "Telangana", Country: "India", CountryCode: "IN",
		DisplayName: "Nampally, Hyderabad, Telangana, 500001, India",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Locate (-want +got):\n%s", diff)
	}
	// A point in the same ~100 m cell is answered from the cache.
	if _, err := c.Locate(context.Background(), geo.Point{Lat: 17.3851, Lng: 78.4868}); err != nil || n.Load() != 1 {
		t.Errorf("second Locate: %v after %d requests, want 1", err, n.Load())
	}
}

func TestLocateNothingThere(t *testing.T) {
	t.Parallel()
	srv, _ := server(t, `{"error":"Unable to geocode"}`, http.StatusOK)
	if _, err := client(t, srv.URL).Locate(context.Background(), geo.Point{Lat: 15, Lng: 65}); !errors.Is(err, farm.ErrNoLocation) {
		t.Errorf("Locate in the sea: %v, want ErrNoLocation", err)
	}
}

func TestLocateServerError(t *testing.T) {
	t.Parallel()
	srv, n := server(t, `busy`, http.StatusServiceUnavailable)
	c := client(t, srv.URL)
	for range 2 {
		if _, err := c.Locate(context.Background(), geo.Point{Lat: 17, Lng: 78}); err == nil || errors.Is(err, farm.ErrNoLocation) {
			t.Errorf("Locate on a 503: %v, want an error other than ErrNoLocation", err)
		}
	}
	if n.Load() != 2 {
		t.Errorf("%d requests, want 2: failures are not cached", n.Load())
	}
}

func TestLocateKeepsTheInterval(t *testing.T) {
	t.Parallel()
	srv, n := server(t, hyderabad, http.StatusOK)
	clk := clocktest.NewFake(time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC))
	c := client(t, srv.URL, nominatim.WithInterval(time.Second), nominatim.WithClock(clk))
	ctx := context.Background()
	if _, err := c.Locate(ctx, geo.Point{Lat: 17, Lng: 78}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.Locate(ctx, geo.Point{Lat: 18, Lng: 79})
		done <- err
	}()
	if err := clk.WaitForTimers(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 1 {
		t.Fatalf("%d requests before the interval passed, want 1", n.Load())
	}
	clk.Advance(time.Second)
	if err := <-done; err != nil || n.Load() != 2 {
		t.Errorf("after the interval: %v, %d requests", err, n.Load())
	}
}

func TestNewNeedsAUserAgent(t *testing.T) {
	t.Parallel()
	if _, err := nominatim.New(" "); err == nil {
		t.Error("New without a User-Agent succeeded")
	}
}
