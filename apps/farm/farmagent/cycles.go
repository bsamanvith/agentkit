package farmagent

import (
	"context"
	"errors"
	"fmt"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/cycle"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// The crop cycle tools.
const (
	GetCyclesTool = "get_cycles"
	SetCycleTool  = "set_cycle"
)

// CyclesSystem is added to the system prompt with the cycle tools.
const CyclesSystem = `
Each plot has up to three crop cycles: the current one (or the next, when the land is fallow) and the two after. For each there is our suggestion (worked out from the plot, its climate and the crop before) and the farmer's own, if they set one; the planned cycle is theirs where they have one.

- get_cycles gives the line: for each cycle the suggestion and why, the farmer's own, the dates (sowing planned or actual, the expected harvest) and warnings about their own cycles. Explain warnings plainly; never change the farmer's own cycle without asking.
- set_cycle, once the farmer agrees: accept a suggestion, set their own crop and sowing date, go back to the suggestion (clear), choose organic or conventional, or record that the current cycle was sown on a day ("I already sowed on ...").
- The current cycle is the plot's saved season plan: setting it saves the plan, and the season roadmap follows.`

// WithCycles gives the assistant the crop cycle tools over s.
func WithCycles(s *cycle.Service) Option { return func(x *setup) { x.cycles = s } }

type cyclesIn struct {
	Farm string `json:"farm" jsonschema:"the farm's id, from list_farms"`
}

type setCycleIn struct {
	Farm     string `json:"farm" jsonschema:"the farm's id, from list_farms"`
	Position int    `json:"position" jsonschema:"0 for the current cycle, 1 and 2 for the next ones"`
	Accept   bool   `json:"accept,omitempty" jsonschema:"take our suggestion for this cycle as the farmer's own"`
	Crop     string `json:"crop,omitempty" jsonschema:"the farmer's own crop id (from the crop library); with sow"`
	Sow      string `json:"sow,omitempty" jsonschema:"YYYY-MM-DD: when the farmer plans to sow their own crop"`
	Area     *int   `json:"area,omitempty" jsonschema:"the area (0, 1, ...) of the planned cycle to change; leave out for the whole plot"`
	Clear    bool   `json:"clear,omitempty" jsonschema:"drop the farmer's own later cycle (position 1 or 2) and follow our suggestion"`
	Method   string `json:"method,omitempty" jsonschema:"organic or conventional"`
	Sown     string `json:"sown,omitempty" jsonschema:"YYYY-MM-DD: the current cycle was really sown that day (position 0 only)"`
}

// CycleTools returns get_cycles and set_cycle over s.
func CycleTools(s *cycle.Service) ([]tool.Tool, error) {
	get, err := tool.Func(GetCyclesTool, "Get a farm's crop cycles: up to three, each with our suggestion (crop per area, sowing date, expected harvest, score and why), the farmer's own if set, which is planned, organic or conventional, and warnings about the farmer's own cycles.",
		func(ctx context.Context, in cyclesIn) (cycle.Line, error) {
			l, err := s.Line(ctx, in.Farm)
			if err != nil {
				return cycle.Line{}, cycleErr(GetCyclesTool, err)
			}
			return l, nil
		}, read)
	if err != nil {
		return nil, err
	}
	set, err := tool.Func(SetCycleTool, "Change a farm's crop cycle once the farmer has agreed: accept our suggestion, set their own crop and sowing date (for the whole plot or one area), clear their own later cycle, choose organic or conventional, or record the real sowing day of the current cycle. Returns the line after.",
		func(ctx context.Context, in setCycleIn) (cycle.Line, error) {
			l, err := setCycle(ctx, s, in)
			if err != nil {
				return cycle.Line{}, cycleErr(SetCycleTool, err)
			}
			return l, nil
		}, write)
	if err != nil {
		return nil, err
	}
	return []tool.Tool{get, set}, nil
}

func setCycle(ctx context.Context, s *cycle.Service, in setCycleIn) (cycle.Line, error) {
	if in.Sown != "" {
		if in.Position != 0 {
			return cycle.Line{}, fmt.Errorf("%w: only the current cycle (position 0) can be sown", cycle.ErrInvalid)
		}
		return s.SetSown(ctx, in.Farm, in.Sown)
	}
	ch := cycle.Change{Accept: in.Accept, Clear: in.Clear, Method: cycle.Method(in.Method)}
	if in.Crop != "" || in.Sow != "" {
		if in.Crop == "" || in.Sow == "" {
			return cycle.Line{}, fmt.Errorf("%w: give both the crop and the sowing date", cycle.ErrInvalid)
		}
		plan := agro.Plan{Zones: []agro.Zone{{Crop: in.Crop, Sow: in.Sow}}}
		if in.Area != nil {
			l, err := s.Line(ctx, in.Farm)
			if err != nil {
				return cycle.Line{}, err
			}
			if in.Position < 0 || in.Position >= len(l.Cycles) || l.Cycles[in.Position].Planned == nil {
				return cycle.Line{}, fmt.Errorf("%w: nothing is planned at position %d to change an area of", cycle.ErrInvalid, in.Position)
			}
			plan = l.Cycles[in.Position].Planned.Plan
			if *in.Area < 0 || *in.Area >= len(plan.Zones) {
				return cycle.Line{}, fmt.Errorf("%w: area %d: the cycle has %d", cycle.ErrInvalid, *in.Area, len(plan.Zones))
			}
			z := &plan.Zones[*in.Area]
			z.Crop, z.Sow, z.With, z.Pattern, z.RowCM, z.PlantCM = in.Crop, in.Sow, "", [2]int{}, 0, 0
		}
		ch.Plan = &plan
	}
	return s.Set(ctx, in.Farm, in.Position, ch)
}

func cycleErr(name string, err error) error {
	if errors.Is(err, cycle.ErrInvalid) || errors.Is(err, agro.ErrInvalid) || errors.Is(err, agro.ErrUnknown) {
		return &tool.Error{Tool: name, Kind: tool.ErrInvalidInput, Err: err}
	}
	return err
}
