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
	"github.com/bsamanvith/agentkit/apps/farm/region"
	"github.com/bsamanvith/agentkit/apps/farm/sun"
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
	regions *region.Set
	locator farm.Locator
}

// Option configures [New].
type Option func(*Planner)

// WithClock sets the clock "today" comes from.
func WithClock(c clock.Clock) Option { return func(p *Planner) { p.clock = c } }

// WithLocation sets the time zone of the farmer's "today" (default India,
// UTC+5:30).
func WithLocation(l *time.Location) Option { return func(p *Planner) { p.loc = l } }

// WithRegions plans each plot with the region pack of its place: the
// crop library and the rules that hold there. Without it every plot gets
// the library New was given and the base rules.
func WithRegions(s *region.Set) Option { return func(p *Planner) { p.regions = s } }

// WithLocator looks up where a point is (its district, state and
// country) for [Planner.RecommendAt] when the caller does not say. Without
// it such a point gets the base rules.
func WithLocator(l farm.Locator) Option { return func(p *Planner) { p.locator = l } }

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

// Library returns the base library: for what is not about one plot (the
// crop list, the handbook). A plot's is [Planner.LibraryFor].
func (p *Planner) Library() *agro.Library { return p.lib }

// Pack returns the region pack for a plot's place; nil without
// [WithRegions].
func (p *Planner) Pack(f farm.Farm) *region.Pack {
	if p.regions == nil {
		return nil
	}
	return p.regions.For(PlaceOf(f))
}

// LibraryFor returns the crop library that holds at a plot.
func (p *Planner) LibraryFor(f farm.Farm) *agro.Library {
	if pk := p.Pack(f); pk != nil {
		return pk.Library()
	}
	return p.lib
}

// RulesFor returns the crop cycle rules that hold at a plot.
func (p *Planner) RulesFor(f farm.Farm) region.Rules {
	if pk := p.Pack(f); pk != nil {
		return pk.Rules()
	}
	return region.DefaultRules()
}

// PlaceOf is a plot's place, from its boundary's location: country code,
// state and district; empty when it was not looked up.
func PlaceOf(f farm.Farm) region.Place { return placeOf(f.Report.Location) }

func placeOf(l *farm.Location) region.Place {
	if l == nil {
		return region.Place{}
	}
	return region.Place{Country: l.CountryCode, State: l.State, District: l.District}
}

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
	return p.climateAtPoint(ctx, geo.Centroid(f.Boundary.Ring()))
}

func (p *Planner) climateAtPoint(ctx context.Context, pt geo.Point) (agro.Climate, error) {
	if p.climate == nil {
		return agro.Climate{}, fmt.Errorf("%w: no climate source", agro.ErrNoClimate)
	}
	return p.climate.Climate(ctx, pt)
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
	// Calendar asks for every sowing date's score in each option.
	Calendar bool `json:"calendar,omitempty"`
}

// Recommendation is the recommender's answer for a farm.
type Recommendation struct {
	// Farm is the plot's id; empty for a place with no plot
	// ([Planner.RecommendAt]).
	Farm    string        `json:"farm,omitempty"`
	Soil    *agro.Soil    `json:"soil,omitempty"`
	Profile *agro.Profile `json:"profile,omitempty"`
	Climate agro.Climate  `json:"climate"`
	// Slope is how the plot's ground lies, from its survey; nil when flat
	// or not surveyed.
	Slope *sun.Slope `json:"slope,omitempty"`
	agro.Result
}

// Recommend recommends crops and sowing windows for a farm, from its
// profile and the climate at its centre.
func (p *Planner) Recommend(ctx context.Context, id string, ask Ask) (Recommendation, error) {
	f, err := p.farms.Get(ctx, id)
	if err != nil {
		return Recommendation{}, err
	}
	return p.recommend(ctx, spot{
		farm: f.ID, point: geo.Centroid(f.Boundary.Ring()), profile: f.Profile, slope: SlopeOf(f), lib: p.LibraryFor(f),
	}, ask)
}

// spot is what the recommender needs to know of where it plans: a saved
// plot, or a place with no plot yet.
type spot struct {
	farm    string
	point   geo.Point
	profile *agro.Profile
	slope   *sun.Slope
	lib     *agro.Library
}

func (p *Planner) recommend(ctx context.Context, s spot, ask Ask) (Recommendation, error) {
	r := agro.Request{Today: p.Today(), Spread: ask.Spread, Crops: ask.Crops, Calendar: ask.Calendar}
	if s.slope != nil {
		r.Slope = *s.slope
	}
	var err error
	if ask.Around != "" {
		if r.Around, err = time.Parse(agro.DateLayout, ask.Around); err != nil {
			return Recommendation{}, fmt.Errorf("%w: around %q: want YYYY-MM-DD", agro.ErrInvalid, ask.Around)
		}
	}
	if ask.Limit < 0 {
		return Recommendation{}, fmt.Errorf("%w: limit %d", agro.ErrInvalid, ask.Limit)
	}
	if r.Climate, err = p.climateAtPoint(ctx, s.point); err != nil {
		return Recommendation{}, err
	}
	out := Recommendation{Farm: s.farm, Profile: s.profile, Climate: r.Climate, Slope: s.slope}
	if s.profile != nil {
		r.Profile = *s.profile
		if soil, err := s.lib.Soil(s.profile.Soil); err == nil {
			r.Soil, out.Soil = &soil, &soil
		}
	}
	if out.Result, err = agro.Recommend(s.lib, r); err != nil {
		return Recommendation{}, err
	}
	if ask.Limit > 0 && len(out.Options) > ask.Limit {
		out.Options = out.Options[:ask.Limit]
	}
	return out, nil
}

// At is a place to recommend for before any plot is drawn there.
type At struct {
	// Point is where: a village's centre, or where the farmer stands.
	Point geo.Point `json:"point"`
	// Place is its country, state and district, for the region pack (the
	// local crop library and sowing windows). Zero: looked up with the
	// locator ([WithLocator]); the base when there is none.
	Place region.Place `json:"place,omitzero"`
	// Profile is what the farmer said of the land and water; nil, or a
	// part left out, is assumed as for a plot with no profile: rain only
	// and the soil not known.
	Profile *agro.Profile `json:"profile,omitempty"`
}

// PlaceRecommendation is the recommender's answer for a place with no
// plot: the same as a plot's, with where it is, the region layers whose
// rules it used and what it assumed.
type PlaceRecommendation struct {
	Point geo.Point `json:"point"`
	// Location is the looked-up address, when the planner looked it up.
	Location *farm.Location `json:"location,omitempty"`
	Place    region.Place   `json:"place"`
	// Regions are the region layers laid on each other, base first:
	// ["base", "Andhra Pradesh", "YSR Kadapa"].
	Regions []string `json:"regions"`
	// Assumed is what the answer assumed because the farmer did not say:
	// "water.rain_only", "soil.unknown". The suggestions may change
	// completely when they do.
	Assumed []agro.Note `json:"assumed,omitempty"`
	Recommendation
}

// The assumptions [Planner.RecommendAt] names when the farmer did not say.
const (
	AssumedRainOnly    = "water.rain_only"
	AssumedSoilUnknown = "soil.unknown"
)

// Assumptions are what a recommendation assumes for a land and water
// profile, as the crop cycles do for a plot with no profile: rain only
// when no water source is given, no soil scored when the soil is not.
func Assumptions(prof agro.Profile) []agro.Note {
	var out []agro.Note
	if prof.Rainfed() {
		out = append(out, agro.Note{ID: AssumedRainOnly, Message: "Assuming rain only, no irrigation: a canal, borewell or well can change the answer completely."})
	}
	if prof.Soil == "" {
		out = append(out, agro.Note{ID: AssumedSoilUnknown, Message: "The soil is not known, so it was not scored."})
	}
	return out
}

// RecommendAt recommends crops and sowing windows for a place with no plot
// yet, by the same rules as [Planner.Recommend]: the climate at the point,
// the region pack of its place, and the profile given (or the
// assumptions). A place that cannot be looked up gets the base rules and a
// note saying so; it never fails for that alone.
func (p *Planner) RecommendAt(ctx context.Context, at At, ask Ask) (PlaceRecommendation, error) {
	if !at.Point.Valid() {
		return PlaceRecommendation{}, fmt.Errorf("%w: %v is not a position", agro.ErrInvalid, at.Point)
	}
	out := PlaceRecommendation{Point: at.Point, Place: at.Place, Regions: []string{region.Base}}
	var lookup *agro.Note
	if at.Place == (region.Place{}) && p.locator != nil {
		loc, err := p.locator.Locate(ctx, at.Point)
		switch {
		case err == nil:
			out.Location, out.Place = &loc, placeOf(&loc)
		case ctx.Err() != nil:
			return PlaceRecommendation{}, ctx.Err()
		default:
			lookup = &agro.Note{ID: "place.unknown", Message: "Where this is could not be looked up, so the district's own sowing windows were not used."}
		}
	}
	lib := p.lib
	if p.regions != nil {
		pk := p.regions.For(out.Place)
		lib, out.Regions = pk.Library(), pk.Layers()
	}
	var prof agro.Profile
	if at.Profile != nil {
		prof = *at.Profile
		if err := prof.Check(lib); err != nil {
			return PlaceRecommendation{}, err
		}
	}
	out.Assumed = Assumptions(prof)
	rec, err := p.recommend(ctx, spot{point: at.Point, profile: at.Profile, lib: lib}, ask)
	if err != nil {
		return PlaceRecommendation{}, err
	}
	if lookup != nil {
		rec.Notes = append(rec.Notes, *lookup)
	}
	out.Recommendation = rec
	return out, nil
}

// Layout cuts a farm into strips for the parts and checks the plan.
func (p *Planner) Layout(ctx context.Context, id string, parts []agro.Part) (agro.Plan, agro.PlanReport, error) {
	f, err := p.farms.Get(ctx, id)
	if err != nil {
		return agro.Plan{}, agro.PlanReport{}, err
	}
	plan, err := agro.Layout(p.LibraryFor(f), f.Boundary.Ring(), parts)
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
	lib := p.LibraryFor(f)
	if f.Profile != nil {
		plot.Profile = *f.Profile
		if s, err := lib.Soil(f.Profile.Soil); err == nil {
			plot.Soil = &s
		}
	}
	rep := agro.CheckPlan(lib, plot, plan, p.Today())
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
