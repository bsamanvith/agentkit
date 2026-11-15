package cycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
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
	if rec.Pending != nil && !farm.IsReadOnly(ctx) {
		// A write stopped before it changed the farm: finish it now.
		return s.update(ctx, farmID, func(*farm.Farm, *Record) error { return nil })
	}
	return s.line(ctx, f, rec, false)
}

func (s *Service) input(ctx context.Context, f farm.Farm, rec Record) Input {
	in := Input{
		Library: s.plan.LibraryFor(f), Rules: s.plan.RulesFor(f), Farm: f.ID, Ring: f.Boundary.Ring(), Current: f.Plan,
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
			if err := s.checkPlan(ctx, f.ID, *plan); err != nil {
				return err
			}
			if pos == 0 {
				pend(*f, rec, Pending{Plan: plan})
			} else {
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
		var save *agro.Plan
		if f.Plan == nil || len(f.Plan.Zones) == 0 {
			line, err := s.line(ctx, *f, *rec, false)
			if err != nil {
				return err
			}
			if len(line.Cycles) == 0 || line.Cycles[0].Suggested == nil {
				return fmt.Errorf("%w: the plot has no plan and nothing is suggested: say the crop first", ErrInvalid)
			}
			p := clonePlan(line.Cycles[0].Suggested.Plan)
			save = &p
		} else if far(*f.Plan, d) {
			// Sown long before or after the plan's date (the roadmap takes a
			// sowing up to work.MaxSowShift days from it): the farmer grew
			// these crops, so the plan is saved again with that sowing.
			p := clonePlan(*f.Plan)
			save = &p
		}
		sown := f.Plan
		if save != nil {
			for i := range save.Zones {
				save.Zones[i].Sow = day
			}
			if err := s.checkPlan(ctx, f.ID, *save); err != nil {
				return err
			}
			sown = save
		}
		p := Pending{Plan: save}
		if s.roadmap != nil {
			p.Sown = day
			if save != nil && f.Plan != nil && len(f.Plan.Zones) > 0 {
				carry, err := s.carry(ctx, *f, day)
				if err != nil {
					return err
				}
				p.Carry = carry
			}
		}
		pend(*f, rec, p)
		rec.Slots[0].Sown, rec.Slots[0].SownFor = day, PlanKey(*sown)
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

// carry returns the farmer's closes (done, skipped, not needed) of the
// saved plan's tasks as events on the same tasks once the plan is saved
// again sown on day: a task's ID carries the plan's sowing, so the save
// renames every one. The sowing itself is recorded apart. Event IDs are
// made from what they record, so recording them twice is once.
func (s *Service) carry(ctx context.Context, f farm.Farm, day string) ([]work.Event, error) {
	r, err := s.roadmap.Roadmap(ctx, f.ID)
	if err != nil {
		return nil, err
	}
	at := s.now().UTC()
	var out []work.Event
	for _, t := range r.Tasks {
		if t.State == work.Open || t.Source == work.FromFarmer || t.Zone < 0 || t.Zone >= len(f.Plan.Zones) {
			continue
		}
		z := f.Plan.Zones[t.Zone]
		key, ok := strings.CutPrefix(t.ID, work.TaskID(t.Zone, z.Crop, z.Sow, ""))
		if !ok || key == "sow" {
			continue
		}
		id := work.TaskID(t.Zone, z.Crop, day, key)
		h := sha256.Sum256([]byte(id + "|" + t.State + "|" + t.Day))
		out = append(out, work.Event{
			ID: "carry-" + hex.EncodeToString(h[:12]), Task: id, Kind: t.State, Day: t.Day,
			At: at, By: t.By, Note: t.Note, Actuals: t.Actuals,
		})
	}
	return out, nil
}

// recordCarry records the carried closes whose task the saved plan has.
func (s *Service) recordCarry(ctx context.Context, f farm.Farm, evs []work.Event) error {
	r, err := s.roadmap.Roadmap(ctx, f.ID)
	if err != nil {
		return err
	}
	evs = slices.DeleteFunc(slices.Clone(evs), func(e work.Event) bool { _, ok := r.Task(e.Task); return !ok })
	if len(evs) == 0 {
		return nil
	}
	_, _, err = s.roadmap.Record(ctx, f.ID, evs)
	return err
}

// sownOn: the roadmap already has every zone sown on day.
func (s *Service) sownOn(ctx context.Context, farmID, day string) bool {
	r, err := s.roadmap.Roadmap(ctx, farmID)
	if err != nil || len(r.Zones) == 0 {
		return false
	}
	for _, z := range r.Zones {
		if z.Sown != day {
			return false
		}
	}
	return true
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
// crop its previous crop, and the later own cycles move up a place. It
// cannot be undone, so it is refused (ErrInvalid) before the line's
// AdvanceFrom, AdvanceDays before the current harvest.
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
		if line.AdvanceFrom == "" {
			return fmt.Errorf("%w: the current cycle's harvest is not known (a perennial, or a crop not in the plot's list): it cannot be moved on", ErrInvalid)
		}
		if s.plan.Today().Before(parse(line.AdvanceFrom)) {
			return fmt.Errorf("%w: the current crop comes off around %s: the next cycle can start from %s", ErrInvalid, line.Cycles[0].Planned.Harvest, line.AdvanceFrom)
		}
		if len(line.Cycles) < 2 || line.Cycles[1].Planned == nil {
			return fmt.Errorf("%w: nothing is planned after the current cycle: set the next one first", ErrInvalid)
		}
		cur, next := line.Cycles[0].Planned, line.Cycles[1].Planned
		plan := clonePlan(next.Plan)
		if err := s.checkPlan(ctx, f.ID, plan); err != nil {
			return err
		}
		pend(*f, rec, Pending{PreviousCrop: cur.MainCrop(), Plan: &plan})
		rec.Slots = [MaxCycles]Slot{{Method: rec.Slots[1].Method}, rec.Slots[2], {}}
		return nil
	})
}

// update reads the plot and its record, applies edit and stores the
// record, again from the start when it changed in between; then makes
// the change edit left pending on the farm, and returns the line.
//
// An edit never writes the farm itself: it stores what to change in the
// record (Record.Pending), so the record's Put is the one place a write
// can lose a race, and a write that loses is done again from what is
// stored, or, after the last try, changes nothing (ErrConflict). Once
// the Put has won, the farm is changed (profile, then plan, then the
// roadmap's sowing) and the pending change is cleared from the record.
//
// A crash (or a failing farm store) between the Put and the farm writes
// leaves a record whose slots are already moved, with the change still
// pending, over a farm with its old plan. The next write, or the next
// Line that may write, finishes it first: over the plan it was made
// against it makes every step again (each is the same when made twice);
// over the plan it saves it makes only the roadmap step; over any other
// plan (changed in the season plan since) it is dropped, as overtaken.
// If the farm refuses the change (the plan no longer checks), the slots
// it moved go back.
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
		if rec.Pending != nil {
			if f, err = s.finish(ctx, f, &rec); err != nil && !refused(err) {
				return Line{}, err
			}
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
		if stored.Pending != nil {
			f, err = s.finish(ctx, f, &stored)
			if err != nil && !refused(err) {
				return Line{}, err // still pending: the next read finishes it
			}
			stored = s.settle(ctx, stored)
			if err != nil {
				return Line{}, err
			}
		}
		return s.line(ctx, f, stored, false)
	}
}

// pend has rec carry change p, made against f's saved plan, keeping
// rec's slots as they are now to put back if the farm refuses it.
func pend(f farm.Farm, rec *Record, p Pending) {
	p.From, p.Was = planSum(f.Plan), rec.Slots
	rec.Pending = &p
}

// planSum identifies a saved plan exactly (nil: ""). The farm store keeps
// a plan as it was given, so the hash of the plan saved is the hash of
// the plan read back.
func planSum(p *agro.Plan) string {
	if p == nil {
		return ""
	}
	b, err := json.Marshal(p)
	if err != nil {
		panic(err) // a plan always marshals
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// refused: the farm will never take the change (the plan no longer
// checks, the profile is refused), so trying again cannot help.
func refused(err error) bool {
	var pe *planner.PlanError
	return errors.As(err, &pe) || errors.Is(err, agro.ErrInvalid) || errors.Is(err, ErrInvalid)
}

// finish makes rec's pending change on the farm f and returns the farm
// after. Made, or overtaken (dropped): rec.Pending is cleared. Refused:
// rec's slots go back as well, and the refusal is returned. Any other
// error leaves rec as it was.
func (s *Service) finish(ctx context.Context, f farm.Farm, rec *Record) (farm.Farm, error) {
	p := *rec.Pending
	switch sum := planSum(f.Plan); {
	case sum == p.From:
		if p.PreviousCrop != "" {
			prof := agro.Profile{}
			if f.Profile != nil {
				prof = *f.Profile
			}
			prof.PreviousCrop = p.PreviousCrop
			g, err := s.plan.SetProfile(ctx, f.ID, prof)
			if err != nil {
				return f, s.undo(rec, err)
			}
			f = g
		}
		if p.Plan != nil {
			g, err := s.plan.SavePlan(ctx, f.ID, clonePlan(*p.Plan))
			if err != nil {
				return f, s.undo(rec, err)
			}
			f = g
		}
	case p.Plan != nil && sum == planSum(p.Plan):
		// The plan was saved: only the roadmap may be left.
	default:
		rec.Pending = nil // overtaken: the newer plan stands
		return f, nil
	}
	if len(p.Carry) > 0 && s.roadmap != nil && f.Plan != nil {
		if err := s.recordCarry(ctx, f, p.Carry); err != nil {
			return f, err // not refused: the next read tries again
		}
	}
	if p.Sown != "" && s.roadmap != nil && f.Plan != nil && !s.sownOn(ctx, f.ID, p.Sown) {
		if err := s.recordSown(ctx, f, p.Sown); err != nil {
			return f, s.undo(rec, err)
		}
	}
	rec.Pending = nil
	return f, nil
}

// undo puts rec's slots back when the farm refused its pending change,
// and returns err.
func (s *Service) undo(rec *Record, err error) error {
	if refused(err) {
		rec.Slots, rec.Pending = rec.Pending.Was, nil
	}
	return err
}

// settle stores rec, which no longer has the change pending. It is best
// effort: when another write got there first, that write finished the
// change itself (or the next read will), and what is stored is returned.
func (s *Service) settle(ctx context.Context, rec Record) Record {
	rec.UpdatedAt = s.now().UTC()
	if stored, err := s.store.Put(ctx, rec); err == nil {
		return stored
	}
	if stored, err := s.store.Get(ctx, rec.Farm); err == nil {
		return stored
	}
	return rec
}

// checkPlan checks a plan for the plot as SavePlan will.
func (s *Service) checkPlan(ctx context.Context, farmID string, plan agro.Plan) error {
	rep, err := s.plan.Check(ctx, farmID, plan)
	if err != nil {
		return err
	}
	if rep.Status == agro.PlanInvalid {
		return &planner.PlanError{Report: rep}
	}
	return nil
}
