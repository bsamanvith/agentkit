package surveyor_test

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/elevation"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farm/farmtest"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/land"
	"github.com/bsamanvith/agentkit/apps/farm/nominatim"
	"github.com/bsamanvith/agentkit/apps/farm/osm"
	"github.com/bsamanvith/agentkit/apps/farm/osm/osmtest"
	"github.com/bsamanvith/agentkit/apps/farm/surveyor"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

var t0 = time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)

// Kothapeta returns a farm on the recorded plot.
func Kothapeta(id string) farm.Farm {
	f := farmtest.Sample(id, t0)
	f.Name = "Paddy by the drain"
	f.Boundary = farm.Boundary{Source: farm.SourceMap}
	for _, p := range osmtest.KothapetaPlot {
		f.Boundary.Corners = append(f.Boundary.Corners, farm.Corner{Point: p})
	}
	f.Plan = nil
	f.PlanReport = nil
	return f
}

type counting struct {
	osm.Source
	n   atomic.Int32
	err error
}

func (c *counting) Features(ctx context.Context, b osm.BBox) ([]osm.Feature, error) {
	c.n.Add(1)
	if c.err != nil {
		return nil, c.err
	}
	return c.Source.Features(ctx, b)
}

type searcher struct{ near *geo.Point }

func (s *searcher) Search(_ context.Context, q string, near *geo.Point, _ string, _ int) ([]nominatim.Place, error) {
	s.near = near
	return []nominatim.Place{{Name: q, Kind: "town", Point: geo.Point{Lat: 16.716755, Lng: 81.896565}}}, nil
}

func setup(t *testing.T, opts ...surveyor.Option) (*surveyor.Surveyor, *counting) {
	t.Helper()
	store := &farmtest.Memory{}
	for _, f := range []farm.Farm{Kothapeta("farm_k"), farmtest.Sample("farm_h", t0)} {
		if err := store.Create(context.Background(), f); err != nil {
			t.Fatal(err)
		}
	}
	svc, err := farm.NewService(store, farm.NewVerifier())
	if err != nil {
		t.Fatal(err)
	}
	src := &counting{Source: osmtest.Source()}
	return surveyor.New(svc, append([]surveyor.Option{surveyor.WithFeatures(src), surveyor.WithClock(clocktest.NewFake(t0))}, opts...)...), src
}

func TestSurveySavesAndRefreshes(t *testing.T) {
	t.Parallel()
	s, src := setup(t, surveyor.WithHeights(elevation.Func(func(p geo.Point) float64 { return 5 + (p.Lng-81.9)*1000 })))
	ctx := context.Background()
	sv, err := s.Survey(ctx, "farm_k", false)
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := sv.Find(osm.Drain); !ok || d.DistanceM < 95 || d.DistanceM > 101 {
		t.Errorf("drain %+v", d)
	}
	if sv.Terrain == nil || !sv.Terrain.Flat || len(sv.Sources) != 2 || !sv.SurveyedAt.Equal(t0) {
		t.Errorf("terrain %+v, sources %v, at %v", sv.Terrain, sv.Sources, sv.SurveyedAt)
	}
	f, _ := s.Farms().Get(ctx, "farm_k")
	if f.Survey == nil {
		t.Fatal("the survey was not saved")
	}
	if _, err := s.Survey(ctx, "farm_k", false); err != nil || src.n.Load() != 1 {
		t.Errorf("saved survey not reused: %v, %d lookups", err, src.n.Load())
	}
	if _, err := s.Survey(ctx, "farm_k", true); err != nil || src.n.Load() != 2 {
		t.Errorf("refresh: %v, %d lookups", err, src.n.Load())
	}
	if _, err := s.Survey(ctx, "farm_nope", false); !errors.Is(err, farm.ErrNotFound) {
		t.Errorf("unknown farm: %v", err)
	}
}

func TestSurveyWhenTheMapIsDown(t *testing.T) {
	t.Parallel()
	s, src := setup(t)
	src.err = osm.ErrUnavailable
	sv, err := s.Survey(context.Background(), "farm_k", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(sv.Missing) == 0 || sv.Shape.AreaM2 == 0 || !slices.ContainsFunc(sv.Notes, func(n land.Note) bool { return n.ID == land.NoteNoMapData }) {
		t.Errorf("survey %+v", sv)
	}
	if f, _ := s.Farms().Get(context.Background(), "farm_k"); f.Survey != nil {
		t.Error("a survey without the map was saved")
	}
	// No source at all is the same.
	plain := surveyor.New(s.Farms())
	sv, err = plain.SurveyRing(context.Background(), osmtest.KothapetaPlot)
	if err != nil || len(sv.Missing) != 1 {
		t.Errorf("no sources: %v %v", err, sv.Missing)
	}
	if _, err := plain.SurveyRing(context.Background(), geo.Ring{{Lat: 1, Lng: 1}}); !errors.Is(err, farm.ErrInvalid) {
		t.Errorf("a one-corner ring: %v", err)
	}
}

func TestDesignFlow(t *testing.T) {
	t.Parallel()
	s, _ := setup(t)
	ctx := context.Background()
	d, rep, err := s.Suggest(ctx, "farm_k", land.Want{Path: true, Shed: true, Pond: true, Border: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != land.DesignSound || len(d.Elements) != 5 {
		t.Fatalf("%s %d elements %+v", rep.Status, len(d.Elements), rep.Checks)
	}
	f, err := s.SaveDesign(ctx, "farm_k", d)
	if err != nil {
		t.Fatal(err)
	}
	if f.Design == nil || f.DesignReport == nil || f.DesignReport.Status != land.DesignSound || f.Survey == nil {
		t.Errorf("saved: %+v %+v survey %v", f.Design, f.DesignReport, f.Survey != nil)
	}
	// A pond outside the plot is refused.
	bad := land.Design{Elements: []land.Element{{Kind: land.Pond, Ring: geo.Rect(geo.Offset(osmtest.KothapetaPlot[0], 0, 50), 0, 10, 10)}}}
	_, err = s.SaveDesign(ctx, "farm_k", bad)
	var de *surveyor.DesignError
	if !errors.As(err, &de) || !errors.Is(err, farm.ErrInvalid) || de.Report.Status != land.DesignInvalid {
		t.Errorf("bad design: %v", err)
	}
	rep, err = s.CheckDesign(ctx, "farm_k", bad)
	if err != nil || rep.Status != land.DesignInvalid {
		t.Errorf("check: %v %s", err, rep.Status)
	}
	if _, _, err := s.Suggest(ctx, "farm_h", land.Want{Pond: true}); err != nil {
		t.Errorf("a pond on the sample farm: %v", err)
	}
	if _, _, err := s.Suggest(ctx, "farm_k", land.Want{}); !errors.Is(err, farm.ErrInvalid) {
		t.Errorf("nothing wanted: %v", err)
	}
	if f, err := s.ClearDesign(ctx, "farm_k"); err != nil || f.Design != nil {
		t.Errorf("clear: %v", err)
	}
}

func TestLocateAndFindPlace(t *testing.T) {
	t.Parallel()
	srch := &searcher{}
	s, _ := setup(t, surveyor.WithSearch(srch, "IN"))
	ctx := context.Background()
	in := geo.Centroid(osmtest.KothapetaPlot)
	l, err := s.Locate(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Plots) != 2 || l.Plots[0].Farm != "farm_k" || !l.Plots[0].Inside || l.Plots[0].DistanceM < 40 {
		t.Errorf("inside: %+v", l.Plots)
	}
	east := geo.Offset(osmtest.KothapetaPlot[1], 90, 30)
	l, _ = s.Locate(ctx, east)
	if p := l.Plots[0]; p.Inside || p.Direction != "E" || p.DistanceM < 29 || p.DistanceM > 31 || p.Faces != "E" {
		t.Errorf("30 m east: %+v", p)
	}
	if _, err := s.Locate(ctx, geo.Point{Lat: 100}); !errors.Is(err, farm.ErrInvalid) {
		t.Errorf("bad point: %v", err)
	}
	found, err := s.FindPlace(ctx, "Kothapeta", "farm_k", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].DistanceM < 1000 || found[0].DistanceM > 1300 || found[0].Direction != "N" || srch.near == nil {
		t.Errorf("found %+v near %v", found, srch.near)
	}
	if _, err := surveyor.New(s.Farms()).FindPlace(ctx, "x", "", 1); !errors.Is(err, surveyor.ErrNoGeocoder) {
		t.Errorf("no geocoder: %v", err)
	}
}
