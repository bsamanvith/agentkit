package farmagent

import (
	"context"
	"errors"
	"fmt"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/planner"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// The planning tools' names.
const (
	ListSoilsTool      = "list_soils"
	ListCropsTool      = "list_crops"
	GetCropTool        = "get_crop"
	GetClimateTool     = "get_climate"
	SetProfileTool     = "set_profile"
	RecommendCropsTool = "recommend_crops"
	SuggestLayoutTool  = "suggest_layout"
	CheckPlanTool      = "check_plan"
	SavePlanTool       = "save_plan"
)

type soilCard struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Names    map[string]string `json:"names,omitempty"`
	Signs    string            `json:"signs"`
	Texture  agro.Texture      `json:"texture"`
	PH       agro.Range        `json:"ph"`
	Drainage string            `json:"drainage"`
	Suits    string            `json:"suits"`
}

type cropCard struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Names   map[string]string `json:"names,omitempty"`
	Kind    string            `json:"kind"`
	Days    agro.Range        `json:"days"`
	Seasons []string          `json:"seasons"`
	Analog  bool              `json:"climate_analog,omitempty"`
}

type kindIn struct {
	Kind string `json:"kind,omitempty" jsonschema:"only crops of this kind: cereal, millet, pulse, oilseed, vegetable, fruit, fibre, cash, spice, ..."`
}

type idIn struct {
	ID string `json:"id" jsonschema:"the crop's id, from list_crops"`
}

type farmIn struct {
	Farm string `json:"farm" jsonschema:"the farm's id, from list_farms"`
}

type climateOut struct {
	Today string `json:"today"`
	agro.Climate
	RainMM float64 `json:"year_rain_mm"`
}

type profileIn struct {
	Farm string `json:"farm" jsonschema:"the farm's id, from list_farms"`
	agro.Profile
}

type recommendIn struct {
	Farm   string   `json:"farm" jsonschema:"the farm's id, from list_farms"`
	Around string   `json:"around,omitempty" jsonschema:"roughly when the farmer wants to sow, as YYYY-MM-DD; leave out for the next good time"`
	Spread int      `json:"spread,omitempty" jsonschema:"how many days either side of around count (default 45)"`
	Crops  []string `json:"crops,omitempty" jsonschema:"only these crop ids, when the farmer asks about particular crops"`
	Limit  int      `json:"limit,omitempty" jsonschema:"how many options at most (default 5)"`
}

type recommendOut struct {
	Farm    string        `json:"farm"`
	Today   string        `json:"today"`
	From    string        `json:"from,omitempty"`
	To      string        `json:"to,omitempty"`
	Soil    string        `json:"soil,omitempty"`
	Options []agro.Option `json:"options"`
	Notes   []agro.Note   `json:"notes,omitempty"`
}

type partsIn struct {
	Farm  string      `json:"farm" jsonschema:"the farm's id, from list_farms"`
	Parts []agro.Part `json:"parts" jsonschema:"the crops in order across the plot: crop id, optional second crop 'with' in rows between it and its row 'pattern' such as [4, 1], 'share' of the plot from 0 to 1, and the 'sow' date as YYYY-MM-DD"`
}

type checkIn struct {
	Farm  string      `json:"farm" jsonschema:"the farm's id, from list_farms"`
	Parts []agro.Part `json:"parts,omitempty" jsonschema:"a plan to check, as for suggest_layout; leave out to check the farm's saved plan"`
}

type planOut struct {
	Plan   agro.Plan       `json:"plan"`
	Report agro.PlanReport `json:"report"`
	Saved  bool            `json:"saved,omitempty"`
}

var write = tool.WithMetadata(tool.Metadata{Kind: tool.KindWrite, Risk: tool.RiskLow})

// PlanningTools returns the crop planning tools over p: list_soils,
// list_crops, get_crop, get_climate, recommend_crops, suggest_layout and
// check_plan, which read, and set_profile and save_plan, which save what
// the farmer decided.
func PlanningTools(p *planner.Planner) ([]tool.Tool, error) {
	lib := p.Library()
	var tools []tool.Tool
	add := func(t tool.Tool, err error) error {
		if err == nil {
			tools = append(tools, t)
		}
		return err
	}
	err := errors.Join(
		add(tool.Func(ListSoilsTool, "List the soil types the app knows, with how a farmer recognises each (colour, cracks, stickiness): show them to a farmer who does not know their soil.",
			func(context.Context, struct{}) (map[string][]soilCard, error) {
				var out []soilCard
				for _, s := range lib.Soils() {
					out = append(out, soilCard{s.ID, s.Name, s.Names, s.Signs, s.Texture, s.PH, s.Drainage, s.Suits})
				}
				return map[string][]soilCard{"soils": out}, nil
			}, read)),
		add(tool.Func(ListCropsTool, "List the crops the app can plan, with their kind, days to harvest and Indian seasons.",
			func(_ context.Context, in kindIn) (map[string][]cropCard, error) {
				out := []cropCard{}
				for _, c := range lib.Crops() {
					if in.Kind == "" || c.Kind == in.Kind {
						out = append(out, cropCard{c.ID, c.Name, c.Names, c.Kind, c.Days, c.Seasons, c.ClimateAnalog})
					}
				}
				return map[string][]cropCard{"crops": out}, nil
			}, read)),
		add(tool.Func(GetCropTool, "Get everything the app knows about a crop: temperatures, water, soil, spacing, seasons, intercrops.",
			func(_ context.Context, in idIn) (agro.Crop, error) { return lib.Crop(in.ID) }, read)),
		add(tool.Func(GetClimateTool, "Get the climate normals at a farm, month by month (temperature, rain, humidity), and today's date.",
			func(ctx context.Context, in farmIn) (climateOut, error) {
				c, err := p.Climate(ctx, in.Farm)
				if err != nil {
					return climateOut{}, err
				}
				return climateOut{Today: p.Today().Format(agro.DateLayout), Climate: c, RainMM: c.RainMM()}, nil
			}, read)),
		add(tool.Func(SetProfileTool, "Save what the farmer told you about a farm's land and water: soil id (from list_soils), measured pH, water sources (canal, river, tank, borewell, open_well, farm_pond, each reliable or not; none for rainfed), drip, and the previous crop. Replaces the whole profile: include what was there before.",
			func(ctx context.Context, in profileIn) (agro.Profile, error) {
				f, err := p.SetProfile(ctx, in.Farm, in.Profile)
				if err != nil {
					return agro.Profile{}, err
				}
				return *f.Profile, nil
			}, write)),
		add(tool.Func(RecommendCropsTool, "Recommend crops for a farm and when to sow them, from its soil, water and the climate at the plot, best first. Each option has a score from 0 to 100, the sowing and harvest dates, the good sowing window, the water balance, and reasons and risks. Give 'around' when the farmer has a time in mind, 'crops' when they ask about particular crops.",
			func(ctx context.Context, in recommendIn) (recommendOut, error) {
				if in.Limit == 0 {
					in.Limit = 5
				}
				r, err := p.Recommend(ctx, in.Farm, planner.Ask{Around: in.Around, Spread: in.Spread, Crops: in.Crops, Limit: in.Limit})
				if err != nil {
					return recommendOut{}, err
				}
				out := recommendOut{Farm: r.Farm, Today: r.Today, From: r.From, To: r.To, Options: r.Options, Notes: r.Notes}
				if r.Soil != nil {
					out.Soil = r.Soil.Name
				}
				return out, nil
			}, read)),
		add(tool.Func(SuggestLayoutTool, "Lay a plan out on a farm: cut the plot into strips along its long side, one per part, sized by their shares, and check it. Saves nothing; returns the zones with their area, rows and plant counts, and the checks.",
			func(ctx context.Context, in partsIn) (planOut, error) {
				plan, rep, err := p.Layout(ctx, in.Farm, in.Parts)
				return planOut{Plan: plan, Report: rep}, err
			}, read)),
		add(tool.Func(CheckPlanTool, "Check a farm's saved plan, or a plan given as parts, against the rules: zones, spacing, sowing window, water, soil, rotation, intercrops and shade. Each check has an id, a severity (info, warning, error) and the zone it is about.",
			func(ctx context.Context, in checkIn) (planOut, error) {
				if len(in.Parts) > 0 {
					plan, rep, err := p.Layout(ctx, in.Farm, in.Parts)
					return planOut{Plan: plan, Report: rep}, err
				}
				f, err := p.Farms().Get(ctx, in.Farm)
				if err != nil {
					return planOut{}, err
				}
				if f.Plan == nil {
					return planOut{}, &tool.Error{Tool: CheckPlanTool, Kind: tool.ErrInvalidInput, Err: fmt.Errorf("farm %s has no saved plan: give parts", in.Farm)}
				}
				rep, err := p.Check(ctx, in.Farm, *f.Plan)
				return planOut{Plan: *f.Plan, Report: rep, Saved: true}, err
			}, read)),
		add(tool.Func(SavePlanTool, "Save a plan for a farm, laid out as suggest_layout does, once the farmer has agreed to it. A plan with errors is not saved: fix them first. It replaces the saved plan and shows on the farmer's map.",
			func(ctx context.Context, in partsIn) (planOut, error) {
				plan, _, err := p.Layout(ctx, in.Farm, in.Parts)
				if err != nil {
					return planOut{}, err
				}
				f, err := p.SavePlan(ctx, in.Farm, plan)
				var bad *planner.PlanError
				if errors.As(err, &bad) {
					return planOut{Plan: plan, Report: bad.Report}, nil
				}
				if err != nil {
					return planOut{}, err
				}
				return planOut{Plan: *f.Plan, Report: *f.PlanReport, Saved: true}, nil
			}, write)),
	)
	if err != nil {
		return nil, err
	}
	return tools, nil
}
