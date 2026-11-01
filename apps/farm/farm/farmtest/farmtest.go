// Package farmtest has an in-memory [farm.Store] for tests, the conformance
// suite every store passes ([RunStore]) and a fake [farm.Locator].
package farmtest

import (
	"context"
	"encoding/json"
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
	"github.com/bsamanvith/agentkit/apps/farm/planting"
)

// Memory is a farm.Store in memory. The zero value is ready to use.
type Memory struct {
	mu     sync.Mutex
	farms  []farm.Farm
	groups []farm.Group
}

var (
	_ farm.Store      = (*Memory)(nil)
	_ farm.GroupStore = (*Memory)(nil)
)

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

// CreateGroup implements farm.GroupStore.
func (m *Memory) CreateGroup(_ context.Context, g farm.Group) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.findGroup(g.ID) >= 0 {
		return fmt.Errorf("%w: group %s exists", farm.ErrInvalid, g.ID)
	}
	m.groups = append(m.groups, cloneGroup(g))
	slices.SortStableFunc(m.groups, func(a, b farm.Group) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return nil
}

// GetGroup implements farm.GroupStore.
func (m *Memory) GetGroup(_ context.Context, id string) (farm.Group, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := m.findGroup(id)
	if i < 0 {
		return farm.Group{}, fmt.Errorf("%w: %s", farm.ErrNotFound, id)
	}
	return cloneGroup(m.groups[i]), nil
}

// ListGroups implements farm.GroupStore.
func (m *Memory) ListGroups(context.Context) ([]farm.Group, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]farm.Group, len(m.groups))
	for i, g := range m.groups {
		out[i] = cloneGroup(g)
	}
	return out, nil
}

// UpdateGroup implements farm.GroupStore.
func (m *Memory) UpdateGroup(_ context.Context, g farm.Group) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := m.findGroup(g.ID)
	if i < 0 {
		return fmt.Errorf("%w: %s", farm.ErrNotFound, g.ID)
	}
	m.groups[i] = cloneGroup(g)
	return nil
}

// DeleteGroup implements farm.GroupStore.
func (m *Memory) DeleteGroup(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := m.findGroup(id)
	if i < 0 {
		return fmt.Errorf("%w: %s", farm.ErrNotFound, id)
	}
	m.groups = slices.Delete(m.groups, i, i+1)
	return nil
}

func (m *Memory) findGroup(id string) int {
	return slices.IndexFunc(m.groups, func(g farm.Group) bool { return g.ID == id })
}

// cloneGroup copies the slices a caller could change, through JSON as the
// stores keep it.
func cloneGroup(g farm.Group) farm.Group {
	b, err := json.Marshal(g)
	if err != nil {
		panic(err)
	}
	var out farm.Group
	if err := json.Unmarshal(b, &out); err != nil {
		panic(err)
	}
	out.CreatedAt, out.UpdatedAt = out.CreatedAt.In(time.UTC), out.UpdatedAt.In(time.UTC)
	return out
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

// SampleGroup is a group of plots with a distribution, created at t.
func SampleGroup(id string, t time.Time, plots ...string) farm.Group {
	return farm.Group{
		ID: id, Name: "Home farm", Plots: append([]string{}, plots...),
		Distribution: &planting.Distribution{
			Arrange: planting.Strips, Rows: planting.NorthSouth,
			Plants: []planting.PlantGroup{
				{ID: "trees", Name: "Border trees", Crops: []string{"mango"}, Place: planting.Border, WidthM: 4},
				{ID: "veg", Name: "Vegetables", Crops: []string{"tomato", "onion"}, Place: planting.Block, Share: 2},
				{ID: "pulse", Name: "Pulses", Crops: []string{"green_gram"}, Place: planting.Block, Share: 1},
			},
		},
		CreatedAt: t, UpdatedAt: t,
	}
}

// RunGroupStore is the conformance suite of farm.GroupStore. newStore
// returns an empty store.
func RunGroupStore(t *testing.T, newStore func(t *testing.T) farm.GroupStore) {
	t.Helper()
	ctx := context.Background()
	t0 := time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)

	t.Run("CreateGetRoundTrips", func(t *testing.T) {
		t.Parallel()
		s := newStore(t)
		want := SampleGroup("g1", t0, "a", "b")
		if err := s.CreateGroup(ctx, want); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetGroup(ctx, "g1")
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("GetGroup (-want +got):\n%s", diff)
		}
		if err := s.CreateGroup(ctx, want); !errors.Is(err, farm.ErrInvalid) {
			t.Errorf("second CreateGroup: %v, want ErrInvalid", err)
		}
	})
	t.Run("MissingIsNotFound", func(t *testing.T) {
		t.Parallel()
		s := newStore(t)
		if _, err := s.GetGroup(ctx, "nope"); !errors.Is(err, farm.ErrNotFound) {
			t.Errorf("GetGroup: %v, want ErrNotFound", err)
		}
		if err := s.UpdateGroup(ctx, SampleGroup("nope", t0)); !errors.Is(err, farm.ErrNotFound) {
			t.Errorf("UpdateGroup: %v, want ErrNotFound", err)
		}
		if err := s.DeleteGroup(ctx, "nope"); !errors.Is(err, farm.ErrNotFound) {
			t.Errorf("DeleteGroup: %v, want ErrNotFound", err)
		}
	})
	t.Run("ListUpdateDelete", func(t *testing.T) {
		t.Parallel()
		s := newStore(t)
		if got, err := s.ListGroups(ctx); err != nil || len(got) != 0 {
			t.Fatalf("empty ListGroups = %v, %v", got, err)
		}
		for _, g := range []farm.Group{SampleGroup("b", t0.Add(time.Hour)), SampleGroup("a", t0)} {
			if err := s.CreateGroup(ctx, g); err != nil {
				t.Fatal(err)
			}
		}
		g := SampleGroup("a", t0, "x")
		g.Name, g.Distribution, g.UpdatedAt = "River fields", nil, t0.Add(time.Minute)
		if err := s.UpdateGroup(ctx, g); err != nil {
			t.Fatal(err)
		}
		got, err := s.ListGroups(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff([]farm.Group{g, SampleGroup("b", t0.Add(time.Hour))}, got); diff != "" {
			t.Errorf("ListGroups, oldest first (-want +got):\n%s", diff)
		}
		if err := s.DeleteGroup(ctx, "a"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetGroup(ctx, "a"); !errors.Is(err, farm.ErrNotFound) {
			t.Errorf("GetGroup after DeleteGroup: %v", err)
		}
	})
	t.Run("CallerCannotChangeStoredGroup", func(t *testing.T) {
		t.Parallel()
		s := newStore(t)
		g := SampleGroup("g", t0, "a")
		if err := s.CreateGroup(ctx, g); err != nil {
			t.Fatal(err)
		}
		g.Plots[0] = "changed"
		g.Distribution.Plants[0].Crops[0] = "changed"
		got, err := s.GetGroup(ctx, "g")
		if err != nil {
			t.Fatal(err)
		}
		got.Plots[0] = "changed again"
		again, err := s.GetGroup(ctx, "g")
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(SampleGroup("g", t0, "a"), again); diff != "" {
			t.Errorf("stored group changed through a caller's copy (-want +got):\n%s", diff)
		}
	})
}
