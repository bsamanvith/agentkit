package farm_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farm/farmtest"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/land"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

func service(t *testing.T) (*farm.Service, *farmtest.Memory) {
	t.Helper()
	store := &farmtest.Memory{}
	s, err := farm.NewService(store, verifier(), farm.WithServiceClock(clocktest.NewFake(t0)),
		farm.WithRandom(bytes.NewReader(bytes.Repeat([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9}, 100))))
	if err != nil {
		t.Fatal(err)
	}
	return s, store
}

func TestServiceCreatesVerifiedFarms(t *testing.T) {
	t.Parallel()
	s, store := service(t)
	ctx := context.Background()
	f, err := s.Create(ctx, farm.Draft{Name: "  East field ", Declared: &farm.Area{Value: 1, Unit: farm.Acres}, Boundary: farm.Boundary{Corners: acre(), Source: farm.SourceGPS}})
	if err != nil {
		t.Fatal(err)
	}
	if f.ID == "" || f.Name != "East field" || f.Report.Status != farm.StatusVerified || !f.CreatedAt.Equal(t0) {
		t.Errorf("Create = %+v", f)
	}
	got, err := store.Get(ctx, f.ID)
	if err != nil || got.Report.Measures == nil {
		t.Errorf("stored farm = %+v, %v", got, err)
	}
}

func TestServiceRefusesInvalidBoundaries(t *testing.T) {
	t.Parallel()
	s, store := service(t)
	ctx := context.Background()
	_, err := s.Create(ctx, farm.Draft{Name: "Bad", Boundary: farm.Boundary{Corners: acre()[:2]}})
	var inv *farm.InvalidError
	if !errors.As(err, &inv) || !errors.Is(err, farm.ErrInvalid) || inv.Report.Status != farm.StatusInvalid {
		t.Errorf("Create of 2 corners: %v, want an *InvalidError with the report", err)
	}
	if fs, _ := store.List(ctx); len(fs) != 0 {
		t.Errorf("an invalid farm was stored: %+v", fs)
	}
}

func TestServiceRefusesBadDrafts(t *testing.T) {
	t.Parallel()
	s, _ := service(t)
	ctx := context.Background()
	for name, d := range map[string]farm.Draft{
		"no name":        {Name: " ", Boundary: farm.Boundary{Corners: acre()}},
		"long name":      {Name: string(bytes.Repeat([]byte("a"), farm.MaxNameLen+1)), Boundary: farm.Boundary{Corners: acre()}},
		"unknown source": {Name: "x", Boundary: farm.Boundary{Corners: acre(), Source: "drone"}},
		"unknown unit":   {Name: "x", Boundary: farm.Boundary{Corners: acre()}, Declared: &farm.Area{Value: 2, Unit: "bigha"}},
	} {
		if _, err := s.Create(ctx, d); !errors.Is(err, farm.ErrInvalid) {
			t.Errorf("%s: Create: %v, want ErrInvalid", name, err)
		}
	}
	// Verifying needs no name.
	if _, err := s.Verify(ctx, farm.Draft{Boundary: farm.Boundary{Corners: acre()}}, ""); err != nil {
		t.Errorf("Verify without a name: %v", err)
	}
}

func TestServiceChecksOverlapsWithOtherFarmsOnly(t *testing.T) {
	t.Parallel()
	s, _ := service(t)
	ctx := context.Background()
	f, err := s.Create(ctx, farm.Draft{Name: "East", Boundary: farm.Boundary{Corners: acre()}})
	if err != nil {
		t.Fatal(err)
	}
	// Moving the farm a little overlaps where it was, which is not another farm.
	moved := acre()
	for i := range moved {
		moved[i].Lng += 10 * lngPerM
	}
	u, err := s.Update(ctx, f.ID, farm.Draft{Name: "East", Boundary: farm.Boundary{Corners: moved}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := check(u.Report, farm.CheckOverlap); ok {
		t.Errorf("a farm overlaps itself: %+v", u.Report.Checks)
	}
	// A new farm on top of it does overlap.
	rep, err := s.Verify(ctx, farm.Draft{Boundary: farm.Boundary{Corners: acre()}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := check(rep, farm.CheckOverlap); !ok {
		t.Errorf("no overlap with the saved farm: %+v", rep.Checks)
	}
	if _, err := s.Update(ctx, "farm_missing", farm.Draft{Name: "x", Boundary: farm.Boundary{Corners: acre()}}); !errors.Is(err, farm.ErrNotFound) {
		t.Errorf("Update of a missing farm: %v, want ErrNotFound", err)
	}
	if _, err := s.Update(ctx, f.ID, farm.Draft{Name: "x", Boundary: farm.Boundary{Corners: corners(geo.Point{Lat: 1, Lng: 1})}}); !errors.Is(err, farm.ErrInvalid) {
		t.Errorf("Update to an invalid boundary: %v, want ErrInvalid", err)
	}
}

func TestServiceKeepsProfileAndPlanAcrossEdits(t *testing.T) {
	t.Parallel()
	s, _ := service(t)
	ctx := context.Background()
	d := farm.Draft{Name: "East field", Boundary: farm.Boundary{Corners: acre(), Source: farm.SourceGPS}}
	f, err := s.Create(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	prof := &agro.Profile{Soil: "black", PreviousCrop: "cotton"}
	plan := &agro.Plan{Zones: []agro.Zone{{Crop: "chickpea", Sow: "2026-11-10"}}}
	if _, err := s.SetProfile(ctx, f.ID, prof); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPlan(ctx, f.ID, plan, &agro.PlanReport{Status: agro.PlanSound}); err != nil {
		t.Fatal(err)
	}
	d.Name = "East field (north)"
	if _, err := s.Update(ctx, f.ID, d); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Profile == nil || got.Profile.Soil != "black" || got.Plan == nil || got.PlanReport == nil || got.Name != d.Name {
		t.Errorf("after an edit: profile %+v, plan %+v, name %q", got.Profile, got.Plan, got.Name)
	}
	if _, err := s.SetPlan(ctx, f.ID, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, f.ID); got.Plan != nil || got.PlanReport != nil {
		t.Error("the plan was not cleared")
	}
	if _, err := s.SetProfile(ctx, "farm_missing", prof); !errors.Is(err, farm.ErrNotFound) {
		t.Errorf("missing farm: err %v, want ErrNotFound", err)
	}
}

func TestServiceSurveyAndDesign(t *testing.T) {
	t.Parallel()
	s, _ := service(t)
	ctx := context.Background()
	d := farm.Draft{Name: "North field", Boundary: farm.Boundary{Corners: acre(), Source: farm.SourceMap}}
	f, err := s.Create(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	sv := &land.Survey{RadiusM: 450}
	design := &land.Design{Elements: []land.Element{{Kind: land.Pond}}}
	if _, err := s.SetSurvey(ctx, f.ID, sv); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetDesign(ctx, f.ID, design, &land.DesignReport{Status: land.DesignSound}); err != nil {
		t.Fatal(err)
	}
	// A rename keeps both.
	d.Name = "North field (old well)"
	got, err := s.Update(ctx, f.ID, d)
	if err != nil {
		t.Fatal(err)
	}
	if got.Survey == nil || got.Design == nil || got.DesignReport == nil {
		t.Fatalf("after a rename: survey %v design %v", got.Survey, got.Design)
	}
	// A new boundary drops the survey, which was of the old one; the
	// design stays for the farmer to check again.
	moved := slices.Clone(d.Boundary.Corners)
	moved[0].Lat += 0.00005
	d.Boundary.Corners = moved
	got, err = s.Update(ctx, f.ID, d)
	if err != nil {
		t.Fatal(err)
	}
	if got.Survey != nil || got.Design == nil {
		t.Errorf("after a boundary change: survey %v design %v", got.Survey, got.Design)
	}
	if _, err := s.SetSurvey(ctx, "farm_nope", sv); !errors.Is(err, farm.ErrNotFound) {
		t.Errorf("unknown farm: %v", err)
	}
}
