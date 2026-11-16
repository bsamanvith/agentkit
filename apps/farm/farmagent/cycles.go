package farmagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/cycle"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// The crop cycle tools.
const (
	GetCyclesTool = "get_cycles"
	SetCycleTool  = "set_cycle"
	GetInputsTool = "get_inputs"
)

// CyclesSystem is added to the system prompt with the cycle tools.
const CyclesSystem = `
Each plot has up to three crop cycles: the current one (or the next, when the land is fallow) and the two after. For each there is our suggestion (worked out from the plot, its climate and the crop before) and the farmer's own, if they set one; the planned cycle is theirs where they have one.

- get_cycles gives the line: for each cycle the suggestion and why, the farmer's own, the dates (sowing planned or actual, the expected harvest) and warnings about their own cycles. Explain warnings plainly; never change the farmer's own cycle without asking.
- set_cycle, once the farmer agrees: accept a suggestion, set their own crop and sowing date, go back to the suggestion (clear), choose organic or conventional, or record that the current cycle was sown on a day. When the farmer says they already sowed ("I sowed groundnut on 5 November"), make one call with position 0, sown and the crop they named; without a crop it records the planned crop as sown. Do this without asking again when the crop and the day are clear, then say the harvest date and what follows from get_cycles.
- The current cycle is the plot's saved season plan: setting it saves the plan, and the season roadmap follows.
- get_inputs answers "what should I buy", "how much urea", "what do I spray and when" for one cycle: the shopping list (before sowing, later) in the units sold for the plot's area, what to buy only if a pest or disease shows up, and the dated feeding and spray schedule with doses, water per acre and the wait before harvest. Give its numbers exactly, never your own; say they are starter values to check with the agriculture office, and that the product label wins. Organic or conventional follows the cycle's method (set_cycle changes it).
- get_cycles' "timing" on a cycle is harvest-month and sell-or-hold advice from the area's past prices, present only where it beat simple guesses on past years; with no advice its reasons say why (too little price history): say so in a line, never guess.`

// WithCycles gives the assistant the crop cycle tools over s.
func WithCycles(s *cycle.Service) Option { return func(x *setup) { x.cycles = s } }

type cyclesIn struct {
	Farm string `json:"farm" jsonschema:"the farm's id, from list_farms"`
}

type setCycleIn struct {
	Farm     string `json:"farm" jsonschema:"the farm's id, from list_farms"`
	Position int    `json:"position" jsonschema:"0 for the current cycle, 1 and 2 for the next ones"`
	Accept   bool   `json:"accept,omitempty" jsonschema:"take our suggestion for this cycle as the farmer's own"`
	Crop     string `json:"crop,omitempty" jsonschema:"the farmer's own crop id (from the crop library); with sow for a plan, or with sown when it was already sown"`
	Sow      string `json:"sow,omitempty" jsonschema:"YYYY-MM-DD: when the farmer plans to sow their own crop"`
	Area     *int   `json:"area,omitempty" jsonschema:"the area (0, 1, ...) of the planned cycle to change; leave out for the whole plot"`
	Clear    bool   `json:"clear,omitempty" jsonschema:"drop the farmer's own later cycle (position 1 or 2) and follow our suggestion"`
	Method   string `json:"method,omitempty" jsonschema:"organic or conventional"`
	Sown     string `json:"sown,omitempty" jsonschema:"YYYY-MM-DD: the crop was really sown that day (position 0 only); add crop when the farmer names what they sowed"`
}

// CycleTools returns get_cycles and set_cycle over s.
func CycleTools(s *cycle.Service) ([]tool.Tool, error) {
	get, err := tool.Func(GetCyclesTool, "Get a farm's crop cycles: up to three, each with our suggestion (crop per area, sowing date, expected harvest, score and why), the farmer's own if set, which is planned, organic or conventional, and warnings about the farmer's own cycles.",
		func(ctx context.Context, in cyclesIn) (cycle.Line, error) {
			l, err := s.Line(ctx, in.Farm)
			if err != nil {
				return cycle.Line{}, cycleErr(GetCyclesTool, err)
			}
			// The inputs plan is get_inputs': kept out to keep this short.
			for i := range l.Cycles {
				if _, ok := l.Cycles[i].Addons[inputsAddon]; ok {
					delete(l.Cycles[i].Addons, inputsAddon)
					l.Cycles[i].Addons["inputs_by"] = GetInputsTool
				}
				if v, ok := l.Cycles[i].Addons[timingAddon]; ok {
					l.Cycles[i].Addons[timingAddon] = lean(v)
				}
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
	inp, err := tool.Func(GetInputsTool, "Get what to buy and when to feed and spray for a farm's crop cycle (0 the current, 1 and 2 the next): the shopping list before sowing and later in the units sold, for the plot's area; what to buy only if a pest or disease shows up, with its signs and threshold; and the dated feeding and spray schedule with doses for the area and per acre, water per acre and the wait before harvest. Starter values from the sources it lists; the product label wins.",
		func(ctx context.Context, in inputsIn) (inputsOut, error) {
			out, err := getInputs(ctx, s, in)
			if err != nil {
				return inputsOut{}, cycleErr(GetInputsTool, err)
			}
			return out, nil
		}, read)
	if err != nil {
		return nil, err
	}
	return []tool.Tool{get, set, inp}, nil
}

// The addons' names on a cycle (package cycle/addon, wired in the farm
// binary).
const (
	inputsAddon = "inputs"
	timingAddon = "timing"
)

type inputsIn struct {
	Farm     string `json:"farm" jsonschema:"the farm's id, from list_farms"`
	Position int    `json:"position,omitempty" jsonschema:"0 (the default) for the current cycle, 1 and 2 for the next ones"`
}

type inputsOut struct {
	Farm     string `json:"farm"`
	Position int    `json:"position"`
	Crops    string `json:"crops,omitempty"`
	Sow      string `json:"sow,omitempty"`
	Harvest  string `json:"harvest,omitempty"`
	Method   string `json:"method,omitempty"`
	// Inputs is the plan: fields (crop and area), notes, buy_before,
	// buy_later, if_seen, schedule and sources.
	Inputs any    `json:"inputs,omitempty"`
	Note   string `json:"note,omitempty"`
}

func getInputs(ctx context.Context, s *cycle.Service, in inputsIn) (inputsOut, error) {
	l, err := s.Line(ctx, in.Farm)
	if err != nil {
		return inputsOut{}, err
	}
	if in.Position < 0 || in.Position >= cycle.MaxCycles {
		return inputsOut{}, fmt.Errorf("%w: position %d: want 0 to %d", cycle.ErrInvalid, in.Position, cycle.MaxCycles-1)
	}
	out := inputsOut{Farm: l.Farm, Position: in.Position}
	if in.Position >= len(l.Cycles) || l.Cycles[in.Position].Planned == nil {
		out.Note = "Nothing is planned for this cycle yet: set or accept a crop first (set_cycle)."
		return out, nil
	}
	p := l.Cycles[in.Position]
	c := p.Planned
	var names []string
	for _, a := range c.Areas {
		if !slices.Contains(names, a.CropName) {
			names = append(names, a.CropName)
		}
	}
	out.Crops, out.Sow, out.Harvest, out.Method = strings.Join(names, ", "), c.Sow, c.Harvest, string(c.Method)
	if v, ok := p.Addons[inputsAddon]; ok {
		out.Inputs = lean(v)
	} else {
		out.Note = "No shopping list or spray schedule for this cycle now (the plot's area may not be known): say so, and do not make one up."
	}
	return out, nil
}

// lean is v as JSON without the per-value source references ("refs"), which
// the plan's sources list already covers: shorter for the model.
func lean(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return v
	}
	var drop func(any)
	drop = func(x any) {
		switch t := x.(type) {
		case map[string]any:
			delete(t, "refs")
			delete(t, "signs_ref")
			for _, y := range t {
				drop(y)
			}
		case []any:
			for _, y := range t {
				drop(y)
			}
		}
	}
	drop(out)
	return out
}

func setCycle(ctx context.Context, s *cycle.Service, in setCycleIn) (cycle.Line, error) {
	if in.Sown != "" {
		if in.Position != 0 {
			return cycle.Line{}, fmt.Errorf("%w: only the current cycle (position 0) can be sown", cycle.ErrInvalid)
		}
		if in.Accept || in.Clear || in.Sow != "" || in.Area != nil || in.Method != "" {
			return cycle.Line{}, fmt.Errorf("%w: record the sowing with farm, position 0, sown and, when the farmer names it, crop; make any other change in a second set_cycle call", cycle.ErrInvalid)
		}
		return s.Sowed(ctx, in.Farm, in.Crop, in.Sown)
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
