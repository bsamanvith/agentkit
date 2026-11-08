package elevation_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/elevation"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

var t0 = time.Date(2026, 11, 8, 6, 0, 0, 0, time.UTC)

// openMeteo answers each request with 10, 11, ... for its points.
func openMeteo(calls *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		n := len(strings.Split(r.URL.Query().Get("latitude"), ","))
		hs := make([]string, n)
		for i := range hs {
			hs[i] = fmt.Sprint(10 + i)
		}
		fmt.Fprintf(w, `{"elevation":[%s]}`, strings.Join(hs, ","))
	}
}

// openTopoData answers each request with 500, 501, ... for its points.
func openTopoData(calls *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		locs := strings.Split(r.URL.Query().Get("locations"), "|")
		rs := make([]string, len(locs))
		for i, l := range locs {
			lat, lng, _ := strings.Cut(l, ",")
			rs[i] = fmt.Sprintf(`{"dataset":"srtm30m","elevation":%d,"location":{"lat":%s,"lng":%s}}`, 500+i, lat, lng)
		}
		fmt.Fprintf(w, `{"results":[%s],"status":"OK"}`, strings.Join(rs, ","))
	}
}

func line(n int) []geo.Point {
	ps := make([]geo.Point, n)
	for i := range ps {
		ps[i] = geo.Point{Lat: 16.7 + float64(i)*1e-4, Lng: 81.9}
	}
	return ps
}

func TestOpenMeteo(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(openMeteo(&calls))
	defer srv.Close()
	c, err := elevation.OpenMeteo(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ps := line(150)
	h, err := c.Elevations(context.Background(), ps)
	if err != nil {
		t.Fatal(err)
	}
	hs := h.Metres
	if calls.Load() != 2 || hs[0] != 10 || hs[99] != 109 || hs[100] != 10 || hs[149] != 59 {
		t.Errorf("%d calls, heights %v", calls.Load(), hs)
	}
	if h.ResolutionM != 90 || !strings.Contains(h.From, "Open-Meteo") || h.From != c.Name() {
		t.Errorf("from %q at %v m", h.From, h.ResolutionM)
	}
	// Cached.
	if _, err := c.Elevations(context.Background(), ps[:5]); err != nil || calls.Load() != 2 {
		t.Errorf("cached: %v, %d calls", err, calls.Load())
	}
	if _, err := c.Elevations(context.Background(), []geo.Point{{Lat: 99, Lng: 0}}); err == nil {
		t.Error("an invalid point was sent")
	}
}

func TestFailures(t *testing.T) {
	t.Parallel()
	for name, h := range map[string]http.HandlerFunc{
		"503":      func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) },
		"not json": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) },
		"too few":  func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"elevation":[1]}`)) },
		"null": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"elevation":[1,2],"results":[{"elevation":1},{"elevation":null}],"status":"OK"}`))
		},
		"not ok": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"elevation":[1,2],"status":"INVALID_REQUEST","error":"bad"}`))
		},
	} {
		srv := httptest.NewServer(h)
		om, _ := elevation.OpenMeteo(srv.URL)
		otd, _ := elevation.OpenTopoData(srv.URL + "/v1/srtm30m")
		for _, c := range []*elevation.Client{om, otd} {
			_, err := c.Elevations(context.Background(), line(2))
			if (name == "null" || name == "not ok") && c == om {
				continue // a good answer to Open-Meteo
			}
			if !errors.Is(err, elevation.ErrUnavailable) {
				t.Errorf("%s, %s: %v, want ErrUnavailable", name, c.Name(), err)
			}
		}
		srv.Close()
	}
	for _, u := range []string{"ftp://x", "https://"} {
		if _, err := elevation.OpenMeteo(u); err == nil {
			t.Errorf("%s accepted", u)
		}
	}
	for _, u := range []string{"ftp://x/v1/srtm30m", "https://api.opentopodata.org/", "https://api.opentopodata.org"} {
		if _, err := elevation.OpenTopoData(u); err == nil {
			t.Errorf("%s accepted", u)
		}
	}
	f := elevation.Func(func(p geo.Point) float64 { return p.Lat })
	if h, _ := f.Elevations(context.Background(), []geo.Point{{Lat: 3}}); h.Metres[0] != 3 || h.From == "" || h.ResolutionM == 0 {
		t.Error("Func")
	}
}

func TestRetriesOnce(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		fail  []int // status of each call before it answers; 0 answers
		calls int32
		ok    bool
	}{
		"a 503 then an answer":  {fail: []int{503}, calls: 2, ok: true},
		"two 503s":              {fail: []int{503, 503}, calls: 2},
		"a 400 is not retried":  {fail: []int{400}, calls: 1},
		"a 429 is not retried":  {fail: []int{429}, calls: 1},
		"a 200 is asked once":   {calls: 1, ok: true},
		"a 502 then a 429 ends": {fail: []int{502, 429}, calls: 2},
	} {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			n := int(calls.Add(1)) - 1
			if n < len(tc.fail) {
				w.WriteHeader(tc.fail[n])
				return
			}
			_, _ = w.Write([]byte(`{"elevation":[7]}`))
		}))
		c, _ := elevation.OpenMeteo(srv.URL)
		h, err := c.Elevations(context.Background(), []geo.Point{{Lat: 16.7, Lng: 81.9}})
		srv.Close()
		if (err == nil) != tc.ok || calls.Load() != tc.calls {
			t.Errorf("%s: %v %v after %d calls, want ok %v after %d", name, h, err, calls.Load(), tc.ok, tc.calls)
		}
	}
}

func TestRateLimitPauses(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		header string
		pause  time.Duration
	}{
		"no Retry-After": {pause: elevation.RateLimitPause},
		"Retry-After":    {header: "120", pause: 2 * time.Minute},
	} {
		var calls, limited atomic.Int32
		limited.Store(1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if limited.Load() == 1 {
				calls.Add(1)
				if tc.header != "" {
					w.Header().Set("Retry-After", tc.header)
				}
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			openMeteo(&calls)(w, r)
		}))
		clk := clocktest.NewFake(t0)
		c, _ := elevation.OpenMeteo(srv.URL, elevation.WithClock(clk))
		ctx := context.Background()
		if _, err := c.Elevations(ctx, line(1)); !errors.Is(err, elevation.ErrRateLimited) || !errors.Is(err, elevation.ErrUnavailable) {
			t.Errorf("%s: %v, want ErrRateLimited", name, err)
		}
		limited.Store(0)
		clk.Advance(tc.pause - time.Second)
		if _, err := c.Elevations(ctx, line(1)); !errors.Is(err, elevation.ErrRateLimited) || calls.Load() != 1 {
			t.Errorf("%s: paused: %v after %d calls", name, err, calls.Load())
		}
		clk.Advance(time.Second)
		if _, err := c.Elevations(ctx, line(1)); err != nil || calls.Load() != 2 {
			t.Errorf("%s: after the pause: %v after %d calls", name, err, calls.Load())
		}
		srv.Close()
	}
}

func TestOpenTopoData(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	var path atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path.Store(r.URL.Path)
		openTopoData(&calls)(w, r)
	}))
	defer srv.Close()
	clk := clocktest.NewFake(t0)
	c, err := elevation.OpenTopoData(srv.URL+"/v1/srtm30m/", elevation.WithClock(clk))
	if err != nil {
		t.Fatal(err)
	}
	type res struct {
		h   elevation.Heights
		err error
	}
	done := make(chan res, 1)
	go func() {
		h, err := c.Elevations(context.Background(), line(150))
		done <- res{h, err}
	}()
	// The second batch waits a second after the first.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := clk.WaitForTimers(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Errorf("%d calls before the pause", calls.Load())
	}
	clk.Advance(elevation.OpenTopoDataInterval)
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	hs := r.h.Metres
	if calls.Load() != 2 || hs[0] != 500 || hs[99] != 599 || hs[100] != 500 || hs[149] != 549 {
		t.Errorf("%d calls, heights %v", calls.Load(), hs)
	}
	if r.h.ResolutionM != 30 || r.h.From != "SRTM 30 m via Open Topo Data" || path.Load() != "/v1/srtm30m" {
		t.Errorf("from %q at %v m, path %v", r.h.From, r.h.ResolutionM, path.Load())
	}
	other, _ := elevation.OpenTopoData(srv.URL + "/v1/eudem25m")
	if !strings.HasPrefix(other.Name(), "eudem25m") {
		t.Errorf("unknown dataset named %q", other.Name())
	}
}

func TestFallback(t *testing.T) {
	t.Parallel()
	var omCalls, otdCalls atomic.Int32
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		omCalls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer limited.Close()
	backup := httptest.NewServer(openTopoData(&otdCalls))
	defer backup.Close()
	om, _ := elevation.OpenMeteo(limited.URL)
	otd, _ := elevation.OpenTopoData(backup.URL + "/v1/srtm30m")
	src := elevation.Fallback(nil, om, otd)
	if !strings.Contains(src.Name(), "Open-Meteo, then SRTM") {
		t.Errorf("name %q", src.Name())
	}
	ctx := context.Background()
	h, err := src.Elevations(ctx, line(3))
	if err != nil || h.From != otd.Name() || h.ResolutionM != 30 || h.Metres[2] != 502 {
		t.Fatalf("%+v, %v", h, err)
	}
	// Open-Meteo is paused: the next survey goes straight to the backup,
	// and points already asked come from the cache.
	if _, err := src.Elevations(ctx, line(3)); err != nil || omCalls.Load() != 1 || otdCalls.Load() != 1 {
		t.Errorf("%v: %d and %d calls", err, omCalls.Load(), otdCalls.Load())
	}

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(400) }))
	defer down.Close()
	a, _ := elevation.OpenMeteo(down.URL)
	b, _ := elevation.OpenTopoData(down.URL + "/v1/srtm30m")
	_, err = elevation.Fallback(nil, a, b).Elevations(ctx, line(1))
	if !errors.Is(err, elevation.ErrUnavailable) || !strings.Contains(err.Error(), "Open-Meteo") || !strings.Contains(err.Error(), "Open Topo Data") {
		t.Errorf("both down: %v", err)
	}

	if elevation.Fallback(nil, om) != elevation.Source(om) {
		t.Error("a fallback of one source is not that source")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	var asked atomic.Int32
	last := httptest.NewServer(openMeteo(&asked))
	defer last.Close()
	l, _ := elevation.OpenMeteo(last.URL)
	if _, err := elevation.Fallback(nil, a, l).Elevations(cctx, line(1)); err == nil || asked.Load() != 0 {
		t.Errorf("cancelled: %v, %d calls to the next source", err, asked.Load())
	}
}

func TestCacheFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "heights.json")
	var omCalls, otdCalls atomic.Int32
	om := httptest.NewServer(openMeteo(&omCalls))
	defer om.Close()
	otd := httptest.NewServer(openTopoData(&otdCalls))
	defer otd.Close()
	ctx := context.Background()

	cache, err := elevation.OpenCache(path)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := elevation.OpenMeteo(om.URL, elevation.WithCache(cache))
	b, _ := elevation.OpenTopoData(otd.URL+"/v1/srtm30m", elevation.WithCache(cache))
	if _, err := a.Elevations(ctx, line(4)); err != nil {
		t.Fatal(err)
	}
	// One cache, two services: their heights are kept apart.
	if h, err := b.Elevations(ctx, line(4)); err != nil || h.Metres[0] != 500 || otdCalls.Load() != 1 {
		t.Errorf("%v %v, %d calls", h, err, otdCalls.Load())
	}
	if cache.Len() != 8 {
		t.Errorf("%d cached", cache.Len())
	}

	// After a restart the file answers.
	again, err := elevation.OpenCache(path)
	if err != nil || again.Len() != 8 {
		t.Fatalf("reopened: %v, %d", err, again.Len())
	}
	a2, _ := elevation.OpenMeteo(om.URL, elevation.WithCache(again))
	if h, err := a2.Elevations(ctx, line(4)); err != nil || h.Metres[3] != 13 || omCalls.Load() != 1 {
		t.Errorf("%v %v, %d calls", h, err, omCalls.Load())
	}

	// A broken file starts empty and is written over.
	if err := os.WriteFile(path, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	broken, err := elevation.OpenCache(path)
	if err != nil || broken.Len() != 0 {
		t.Errorf("broken: %v, %d", err, broken.Len())
	}
	if _, err := elevation.OpenCache(""); err == nil {
		t.Error("a cache file with no name")
	}
	if _, err := elevation.OpenCache(t.TempDir()); err == nil {
		t.Error("a directory read as a cache")
	}
}
