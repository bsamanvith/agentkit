package cycle_test

import (
	"context"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/agro/agrotest"
	"github.com/bsamanvith/agentkit/apps/farm/cycle"
	"github.com/bsamanvith/agentkit/apps/farm/cycle/cycletest"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farm/farmtest"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/planner"
	"github.com/bsamanvith/agentkit/apps/farm/work"
	"github.com/bsamanvith/agentkit/apps/farm/work/worktest"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

// 1 June 2026, 06:00 in India: the monsoon is coming.
var now = time.Date(2026, 6, 1, 0, 30, 0, 0, time.UTC)

var field = []farm.Corner{
	{Point: geo.Point{Lat: 17.07, Lng: 78.20}},
	{Point: geo.Point{Lat: 17.07, Lng: 78.201883}},
	{Point: geo.Point{Lat: 17.070904, Lng: 78.201883}},
	{Point: geo.Point{Lat: 17.070904, Lng: 78.20}},
}

type world struct {
	svc     *farm.Service
	plan    *planner.Planner
	climate *agrotest.Source
	store   *cycletest.Memory
	work    *work.Service
	cycles  *cycle.Service
	clock   *clocktest.Fake
	plot    string
}

func setup(t *testing.T, opts ...cycle.Option) *world {
	t.Helper()
	clk := clocktest.NewFake(now)
	svc, err := farm.NewService(&farmtest.Memory{}, farm.NewVerifier(), farm.WithServiceClock(clk))
	if err != nil {
		t.Fatal(err)
	}
	lib, err := agro.Default()
	if err != nil {
		t.Fatal(err)
	}
	w := &world{svc: svc, climate: &agrotest.Source{Clim: agrotest.Hyderabad()}, store: cycletest.NewMemory(), clock: clk}
	w.plan = planner.New(svc, lib, w.climate, planner.WithClock(clk))
	w.work = work.New(w.plan, worktest.NewMemory())
	w.work.SetNow(clk.Now)
	w.cycles = cycle.New(w.plan, w.store, append([]cycle.Option{cycle.WithRoadmap(w.work)}, opts...)...)
	w.cycles.SetNow(clk.Now)
	f, err := svc.Create(context.Background(), farm.Draft{Name: "Farooqnagar plot", Boundary: farm.Boundary{Corners: field, Source: farm.SourceTyped}})
	if err != nil {
		t.Fatal(err)
	}
	w.plot = f.ID
	if _, err := w.plan.SetProfile(context.Background(), f.ID, agro.Profile{Soil: "black", Water: []agro.Water{{Kind: agro.Borewell, Reliable: true}}, PreviousCrop: "maize"}); err != nil {
		t.Fatal(err)
	}
	return w
}
