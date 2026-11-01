package farm_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farm/farmtest"
	"github.com/bsamanvith/agentkit/apps/farm/planting"
)

func TestGroups(t *testing.T) {
	t.Parallel()
	s, store := service(t)
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c"} {
		if err := store.Create(ctx, farmtest.Sample(id, t0)); err != nil {
			t.Fatal(err)
		}
	}
	home, err := s.CreateGroup(ctx, farm.GroupDraft{Name: " Home farm ", Plots: []string{"a", "b", "a"}})
	if err != nil {
		t.Fatal(err)
	}
	if home.Name != "Home farm" || !slices.Equal(home.Plots, []string{"a", "b"}) || home.ID == "" || !home.CreatedAt.Equal(t0) {
		t.Errorf("CreateGroup = %+v", home)
	}
	// Many groups, but a plot is in one at a time.
	if _, err := s.CreateGroup(ctx, farm.GroupDraft{Name: "River", Plots: []string{"b", "c"}}); !errors.Is(err, farm.ErrInvalid) {
		t.Errorf("a plot in two groups: %v, want ErrInvalid", err)
	}
	river, err := s.CreateGroup(ctx, farm.GroupDraft{Name: "River", Plots: []string{"c"}})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := s.CreateGroup(ctx, farm.GroupDraft{Name: "Leased next year"})
	if err != nil || empty.Plots == nil {
		t.Errorf("an empty group: %+v, %v", empty, err)
	}
	for name, d := range map[string]farm.GroupDraft{
		"no name":      {Plots: []string{"a"}},
		"unknown plot": {Name: "X", Plots: []string{"nope"}},
	} {
		if _, err := s.CreateGroup(ctx, d); !errors.Is(err, farm.ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	// Moving a plot: out of one group, then into another.
	if _, err := s.UpdateGroup(ctx, home.ID, farm.GroupDraft{Name: "Home farm", Plots: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	if river, err = s.UpdateGroup(ctx, river.ID, farm.GroupDraft{Name: "River fields", Plots: []string{"c", "b"}}); err != nil {
		t.Fatal(err)
	}
	if g, ok, err := s.GroupOf(ctx, "b"); err != nil || !ok || g.ID != river.ID {
		t.Errorf("GroupOf(b) = %+v, %v, %v; want the river group", g, ok, err)
	}
	d := &planting.Distribution{Plants: []planting.PlantGroup{{ID: "veg", Crops: []string{"tomato"}}}}
	if _, err := s.SetDistribution(ctx, river.ID, d); err != nil {
		t.Fatal(err)
	}
	// Deleting a plot takes it out of its group; the group stays.
	if err := s.Delete(ctx, "c"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Group(ctx, river.ID)
	if err != nil || !slices.Equal(got.Plots, []string{"b"}) || got.Distribution == nil {
		t.Errorf("after deleting plot c: %+v, %v", got, err)
	}
	if err := s.DeleteGroup(ctx, home.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "a"); err != nil {
		t.Errorf("deleting a group deleted its plot: %v", err)
	}
	if gs, err := s.Groups(ctx); err != nil || len(gs) != 2 {
		t.Errorf("Groups = %+v, %v", gs, err)
	}
	if _, err := s.Group(ctx, home.ID); !errors.Is(err, farm.ErrNotFound) {
		t.Errorf("deleted group: %v, want ErrNotFound", err)
	}
}

// A store without groups: the service says so.
type farmsOnly struct{ farm.Store }

func TestNoGroupStore(t *testing.T) {
	t.Parallel()
	s, err := farm.NewService(farmsOnly{&farmtest.Memory{}}, verifier())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateGroup(context.Background(), farm.GroupDraft{Name: "x"}); !errors.Is(err, farm.ErrNoGroups) {
		t.Errorf("CreateGroup: %v, want ErrNoGroups", err)
	}
	if gs, err := s.Groups(context.Background()); err != nil || len(gs) != 0 {
		t.Errorf("Groups = %v, %v; want none", gs, err)
	}
}
