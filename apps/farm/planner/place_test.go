package planner_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/agro/agrotest"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farm/farmtest"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/planner"
	"github.com/bsamanvith/agentkit/apps/farm/region"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

// Kadapa town, and where Nominatim says it is.
var (
	kadapaPoint = geo.Point{Lat: 14.4673, Lng: 78.8242}
	kadapaLoc   = farm.Location{Village: "Kadapa", District: "YSR Kadapa", State: "Andhra Pradesh", Country: "India", CountryCode: "IN"}
	gunturLoc   = farm.Location{Village: "Tenali", District: "Guntur", State: "Andhra Pradesh", Country: "India", CountryCode: "IN"}
)

// placePlanner is a planner with the embedded region packs, looking places
// up with loc, the plots' verifier too.
func placePlanner(t *testing.T, loc farm.Locator) (*planner.Planner, *farm.Service) {
	t.Helper()
	svc, err := farm.NewService(&farmtest.Memory{}, farm.NewVerifier(farm.WithLocator(loc)))
	if err != nil {
		t.Fatal(err)
	}
	regions, err := region.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	p := planner.New(svc, regions.Base().Library(), &agrotest.Source{Clim: agrotest.Hyderabad()},
		planner.WithClock(clocktest.NewFake(now)), planner.WithRegions(regions), planner.WithLocator(loc))
	return p, svc
}

// recommendAt asks and checks what holds for every answer: the point is
// the one asked, the regions start at the base, the options are scored by
// the planner's today.
func recommendAt(t *testing.T, p *planner.Planner, at planner.At, ask planner.Ask) planner.PlaceRecommendation {
	t.Helper()
	r, err := p.RecommendAt(context.Background(), at, ask)
	if err != nil {
		t.Fatal(err)
	}
	if r.Point != at.Point || len(r.Regions) == 0 || r.Regions[0] != region.Base || r.Farm != "" || r.Today != "2026-11-01" {
		t.Fatalf("point %v, regions %v, farm %q, today %s", r.Point, r.Regions, r.Farm, r.Today)
	}
	return r
}

func ids(notes []agro.Note) []string {
	out := []string{}
	for _, n := range notes {
		out = append(out, n.ID)
	}
	return out
}

func option(t *testing.T, r planner.PlaceRecommendation, crop string) agro.Option {
	t.Helper()
	for _, o := range r.Options {
		if o.Crop == crop {
			return o
		}
	}
	t.Fatalf("no %s in %d options", crop, len(r.Options))
	return agro.Option{}
}

func month(t *testing.T, day string) time.Month {
	t.Helper()
	d, err := time.Parse(agro.DateLayout, day)
	if err != nil {
		t.Fatal(err)
	}
	return d.Month()
}

// A point in Kadapa gets the district's rainfed groundnut window (1 June
// to 31 July); one elsewhere in the state does not.
func TestRecommendAtUsesTheDistrictsWindows(t *testing.T) {
	t.Parallel()
	p, _ := placePlanner(t, &farmtest.Locator{Location: kadapaLoc})
	r := recommendAt(t, p, planner.At{Point: kadapaPoint}, planner.Ask{Crops: []string{"groundnut"}})
	if r.Place.District != "YSR Kadapa" || r.Regions[len(r.Regions)-1] != "YSR Kadapa" || r.Location == nil {
		t.Fatalf("place %+v, regions %v, location %v", r.Place, r.Regions, r.Location)
	}
	gn := option(t, r, "groundnut")
	if m := month(t, gn.Sow); m != time.June && m != time.July || !slices.Contains(ids(gn.Reasons), "window.local") {
		t.Errorf("Kadapa groundnut: sow %s, reasons %v", gn.Sow, ids(gn.Reasons))
	}
	g, _ := placePlanner(t, &farmtest.Locator{Location: gunturLoc})
	other := option(t, recommendAt(t, g, planner.At{Point: kadapaPoint}, planner.Ask{Crops: []string{"groundnut"}}), "groundnut")
	if slices.Contains(ids(other.Reasons), "window.local") || slices.Contains(ids(other.Risks), "window.outside") {
		t.Errorf("Guntur groundnut has Kadapa's window: %v %v", ids(other.Reasons), ids(other.Risks))
	}
	// A place given by the caller is used as it is, without a lookup.
	loc := &farmtest.Locator{Location: gunturLoc}
	q, _ := placePlanner(t, loc)
	given := recommendAt(t, q, planner.At{Point: kadapaPoint, Place: region.Place{Country: "IN", State: "Andhra Pradesh", District: "Kadapa"}}, planner.Ask{Crops: []string{"groundnut"}})
	if loc.Calls() != 0 || !reflect.DeepEqual(given.Options, r.Options) {
		t.Errorf("given place: %d lookups, options %+v", loc.Calls(), given.Options)
	}
}

// With no profile, a place gets exactly what a saved plot with no profile
// gets there: the same rules, not a fork of them.
func TestRecommendAtIsWhatAPlotThereGets(t *testing.T) {
	t.Parallel()
	p, svc := placePlanner(t, &farmtest.Locator{Location: kadapaLoc})
	d := 0.0009
	f, err := svc.Create(context.Background(), farm.Draft{Name: "Kadapa field", Boundary: farm.Boundary{Source: farm.SourceTyped, Corners: []farm.Corner{
		{Point: kadapaPoint},
		{Point: geo.Point{Lat: kadapaPoint.Lat, Lng: kadapaPoint.Lng + d}},
		{Point: geo.Point{Lat: kadapaPoint.Lat + d, Lng: kadapaPoint.Lng + d}},
		{Point: geo.Point{Lat: kadapaPoint.Lat + d, Lng: kadapaPoint.Lng}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, ask := range []planner.Ask{{Limit: 5}, {Around: "2027-06-20", Crops: []string{"groundnut", "rice", "cotton"}}} {
		plot, err := p.Recommend(context.Background(), f.ID, ask)
		if err != nil {
			t.Fatal(err)
		}
		at := recommendAt(t, p, planner.At{Point: geo.Centroid(f.Boundary.Ring())}, ask)
		if !reflect.DeepEqual(plot.Result, at.Result) {
			t.Errorf("ask %+v: the place's answer differs from the plot's\nplot  %+v\nplace %+v", ask, plot.Options, at.Options)
		}
	}
}

// A date the farmer gives moves the answer to sowings around it.
func TestRecommendAtAroundADate(t *testing.T) {
	t.Parallel()
	p, _ := placePlanner(t, &farmtest.Locator{Location: kadapaLoc})
	soon := recommendAt(t, p, planner.At{Point: kadapaPoint}, planner.Ask{Crops: []string{"chickpea"}})
	june := recommendAt(t, p, planner.At{Point: kadapaPoint}, planner.Ask{Crops: []string{"chickpea"}, Around: "2027-06-20"})
	a, b := option(t, soon, "chickpea"), option(t, june, "chickpea")
	if june.From == "" || june.To == "" || soon.From != "" || a.Sow == b.Sow {
		t.Errorf("range %s to %s (none asked: %q); sow %s and %s", june.From, june.To, soon.From, a.Sow, b.Sow)
	}
	if b.Sow < june.From || b.Sow > june.To {
		t.Errorf("sow %s outside %s to %s", b.Sow, june.From, june.To)
	}
}

// What the farmer did not say is assumed as for a plot with no profile,
// and named.
func TestRecommendAtNamesItsAssumptions(t *testing.T) {
	t.Parallel()
	p, _ := placePlanner(t, &farmtest.Locator{Location: kadapaLoc})
	for _, c := range []struct {
		prof *agro.Profile
		want []string
	}{
		{nil, []string{planner.AssumedRainOnly, planner.AssumedSoilUnknown}},
		{&agro.Profile{}, []string{planner.AssumedRainOnly, planner.AssumedSoilUnknown}},
		{&agro.Profile{Soil: "red_sandy"}, []string{planner.AssumedRainOnly}},
		{&agro.Profile{Water: []agro.Water{{Kind: agro.Borewell, Reliable: true}}}, []string{planner.AssumedSoilUnknown}},
		{&agro.Profile{Soil: "black", Water: []agro.Water{{Kind: agro.Canal}}}, []string{}},
	} {
		r := recommendAt(t, p, planner.At{Point: kadapaPoint, Profile: c.prof}, planner.Ask{Limit: 3})
		if got := ids(r.Assumed); !slices.Equal(got, c.want) {
			t.Errorf("profile %+v: assumed %v, want %v", c.prof, got, c.want)
		}
		for _, n := range r.Assumed {
			if n.Message == "" {
				t.Errorf("assumption %s has no message", n.ID)
			}
		}
		if (c.prof != nil && c.prof.Soil != "") != (r.Soil != nil) {
			t.Errorf("profile %+v: soil %v", c.prof, r.Soil)
		}
	}
}

// Irrigation changes the answer: groundnut sown in Kadapa in late
// November is inside the district's irrigated rabi window on a borewell,
// and outside every window on rain alone, which scores less.
func TestRecommendAtIrrigatedChangesTheAnswer(t *testing.T) {
	t.Parallel()
	p, _ := placePlanner(t, &farmtest.Locator{Location: kadapaLoc})
	ask := planner.Ask{Crops: []string{"groundnut"}, Around: "2026-11-25", Spread: 10}
	rain := option(t, recommendAt(t, p, planner.At{Point: kadapaPoint}, ask), "groundnut")
	wet := recommendAt(t, p, planner.At{Point: kadapaPoint, Profile: &agro.Profile{Water: []agro.Water{{Kind: agro.Borewell, Reliable: true}}}}, ask)
	well := option(t, wet, "groundnut")
	if !slices.Contains(ids(well.Reasons), "window.local") || !slices.Contains(ids(rain.Risks), "window.outside") {
		t.Errorf("borewell reasons %v; rain risks %v", ids(well.Reasons), ids(rain.Risks))
	}
	if well.Score <= rain.Score || rain.Water.IrrigationMM != 0 || well.Water.IrrigationMM == 0 {
		t.Errorf("rain %d (%v mm irrigation), borewell %d (%v mm)", rain.Score, rain.Water.IrrigationMM, well.Score, well.Water.IrrigationMM)
	}
	if wet.Profile == nil || len(wet.Profile.Water) != 1 {
		t.Errorf("profile %+v", wet.Profile)
	}
}

// A place that cannot be looked up gets the base rules and a note; bad
// input and no climate fail as Recommend does.
func TestRecommendAtFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p, _ := placePlanner(t, &farmtest.Locator{Err: errors.New("nominatim: 503")})
	r := recommendAt(t, p, planner.At{Point: kadapaPoint}, planner.Ask{Crops: []string{"groundnut"}})
	if len(r.Regions) != 1 || r.Location != nil || !slices.Contains(ids(r.Notes), "place.unknown") {
		t.Errorf("lookup down: regions %v, location %v, notes %v", r.Regions, r.Location, ids(r.Notes))
	}
	for _, n := range r.Notes {
		if strings.Contains(n.Message, "nominatim") {
			t.Errorf("note leaks the error: %s", n.Message)
		}
	}
	p, _ = placePlanner(t, &farmtest.Locator{Location: kadapaLoc})
	for name, tc := range map[string]struct {
		at  planner.At
		ask planner.Ask
	}{
		"no point":     {planner.At{Point: geo.Point{Lat: 200}}, planner.Ask{}},
		"bad soil":     {planner.At{Point: kadapaPoint, Profile: &agro.Profile{Soil: "moon_dust"}}, planner.Ask{}},
		"bad water":    {planner.At{Point: kadapaPoint, Profile: &agro.Profile{Water: []agro.Water{{Kind: "sea"}}}}, planner.Ask{}},
		"bad date":     {planner.At{Point: kadapaPoint}, planner.Ask{Around: "June"}},
		"bad limit":    {planner.At{Point: kadapaPoint}, planner.Ask{Limit: -1}},
		"unknown crop": {planner.At{Point: kadapaPoint}, planner.Ask{Crops: []string{"unobtainium"}}},
	} {
		if _, err := p.RecommendAt(ctx, tc.at, tc.ask); !errors.Is(err, agro.ErrInvalid) && !errors.Is(err, agro.ErrUnknown) {
			t.Errorf("%s: err %v", name, err)
		}
	}
	none := planner.New(nil, p.Library(), nil, planner.WithClock(clocktest.NewFake(now)))
	if _, err := none.RecommendAt(ctx, planner.At{Point: kadapaPoint}, planner.Ask{}); !errors.Is(err, agro.ErrNoClimate) {
		t.Errorf("no climate: err %v", err)
	}
}
