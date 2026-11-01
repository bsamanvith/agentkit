package planner_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/planting"
)

func TestDistribution(t *testing.T) {
	t.Parallel()
	p, id := setup(t, nil)
	ctx := context.Background()
	svc := p.Farms()
	g, err := svc.CreateGroup(ctx, farm.GroupDraft{Name: "Home farm", Plots: []string{id}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Distribute(ctx, g.ID, nil); !errors.Is(err, planting.ErrInvalid) {
		t.Errorf("no distribution yet: %v, want ErrInvalid", err)
	}
	d := planting.Distribution{Plants: []planting.PlantGroup{
		{ID: "trees", Name: "Mango border", Crops: []string{"mango"}, Place: planting.Border},
		{ID: "veg", Name: "Vegetables", Crops: []string{"tomato"}},
	}}
	saved, res, err := p.SaveDistribution(ctx, g.ID, d)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Plots) != 1 || len(res.Plots[0].Regions) != 2 || res.Plots[0].Name != "Black field" {
		t.Errorf("layout %+v", res.Plots)
	}
	// Saved with its defaults filled in.
	if saved.Distribution == nil || saved.Distribution.Arrange != planting.Strips || saved.Distribution.Plants[0].WidthM != planting.DefaultBorderM {
		t.Errorf("saved %+v", saved.Distribution)
	}
	again, err := p.Distribute(ctx, g.ID, nil)
	if err != nil || again.AreaM2 != res.AreaM2 {
		t.Errorf("the saved distribution laid out again: %+v, %v", again, err)
	}
	if _, _, err := p.SaveDistribution(ctx, g.ID, planting.Distribution{}); !errors.Is(err, planting.ErrInvalid) {
		t.Errorf("an empty distribution saved: %v", err)
	}
	if _, err := svc.UpdateGroup(ctx, g.ID, farm.GroupDraft{Name: "Home farm"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Distribute(ctx, g.ID, nil); !errors.Is(err, planting.ErrInvalid) {
		t.Errorf("a group with no plots: %v, want ErrInvalid", err)
	}
	cleared, err := p.ClearDistribution(ctx, g.ID)
	if err != nil || cleared.Distribution != nil {
		t.Errorf("ClearDistribution = %+v, %v", cleared, err)
	}
	if _, err := p.Distribute(ctx, "group_nope", nil); !errors.Is(err, farm.ErrNotFound) {
		t.Errorf("unknown group: %v, want ErrNotFound", err)
	}
}
