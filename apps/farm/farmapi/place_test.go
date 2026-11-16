package farmapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/agro/agrotest"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farm/farmtest"
	"github.com/bsamanvith/agentkit/apps/farm/farmagent"
	"github.com/bsamanvith/agentkit/apps/farm/farmapi"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/nominatim"
	"github.com/bsamanvith/agentkit/apps/farm/planner"
	"github.com/bsamanvith/agentkit/apps/farm/region"
	"github.com/bsamanvith/agentkit/apps/farm/surveyor"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

// villages knows Pulivendula; down fails every search.
type villages struct{ down bool }

func (v villages) Search(_ context.Context, q string, _ *geo.Point, _ string, _ int) ([]nominatim.Place, error) {
	switch {
	case v.down:
		return nil, errors.New("nominatim: 503")
	case strings.Contains(q, "Pulivendula"):
		return []nominatim.Place{{Name: "Pulivendula", Kind: "town", Point: geo.Point{Lat: 14.4221, Lng: 78.2256}, Address: "Pulivendula, YSR Kadapa, Andhra Pradesh, India"}}, nil
	}
	return []nominatim.Place{}, nil
}

// placeServer serves the API with a planner whose every point is in YSR
// Kadapa, and the map with search when it is given.
func placeServer(t *testing.T, search surveyor.Searcher) *httptest.Server {
	t.Helper()
	svc, err := farm.NewService(&farmtest.Memory{}, farm.NewVerifier())
	if err != nil {
		t.Fatal(err)
	}
	regions, err := region.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	loc := &farmtest.Locator{Location: farm.Location{District: "YSR Kadapa", State: "Andhra Pradesh", CountryCode: "IN"}}
	p := planner.New(svc, regions.Base().Library(), &agrotest.Source{Clim: agrotest.Hyderabad()},
		planner.WithClock(clocktest.NewFake(time.Date(2026, 11, 16, 6, 0, 0, 0, time.UTC))), planner.WithRegions(regions), planner.WithLocator(loc))
	opts := []farmapi.Option{farmapi.WithPlanner(p)}
	if search != nil {
		opts = append(opts, farmapi.WithSurveyor(surveyor.New(svc, surveyor.WithSearch(search, "IN"))))
	}
	srv := httptest.NewServer(farmapi.New(svc, opts...))
	t.Cleanup(srv.Close)
	return srv
}

// No plot is needed: the answer for a village has the place, its region
// layers, the assumptions and the options, and saves nothing.
func TestRecommendForAPlace(t *testing.T) {
	t.Parallel()
	srv := placeServer(t, villages{})
	var out farmagent.PlaceAnswer
	if code := call(t, srv, "POST", "/api/recommend", `{"place":"Pulivendula","limit":3}`, &out); code != http.StatusOK {
		t.Fatalf("recommend: %d", code)
	}
	if len(out.Options) != 3 || out.Regions[len(out.Regions)-1] != "YSR Kadapa" || len(out.Assumed) != 2 || !strings.Contains(out.Place, "Pulivendula") {
		t.Errorf("%d options, regions %v, assumed %v, place %q", len(out.Options), out.Regions, out.Assumed, out.Place)
	}
	for _, o := range out.Options {
		if o.Sow == "" || o.Harvest == "" {
			t.Errorf("option %s without dates", o.Crop)
		}
	}
	var irrigated farmagent.PlaceAnswer
	if code := call(t, srv, "POST", "/api/recommend", `{"lat":14.4221,"lng":78.2256,"soil":"red_sandy","water":[{"kind":"canal","reliable":true}],"around":"2027-06-20"}`, &irrigated); code != http.StatusOK {
		t.Fatalf("by position: %d", code)
	}
	if len(irrigated.Assumed) != 0 || irrigated.From == "" || irrigated.Soil == "" {
		t.Errorf("assumed %v, from %q, soil %q", irrigated.Assumed, irrigated.From, irrigated.Soil)
	}
	var farms struct{ Farms []farm.Farm }
	if call(t, srv, "GET", "/api/farms", "", &farms); len(farms.Farms) != 0 {
		t.Errorf("a recommendation saved %d plots", len(farms.Farms))
	}
}

func TestRecommendForAPlaceFailures(t *testing.T) {
	t.Parallel()
	srv := placeServer(t, villages{})
	for body, want := range map[string]int{
		`{}`:                                 http.StatusBadRequest,
		`{"place":"Atlantis"}`:               http.StatusNotFound,
		`{"place":"Pulivendula","soil":"x"}`: http.StatusBadRequest,
		`{"place":"Pulivendula","around":"June"}`: http.StatusBadRequest,
		`{"lat":200,"lng":0}`:                     http.StatusBadRequest,
		`{"place":"Pulivendula","farm":"farm_1"}`: http.StatusBadRequest,
	} {
		if code := call(t, srv, "POST", "/api/recommend", body, nil); code != want {
			t.Errorf("%s: %d, want %d", body, code, want)
		}
	}
	if code := call(t, placeServer(t, villages{down: true}), "POST", "/api/recommend", `{"place":"Pulivendula"}`, nil); code != http.StatusBadGateway {
		t.Errorf("geocoder down: %d, want 502", code)
	}
	// Without the map a name cannot be found, but a position still works.
	bare := placeServer(t, nil)
	if code := call(t, bare, "POST", "/api/recommend", `{"place":"Pulivendula"}`, nil); code != http.StatusServiceUnavailable {
		t.Errorf("no geocoder: %d, want 503", code)
	}
	if code := call(t, bare, "POST", "/api/recommend", `{"lat":14.4221,"lng":78.2256}`, nil); code != http.StatusOK {
		t.Errorf("no geocoder, by position: %d", code)
	}
	var none struct{ Error string }
	if code := call(t, placeServerNoClimate(t), "POST", "/api/recommend", `{"lat":14.4221,"lng":78.2256}`, &none); code != http.StatusServiceUnavailable {
		t.Errorf("no climate: %d", code)
	}
}

func placeServerNoClimate(t *testing.T) *httptest.Server {
	t.Helper()
	svc, err := farm.NewService(&farmtest.Memory{}, farm.NewVerifier())
	if err != nil {
		t.Fatal(err)
	}
	lib, err := agro.Default()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(farmapi.New(svc, farmapi.WithPlanner(planner.New(svc, lib, nil))))
	t.Cleanup(srv.Close)
	return srv
}
