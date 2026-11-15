package cycle

import (
	"cmp"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/region"
	"github.com/bsamanvith/agentkit/apps/farm/sun"
)

// Input is what [Compute] works from.
type Input struct {
	Library *agro.Library
	Farm    string
	// Ring is the plot's boundary: a suggestion with no cycle before it
	// covers the whole plot.
	Ring geo.Ring
	// Profile is the plot's land and water; its PreviousCrop is the last
	// crop before the first cycle.
	Profile agro.Profile
	Soil    *agro.Soil
	Slope   sun.Slope
	// Climate is nil when it could not be had: then nothing is suggested,
	// and the farmer's own cycles are checked without sowing scores.
	Climate *agro.Climate
	// Current is the plot's saved plan: the farmer's own current cycle;
	// nil when none.
	Current *agro.Plan
	Record  Record
	// Sown is the real sowing date of each zone of Current that has one
	// (from the season roadmap), by zone.
	Sown map[int]string
	// Today is the farmer's date.
	Today time.Time
	// AllPositions computes every position even when the farmer turned
	// the later cycles off (to advance the line).
	AllPositions bool
	// Rules are the cycle rules at the plot (its region pack's); zero for
	// region.DefaultRules.
	Rules region.Rules
}

// PlanKey identifies a plan's crops and sowing dates: a sowing date typed
// for one plan does not apply to another.
func PlanKey(p agro.Plan) string {
	var b strings.Builder
	for _, z := range p.Zones {
		fmt.Fprintf(&b, "%s+%s@%s;", z.Crop, z.With, z.Sow)
	}
	return b.String()
}

// Compute works out a plot's line by the rules: for each position, the
// suggestion from the planned cycle before it, the farmer's own, the
// planned one and the checks. It is deterministic.
func Compute(in Input) (Line, error) {
	today := day(in.Today)
	line := Line{Farm: in.Farm, Today: today.Format(agro.DateLayout), Future: !in.Record.FutureOff, Cycles: []Position{}}
	n := MaxCycles
	if in.Record.FutureOff && !in.AllPositions {
		n = 1
	}
	if in.Climate == nil {
		line.Notes = append(line.Notes, agro.Note{ID: "climate.none", Message: "The climate could not be looked up, so nothing is suggested now; your own cycles still count."})
	}
	var prev *Cycle
	for pos := range n {
		p := Position{Position: pos}
		own, err := in.own(pos)
		if err != nil {
			return Line{}, err
		}
		from := today
		if prev != nil {
			from = later(today, parse(prev.Harvest).AddDate(0, 0, in.Rules.Or().TurnaroundDays))
		} else if own != nil && parse(own.Sow).Before(today) {
			from = parse(own.Sow) // the season the farmer is in
		}
		p.From = from.Format(agro.DateLayout)
		if in.Climate != nil {
			p.Suggested = in.suggest(pos, from, prev, own)
		}
		p.Own = own
		p.Planned = cmp.Or(own, p.Suggested)
		if own != nil {
			p.Checks = in.check(own, prev, pos, today)
		}
		line.Cycles = append(line.Cycles, p)
		if p.Planned == nil {
			break
		}
		if p.Planned.Perennial {
			if pos < n-1 {
				line.Notes = append(line.Notes, agro.Note{ID: "cycle.perennial", Message: fmt.Sprintf("%s is a perennial: it holds the land, so no later cycle is planned.", cropName(in.Library, p.Planned.MainCrop()))})
			}
			break
		}
		prev = p.Planned
	}
	return line, nil
}

// own returns the farmer's own cycle at pos, or nil.
func (in Input) own(pos int) (*Cycle, error) {
	slot := in.Record.Slots[pos]
	var plan *agro.Plan
	if pos == 0 {
		plan = in.Current
	} else {
		plan = slot.Plan
	}
	if plan == nil || len(plan.Zones) == 0 {
		return nil, nil
	}
	sown := map[int]string{}
	if pos == 0 {
		for i := range plan.Zones {
			if d := in.Sown[i]; d != "" {
				sown[i] = d
			} else if slot.Sown != "" && slot.SownFor == PlanKey(*plan) {
				sown[i] = slot.Sown
			}
		}
	}
	c, err := in.cycleOf(*plan, Farmer, methodOf(slot), sown)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func methodOf(s Slot) Method { return cmp.Or(s.Method, Conventional) }

// cycleOf makes a cycle of a plan: its areas, dates and season.
func (in Input) cycleOf(plan agro.Plan, origin Origin, m Method, sown map[int]string) (Cycle, error) {
	c := Cycle{Origin: origin, Method: m, Plan: clonePlan(plan), Areas: make([]Area, len(plan.Zones)), SowKind: Planned}
	for i, z := range plan.Zones {
		crop, err := in.Library.Crop(z.Crop)
		if err != nil {
			return Cycle{}, fmt.Errorf("%w: zone %d: %w", ErrInvalid, i+1, err)
		}
		a := Area{Zone: i, Crop: z.Crop, CropName: crop.Name, With: z.With, Sow: z.Sow, SowKind: Planned}
		if d := sown[i]; d != "" {
			a.Sow, a.SowKind = d, Actual
			c.SowKind = Actual
		}
		sow, err := time.Parse(agro.DateLayout, a.Sow)
		if err != nil {
			return Cycle{}, fmt.Errorf("%w: zone %d: sowing date %q: want YYYY-MM-DD", ErrInvalid, i+1, a.Sow)
		}
		if len(z.Ring.Open()) >= 3 {
			a.AreaM2 = math.Round(geo.Area(z.Ring)*10) / 10
			a.AreaHa = math.Round(a.AreaM2/10) / 1000
		}
		harvest := sow.AddDate(0, 0, days(crop))
		if z.With != "" {
			w, err := in.Library.Crop(z.With)
			if err != nil {
				return Cycle{}, fmt.Errorf("%w: zone %d: %w", ErrInvalid, i+1, err)
			}
			harvest = later(harvest, sow.AddDate(0, 0, days(w)))
			c.Perennial = c.Perennial || w.Perennial
		}
		c.Perennial = c.Perennial || crop.Perennial
		a.Harvest = harvest.Format(agro.DateLayout)
		c.Areas[i] = a
		if c.Sow == "" || a.Sow < c.Sow {
			c.Sow = a.Sow
		}
		if a.Harvest > c.Harvest {
			c.Harvest = a.Harvest
		}
	}
	if c.Sow != "" {
		c.Season = in.Rules.Or().SeasonOf(parse(c.Sow))
	}
	return c, nil
}

// days is a crop's season, as the recommender counts it: the middle of
// its days to harvest (a perennial: a year to its first).
func days(c agro.Crop) int {
	if c.Perennial {
		return 365
	}
	return int(math.Round(c.Days.Mid()))
}

// rotation scores a crop after the one before: the same family twice is
// marked down, a legume after a cereal (or any non-legume) puts nitrogen
// back, a cereal after a legume uses what it left.
func rotation(r region.Rotation, prev *agro.Crop, c agro.Crop) (int, *agro.Note, *agro.Note) {
	if prev == nil {
		return 0, nil, nil
	}
	switch {
	case prev.Family == c.Family:
		return r.SameFamily, nil, &agro.Note{ID: "rotation.same", Message: fmt.Sprintf("%s after %s, the same family: pests carry over and the soil tires.", c.Name, prev.Name)}
	case legume(c) && !legume(*prev):
		return r.LegumeAfterOther, &agro.Note{ID: "rotation.pulse", Message: fmt.Sprintf("A legume after %s puts nitrogen back in the soil.", prev.Name)}, nil
	case legume(*prev) && c.Kind == "cereal":
		return r.CerealAfterLegume, &agro.Note{ID: "rotation.after-pulse", Message: fmt.Sprintf("%s uses the nitrogen %s left in the soil.", c.Name, prev.Name)}, nil
	}
	return 0, &agro.Note{ID: "rotation.ok", Message: fmt.Sprintf("%s after %s is a change of family: a good rotation.", c.Name, prev.Name)}, nil
}

func legume(c agro.Crop) bool { return c.Family == "Fabaceae" }

// previous returns the crop before area i of a cycle with n areas: the
// same area's when the cycle before has as many, else its main crop; the
// profile's previous crop before the first cycle.
func (in Input) previous(prev *Cycle, i, n int) *agro.Crop {
	id := in.Profile.PreviousCrop
	if prev != nil {
		id = prev.MainCrop()
		if len(prev.Areas) == n {
			id = prev.Areas[i].Crop
		}
	}
	c, err := in.Library.Crop(id)
	if err != nil {
		return nil
	}
	return &c
}

// suggest works out the suggestion at pos from the day the land is free:
// the areas of the cycle before (or the farmer's own here, or the whole
// plot), each with the crop that scores best there, rotation counted, and
// sown soonest. Nil when no crop fits.
func (in Input) suggest(pos int, from time.Time, prev, own *Cycle) *Cycle {
	var rings []geo.Ring
	switch {
	case own != nil:
		rings = ringsOf(own.Plan)
	case prev != nil:
		rings = ringsOf(prev.Plan)
	default:
		rings = []geo.Ring{in.Ring}
	}
	var annuals []string
	for _, c := range in.Library.Crops() {
		if !c.Perennial && !c.Off {
			annuals = append(annuals, c.ID)
		}
	}
	req := agro.Request{Climate: *in.Climate, Soil: in.Soil, Profile: in.Profile, Slope: in.Slope, Today: from, Crops: annuals}
	res, err := agro.Recommend(in.Library, req)
	if err != nil {
		return nil
	}
	plan := agro.Plan{Zones: make([]agro.Zone, len(rings))}
	var reasons, risks []agro.Note
	score := 0
	for i, ring := range rings {
		before := in.previous(prev, i, len(rings))
		best, bestAdj, bestScore := -1, math.MinInt, 0
		var bestNotes [2]*agro.Note
		for k, o := range res.Options {
			c, err := in.Library.Crop(o.Crop)
			if err != nil || o.Score < agro.Fair {
				continue
			}
			wait := int(parse(o.Sow).Sub(from).Hours() / 24)
			if wait > in.Rules.Or().MaxWaitDays {
				continue
			}
			bonus, good, bad := rotation(in.Rules.Or().Rotation, before, c)
			adj := o.Score + bonus - wait/in.Rules.Or().WaitDaysPerPoint
			if adj > bestAdj {
				best, bestAdj, bestScore, bestNotes = k, adj, max(0, min(100, o.Score+bonus)), [2]*agro.Note{good, bad}
			}
		}
		if best < 0 {
			return nil
		}
		o := res.Options[best]
		plan.Zones[i] = agro.Zone{Crop: o.Crop, Ring: slicesClone(ring), Sow: o.Sow}
		if len(rings) > 1 {
			for k := range bestNotes {
				if bestNotes[k] != nil {
					n := *bestNotes[k]
					n.Message = fmt.Sprintf("Area %d: %s", i+1, n.Message)
					bestNotes[k] = &n
				}
			}
		}
		if bestNotes[0] != nil {
			reasons = append(reasons, *bestNotes[0])
		}
		if bestNotes[1] != nil {
			risks = append(risks, *bestNotes[1])
		}
		if i == 0 {
			reasons = append(reasons, o.Reasons...)
			risks = append(risks, o.Risks...)
		}
		score += bestScore
	}
	c, err := in.cycleOf(plan, Suggested, methodOf(in.Record.Slots[pos]), nil)
	if err != nil {
		return nil
	}
	c.Score = score / len(rings)
	c.Reasons, c.Risks = reasons, risks
	return &c
}

// check checks the farmer's own cycle against the planned cycle before
// it: sown before it comes off, little time to prepare, the same family
// again; and its own sowing score and dates.
func (in Input) check(own, prev *Cycle, pos int, today time.Time) []Check {
	var out []Check
	add := func(id string, sev agro.Severity, area int, format string, a ...any) {
		out = append(out, Check{ID: id, Severity: sev, Area: area, Message: fmt.Sprintf(format, a...)})
	}
	for i, a := range own.Areas {
		label := a.CropName
		if len(own.Areas) > 1 {
			label = fmt.Sprintf("area %d's %s", i+1, a.CropName)
		}
		sow := parse(a.Sow)
		if pos > 0 && a.SowKind == Planned && sow.Before(today) {
			add("cycle.past", agro.Warning, i, "Your %s sowing of %s is already past: change the date.", label, long(sow))
		}
		if prev != nil {
			pa := prev.Areas[0]
			if len(prev.Areas) == len(own.Areas) {
				pa = prev.Areas[i]
			} else {
				for _, x := range prev.Areas {
					if x.Harvest > pa.Harvest {
						pa = x
					}
				}
			}
			harvest := parse(pa.Harvest)
			switch gap := int(sow.Sub(harvest).Hours() / 24); {
			case gap < 0:
				add("cycle.overlap", agro.Warning, i, "%s now ends %s; your %s sowing of %s is before that.", pa.CropName, long(harvest), label, long(sow))
			case gap < in.Rules.Or().TurnaroundDays:
				add("cycle.turnaround", agro.Info, i, "Only %d days from the %s harvest to your %s sowing to prepare the land.", gap, pa.CropName, label)
			}
		}
		crop, err := in.Library.Crop(a.Crop)
		if err != nil {
			continue
		}
		if pos > 0 {
			if _, good, bad := rotation(in.Rules.Or().Rotation, in.previous(prev, i, len(own.Areas)), crop); bad != nil {
				add(bad.ID, agro.Warning, i, "%s", bad.Message)
			} else if good != nil && good.ID == "rotation.pulse" {
				add(good.ID, agro.Info, i, "%s", good.Message)
			}
			if in.Climate != nil {
				s := agro.Simulate(crop, agro.Request{Climate: *in.Climate, Soil: in.Soil, Profile: in.Profile, Slope: in.Slope, Today: today}, sow)
				switch {
				case s.Score < agro.Fair:
					add("sow.poor", agro.Warning, i, "Sowing %s on %s scores %d: pick another date or crop.", label, long(sow), s.Score)
				case s.Score < agro.Good:
					add("sow.fair", agro.Warning, i, "Sowing %s on %s scores only %d.", label, long(sow), s.Score)
				}
			}
		}
	}
	return out
}

func ringsOf(p agro.Plan) []geo.Ring {
	out := make([]geo.Ring, len(p.Zones))
	for i, z := range p.Zones {
		out[i] = z.Ring
	}
	return out
}

func clonePlan(p agro.Plan) agro.Plan {
	out := agro.Plan{Zones: make([]agro.Zone, len(p.Zones))}
	for i, z := range p.Zones {
		z.Ring = slicesClone(z.Ring)
		out.Zones[i] = z
	}
	return out
}

func slicesClone(r geo.Ring) geo.Ring {
	if r == nil {
		return nil
	}
	return append(geo.Ring{}, r...)
}

func cropName(l *agro.Library, id string) string {
	if c, err := l.Crop(id); err == nil {
		return c.Name
	}
	return id
}

func day(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC) }

func parse(s string) time.Time {
	d, _ := time.Parse(agro.DateLayout, s)
	return d
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// long writes a date as the farmer says it: 20 January 2027.
func long(t time.Time) string { return t.Format("2 January 2006") }
