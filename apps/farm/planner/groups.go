package planner

import (
	"context"
	"fmt"

	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/planting"
)

// Distribute lays a distribution out on a group's plots and works out its
// effects; d nil lays out the group's saved one. The plots' surveys and
// layouts (as saved) tell where the water is and what the pond and shed
// take.
func (p *Planner) Distribute(ctx context.Context, groupID string, d *planting.Distribution) (planting.Result, error) {
	g, err := p.farms.Group(ctx, groupID)
	if err != nil {
		return planting.Result{}, err
	}
	return p.distribute(ctx, g, d)
}

func (p *Planner) distribute(ctx context.Context, g farm.Group, d *planting.Distribution) (planting.Result, error) {
	if d == nil {
		d = g.Distribution
	}
	if d == nil {
		return planting.Result{}, fmt.Errorf("%w: the group %s has no distribution yet", planting.ErrInvalid, g.Name)
	}
	if len(g.Plots) == 0 {
		return planting.Result{}, fmt.Errorf("%w: the group %s has no plots: add some first", planting.ErrInvalid, g.Name)
	}
	plots := make([]planting.Plot, 0, len(g.Plots))
	for _, id := range g.Plots {
		f, err := p.farms.Get(ctx, id)
		if err != nil {
			return planting.Result{}, err
		}
		plots = append(plots, planting.Plot{ID: f.ID, Name: f.Name, Ring: f.Boundary.Ring(), Survey: f.Survey, Design: f.Design})
	}
	return planting.Distribute(p.lib, plots, *d)
}

// SaveDistribution lays a distribution out on a group's plots and saves it
// (with its defaults filled in) when it can be laid out; it returns the
// group and the layout.
func (p *Planner) SaveDistribution(ctx context.Context, groupID string, d planting.Distribution) (farm.Group, planting.Result, error) {
	res, err := p.Distribute(ctx, groupID, &d)
	if err != nil {
		return farm.Group{}, planting.Result{}, err
	}
	g, err := p.farms.SetDistribution(ctx, groupID, &res.Distribution)
	if err != nil {
		return farm.Group{}, planting.Result{}, err
	}
	return g, res, nil
}

// ClearDistribution removes a group's distribution.
func (p *Planner) ClearDistribution(ctx context.Context, groupID string) (farm.Group, error) {
	return p.farms.SetDistribution(ctx, groupID, nil)
}
