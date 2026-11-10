package farm_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/farm"
)

func hasCheck(r farm.Report, id string) bool {
	return slices.ContainsFunc(r.Checks, func(c farm.Check) bool { return c.ID == id })
}

func TestAHoldingSeesOnlyItsOwnPlotsAndGroups(t *testing.T) {
	t.Parallel()
	s, _ := service(t)
	bg := context.Background()
	a, b := farm.WithHolding(bg, "h_a"), farm.WithHolding(bg, "h_b")
	draft := farm.Draft{Name: "East field", Boundary: farm.Boundary{Corners: acre()}}
	fa, err := s.Create(a, draft)
	if err != nil {
		t.Fatal(err)
	}
	if fa.Holding != "h_a" {
		t.Errorf("a plot made in holding h_a has holding %q", fa.Holding)
	}
	// The same land in another holding: no warning that names a's plot.
	fb, err := s.Create(b, draft)
	if err != nil {
		t.Fatal(err)
	}
	if hasCheck(fb.Report, farm.CheckOverlap) {
		t.Error("holding b was told about holding a's plot (overlap)")
	}
	if again, _ := s.Verify(a, draft, ""); !hasCheck(again, farm.CheckOverlap) {
		t.Error("holding a was not told about its own overlapping plot")
	}

	list, err := s.List(b)
	if err != nil || len(list) != 1 || list[0].ID != fb.ID {
		t.Errorf("List(b) = %v, %v; want only b's plot", list, err)
	}
	all, _ := s.List(bg)
	if len(all) != 2 {
		t.Errorf("unscoped List = %d plots, want both", len(all))
	}
	for name, err := range map[string]error{
		"Get":        second(s.Get(b, fa.ID)),
		"Update":     second(s.Update(b, fa.ID, draft)),
		"SetProfile": second(s.SetProfile(b, fa.ID, nil)),
		"Delete":     s.Delete(b, fa.ID),
	} {
		if !errors.Is(err, farm.ErrNotFound) {
			t.Errorf("%s of a's plot from b: %v, want ErrNotFound", name, err)
		}
	}
	if _, err := s.Get(a, fa.ID); err != nil {
		t.Errorf("a's plot after b's attempts: %v", err)
	}

	// Groups: b cannot put a's plot in its group, or see a's groups.
	if _, err := s.CreateGroup(b, farm.GroupDraft{Name: "Mine", Plots: []string{fa.ID}}); !errors.Is(err, farm.ErrInvalid) {
		t.Errorf("b grouping a's plot: %v, want ErrInvalid", err)
	}
	ga, err := s.CreateGroup(a, farm.GroupDraft{Name: "Home", Plots: []string{fa.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if gs, _ := s.Groups(b); len(gs) != 0 {
		t.Errorf("b sees groups %v", gs)
	}
	if _, err := s.Group(b, ga.ID); !errors.Is(err, farm.ErrNotFound) {
		t.Errorf("b reading a's group: %v", err)
	}
	if err := s.DeleteGroup(b, ga.ID); !errors.Is(err, farm.ErrNotFound) {
		t.Errorf("b deleting a's group: %v", err)
	}
	if g, err := s.Group(a, ga.ID); err != nil || g.Holding != "h_a" {
		t.Errorf("a's group = %+v, %v", g, err)
	}
}

func TestAdoptGivesPlotsWithoutAHolding(t *testing.T) {
	t.Parallel()
	s, _ := service(t)
	bg := context.Background()
	old, err := s.Create(bg, farm.Draft{Name: "Before sign-in", Boundary: farm.Boundary{Corners: acre()}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateGroup(bg, farm.GroupDraft{Name: "Old group", Plots: []string{old.ID}}); err != nil {
		t.Fatal(err)
	}
	owned, _ := s.Create(farm.WithHolding(bg, "h_x"), farm.Draft{Name: "X's", Boundary: farm.Boundary{Corners: acre()}})
	n, err := s.Adopt(bg, "h_first")
	if err != nil || n != 1 {
		t.Fatalf("Adopt = %d, %v; want the one plot without a holding", n, err)
	}
	h := farm.WithHolding(bg, "h_first")
	if f, err := s.Get(h, old.ID); err != nil || f.Holding != "h_first" {
		t.Errorf("the old plot after Adopt: %+v, %v", f, err)
	}
	if gs, _ := s.Groups(h); len(gs) != 1 {
		t.Errorf("the old group after Adopt: %v", gs)
	}
	if _, err := s.Get(h, owned.ID); !errors.Is(err, farm.ErrNotFound) {
		t.Errorf("Adopt took another holding's plot: %v", err)
	}
}

func second[T any](_ T, err error) error { return err }

func TestAReadOnlyContextChangesNothing(t *testing.T) {
	t.Parallel()
	s, _ := service(t)
	a := farm.WithHolding(context.Background(), "h_a")
	f, err := s.Create(a, farm.Draft{Name: "East", Boundary: farm.Boundary{Corners: acre()}})
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.CreateGroup(a, farm.GroupDraft{Name: "Home", Plots: []string{f.ID}})
	if err != nil {
		t.Fatal(err)
	}
	ro := farm.WithReadOnly(a)
	for name, err := range map[string]error{
		"Create":      second(s.Create(ro, farm.Draft{Name: "New", Boundary: farm.Boundary{Corners: acre()}})),
		"Update":      second(s.Update(ro, f.ID, farm.Draft{Name: "Renamed", Boundary: farm.Boundary{Corners: acre()}})),
		"SetPlan":     second(s.SetPlan(ro, f.ID, nil, nil)),
		"Delete":      s.Delete(ro, f.ID),
		"CreateGroup": second(s.CreateGroup(ro, farm.GroupDraft{Name: "X"})),
		"UpdateGroup": second(s.UpdateGroup(ro, g.ID, farm.GroupDraft{Name: "Y"})),
		"DeleteGroup": s.DeleteGroup(ro, g.ID),
	} {
		if !errors.Is(err, farm.ErrReadOnly) {
			t.Errorf("%s while read-only: %v, want ErrReadOnly", name, err)
		}
	}
	if got, err := s.Get(ro, f.ID); err != nil || got.Name != "East" {
		t.Errorf("reading while read-only: %+v, %v", got, err)
	}
	if list, _ := s.List(ro); len(list) != 1 {
		t.Errorf("List while read-only = %v", list)
	}
}
