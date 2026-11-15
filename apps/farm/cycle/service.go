package cycle

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/planner"
	"github.com/bsamanvith/agentkit/apps/farm/work"
)

// Roadmap is what the line needs of the season roadmap: the real sowing
// dates of the saved plan, and recording a sowing the farmer typed.
// *work.Service is one.
type Roadmap interface {
	Roadmap(ctx context.Context, farmID string) (work.Roadmap, error)
	Record(ctx context.Context, farmID string, evs []work.Event) (int, work.Roadmap, error)
}

// Option configures [New].
type Option func(*Service)

// WithRoadmap reads real sowing dates from r and records typed ones on
// it; without it, only typed dates count.
func WithRoadmap(r Roadmap) Option { return func(s *Service) { s.roadmap = r } }

// WithAddons adds what other features compute from each planned cycle.
func WithAddons(a ...Addon) Option { return func(s *Service) { s.addons = append(s.addons, a...) } }

// Service keeps plots' lines of cycles. It is safe for concurrent use.
type Service struct {
	plan    *planner.Planner
	store   Store
	roadmap Roadmap
	addons  []Addon
	now     func() time.Time
}

// New returns a Service over the planner's plots, with the farmer's own
// later cycles in store.
func New(p *planner.Planner, store Store, opts ...Option) *Service {
	s := &Service{plan: p, store: store, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	return s
}

// SetNow sets the clock records are stamped with (for tests).
func (s *Service) SetNow(now func() time.Time) { s.now = now }

// Line returns a plot's line of cycles.
func (s *Service) Line(ctx context.Context, farmID string) (Line, error) {
	f, err := s.plan.Farms().Get(ctx, farmID)
	if err != nil {
		return Line{}, err
	}
	rec, err := s.store.Get(ctx, f.ID)
	if err != nil {
		return Line{}, err
	}
	return s.line(ctx, f, rec, false)
}

func (s *Service) input(ctx context.Context, f farm.Farm, rec Record) Input {
	in := Input{
		Library: s.plan.Library(), Farm: f.ID, Ring: f.Boundary.Ring(), Current: f.Plan,
		Record: rec, Today: s.plan.Today(), Sown: map[int]string{},
	}
	if f.Profile != nil {
		in.Profile = *f.Profile
		if soil, err := in.Library.Soil(f.Profile.Soil); err == nil {
			in.Soil = &soil
		}
	}
	if sl := planner.SlopeOf(f); sl != nil {
		in.Slope = *sl
	}
	if c, err := s.plan.Climate(ctx, f.ID); err == nil {
		in.Climate = &c
	}
	if s.roadmap != nil && f.Plan != nil && len(f.Plan.Zones) > 0 {
		// The roadmap is advice: when it cannot be read, planned dates stand.
		if r, err := s.roadmap.Roadmap(ctx, f.ID); err == nil {
			for _, z := range r.Zones {
				if z.Sown != "" {
					in.Sown[z.Zone] = z.Sown
				}
			}
		}
	}
	return in
}

func (s *Service) line(ctx context.Context, f farm.Farm, rec Record, all bool) (Line, error) {
	in := s.input(ctx, f, rec)
	in.AllPositions = all
	line, err := Compute(in)
	if err != nil {
		return Line{}, err
	}
	for i := range line.Cycles {
		p := &line.Cycles[i]
		if p.Planned == nil {
			continue
		}
		for _, a := range s.addons {
			v, err := a.For(ctx, f, *p.Planned)
			if err != nil {
				if ctx.Err() != nil {
					return Line{}, ctx.Err()
				}
				what := a.Name()
				if l, ok := a.(interface{ Label() string }); ok {
					what = l.Label()
				}
				line.Notes = append(line.Notes, agro.Note{ID: "addon." + a.Name(), Message: fmt.Sprintf("Cycle %d: %s is not available now.", i+1, what)})
				continue
			}
			if v == nil {
				continue // nothing to say for this cycle
			}
			if p.Addons == nil {
				p.Addons = map[string]any{}
			}
			p.Addons[a.Name()] = v
		}
	}
	return line, nil
}

// Change is a change to one position of the line: accept the suggestion,
// set the farmer's own plan, clear it (back to the suggestion), and or
// set the method.
type Change struct {
	// Accept takes the suggestion as the farmer's own.
	Accept bool `json:"accept,omitempty"`
	// Plan is the farmer's own plan. A zone without a boundary takes the
	// same area of the cycle before (or the whole plot, for one zone).
	Plan *agro.Plan `json:"plan,omitempty"`
	// Clear drops the farmer's own plan at a later position, so the
	// suggestion is planned again.
	Clear bool `json:"clear,omitempty"`
	// Method sets how the cycle is grown; empty leaves it.
	Method Method `json:"method,omitempty"`
}

// Set changes the cycle at position pos and returns the line after. At
// position 0 an own plan, or an accepted suggestion, is checked and saved
// as the plot's plan (a plan with errors is a *planner.PlanError); later
// own plans are checked the same way and kept here.
func (s *Service) Set(ctx context.Context, farmID string, pos int, ch Change) (Line, error) {
	if farm.IsReadOnly(ctx) {
		return Line{}, farm.ErrReadOnly
	}
	if pos < 0 || pos >= MaxCycles {
		return Line{}, fmt.Errorf("%w: position %d: want 0 to %d", ErrInvalid, pos, MaxCycles-1)
	}
	n := 0
	for _, b := range []bool{ch.Accept, ch.Plan != nil, ch.Clear} {
		if b {
			n++
		}
	}
	if n > 1 || (n == 0 && ch.Method == "") {
		return Line{}, fmt.Errorf("%w: give one of accept, plan or clear, and or a method", ErrInvalid)
	}
	if ch.Method != "" && ch.Method != Organic && ch.Method != Conventional {
		return Line{}, fmt.Errorf("%w: method %q: want organic or conventional", ErrInvalid, ch.Method)
	}
	if ch.Clear && pos == 0 {
		return Line{}, fmt.Errorf("%w: the current cycle is the plot's saved plan: change it, or remove it in the season plan", ErrInvalid)
	}
	return s.update(ctx, farmID, func(f *farm.Farm, rec *Record) error {
		if pos > 0 && rec.FutureOff {
			return fmt.Errorf("%w: later cycles are turned off for this plot: turn them on first", ErrInvalid)
		}
		var plan *agro.Plan
		switch {
		case ch.Accept:
			line, err := s.line(ctx, *f, *rec, false)
			if err != nil {
				return err
			}
			if pos >= len(line.Cycles) || line.Cycles[pos].Suggested == nil {
				return fmt.Errorf("%w: nothing is suggested for cycle %d now", ErrInvalid, pos+1)
			}
			p := clonePlan(line.Cycles[pos].Suggested.Plan)
			plan = &p
		case ch.Plan != nil:
			p, err := s.fill(ctx, *f, *rec, pos, *ch.Plan)
			if err != nil {
				return err
			}
			plan = &p
		case ch.Clear:
			rec.Slots[pos].Plan = nil
		}
		if plan != nil {
			if pos == 0 {
				saved, err := s.plan.SavePlan(ctx, f.ID, *plan)
				if err != nil {
					return err
				}
				*f = saved
			} else {
				rep, err := s.plan.Check(ctx, f.ID, *plan)
				if err != nil {
					return err
				}
				if rep.Status == agro.PlanInvalid {
					return &planner.PlanError{Report: rep}
				}
				rec.Slots[pos].Plan = plan
			}
		}
		if ch.Method != "" {
			rec.Slots[pos].Method = ch.Method
		}
		return nil
	})
}

// fill gives a plan's zones without a boundary the same area of the cycle
// before, or the whole plot when the plan has one zone.
func (s *Service) fill(ctx context.Context, f farm.Farm, rec Record, pos int, plan agro.Plan) (agro.Plan, error) {
	plan = clonePlan(plan)
	if len(plan.Zones) == 0 {
		return plan, nil // the plan check says what is wrong
	}
	var before []agro.Zone
	if pos > 0 {
		line, err := s.line(ctx, f, rec, true)
		if err != nil {
			return agro.Plan{}, err
		}
		if pos-1 < len(line.Cycles) && line.Cycles[pos-1].Planned != nil {
			before = line.Cycles[pos-1].Planned.Plan.Zones
		}
	} else if f.Plan != nil {
		before = f.Plan.Zones
	}
	for i := range plan.Zones {
		z := &plan.Zones[i]
		if len(z.Ring) > 0 {
			continue
		}
		switch {
		case len(before) == len(plan.Zones):
			z.Ring = slicesClone(before[i].Ring)
		case len(plan.Zones) == 1:
			z.Ring = f.Boundary.Ring()
		default:
			return agro.Plan{}, fmt.Errorf("%w: zone %d has no boundary, and the cycle before has %d areas, not %d", ErrInvalid, i+1, len(before), len(plan.Zones))
		}
	}
	return plan, nil
}

// SetSown records that the current cycle was sown on day (YYYY-MM-DD),
// which becomes its date and moves what follows. With no saved plan, the
// suggested current cycle is saved first, sown that day; a saved plan
// planned more than work.MaxSowShift days from day is saved again with
// that sowing. With the roadmap, the sowing is recorded there too.
func (s *Service) SetSown(ctx context.Context, farmID, day string) (Line, error) {
	if farm.IsReadOnly(ctx) {
		return Line{}, farm.ErrReadOnly
	}
	d, err := time.Parse(agro.DateLayout, day)
	if err != nil {
		return Line{}, fmt.Errorf("%w: day %q: want YYYY-MM-DD", ErrInvalid, day)
	}
	today := s.plan.Today()
	if d.After(today.AddDate(0, 0, 1)) {
		return Line{}, fmt.Errorf("%w: day %s is in the future", ErrInvalid, day)
	}
	if d.Before(today.AddDate(-1, 0, 0)) {
		return Line{}, fmt.Errorf("%w: day %s is more than a year ago", ErrInvalid, day)
	}
	return s.update(ctx, farmID, func(f *farm.Farm, rec *Record) error {
		if f.Plan == nil || len(f.Plan.Zones) == 0 {
			line, err := s.line(ctx, *f, *rec, false)
			if err != nil {
				return err
			}
			if len(line.Cycles) == 0 || line.Cycles[0].Suggested == nil {
				return fmt.Errorf("%w: the plot has no plan and nothing is suggested: say the crop first", ErrInvalid)
			}
			p := clonePlan(line.Cycles[0].Suggested.Plan)
			for i := range p.Zones {
				p.Zones[i].Sow = day
			}
			saved, err := s.plan.SavePlan(ctx, f.ID, p)
			if err != nil {
				return err
			}
			*f = saved
		} else if far(*f.Plan, d) {
			// Sown long before or after the plan's date (the roadmap takes a
			// sowing up to work.MaxSowShift days from it): the farmer grew
			// these crops, so the plan is saved again with that sowing.
			p := clonePlan(*f.Plan)
			for i := range p.Zones {
				p.Zones[i].Sow = day
			}
			saved, err := s.plan.SavePlan(ctx, f.ID, p)
			if err != nil {
				return err
			}
			*f = saved
		}
		if s.roadmap != nil {
			if err := s.recordSown(ctx, *f, day); err != nil {
				return err
			}
		}
		rec.Slots[0].Sown, rec.Slots[0].SownFor = day, PlanKey(*f.Plan)
		return nil
	})
}

// far: some zone of the plan is planned more than work.MaxSowShift days
// from day.
func far(p agro.Plan, day time.Time) bool {
	for _, z := range p.Zones {
		sow, err := time.Parse(agro.DateLayout, z.Sow)
		if err != nil {
			continue
		}
		if d := day.Sub(sow).Hours() / 24; d > work.MaxSowShift || d < -work.MaxSowShift {
			return true
		}
	}
	return false
}

// recordSown marks each zone's sowing done on day on the roadmap,
// reopening it first so the new day wins over an earlier one.
func (s *Service) recordSown(ctx context.Context, f farm.Farm, day string) error {
	at := s.now().UTC()
	// The day and the plan's sowing are in the IDs too: two records in
	// one instant (a stopped clock) are still two.
	stamp := strconv.FormatInt(at.UnixNano(), 36) + "-" + day
	var evs []work.Event
	for i, z := range f.Plan.Zones {
		task := work.TaskID(i, z.Crop, z.Sow, "sow")
		evs = append(evs,
			work.Event{ID: fmt.Sprintf("cycle-%s-z%d-%s-r", stamp, i, z.Sow), Task: task, Kind: work.Reopened, At: at, By: "cycles"},
			work.Event{ID: fmt.Sprintf("cycle-%s-z%d-%s-s", stamp, i, z.Sow), Task: task, Kind: work.Done, Day: day, At: at.Add(time.Nanosecond), By: "cycles"},
		)
	}
	_, _, err := s.roadmap.Record(ctx, f.ID, evs)
	if errors.Is(err, work.ErrInvalid) {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return err
}

// SetFuture turns the later cycles on or off for a plot. Off keeps the
// farmer's own later cycles, unshown.
func (s *Service) SetFuture(ctx context.Context, farmID string, on bool) (Line, error) {
	if farm.IsReadOnly(ctx) {
		return Line{}, farm.ErrReadOnly
	}
	return s.update(ctx, farmID, func(_ *farm.Farm, rec *Record) error {
		rec.FutureOff = !on
		return nil
	})
}

// Advance moves the line on once the current cycle is over: the next
// planned cycle becomes the plot's saved plan, the current one's main
// crop its previous crop, and the later own cycles move up a place.
func (s *Service) Advance(ctx context.Context, farmID string) (Line, error) {
	if farm.IsReadOnly(ctx) {
		return Line{}, farm.ErrReadOnly
	}
	return s.update(ctx, farmID, func(f *farm.Farm, rec *Record) error {
		if f.Plan == nil || len(f.Plan.Zones) == 0 {
			return fmt.Errorf("%w: the plot has no current cycle to move on from", ErrInvalid)
		}
		line, err := s.line(ctx, *f, *rec, true)
		if err != nil {
			return err
		}
		if len(line.Cycles) < 2 || line.Cycles[1].Planned == nil {
			return fmt.Errorf("%w: nothing is planned after the current cycle: set the next one first", ErrInvalid)
		}
		cur, next := line.Cycles[0].Planned, line.Cycles[1].Planned
		prof := agro.Profile{}
		if f.Profile != nil {
			prof = *f.Profile
		}
		prof.PreviousCrop = cur.MainCrop()
		if _, err := s.plan.SetProfile(ctx, f.ID, prof); err != nil {
			return err
		}
		saved, err := s.plan.SavePlan(ctx, f.ID, clonePlan(next.Plan))
		if err != nil {
			return err
		}
		*f = saved
		rec.Slots = [MaxCycles]Slot{{Method: rec.Slots[1].Method}, rec.Slots[2], {}}
		return nil
	})
}

// update reads the plot and its record, applies edit and stores the
// record, again from the start when it changed in between; then returns
// the line.
func (s *Service) update(ctx context.Context, farmID string, edit func(*farm.Farm, *Record) error) (Line, error) {
	for try := 0; ; try++ {
		f, err := s.plan.Farms().Get(ctx, farmID)
		if err != nil {
			return Line{}, err
		}
		rec, err := s.store.Get(ctx, f.ID)
		if err != nil {
			return Line{}, err
		}
		if err := edit(&f, &rec); err != nil {
			return Line{}, err
		}
		rec.Farm, rec.UpdatedAt = f.ID, s.now().UTC()
		stored, err := s.store.Put(ctx, rec)
		if errors.Is(err, ErrConflict) && try < 3 {
			continue
		}
		if err != nil {
			return Line{}, err
		}
		return s.line(ctx, f, stored, false)
	}
}
