package farmagent

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/planner"
	"github.com/bsamanvith/agentkit/apps/farm/region"
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
	GetSunTool         = "get_sun"
	CompareCropsTool   = "compare_crops"
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
	// Light and DayLength: how much sun it wants, and how day length
	// sets its flowering.
	Light     string `json:"light"`
	DayLength string `json:"day_length"`
	// An experimental plant's other names, and that it is one.
	Common       []string `json:"common,omitempty"`
	Experimental bool     `json:"experimental,omitempty"`
}

type compareIn struct {
	Farm  string   `json:"farm" jsonschema:"the farm's id, from list_farms"`
	Crops []string `json:"crops" jsonschema:"one to eight crop ids, from list_crops"`
}

// cropMonths is one crop's year at a farm, sowing month by sowing month.
type cropMonths struct {
	Crop      string   `json:"crop"`
	Name      string   `json:"name"`
	Seasons   []string `json:"seasons"`
	Light     string   `json:"light"`
	DayLength string   `json:"day_length"`
	// ByMonth is the best score of a sowing in each of Months, 0 to 100.
	ByMonth []int `json:"by_month"`
	// Best is its best sowing over the year, with why.
	Best agro.Season `json:"best"`
}

type compareOut struct {
	Farm  string `json:"farm"`
	Today string `json:"today"`
	// Months are the next twelve sowing months, as YYYY-MM.
	Months []string     `json:"months"`
	Crops  []cropMonths `json:"crops"`
	Notes  []agro.Note  `json:"notes,omitempty"`
}

type kindIn struct {
	Kind         string `json:"kind,omitempty" jsonschema:"only crops of this kind: cereal, millet, pulse, oilseed, vegetable, fruit, fibre, cash, spice, ..."`
	Search       string `json:"search,omitempty" jsonschema:"only crops that go by this name, in any language or botanical"`
	Experimental bool   `json:"experimental,omitempty" jsonschema:"true to search the experimental tier (FAO ECOCROP, about 1,300 more plants) instead; give search or kind with it"`
}

type idIn struct {
	ID string `json:"id" jsonschema:"the crop's id, from list_crops"`
	// Where, for the district's own sowing windows and varieties.
	Farm     string `json:"farm,omitempty" jsonschema:"the farm's id, to get the crop as it is known where the farm is: the district's own sowing windows and varieties"`
	State    string `json:"state,omitempty" jsonschema:"instead of farm, the state: 'Andhra Pradesh'"`
	District string `json:"district,omitempty" jsonschema:"with state, the district: 'Kadapa'"`
	Country  string `json:"country,omitempty" jsonschema:"with state, the ISO country code (default IN)"`
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
	experimentalIn
}

// experimentalIn asks the experimental tier in place of the library.
type experimentalIn struct {
	Experimental bool     `json:"experimental,omitempty" jsonschema:"true to ask the experimental tier instead: about 1,300 more plants from FAO ECOCROP (coffee, cashew, jackfruit, pineapple, cassava, dates, lentil, ...), scored on climate and soil alone. Only when the farmer asks what else could grow, wants something new or unusual, or names a plant list_crops does not have"`
	Kinds        []string `json:"kinds,omitempty" jsonschema:"with experimental: only these kinds: cereal, pulse, root, vegetable, fruit, fodder, medicinal (also spices and beverages), material (fibre, oil, timber)"`
	Search       string   `json:"search,omitempty" jsonschema:"with experimental: only plants that go by this name, common or botanical: 'coffee', 'Artocarpus'"`
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
// list_crops, get_crop, get_climate, get_sun, compare_crops,
// recommend_crops, suggest_layout and check_plan, which read, and set_profile and save_plan, which save what
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
		add(tool.Func(ListCropsTool, "List the crops the app can plan, with their kind, days to harvest and Indian seasons. With experimental and a search or kind, list plants of the experimental tier instead (at most 60).",
			func(_ context.Context, in kindIn) (map[string][]cropCard, error) {
				out := []cropCard{}
				from := lib
				if in.Experimental {
					if in.Kind == "" && strings.TrimSpace(in.Search) == "" {
						return nil, fmt.Errorf("%w: give search or kind with experimental", agro.ErrInvalid)
					}
					var err error
					if from, err = agro.Wide(); err != nil {
						return nil, err
					}
				}
				for _, c := range from.Crops() {
					if (in.Kind == "" || c.Kind == in.Kind) && c.Matches(in.Search) {
						out = append(out, cropCard{c.ID, c.Name, c.Names, c.Kind, c.Days, c.Seasons, c.ClimateAnalog, c.LightNeed(), c.DayHabit(), c.Common, c.Experimental})
					}
				}
				if in.Experimental && len(out) > 60 {
					out = out[:60] // a kind of the tier can be hundreds: narrow it with search
				}
				return map[string][]cropCard{"crops": out}, nil
			}, read)),
		add(tool.Func(GetCropTool, "Get everything the app knows about a crop: temperatures, water, soil, spacing, seasons, intercrops, and its named varieties, each with the document that lists it. Give the farm, or the state and district, to get the district's own sowing windows and varieties where the app has them. An experimental plant's id works too.",
			func(ctx context.Context, in idIn) (agro.Crop, error) {
				lib := lib
				switch {
				case in.Farm != "":
					f, err := p.Farms().Get(ctx, in.Farm)
					if err != nil {
						return agro.Crop{}, err
					}
					lib = p.LibraryFor(f)
				case in.State != "":
					lib = p.LibraryAt(region.Place{Country: cmp.Or(in.Country, "IN"), State: in.State, District: in.District})
				}
				c, err := lib.Crop(in.ID)
				if w, werr := agro.Wide(); err != nil && werr == nil {
					if x, xerr := w.Crop(in.ID); xerr == nil {
						return x, nil
					}
				}
				return c, err
			}, read)),
		add(tool.Func(GetClimateTool, "Get the climate normals at a farm, month by month (temperature, rain, humidity), and today's date.",
			func(ctx context.Context, in farmIn) (climateOut, error) {
				c, err := p.Climate(ctx, in.Farm)
				if err != nil {
					return climateOut{}, err
				}
				return climateOut{Today: p.Today().Format(agro.DateLayout), Climate: c, RainMM: c.RainMM()}, nil
			}, read)),
		add(tool.Func(GetSunTool, "Get the sun at a farm month by month: day length, hours of bright sun, the noon sun's height and side (north or south of the plot), the angle its rays strike the ground at, the sunlight on the plot's slope (from its survey) against flat ground, the share cloud takes, and notes on what that means for crops. Use it for questions about sunlight, shade, slope, row direction or the seasons.",
			func(ctx context.Context, in farmIn) (planner.SunReport, error) { return p.Sun(ctx, in.Farm) }, read)),
		add(tool.Func(CompareCropsTool, "Compare crops at a farm through the year: for each crop the score (0 to 100) of sowing it in each of the next twelve months, its usual seasons, how much sun it wants and its day-length habit, and its best sowing with reasons and risks. Use it when the farmer researches what to grow when, or wants crops side by side.",
			func(ctx context.Context, in compareIn) (compareOut, error) {
				if len(in.Crops) == 0 || len(in.Crops) > 8 {
					return compareOut{}, &tool.Error{Tool: CompareCropsTool, Kind: tool.ErrInvalidInput, Err: fmt.Errorf("give one to eight crops, not %d", len(in.Crops))}
				}
				r, err := p.Recommend(ctx, in.Farm, planner.Ask{Crops: in.Crops, Calendar: true})
				if err != nil {
					return compareOut{}, err
				}
				// In the order asked, for a side-by-side.
				slices.SortStableFunc(r.Options, func(a, b agro.Option) int {
					return slices.Index(in.Crops, a.Crop) - slices.Index(in.Crops, b.Crop)
				})
				here := lib
				if f, err := p.Farms().Get(ctx, in.Farm); err == nil {
					here = p.LibraryFor(f) // the seasons where the plot is
				}
				return compare(r, here), nil
			}, read)),
		add(tool.Func(SetProfileTool, "Save what the farmer told you about a farm's land and water: soil id (from list_soils), measured pH, water sources (canal, river, tank, borewell, open_well, farm_pond, each reliable or not; none for rainfed), drip, and the previous crop. Replaces the whole profile: include what was there before.",
			func(ctx context.Context, in profileIn) (agro.Profile, error) {
				f, err := p.SetProfile(ctx, in.Farm, in.Profile)
				if err != nil {
					return agro.Profile{}, err
				}
				return *f.Profile, nil
			}, write)),
		add(tool.Func(RecommendCropsTool, "Recommend crops for a farm and when to sow them, from its soil, water and the climate at the plot, best first. Each option has a score from 0 to 100, the sowing and harvest dates, the good sowing window, the water balance, and reasons and risks. Give 'around' when the farmer has a time in mind, 'crops' when they ask about particular crops. With 'experimental' it answers from the experimental tier instead: plants outside the reviewed library, to try on a small area.",
			func(ctx context.Context, in recommendIn) (recommendOut, error) {
				if in.Limit == 0 && !in.Experimental {
					in.Limit = 5
				}
				r, err := p.Recommend(ctx, in.Farm, planner.Ask{Around: in.Around, Spread: in.Spread, Crops: in.Crops, Limit: in.Limit, Experimental: in.Experimental, Kinds: in.Kinds, Search: in.Search})
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

// compare folds each option's weekly calendar into the best score of a
// sowing in each of the twelve months from today's.
func compare(r planner.Recommendation, lib *agro.Library) compareOut {
	out := compareOut{Farm: r.Farm, Today: r.Today, Notes: r.Notes, Crops: []cropMonths{}}
	idx := map[string]int{}
	if today, err := time.Parse(agro.DateLayout, r.Today); err == nil {
		first := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
		for i := range 12 {
			label := first.AddDate(0, i, 0).Format("2006-01")
			idx[label] = i
			out.Months = append(out.Months, label)
		}
	}
	for _, o := range r.Options {
		cm := cropMonths{Crop: o.Crop, Name: o.Name, ByMonth: make([]int, 12), Best: o.Season}
		if c, err := lib.Crop(o.Crop); err == nil {
			cm.Seasons, cm.Light, cm.DayLength = c.Seasons, c.LightNeed(), c.DayHabit()
		}
		for _, s := range o.Calendar {
			if i, ok := idx[s.Sow[:7]]; ok {
				cm.ByMonth[i] = max(cm.ByMonth[i], s.Score)
			}
		}
		out.Crops = append(out.Crops, cm)
	}
	return out
}
