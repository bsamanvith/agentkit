package farmagent_test

import (
	"context"
	"encoding/json"
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
	"github.com/bsamanvith/agentkit/apps/farm/farmagent"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/nominatim"
	"github.com/bsamanvith/agentkit/apps/farm/planner"
	"github.com/bsamanvith/agentkit/apps/farm/region"
	"github.com/bsamanvith/agentkit/apps/farm/surveyor"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// pulivendula is a fake geocoder that knows one village in YSR Kadapa.
type pulivendula struct{}

var pulivendulaAt = geo.Point{Lat: 14.4221, Lng: 78.2256}

func (pulivendula) FindPlace(_ context.Context, q, _ string, _ int) ([]surveyor.Found, error) {
	if !strings.Contains(strings.ToLower(q), "pulivendula") {
		return []surveyor.Found{}, nil
	}
	return []surveyor.Found{{Place: nominatim.Place{Name: "Pulivendula", Kind: "town", Point: pulivendulaAt, Address: "Pulivendula, YSR Kadapa, Andhra Pradesh, India"}}}, nil
}

// placePlanner plans with the embedded region packs; every point is in
// YSR Kadapa.
func placePlanner(t *testing.T) *planner.Planner {
	t.Helper()
	svc, _ := service(t)
	regions, err := region.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	loc := &farmtest.Locator{Location: farm.Location{Village: "Pulivendula", District: "YSR Kadapa", State: "Andhra Pradesh", Country: "India", CountryCode: "IN"}}
	return planner.New(svc, regions.Base().Library(), &agrotest.Source{Clim: agrotest.Hyderabad()},
		planner.WithClock(clocktest.NewFake(time.Date(2026, 11, 16, 6, 0, 0, 0, time.UTC))), planner.WithRegions(regions), planner.WithLocator(loc))
}

func placeRegistry(t *testing.T, p *planner.Planner, find farmagent.PlaceFinder) tool.Registry {
	t.Helper()
	tools, err := farmagent.PlaceTools(p, find)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tool.NewRegistry(tools)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

type placeOut struct {
	Place   string
	Point   geo.Point
	Regions []string
	Assumed []agro.Note
	Today   string
	Options []agro.Option
}

// A farmer with no plot asks by the village's name: the tool finds it and
// answers with the planner's own numbers for that point, the district's
// layers and what it assumed.
func TestRecommendForPlaceByName(t *testing.T) {
	t.Parallel()
	p := placePlanner(t)
	reg := placeRegistry(t, p, pulivendula{})
	out := invoke[placeOut](t, reg, farmagent.RecommendForPlaceTool, `{"place":"Pulivendula","crops":["groundnut","cotton","rice"]}`)
	if out.Point != pulivendulaAt || !strings.Contains(out.Place, "YSR Kadapa") || !slices.Equal(out.Regions, []string{region.Base, "Andhra Pradesh", "YSR Kadapa"}) || out.Today != "2026-11-16" {
		t.Fatalf("place %q at %v, regions %v, today %s", out.Place, out.Point, out.Regions, out.Today)
	}
	if len(out.Assumed) != 2 || out.Assumed[0].ID != planner.AssumedRainOnly || out.Assumed[1].ID != planner.AssumedSoilUnknown {
		t.Errorf("assumed %+v", out.Assumed)
	}
	// Every number is the planner's for that point.
	want, err := p.RecommendAt(context.Background(), planner.At{Point: pulivendulaAt}, planner.Ask{Crops: []string{"groundnut", "cotton", "rice"}, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out.Options, want.Options) {
		t.Errorf("tool options differ from the planner's:\n%+v\n%+v", out.Options, want.Options)
	}
	// By position, with a borewell and a date: only the soil is assumed,
	// and the sowing is near the date.
	at := invoke[placeOut](t, reg, farmagent.RecommendForPlaceTool,
		`{"lat":14.4221,"lng":78.2256,"around":"2027-01-10","water":[{"kind":"borewell","reliable":true}],"limit":3}`)
	if len(at.Assumed) != 1 || at.Assumed[0].ID != planner.AssumedSoilUnknown || len(at.Options) != 3 {
		t.Errorf("assumed %+v, %d options", at.Assumed, len(at.Options))
	}
	for _, o := range at.Options {
		if !o.InRange {
			t.Errorf("%s sown %s, outside the farmer's range", o.Crop, o.Sow)
		}
	}
}

func TestRecommendForPlaceFailures(t *testing.T) {
	t.Parallel()
	p := placePlanner(t)
	ctx := context.Background()
	for name, c := range map[string]struct {
		find  farmagent.PlaceFinder
		input string
		want  error
	}{
		"nothing":       {pulivendula{}, `{}`, tool.ErrInvalidInput},
		"unknown place": {pulivendula{}, `{"place":"Atlantis"}`, tool.ErrInvalidInput},
		"bad soil":      {pulivendula{}, `{"place":"Pulivendula","soil":"moon_dust"}`, tool.ErrInvalidInput},
		"no geocoder":   {nil, `{"place":"Pulivendula"}`, surveyor.ErrNoGeocoder},
	} {
		_, err := placeRegistry(t, p, c.find).Invoke(ctx, farmagent.RecommendForPlaceTool, json.RawMessage(c.input))
		if !errors.Is(err, c.want) && !errors.Is(err, tool.ErrToolFailed) {
			t.Errorf("%s: err %v, want %v", name, err, c.want)
		}
		if err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// Without a geocoder a position still works.
	out := invoke[placeOut](t, placeRegistry(t, p, nil), farmagent.RecommendForPlaceTool, `{"lat":14.4221,"lng":78.2256}`)
	if len(out.Options) != 5 {
		t.Errorf("%d options by position", len(out.Options))
	}
}

// The assistant has the tool beside recommend_crops, read-only, whenever
// it plans crops.
func TestRecommendForPlaceIsOffered(t *testing.T) {
	t.Parallel()
	svc, _ := service(t)
	sets, err := farmagent.Toolsets(svc, farmagent.WithPlanner(placePlanner(t)))
	if err != nil {
		t.Fatal(err)
	}
	defs, err := sets[farmagent.ToolsetFarm].List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range defs {
		names = append(names, d.Name)
		if d.Name == farmagent.RecommendForPlaceTool && d.Kind != tool.KindRead {
			t.Errorf("%s is %s", d.Name, d.Kind)
		}
	}
	if !slices.Contains(names, farmagent.RecommendForPlaceTool) || !slices.Contains(names, farmagent.RecommendCropsTool) {
		t.Errorf("tools %v", names)
	}
}

// The open question, "what can I sow here now": field crops that can go
// in soon come apart from the ones whose time is later and from trees, a
// headline says which it is, and no crop new to the country is offered.
func TestRecommendForPlaceAnswersNowAndNext(t *testing.T) {
	t.Parallel()
	p := placePlanner(t)
	lib := p.Library()
	check := func(name string, opts []agro.Option, perennial bool) {
		t.Helper()
		for _, o := range opts {
			c, err := lib.Crop(o.Crop)
			if err != nil {
				t.Fatal(err)
			}
			if c.ClimateAnalog || c.Perennial != perennial {
				t.Errorf("%s has %s (new to the country %v, perennial %v)", name, o.Crop, c.ClimateAnalog, c.Perennial)
			}
		}
	}
	soonBy := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC).AddDate(0, 0, farmagent.SoonDays).Format(agro.DateLayout)

	// Rain only in November: nothing soon; the headline names the next
	// sowing, which is the first option.
	dry, err := farmagent.RecommendForPlace(context.Background(), p, pulivendula{}, farmagent.PlaceAsk{Place: "Pulivendula"})
	if err != nil {
		t.Fatal(err)
	}
	check("options", dry.Options, false)
	check("trees", dry.Trees, true)
	if len(dry.Options) == 0 || len(dry.Options) > 5 || len(dry.Trees) > 2 {
		t.Fatalf("%d options, %d trees", len(dry.Options), len(dry.Trees))
	}
	if len(dry.Soon) != 0 || !strings.Contains(dry.Headline, "No field crop suits sowing in the next 6 weeks") || !strings.Contains(dry.Headline, strings.ToLower(dry.Options[0].Name)) {
		t.Errorf("rain only: soon %d, headline %q, first option %s", len(dry.Soon), dry.Headline, dry.Options[0].Name)
	}

	// With a borewell something can go in within six weeks, and the
	// headline leads with it.
	wet, err := farmagent.RecommendForPlace(context.Background(), p, pulivendula{}, farmagent.PlaceAsk{Place: "Pulivendula", Water: []agro.Water{{Kind: agro.Borewell, Reliable: true}}})
	if err != nil {
		t.Fatal(err)
	}
	check("soon", wet.Soon, false)
	if len(wet.Soon) == 0 || len(wet.Soon) > 3 || !strings.HasPrefix(wet.Headline, wet.Soon[0].Name+" can be sown") {
		t.Fatalf("a borewell: soon %d, headline %q", len(wet.Soon), wet.Headline)
	}
	for _, o := range wet.Soon {
		if o.Sow > soonBy || o.Score < agro.Good {
			t.Errorf("soon has %s sown %s scoring %d", o.Crop, o.Sow, o.Score)
		}
	}

	// A named crop or a date is answered as asked, without the layout.
	named, err := farmagent.RecommendForPlace(context.Background(), p, pulivendula{}, farmagent.PlaceAsk{Place: "Pulivendula", Crops: []string{"mango", "groundnut"}})
	if err != nil {
		t.Fatal(err)
	}
	if named.Headline != "" || named.Soon != nil || named.Trees != nil || len(named.Options) != 2 {
		t.Errorf("named crops: %+v", named)
	}
}
