package planner_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/agro/agrotest"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farm/farmtest"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/planner"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

// The planner's clock: 31 October 2026, 20:00 UTC, already 1 November in
// India.
var now = time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)

// field is a 200 m by 100 m plot at Farooqnagar.
var field = []farm.Corner{
	{Point: geo.Point{Lat: 17.07, Lng: 78.20}},
	{Point: geo.Point{Lat: 17.07, Lng: 78.201883}},
	{Point: geo.Point{Lat: 17.070904, Lng: 78.201883}},
	{Point: geo.Point{Lat: 17.070904, Lng: 78.20}},
}

func setup(t *testing.T, climate agro.ClimateSource) (*planner.Planner, string) {
	t.Helper()
	svc, err := farm.NewService(&farmtest.Memory{}, farm.NewVerifier())
	if err != nil {
		t.Fatal(err)
	}
	f, err := svc.Create(context.Background(), farm.Draft{Name: "Black field", Boundary: farm.Boundary{Corners: field, Source: farm.SourceTyped}})
	if err != nil {
		t.Fatal(err)
	}
	lib, err := agro.Default()
	if err != nil {
		t.Fatal(err)
	}
	return planner.New(svc, lib, climate, planner.WithClock(clocktest.NewFake(now))), f.ID
}

func TestTodayIsTheFarmersDate(t *testing.T) {
	t.Parallel()
	p, _ := setup(t, nil)
	if got := p.Today().Format(agro.DateLayout); got != "2026-11-01" {
		t.Errorf("today %s, want 2026-11-01 in India", got)
	}
}

func TestRecommendUsesTheProfile(t *testing.T) {
	t.Parallel()
	src := &agrotest.Source{Clim: agrotest.Hyderabad()}
	p, id := setup(t, src)
	ctx := context.Background()
	before, err := p.Recommend(ctx, id, planner.Ask{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Options) != 5 || before.Soil != nil || before.Today != "2026-11-01" {
		t.Errorf("%d options, soil %v, today %s", len(before.Options), before.Soil, before.Today)
	}
	if _, err := p.SetProfile(ctx, id, agro.Profile{Soil: "black"}); err != nil {
		t.Fatal(err)
	}
	after, err := p.Recommend(ctx, id, planner.Ask{Around: "2027-06-15", Crops: []string{"cotton", "rice"}})
	if err != nil {
		t.Fatal(err)
	}
	if after.Soil == nil || after.Soil.ID != "black" || len(after.Options) != 2 || after.From == "" {
		t.Errorf("soil %v, %d options, range %s", after.Soil, len(after.Options), after.From)
	}
	// The climate is looked up at the plot's centre.
	if c := after.Climate.Point; c.Lat < 17.07 || c.Lat > 17.071 {
		t.Errorf("climate at %v", c)
	}
}

func TestRecommendFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p, id := setup(t, &agrotest.Source{Err: agro.ErrNoClimate})
	if _, err := p.Recommend(ctx, id, planner.Ask{}); !errors.Is(err, agro.ErrNoClimate) {
		t.Errorf("climate down: err %v", err)
	}
	p, id = setup(t, &agrotest.Source{Clim: agrotest.Hyderabad()})
	for name, tc := range map[string]struct {
		id   string
		ask  planner.Ask
		want error
	}{
		"missing farm": {"farm_missing", planner.Ask{}, farm.ErrNotFound},
		"bad date":     {id, planner.Ask{Around: "next June"}, agro.ErrInvalid},
		"bad limit":    {id, planner.Ask{Limit: -1}, agro.ErrInvalid},
		"unknown crop": {id, planner.Ask{Crops: []string{"unobtainium"}}, agro.ErrUnknown},
	} {
		if _, err := p.Recommend(ctx, tc.id, tc.ask); !errors.Is(err, tc.want) {
			t.Errorf("%s: err %v, want %v", name, err, tc.want)
		}
	}
	if _, err := p.SetProfile(ctx, id, agro.Profile{Soil: "moon_dust"}); !errors.Is(err, agro.ErrInvalid) {
		t.Errorf("bad profile: err %v", err)
	}
}

func TestLayoutCheckAndSave(t *testing.T) {
	t.Parallel()
	p, id := setup(t, &agrotest.Source{Clim: agrotest.Hyderabad()})
	ctx := context.Background()
	plan, rep, err := p.Layout(ctx, id, []agro.Part{{Crop: "chickpea", Share: 0.5, Sow: "2026-11-10"}, {Crop: "mustard", Share: 0.5, Sow: "2026-11-10"}})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status == agro.PlanInvalid || len(plan.Zones) != 2 {
		t.Fatalf("status %s, %d zones: %+v", rep.Status, len(plan.Zones), rep.Checks)
	}
	f, err := p.SavePlan(ctx, id, plan)
	if err != nil {
		t.Fatal(err)
	}
	if f.Plan == nil || f.PlanReport == nil || f.PlanReport.Status != rep.Status {
		t.Errorf("saved plan %v, report %v", f.Plan, f.PlanReport)
	}
	// A zone off the plot cannot be saved, and the saved plan stays.
	bad := plan
	bad.Zones = append(slices.Clone(plan.Zones), agro.Zone{Crop: "maize", Sow: "2027-06-15", Ring: geo.Ring{{Lat: 17.08, Lng: 78.21}, {Lat: 17.08, Lng: 78.211}, {Lat: 17.081, Lng: 78.211}}})
	var pe *planner.PlanError
	if _, err := p.SavePlan(ctx, id, bad); !errors.As(err, &pe) || !errors.Is(err, agro.ErrInvalid) || pe.Report.Status != agro.PlanInvalid {
		t.Fatalf("err %v, want a PlanError", err)
	}
	if f, _ := p.ClearPlan(ctx, id); f.Plan != nil {
		t.Error("ClearPlan kept the plan")
	}
}

func TestCheckWithoutClimate(t *testing.T) {
	t.Parallel()
	p, id := setup(t, nil)
	rep, err := p.Check(context.Background(), id, agro.Plan{Zones: []agro.Zone{{Crop: "maize", Sow: "2027-06-15", Ring: geo.Ring{field[0].Point, field[1].Point, field[2].Point, field[3].Point}}}})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, c := range rep.Checks {
		ids = append(ids, c.ID)
	}
	if !slices.Contains(ids, "climate.none") || rep.Status == agro.PlanInvalid {
		t.Errorf("checks %v, status %s", ids, rep.Status)
	}
}
