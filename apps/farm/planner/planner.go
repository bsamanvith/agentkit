// Package planner joins a farmer's plots to the crop planning rules: it
// looks up the climate at a plot, keeps its land and water profile, and
// recommends crops, lays out and checks plans for it.
package planner

import (
	"context"
	"fmt"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/pkg/clock"
)

// PlanError is a plan that cannot be saved; it matches agro.ErrInvalid.
type PlanError struct{ Report agro.PlanReport }

func (e *PlanError) Error() string {
	n := 0
	for _, c := range e.Report.Checks {
		if c.Severity == agro.Error {
			n++
		}
	}
	return fmt.Sprintf("planner: the plan has %d errors", n)
}

// Is makes a PlanError match agro.ErrInvalid.
func (e *PlanError) Is(target error) bool { return target == agro.ErrInvalid }

// Planner plans seasons for the farms of a service. It is safe for
// concurrent use.
type Planner struct {
	farms   *farm.Service
	lib     *agro.Library
	climate agro.ClimateSource
	clock   clock.Clock
	loc     *time.Location
}

// Option configures [New].
type Option func(*Planner)

// WithClock sets the clock "today" comes from.
func WithClock(c clock.Clock) Option { return func(p *Planner) { p.clock = c } }

// WithLocation sets the time zone of the farmer's "today" (default India,
// UTC+5:30).
func WithLocation(l *time.Location) Option { return func(p *Planner) { p.loc = l } }

// India is UTC+5:30, without the time zone database.
var India = time.FixedZone("IST", 5*3600+1800)

// New returns a planner over farms, with lib's rules and the climate of
// climate (nil: none, so recommendations fail and plan checks skip
// sowing and water).
func New(farms *farm.Service, lib *agro.Library, climate agro.ClimateSource, opts ...Option) *Planner {
	p := &Planner{farms: farms, lib: lib, climate: climate, clock: clock.Real(), loc: India}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Farms returns the farm service the planner plans for.
func (p *Planner) Farms() *farm.Service { return p.farms }

// Library returns the rules' library.
func (p *Planner) Library() *agro.Library { return p.lib }

// Today returns the farmer's date.
func (p *Planner) Today() time.Time {
	now := p.clock.Now().In(p.loc)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

// Climate returns the climate at a farm's centre.
func (p *Planner) Climate(ctx context.Context, id string) (agro.Climate, error) {
	f, err := p.farms.Get(ctx, id)
	if err != nil {
		return agro.Climate{}, err
	}
	return p.climateAt(ctx, f)
}

func (p *Planner) climateAt(ctx context.Context, f farm.Farm) (agro.Climate, error) {
	if p.climate == nil {
		return agro.Climate{}, fmt.Errorf("%w: no climate source", agro.ErrNoClimate)
	}
	return p.climate.Climate(ctx, geo.Centroid(f.Boundary.Ring()))
}

// SetProfile checks and saves a farm's land and water profile.
func (p *Planner) SetProfile(ctx context.Context, id string, prof agro.Profile) (farm.Farm, error) {
	if err := prof.Check(p.lib); err != nil {
		return farm.Farm{}, err
	}
	return p.farms.SetProfile(ctx, id, &prof)
}

// Ask is what the farmer asks the recommender.
type Ask struct {
	// Around is roughly when they want to sow (DateLayout); empty for
	// "when is the next good time". Spread is how many days either side
	// count (0: agro.DefaultSpread).
	Around string `json:"around,omitempty"`
	Spread int    `json:"spread,omitempty"`
	// Crops limits the answer to these crop ids.
	Crops []string `json:"crops,omitempty"`
	// Limit caps the options (0: all).
	Limit int `json:"limit,omitempty"`
}

// Recommendation is the recommender's answer for a farm.
type Recommendation struct {
	Farm    string        `json:"farm"`
	Soil    *agro.Soil    `json:"soil,omitempty"`
	Profile *agro.Profile `json:"profile,omitempty"`
	Climate agro.Climate  `json:"climate"`
	agro.Result
}

// Recommend recommends crops and sowing windows for a farm, from its
// profile and the climate at its centre.
func (p *Planner) Recommend(ctx context.Context, id string, ask Ask) (Recommendation, error) {
	f, err := p.farms.Get(ctx, id)
	if err != nil {
		return Recommendation{}, err
	}
	r := agro.Request{Today: p.Today(), Spread: ask.Spread, Crops: ask.Crops}
	if ask.Around != "" {
		if r.Around, err = time.Parse(agro.DateLayout, ask.Around); err != nil {
			return Recommendation{}, fmt.Errorf("%w: around %q: want YYYY-MM-DD", agro.ErrInvalid, ask.Around)
		}
	}
	if ask.Limit < 0 {
		return Recommendation{}, fmt.Errorf("%w: limit %d", agro.ErrInvalid, ask.Limit)
	}
	if r.Climate, err = p.climateAt(ctx, f); err != nil {
		return Recommendation{}, err
	}
	out := Recommendation{Farm: f.ID, Profile: f.Profile, Climate: r.Climate}
	if f.Profile != nil {
		r.Profile = *f.Profile
		if s, err := p.lib.Soil(f.Profile.Soil); err == nil {
			r.Soil, out.Soil = &s, &s
		}
	}
	if out.Result, err = agro.Recommend(p.lib, r); err != nil {
		return Recommendation{}, err
	}
	if ask.Limit > 0 && len(out.Options) > ask.Limit {
		out.Options = out.Options[:ask.Limit]
	}
	return out, nil
}

// Layout cuts a farm into strips for the parts and checks the plan.
func (p *Planner) Layout(ctx context.Context, id string, parts []agro.Part) (agro.Plan, agro.PlanReport, error) {
	f, err := p.farms.Get(ctx, id)
	if err != nil {
		return agro.Plan{}, agro.PlanReport{}, err
	}
	plan, err := agro.Layout(p.lib, f.Boundary.Ring(), parts)
	if err != nil {
		return agro.Plan{}, agro.PlanReport{}, err
	}
	return plan, p.check(ctx, f, plan), nil
}

// Check checks a plan for a farm. Without a climate it checks all but
// sowing and water.
func (p *Planner) Check(ctx context.Context, id string, plan agro.Plan) (agro.PlanReport, error) {
	f, err := p.farms.Get(ctx, id)
	if err != nil {
		return agro.PlanReport{}, err
	}
	return p.check(ctx, f, plan), nil
}

func (p *Planner) check(ctx context.Context, f farm.Farm, plan agro.Plan) agro.PlanReport {
	plot := agro.Plot{Ring: f.Boundary.Ring()}
	if c, err := p.climateAt(ctx, f); err == nil {
		plot.Climate = &c
	}
	if f.Profile != nil {
		plot.Profile = *f.Profile
		if s, err := p.lib.Soil(f.Profile.Soil); err == nil {
			plot.Soil = &s
		}
	}
	rep := agro.CheckPlan(p.lib, plot, plan, p.Today())
	if plot.Climate == nil {
		rep.Checks = append(rep.Checks, agro.PlanCheck{
			ID: "climate.none", Severity: agro.Info, Zone: -1, Other: -1,
			Message: "The climate could not be looked up, so sowing dates and water were not checked.",
		})
	}
	return rep
}

// SavePlan checks a plan and saves it with its report, unless it has
// errors: then it is a *PlanError.
func (p *Planner) SavePlan(ctx context.Context, id string, plan agro.Plan) (farm.Farm, error) {
	rep, err := p.Check(ctx, id, plan)
	if err != nil {
		return farm.Farm{}, err
	}
	if rep.Status == agro.PlanInvalid {
		return farm.Farm{}, &PlanError{Report: rep}
	}
	return p.farms.SetPlan(ctx, id, &plan, &rep)
}

// ClearPlan removes a farm's plan.
func (p *Planner) ClearPlan(ctx context.Context, id string) (farm.Farm, error) {
	return p.farms.SetPlan(ctx, id, nil, nil)
}
