package planner_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/agro/agrotest"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/land"
	"github.com/bsamanvith/agentkit/apps/farm/planner"
)

func joined(notes []string) string { return strings.Join(notes, "\n") }

func TestSunAtAFarm(t *testing.T) {
	t.Parallel()
	p, id := setup(t, &agrotest.Source{Clim: agrotest.Hyderabad()})
	ctx := context.Background()
	rep, err := p.Sun(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Slope != nil || !strings.Contains(rep.SlopeNote, "Not surveyed") || rep.Today != "2026-11-01" {
		t.Errorf("slope %v, note %q, today %s", rep.Slope, rep.SlopeNote, rep.Today)
	}
	// Farooqnagar is inside the tropics: the noon sun is north of the
	// plot in June, south in December.
	if rep.Months[5].Noon.Azimuth != 0 || rep.Months[11].Noon.Azimuth != 180 {
		t.Errorf("noon sun June %v°, December %v°", rep.Months[5].Noon.Azimuth, rep.Months[11].Noon.Azimuth)
	}
	notes := joined(rep.Notes)
	for _, want := range []string{"Inside the tropics", "to the north of the plot in May to July", "Days run from 11.0 h in December to 13.0 h in June", "Sunniest in May", "Cloud takes a third or more of the sun in July and August"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes lack %q:\n%s", want, notes)
		}
	}
	if len(rep.Seasons) != 3 || rep.Months[0].FlatMJ == 0 {
		t.Errorf("seasons %v, January light %v", rep.Seasons, rep.Months[0].FlatMJ)
	}
}

// A survey that finds the ground falling north turns the light on the
// plot down in winter, in the sun report and in the recommendations.
func TestSlopeFromTheSurvey(t *testing.T) {
	t.Parallel()
	p, id := setup(t, &agrotest.Source{Clim: agrotest.Hyderabad()})
	ctx := context.Background()
	south, north := geo.Point{Lat: 17.07, Lng: 78.201}, geo.Point{Lat: 17.070904, Lng: 78.201}
	survey := func(fall float64) {
		t.Helper()
		f, err := p.Farms().Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		d := geo.Distance(south, north)
		sv := &land.Survey{Terrain: &land.Terrain{
			Points: []geo.Point{south, north}, Heights: []float64{600 + fall, 600}, Highest: 0, Lowest: 1,
			FallM: fall, SlopePct: fall / d * 100, Flat: fall < land.FlatM,
		}}
		if _, err := p.Farms().SetSurvey(ctx, f.ID, sv); err != nil {
			t.Fatal(err)
		}
	}
	survey(30) // 30 m over 100 m
	rep, err := p.Sun(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Slope == nil || rep.Slope.AspectDeg != 0 || rep.Slope.TiltDeg < 16 || rep.Slope.TiltDeg > 17 {
		t.Fatalf("slope %+v", rep.Slope)
	}
	if !strings.Contains(rep.SlopeNote, "towards the north") || !strings.Contains(joined(rep.Notes), "faces north: in December") {
		t.Errorf("note %q, notes:\n%s", rep.SlopeNote, joined(rep.Notes))
	}
	if rep.Months[11].SlopeFactor > 0.8 {
		t.Errorf("December on a 30%% north slope: factor %v", rep.Months[11].SlopeFactor)
	}
	rec, err := p.Recommend(ctx, id, planner.Ask{Around: "2026-11-15", Crops: []string{"wheat"}, Calendar: true})
	if err != nil {
		t.Fatal(err)
	}
	o := rec.Options[0]
	if rec.Slope == nil || o.Light.SlopeFactor >= 0.9 || !slices.ContainsFunc(o.Risks, func(n agro.Note) bool { return n.ID == "sun.slope" }) {
		t.Errorf("slope %v, light %+v, risks %v", rec.Slope, o.Light, o.Risks)
	}
	if len(o.Calendar) == 0 {
		t.Error("no calendar")
	}
	// Flat to the elevation model: no slope.
	survey(1)
	if rep, err = p.Sun(ctx, id); err != nil || rep.Slope != nil || !strings.Contains(rep.SlopeNote, "Flat") {
		t.Errorf("flat: slope %v, note %q, err %v", rep.Slope, rep.SlopeNote, err)
	}
}
