// Package farmtest has an in-memory [farm.Store] for tests, the conformance
// suite every store passes ([RunStore]) and a fake [farm.Locator].
package farmtest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
)

// Memory is a farm.Store in memory. The zero value is ready to use.
type Memory struct {
	mu    sync.Mutex
	farms []farm.Farm
}

var _ farm.Store = (*Memory)(nil)

// Create implements farm.Store.
func (m *Memory) Create(_ context.Context, f farm.Farm) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.find(f.ID) >= 0 {
		return fmt.Errorf("%w: farm %s exists", farm.ErrInvalid, f.ID)
	}
	m.farms = append(m.farms, clone(f))
	slices.SortStableFunc(m.farms, func(a, b farm.Farm) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return nil
}

// Get implements farm.Store.
func (m *Memory) Get(_ context.Context, id string) (farm.Farm, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := m.find(id)
	if i < 0 {
		return farm.Farm{}, fmt.Errorf("%w: %s", farm.ErrNotFound, id)
	}
	return clone(m.farms[i]), nil
}

// List implements farm.Store.
func (m *Memory) List(context.Context) ([]farm.Farm, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]farm.Farm, len(m.farms))
	for i, f := range m.farms {
		out[i] = clone(f)
	}
	return out, nil
}

// Update implements farm.Store.
func (m *Memory) Update(_ context.Context, f farm.Farm) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := m.find(f.ID)
	if i < 0 {
		return fmt.Errorf("%w: %s", farm.ErrNotFound, f.ID)
	}
	m.farms[i] = clone(f)
	return nil
}

// Delete implements farm.Store.
func (m *Memory) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := m.find(id)
	if i < 0 {
		return fmt.Errorf("%w: %s", farm.ErrNotFound, id)
	}
	m.farms = slices.Delete(m.farms, i, i+1)
	return nil
}

func (m *Memory) find(id string) int {
	return slices.IndexFunc(m.farms, func(f farm.Farm) bool { return f.ID == id })
}

// clone copies the slices a caller could change.
func clone(f farm.Farm) farm.Farm {
	f.Boundary.Corners = slices.Clone(f.Boundary.Corners)
	f.Report.Checks = slices.Clone(f.Report.Checks)
	if f.Declared != nil {
		d := *f.Declared
		f.Declared = &d
	}
	return f
}

// Locator is a fake farm.Locator: it answers Location, or Err if set, and
// counts its calls.
type Locator struct {
	Location farm.Location
	Err      error

	mu    sync.Mutex
	calls int
}

// Locate implements farm.Locator.
func (l *Locator) Locate(ctx context.Context, _ geo.Point) (farm.Location, error) {
	l.mu.Lock()
	l.calls++
	l.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return farm.Location{}, err
	}
	if l.Err != nil {
		return farm.Location{}, l.Err
	}
	return l.Location, nil
}

// Calls returns how many times Locate was called.
func (l *Locator) Calls() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

// Sample returns a valid farm: a plot of about 1 acre near Hyderabad.
func Sample(id string, created time.Time) farm.Farm {
	return farm.Farm{
		ID:       id,
		Name:     "East field " + id,
		Declared: &farm.Area{Value: 1, Unit: farm.Acres},
		Boundary: farm.Boundary{Source: farm.SourceMap, Corners: []farm.Corner{
			{Point: geo.Point{Lat: 17.385, Lng: 78.4867}},
			{Point: geo.Point{Lat: 17.385, Lng: 78.48733}, AccuracyM: 4},
			{Point: geo.Point{Lat: 17.38557, Lng: 78.48733}},
			{Point: geo.Point{Lat: 17.38557, Lng: 78.4867}},
		}},
		Report: farm.Report{
			Status:    farm.StatusVerified,
			Measures:  &farm.Measures{AreaM2: 4046, Hectares: 0.4046, AcresText: "1 acre"},
			Location:  &farm.Location{Village: "Nampally", State: "Telangana", CountryCode: "IN"},
			Checks:    []farm.Check{{ID: farm.CheckDeclared, Severity: farm.SeverityInfo, Message: "matches"}},
			CheckedAt: created,
		},
		Profile: &agro.Profile{Soil: "red_loam", Water: []agro.Water{{Kind: agro.Borewell, Reliable: true}}, PreviousCrop: "maize"},
		Plan: &agro.Plan{Zones: []agro.Zone{{
			Crop: "groundnut", With: "pigeon_pea", Pattern: [2]int{4, 1}, Sow: "2027-06-20",
			Ring: geo.Ring{{Lat: 17.385, Lng: 78.4867}, {Lat: 17.385, Lng: 78.48733}, {Lat: 17.38557, Lng: 78.48733}},
		}}},
		PlanReport: &agro.PlanReport{Status: agro.PlanSound, Checks: []agro.PlanCheck{}, Zones: []agro.ZoneMeasures{{AreaM2: 2023}}},
		CreatedAt:  created,
		UpdatedAt:  created,
	}
}

// RunStore is the conformance suite of farm.Store: every implementation
// passes it. newStore returns an empty store.
func RunStore(t *testing.T, newStore func(t *testing.T) farm.Store) {
	t.Helper()
	ctx := context.Background()
	t0 := time.Date(2026, 10, 31, 6, 0, 0, 0, time.UTC)

	t.Run("CreateGetRoundTrips", func(t *testing.T) {
		t.Parallel()
		s := newStore(t)
		want := Sample("a", t0)
		if err := s.Create(ctx, want); err != nil {
			t.Fatal(err)
		}
		got, err := s.Get(ctx, "a")
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("Get (-want +got):\n%s", diff)
		}
	})
	t.Run("CreateTakenIDIsInvalid", func(t *testing.T) {
		t.Parallel()
		s := newStore(t)
		if err := s.Create(ctx, Sample("a", t0)); err != nil {
			t.Fatal(err)
		}
		if err := s.Create(ctx, Sample("a", t0)); !errors.Is(err, farm.ErrInvalid) {
			t.Errorf("second Create: %v, want ErrInvalid", err)
		}
	})
	t.Run("MissingIsNotFound", func(t *testing.T) {
		t.Parallel()
		s := newStore(t)
		if _, err := s.Get(ctx, "nope"); !errors.Is(err, farm.ErrNotFound) {
			t.Errorf("Get: %v, want ErrNotFound", err)
		}
		if err := s.Update(ctx, Sample("nope", t0)); !errors.Is(err, farm.ErrNotFound) {
			t.Errorf("Update: %v, want ErrNotFound", err)
		}
		if err := s.Delete(ctx, "nope"); !errors.Is(err, farm.ErrNotFound) {
			t.Errorf("Delete: %v, want ErrNotFound", err)
		}
	})
	t.Run("ListIsOldestFirst", func(t *testing.T) {
		t.Parallel()
		s := newStore(t)
		if got, err := s.List(ctx); err != nil || len(got) != 0 {
			t.Fatalf("empty List = %v, %v; want none", got, err)
		}
		for _, f := range []farm.Farm{Sample("b", t0.Add(time.Hour)), Sample("a", t0), Sample("c", t0.Add(2*time.Hour))} {
			if err := s.Create(ctx, f); err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, f := range got {
			ids = append(ids, f.ID)
		}
		if diff := cmp.Diff([]string{"a", "b", "c"}, ids); diff != "" {
			t.Errorf("List IDs (-want +got):\n%s", diff)
		}
	})
	t.Run("UpdateAndDelete", func(t *testing.T) {
		t.Parallel()
		s := newStore(t)
		f := Sample("a", t0)
		if err := s.Create(ctx, f); err != nil {
			t.Fatal(err)
		}
		f.Name = "West field"
		f.Boundary.Corners = f.Boundary.Corners[:3]
		f.UpdatedAt = t0.Add(time.Minute)
		if err := s.Update(ctx, f); err != nil {
			t.Fatal(err)
		}
		got, err := s.Get(ctx, "a")
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(f, got); diff != "" {
			t.Errorf("after Update (-want +got):\n%s", diff)
		}
		if err := s.Delete(ctx, "a"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Get(ctx, "a"); !errors.Is(err, farm.ErrNotFound) {
			t.Errorf("Get after Delete: %v, want ErrNotFound", err)
		}
	})
	t.Run("CallerCannotChangeStoredFarm", func(t *testing.T) {
		t.Parallel()
		s := newStore(t)
		f := Sample("a", t0)
		if err := s.Create(ctx, f); err != nil {
			t.Fatal(err)
		}
		f.Boundary.Corners[0].Lat = 0
		got, err := s.Get(ctx, "a")
		if err != nil {
			t.Fatal(err)
		}
		got.Boundary.Corners[1].Lat = 0
		again, err := s.Get(ctx, "a")
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(Sample("a", t0), again); diff != "" {
			t.Errorf("stored farm changed through a caller's copy (-want +got):\n%s", diff)
		}
	})
	t.Run("ConcurrentCreates", func(t *testing.T) {
		t.Parallel()
		s := newStore(t)
		var wg sync.WaitGroup
		for i := range 20 {
			wg.Go(func() {
				if err := s.Create(ctx, Sample(fmt.Sprint("f", i), t0.Add(time.Duration(i)*time.Second))); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		got, err := s.List(ctx)
		if err != nil || len(got) != 20 {
			t.Errorf("List after 20 concurrent creates: %d farms, %v", len(got), err)
		}
	})
}
