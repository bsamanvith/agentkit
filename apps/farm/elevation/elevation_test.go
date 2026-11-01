package elevation_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/elevation"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
)

func TestOpenMeteo(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		n := len(strings.Split(r.URL.Query().Get("latitude"), ","))
		hs := make([]string, n)
		for i := range hs {
			hs[i] = fmt.Sprint(10 + i)
		}
		fmt.Fprintf(w, `{"elevation":[%s]}`, strings.Join(hs, ","))
	}))
	defer srv.Close()
	c, err := elevation.OpenMeteo(srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	ps := make([]geo.Point, 150)
	for i := range ps {
		ps[i] = geo.Point{Lat: 16.7 + float64(i)*1e-4, Lng: 81.9}
	}
	hs, err := c.Elevations(context.Background(), ps)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || hs[0] != 10 || hs[99] != 109 || hs[100] != 10 || hs[149] != 59 {
		t.Errorf("%d calls, heights %v", calls.Load(), hs)
	}
	// Cached.
	if _, err := c.Elevations(context.Background(), ps[:5]); err != nil || calls.Load() != 2 {
		t.Errorf("cached: %v, %d calls", err, calls.Load())
	}
	if _, err := c.Elevations(context.Background(), []geo.Point{{Lat: 99, Lng: 0}}); err == nil {
		t.Error("an invalid point was sent")
	}
}

func TestOpenMeteoFailures(t *testing.T) {
	t.Parallel()
	for name, h := range map[string]http.HandlerFunc{
		"503":      func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) },
		"not json": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) },
		"too few":  func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"elevation":[1]}`)) },
	} {
		srv := httptest.NewServer(h)
		c, _ := elevation.OpenMeteo(srv.URL, nil)
		_, err := c.Elevations(context.Background(), []geo.Point{{Lat: 16.7, Lng: 81.9}, {Lat: 16.8, Lng: 81.9}})
		if !errors.Is(err, elevation.ErrUnavailable) {
			t.Errorf("%s: %v, want ErrUnavailable", name, err)
		}
		srv.Close()
	}
	if _, err := elevation.OpenMeteo("ftp://x", nil); err == nil {
		t.Error("ftp accepted")
	}
	f := elevation.Func(func(p geo.Point) float64 { return p.Lat })
	if hs, _ := f.Elevations(context.Background(), []geo.Point{{Lat: 3}}); hs[0] != 3 || f.Name() == "" {
		t.Error("Func")
	}
}

func TestOpenMeteoRetriesOnce(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		fail  []int // status of each call before it answers; 0 answers
		calls int32
		ok    bool
	}{
		"a 503 then an answer": {fail: []int{503}, calls: 2, ok: true},
		"a 429 then an answer": {fail: []int{429}, calls: 2, ok: true},
		"two 503s":             {fail: []int{503, 503}, calls: 2},
		"a 400 is not retried": {fail: []int{400}, calls: 1},
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
		c, _ := elevation.OpenMeteo(srv.URL, nil)
		hs, err := c.Elevations(context.Background(), []geo.Point{{Lat: 16.7, Lng: 81.9}})
		srv.Close()
		if (err == nil) != tc.ok || calls.Load() != tc.calls {
			t.Errorf("%s: %v %v after %d calls, want ok %v after %d", name, hs, err, calls.Load(), tc.ok, tc.calls)
		}
	}
}
