package farmagent

import (
	"context"
	"errors"
	"math"

	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/planner"
	"github.com/bsamanvith/agentkit/apps/farm/planting"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// The plot group tools' names.
const (
	ListGroupsTool       = "list_groups"
	SaveGroupTool        = "save_group"
	DistributePlantsTool = "distribute_plants"
	SaveDistributionTool = "save_distribution"
)

// GroupSystem is added to the system prompt with the group tools.
const GroupSystem = `
The farmer can keep plots in groups (the home farm, the fields by the river), as many groups as they like, a plot in one group at a time. Each group has its own plant groups (fruit trees, vegetables, fodder, ...) and its own distribution: where each plant group goes on the group's plots (blocks, a border band, rows of trees with crops in the alleys) and how the blocks are cut (strips by share, alternating strips, whole plots).

- Lay a distribution out with distribute_plants before you advise on it: the areas, rows, plant and tree counts, and its effects (shade hour by hour on the shortest day, edges, how many trees the shape fits, water, neighbours, wind shelter, what a pond takes) come from the tool. Never estimate a shadow, an area or a count yourself.
- Change one thing at a time and lay it out again to compare (north-south rows against the long side, alternating strips against blocks, a narrower border): say what changed in numbers.
- Save with save_distribution, and create or change a group with save_group, only when the farmer agrees.`

type groupOut struct {
	ID           string                 `json:"id"`
	Name         string                 `json:"name"`
	Plots        []groupPlot            `json:"plots"`
	Distribution *planting.Distribution `json:"distribution,omitempty"`
}

type groupPlot struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	AreaM2 float64 `json:"area_m2"`
}

type saveGroupIn struct {
	ID    string   `json:"id,omitempty" jsonschema:"the group's id to change it; leave out to create a new group"`
	Name  string   `json:"name" jsonschema:"the group's name, such as Home farm"`
	Plots []string `json:"plots" jsonschema:"the farm ids of every plot in the group (from list_farms); a plot can be in one group only"`
}

type distributeIn struct {
	Group        string                 `json:"group" jsonschema:"the group's id, from list_groups"`
	Distribution *planting.Distribution `json:"distribution,omitempty" jsonschema:"the distribution to lay out; leave out to lay out the group's saved one"`
}

type saveDistributionIn struct {
	Group        string                `json:"group" jsonschema:"the group's id, from list_groups"`
	Distribution planting.Distribution `json:"distribution" jsonschema:"the distribution to save, as laid out with distribute_plants"`
}

type distributionOut struct {
	Group  string             `json:"group"`
	Status planting.Status    `json:"status"`
	AreaM2 float64            `json:"area_m2"`
	Plants []planting.Measure `json:"plants"`
	Plots  []plotOut          `json:"plots"`
	// Effects, errors and warnings first.
	Effects []planting.Effect `json:"effects"`
	// Raw is the distribution with its defaults: save this one.
	Raw   planting.Distribution `json:"raw"`
	Saved bool                  `json:"saved,omitempty"`
}

type plotOut struct {
	Plot       string     `json:"plot"`
	Name       string     `json:"name"`
	RowBearing float64    `json:"row_bearing"`
	Pieces     []pieceOut `json:"pieces"`
}

type pieceOut struct {
	Plant   string  `json:"plant"`
	AreaM2  float64 `json:"area_m2"`
	LengthM float64 `json:"length_m"`
	WidthM  float64 `json:"width_m"`
	Rows    int     `json:"rows"`
	Trees   int     `json:"trees,omitempty"`
	Centre  string  `json:"centre"`
}

func describeDistribution(id string, r planting.Result) distributionOut {
	out := distributionOut{Group: id, Status: r.Status, AreaM2: math.Round(r.AreaM2), Plants: r.Plants, Effects: r.Effects, Raw: r.Distribution, Plots: []plotOut{}}
	for _, p := range r.Plots {
		po := plotOut{Plot: p.Plot, Name: p.Name, RowBearing: p.RowBearing, Pieces: []pieceOut{}}
		for _, g := range p.Regions {
			po.Pieces = append(po.Pieces, pieceOut{Plant: g.Plant, AreaM2: math.Round(g.AreaM2), LengthM: g.LengthM, WidthM: g.WidthM, Rows: g.Rows, Trees: len(g.Trees), Centre: ll(geo.Centroid(g.Ring))})
		}
		out.Plots = append(out.Plots, po)
	}
	return out
}

func describeGroup(ctx context.Context, svc *farm.Service, g farm.Group) groupOut {
	out := groupOut{ID: g.ID, Name: g.Name, Plots: []groupPlot{}, Distribution: g.Distribution}
	for _, id := range g.Plots {
		gp := groupPlot{ID: id}
		if f, err := svc.Get(ctx, id); err == nil {
			gp.Name = f.Name
			if m := f.Report.Measures; m != nil {
				gp.AreaM2 = math.Round(m.AreaM2)
			}
		}
		out.Plots = append(out.Plots, gp)
	}
	return out
}

// GroupTools returns the plot group tools over p: list_groups and
// distribute_plants, which read, and save_group and save_distribution,
// which save what the farmer agreed to.
func GroupTools(p *planner.Planner) ([]tool.Tool, error) {
	svc := p.Farms()
	var tools []tool.Tool
	add := func(t tool.Tool, err error) error {
		if err == nil {
			tools = append(tools, t)
		}
		return err
	}
	err := errors.Join(
		add(tool.Func(ListGroupsTool, "List the farmer's plot groups: each group's plots (id, name, area) and its saved plant groups and distribution, if any. Plots in no group are not listed: see list_farms.",
			func(ctx context.Context, _ struct{}) (map[string][]groupOut, error) {
				gs, err := svc.Groups(ctx)
				if err != nil {
					return nil, err
				}
				out := []groupOut{}
				for _, g := range gs {
					out = append(out, describeGroup(ctx, svc, g))
				}
				return map[string][]groupOut{"groups": out}, nil
			}, read)),
		add(tool.Func(SaveGroupTool, "Create a plot group, or change one (give its id): its name and all its plots. A plot is in one group at a time: take it out of the other group first. Only when the farmer agrees.",
			func(ctx context.Context, in saveGroupIn) (groupOut, error) {
				d := farm.GroupDraft{Name: in.Name, Plots: in.Plots}
				var g farm.Group
				var err error
				if in.ID == "" {
					g, err = svc.CreateGroup(ctx, d)
				} else {
					g, err = svc.UpdateGroup(ctx, in.ID, d)
				}
				if err != nil {
					return groupOut{}, err
				}
				return describeGroup(ctx, svc, g), nil
			}, write)),
		add(tool.Func(DistributePlantsTool, "Lay out a group's plant groups on its plots and work out the effects. Each plant group has crops (ids from list_crops, the main one first, more in alternating rows) and a place: block (a share of the land), border (a band round each plot) or alley (rows of trees every every_m with crops between). Blocks are cut as strips by share, alternating strips of strip_m, or whole plots (by_plot); rows run along the long side, north_south or east_west. Returns each plant group's area, share, plant and tree counts, edge share and shaded area; each plot's pieces; and the effects: shade through the shortest day, edges and narrow pieces, how many trees fit, water against the pond or canal, neighbours of one family or good pairs, wind shelter, what the farm pond takes. Saves nothing.",
			func(ctx context.Context, in distributeIn) (distributionOut, error) {
				r, err := p.Distribute(ctx, in.Group, in.Distribution)
				if err != nil {
					return distributionOut{}, invalidIf(DistributePlantsTool, err)
				}
				return describeDistribution(in.Group, r), nil
			}, read)),
		add(tool.Func(SaveDistributionTool, "Save a group's plant groups and distribution once the farmer agrees; it shows on the group's page in the app.",
			func(ctx context.Context, in saveDistributionIn) (distributionOut, error) {
				_, r, err := p.SaveDistribution(ctx, in.Group, in.Distribution)
				if err != nil {
					return distributionOut{}, invalidIf(SaveDistributionTool, err)
				}
				out := describeDistribution(in.Group, r)
				out.Saved = true
				return out, nil
			}, write)),
	)
	if err != nil {
		return nil, err
	}
	return tools, nil
}

// invalidIf marks a distribution the rules refuse as the model's mistake,
// so it reads the message and fixes its input.
func invalidIf(name string, err error) error {
	if errors.Is(err, planting.ErrInvalid) || errors.Is(err, farm.ErrInvalid) {
		return &tool.Error{Tool: name, Kind: tool.ErrInvalidInput, Err: err}
	}
	return err
}
