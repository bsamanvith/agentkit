package farmagent

import (
	"context"
	"fmt"

	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// Summary is a farm as the tools show it to a model: enough to answer
// questions, without every corner.
type Summary struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Status   farm.Status    `json:"status"`
	Declared *farm.Area     `json:"declared,omitempty"`
	Measures *farm.Measures `json:"measures,omitempty"`
	Location *farm.Location `json:"location,omitempty"`
	Checks   []farm.Check   `json:"checks,omitempty"`
	Corners  int            `json:"corners"`
	Source   farm.Source    `json:"source,omitempty"`
}

func summarise(f farm.Farm, withChecks bool) Summary {
	s := Summary{
		ID: f.ID, Name: f.Name, Status: f.Report.Status, Declared: f.Declared,
		Measures: f.Report.Measures, Location: f.Report.Location,
		Corners: len(f.Boundary.Corners), Source: f.Boundary.Source,
	}
	if withChecks {
		s.Checks = f.Report.Checks
	}
	return s
}

type listIn struct{}

type listOut struct {
	Farms []Summary `json:"farms"`
}

type getIn struct {
	ID string `json:"id" jsonschema:"the farm's id, from list_farms"`
}

type verifyIn struct {
	Corners       []geo.Point `json:"corners" jsonschema:"the boundary's corners in order around the plot, as decimal degrees"`
	DeclaredValue float64     `json:"declared_value,omitempty" jsonschema:"the size the farmer believes the plot is, if they said"`
	DeclaredUnit  farm.Unit   `json:"declared_unit,omitempty" jsonschema:"the unit of declared_value: m2, ha, acre, gunta or cent"`
}

type convertIn struct {
	Value float64   `json:"value" jsonschema:"the amount of land"`
	From  farm.Unit `json:"from" jsonschema:"its unit: m2, ha, acre, gunta or cent"`
	To    farm.Unit `json:"to" jsonschema:"the unit to convert to: m2, ha, acre, gunta or cent"`
}

type convertOut struct {
	Value     float64   `json:"value"`
	Unit      farm.Unit `json:"unit"`
	AcresText string    `json:"acres_text"`
}

var read = tool.WithMetadata(tool.Metadata{Kind: tool.KindRead, Risk: tool.RiskLow})

// Tools returns the farm tools over svc: list_farms, get_farm,
// verify_boundary and convert_area. All of them only read.
func Tools(svc *farm.Service) ([]tool.Tool, error) {
	list, err := tool.Func("list_farms", "List the farmer's plots with their size, location and verification status.",
		func(ctx context.Context, _ listIn) (listOut, error) {
			fs, err := svc.List(ctx)
			if err != nil {
				return listOut{}, err
			}
			out := listOut{Farms: make([]Summary, len(fs))}
			for i, f := range fs {
				out.Farms[i] = summarise(f, false)
			}
			return out, nil
		}, read)
	if err != nil {
		return nil, err
	}
	get, err := tool.Func("get_farm", "Get one of the farmer's plots: its size, location, verification status and every check of its boundary.",
		func(ctx context.Context, in getIn) (Summary, error) {
			f, err := svc.Get(ctx, in.ID)
			if err != nil {
				return Summary{}, err
			}
			return summarise(f, true), nil
		}, read)
	if err != nil {
		return nil, err
	}
	verify, err := tool.Func("verify_boundary", "Check a plot boundary given as corner coordinates: valid shape, area in every unit, match with the declared size, plausibility, location and overlaps with the farmer's plots. Saves nothing.",
		func(ctx context.Context, in verifyIn) (farm.Report, error) {
			d := farm.Draft{Boundary: farm.Boundary{Source: farm.SourceTyped}}
			for _, p := range in.Corners {
				d.Boundary.Corners = append(d.Boundary.Corners, farm.Corner{Point: p})
			}
			if in.DeclaredUnit != "" {
				d.Declared = &farm.Area{Value: in.DeclaredValue, Unit: in.DeclaredUnit}
			}
			return svc.Verify(ctx, d, "")
		}, read)
	if err != nil {
		return nil, err
	}
	convert, err := tool.Func("convert_area", "Convert an amount of land between m2, ha, acre, gunta (40 to an acre) and cent (100 to an acre).",
		func(_ context.Context, in convertIn) (convertOut, error) {
			m2, err := farm.Area{Value: in.Value, Unit: in.From}.SquareMetres()
			if err != nil {
				return convertOut{}, err
			}
			v, err := farm.Convert(m2, in.To)
			if err != nil {
				return convertOut{}, fmt.Errorf("to: %w", err)
			}
			return convertOut{Value: v, Unit: in.To, AcresText: farm.AcresAndGuntas(m2)}, nil
		}, read)
	if err != nil {
		return nil, err
	}
	return []tool.Tool{list, get, verify, convert}, nil
}
