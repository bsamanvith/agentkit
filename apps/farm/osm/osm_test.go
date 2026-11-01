package osm_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/osm"
	"github.com/bsamanvith/agentkit/apps/farm/osm/osmtest"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

const ua = "agentkit-farm-test/0 (+https://github.com/bsamanvith/agentkit)"

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func count(fs []osm.Feature) map[osm.Kind]int {
	out := map[osm.Kind]int{}
	for _, f := range fs {
		out[f.Kind]++
	}
	return out
}

// The map call's answer for a box of paddy and coconut land by Kothapeta,
// in the Godavari delta (recorded 2026-11-01, © OpenStreetMap
// contributors).
func TestParseMapCall(t *testing.T) {
	t.Parallel()
	fs, err := osm.Parse(osmtest.KothapetaMap)
	if err != nil {
		t.Fatal(err)
	}
	n := count(fs)
	for _, k := range []osm.Kind{osm.Road, osm.Canal, osm.Drain} {
		if n[k] == 0 {
			t.Errorf("no %s in %v", k, n)
		}
	}
	var nh bool
	for _, f := range fs {
		if len(f.Points) == 0 || !f.Points[0].Valid() || !strings.Contains(f.ID, "/") {
			t.Errorf("feature without geometry or id: %+v", f)
		}
		if f.Ref == "NH216E" {
			nh = true
			if f.Kind != osm.Road || f.Class != "trunk" || f.Name == "" {
				t.Errorf("NH216E: %+v", f)
			}
		}
		if f.Closed && f.Kind == osm.Road {
			t.Errorf("a road is an area: %+v", f)
		}
	}
	if !nh {
		t.Error("the national highway NH216E is missing")
	}
}

func TestParseOverpass(t *testing.T) {
	t.Parallel()
	fs, err := osm.Parse(read(t, "overpass-geom.json"))
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]osm.Feature{}
	for _, f := range fs {
		byID[f.ID] = f
	}
	if len(fs) != 6 {
		t.Errorf("%d features, want 6 (well, village, road, building, service loop, tank): %+v", len(fs), fs)
	}
	if f := byID["node/1"]; f.Kind != osm.Well || len(f.Points) != 1 {
		t.Errorf("well: %+v", f)
	}
	if f := byID["node/3"]; f.Kind != osm.Settlement || f.Name != "Mukkamala" {
		t.Errorf("village: %+v", f)
	}
	if f := byID["way/10"]; f.Kind != osm.Road || f.Class != "tertiary" || f.Ref != "MDR1" || f.Closed {
		t.Errorf("road: %+v", f)
	}
	if f := byID["way/11"]; f.Kind != osm.Building || !f.Closed || len(f.Points) != 4 {
		t.Errorf("building (closed, closing point dropped): %+v", f)
	}
	if f := byID["way/12"]; f.Closed {
		t.Errorf("a road loop is not an area: %+v", f)
	}
	if f := byID["relation/20"]; f.Kind != osm.Water || f.Class != "pond" || !f.Closed || len(f.Points) != 4 || f.Name != "Village tank" {
		t.Errorf("tank (outer way only): %+v", f)
	}
	if _, err := osm.Parse(read(t, "overpass-timeout.json")); !errors.Is(err, osm.ErrUnavailable) {
		t.Errorf("a timed-out query: %v, want ErrUnavailable", err)
	}
	if _, err := osm.Parse([]byte("<html>busy</html>")); !errors.Is(err, osm.ErrUnavailable) {
		t.Errorf("not JSON: %v", err)
	}
}

func TestClassify(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		tags  map[string]string
		kind  osm.Kind
		class string
	}{
		{map[string]string{"highway": "track"}, osm.Track, "track"},
		{map[string]string{"highway": "footway"}, osm.Path, "footway"},
		{map[string]string{"waterway": "canal", "area": "yes"}, osm.Canal, "canal"},
		{map[string]string{"natural": "water", "water": "canal"}, osm.Canal, "canal"},
		{map[string]string{"natural": "water"}, osm.Water, "water"},
		{map[string]string{"landuse": "reservoir"}, osm.Water, "reservoir"},
		{map[string]string{"building": "house"}, osm.Building, "house"},
		{map[string]string{"power": "line"}, osm.Power, "line"},
		{map[string]string{"amenity": "place_of_worship"}, osm.Facility, "place_of_worship"},
		{map[string]string{"landuse": "orchard"}, osm.Farmland, "orchard"},
		{map[string]string{"natural": "wood"}, osm.Trees, "wood"},
		{map[string]string{"railway": "rail"}, osm.Rail, "rail"},
	} {
		k, cl, ok := osm.Classify(c.tags)
		if !ok || k != c.kind || cl != c.class {
			t.Errorf("Classify(%v) = %s %s %v, want %s %s", c.tags, k, cl, ok, c.kind, c.class)
		}
	}
	for _, tags := range []map[string]string{{"barrier": "fence"}, {"building": "no"}, {"highway": "proposed"}, nil} {
		if _, _, ok := osm.Classify(tags); ok {
			t.Errorf("Classify(%v) read it", tags)
		}
	}
	for _, k := range osm.Kinds() {
		want := k == osm.River || k == osm.Stream || k == osm.Canal || k == osm.Drain || k == osm.Water || k == osm.Wetland || k == osm.Well
		if k.IsWater() != want {
			t.Errorf("%s.IsWater() = %v", k, !want)
		}
	}
}

func TestAPIClient(t *testing.T) {
	t.Parallel()
	body := osmtest.KothapetaMap
	var calls atomic.Int32
	var gotUA, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		gotUA, gotQuery = r.UserAgent(), r.URL.RawQuery
		if r.URL.Path != "/api/0.6/map.json" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	src, err := osm.API(ua, srv.URL, osm.WithInterval(0))
	if err != nil {
		t.Fatal(err)
	}
	box := osm.Around(geo.Point{Lat: 16.7065, Lng: 81.9008}, 500)
	fs, err := src.Features(context.Background(), box)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) == 0 || gotUA != ua || !strings.HasPrefix(gotQuery, "bbox=81.89") {
		t.Errorf("%d features, UA %q, query %q", len(fs), gotUA, gotQuery)
	}
	// The same box again comes from the cache.
	if _, err := src.Features(context.Background(), box); err != nil || calls.Load() != 1 {
		t.Errorf("second call: %v, %d requests", err, calls.Load())
	}
	// Too big a box is refused without a request.
	if _, err := src.Features(context.Background(), osm.Around(geo.Point{Lat: 16.7, Lng: 81.9}, 5000)); !errors.Is(err, osm.ErrBox) {
		t.Errorf("a 10 km box: %v, want ErrBox", err)
	}
	if _, err := osm.API("", srv.URL); err == nil {
		t.Error("no User-Agent accepted")
	}
	if _, err := osm.API(ua, "ftp://x"); err == nil {
		t.Error("ftp URL accepted")
	}
}

func TestOverpassFallsBackAndChain(t *testing.T) {
	t.Parallel()
	good := read(t, "overpass-geom.json")
	var down, up atomic.Int32
	busy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		down.Add(1)
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	defer busy.Close()
	var query string
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up.Add(1)
		b, _ := io.ReadAll(r.Body)
		query = string(b)
		_, _ = w.Write(good)
	}))
	defer ok.Close()
	src, err := osm.Overpass(ua, []string{busy.URL, ok.URL}, osm.WithInterval(0))
	if err != nil {
		t.Fatal(err)
	}
	fs, err := src.Features(context.Background(), osm.Around(geo.Point{Lat: 16.706, Lng: 81.901}, 400))
	if err != nil || len(fs) != 6 {
		t.Fatalf("%d features, %v", len(fs), err)
	}
	if down.Load() != 1 || up.Load() != 1 || !strings.Contains(query, "out+geom") {
		t.Errorf("down %d up %d query %q", down.Load(), up.Load(), query)
	}

	// A chain whose first source fails uses the second; when all fail,
	// the error names each.
	allDown, _ := osm.Overpass(ua, []string{busy.URL}, osm.WithInterval(0))
	ch := osm.Chain(allDown, osm.Static{{ID: "node/1", Kind: osm.Well, Points: []geo.Point{{Lat: 16.706, Lng: 81.901}}}})
	fs, err = ch.Features(context.Background(), osm.Around(geo.Point{Lat: 16.706, Lng: 81.901}, 400))
	if err != nil || len(fs) != 1 {
		t.Errorf("chain: %v %v", fs, err)
	}
	if !strings.Contains(ch.Name(), "Overpass") || !strings.Contains(ch.Name(), "fixed") {
		t.Errorf("chain name %q", ch.Name())
	}
	// FeaturesFrom names the source that answered, also through a chain in a chain.
	_, from, err := osm.FeaturesFrom(context.Background(), osm.Chain(ch), osm.Around(geo.Point{Lat: 16.706, Lng: 81.901}, 400))
	if err != nil || from != (osm.Static{}).Name() {
		t.Errorf("answered by %q, %v", from, err)
	}
	_, err = osm.Chain(allDown).Features(context.Background(), osm.Around(geo.Point{Lat: 16.706, Lng: 81.901}, 400))
	if !errors.Is(err, osm.ErrUnavailable) || !strings.Contains(err.Error(), "504") {
		t.Errorf("all down: %v", err)
	}
	// Static leaves out what is outside the box.
	fs, _ = osm.Static{{Kind: osm.Well, Points: []geo.Point{{Lat: 10, Lng: 10}}}}.Features(context.Background(), osm.Around(geo.Point{Lat: 16.7, Lng: 81.9}, 100))
	if len(fs) != 0 {
		t.Errorf("static outside the box: %v", fs)
	}
}

func TestRequestsAreSpaced(t *testing.T) {
	t.Parallel()
	clk := clocktest.NewFake(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"elements":[]}`))
	}))
	defer srv.Close()
	src, err := osm.API(ua, srv.URL, osm.WithClock(clk), osm.WithInterval(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := src.Features(ctx, osm.Around(geo.Point{Lat: 16.7, Lng: 81.9}, 100)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := src.Features(ctx, osm.Around(geo.Point{Lat: 16.71, Lng: 81.9}, 100))
		done <- err
	}()
	if err := clk.WaitForTimers(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("second request sent before the interval: %d", calls.Load())
	}
	clk.Advance(time.Second)
	if err := <-done; err != nil || calls.Load() != 2 {
		t.Errorf("after the interval: %v, %d requests", err, calls.Load())
	}
}
