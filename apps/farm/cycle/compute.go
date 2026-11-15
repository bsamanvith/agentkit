package cycle

import (
	"cmp"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
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
	line.Assumed = in.Profile.Rainfed() && in.Profile.Soil == ""
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
			from = later(today, parse(prev.Harvest).AddDate(0, 0, Turnaround))
		} else if own != nil && parse(own.Sow).Before(today) {
			from = parse(own.Sow) // the season the farmer is in
		}
		p.From = from.Format(agro.DateLayout)
		if in.Climate != nil {
			var rest time.Time
			p.Suggested, rest = in.suggest(pos, from, prev, own)
			if pos == 0 && own == nil && p.Suggested != nil && !rest.IsZero() {
				p.Rest = in.rest(rest)
			}
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
		c.Season = agro.SeasonOf(parse(c.Sow))
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
func rotation(prev *agro.Crop, c agro.Crop) (int, *agro.Note, *agro.Note) {
	if prev == nil {
		return 0, nil, nil
	}
	switch {
	case prev.Family == c.Family:
		return -20, nil, &agro.Note{ID: "rotation.same", Message: fmt.Sprintf("%s after %s, the same family: pests carry over and the soil tires.", c.Name, prev.Name)}
	case legume(c) && !legume(*prev):
		return 10, &agro.Note{ID: "rotation.pulse", Message: fmt.Sprintf("A legume after %s puts nitrogen back in the soil.", prev.Name)}, nil
	case legume(*prev) && c.Kind == "cereal":
		return 5, &agro.Note{ID: "rotation.after-pulse", Message: fmt.Sprintf("%s uses the nitrogen %s left in the soil.", c.Name, prev.Name)}, nil
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

// How the suggestion weighs a sowing soon against a better one later.
const (
	// SoonDays is how far ahead of the day the land is free a sowing
	// counts as soon: a crop that suits sowing by then is preferred to a
	// better one later, so the land is not left waiting for months.
	SoonDays = 42
	// SoonScore is the sowing score from which a crop sown soon suits the
	// land well enough to be preferred: the recommender's good.
	SoonScore = agro.Good
	// ReadyDays is the time a farmer needs to get seed and ready the land
	// before a sowing suggested for the current cycle: a sowing soon is
	// not suggested sooner, unless only a sooner one suits.
	ReadyDays = 7
)

// pick is one crop chosen for an area, sown on sow.
type pick struct {
	crop       agro.Crop
	sow        string
	score, adj int
	good, bad  *agro.Note
	reasons    []agro.Note
	risks      []agro.Note
}

// suggest works out the suggestion at pos from the day the land is free:
// the areas of the cycle before (or the farmer's own here, or the whole
// plot), each with the crop that scores best there, rotation counted. A
// crop that suits sowing within SoonDays (SoonScore), ReadyDays from now
// for a current cycle when it can be, comes first; else the
// best one sown within MaxWait. Nil when no crop fits. rest is the first
// day any crop suits sowing when none does soon (zero when one does).
func (in Input) suggest(pos int, from time.Time, prev, own *Cycle) (sug *Cycle, rest time.Time) {
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
		if !c.Perennial {
			annuals = append(annuals, c.ID)
		}
	}
	req := agro.Request{Climate: *in.Climate, Soil: in.Soil, Profile: in.Profile, Slope: in.Slope, Today: from, Crops: annuals, Calendar: true}
	res, err := agro.Recommend(in.Library, req)
	if err != nil {
		return nil, time.Time{}
	}
	soonBy := from.AddDate(0, 0, SoonDays)
	ready := 0
	if pos == 0 && prev == nil {
		ready = ReadyDays
	}
	waitOf := func(sow string) int { return int(parse(sow).Sub(from).Hours() / 24) }
	// The first day anything suits sowing, when nothing does soon.
	var first string
	for _, o := range res.Options {
		for _, s := range o.Calendar {
			if s.Score >= SoonScore && waitOf(s.Sow) >= 0 && (first == "" || s.Sow < first) {
				first = s.Sow
			}
		}
	}
	if first != "" && parse(first).After(soonBy) {
		rest = parse(first)
	}
	plan := agro.Plan{Zones: make([]agro.Zone, len(rings))}
	var reasons, risks []agro.Note
	score := 0
	for i, ring := range rings {
		before := in.previous(prev, i, len(rings))
		var soon, later *pick
		for _, o := range res.Options {
			c, err := in.Library.Crop(o.Crop)
			if err != nil {
				continue
			}
			bonus, good, bad := rotation(before, c)
			// Soon: the crop's best date within SoonDays that suits, with
			// time to get ready first when that can be had.
			for _, s := range o.Calendar {
				wait := waitOf(s.Sow)
				if wait < 0 || wait > SoonDays || s.Score < SoonScore {
					continue
				}
				adj := s.Score + bonus - wait/10
				if wait < ready {
					adj -= 100 // only when nothing suits after the time to get ready
				}
				if soon == nil || adj > soon.adj {
					soon = &pick{crop: c, sow: s.Sow, score: max(0, min(100, s.Score+bonus)), adj: adj, good: good, bad: bad}
				}
			}
			// Later: the crop's best sowing, within MaxWait.
			if o.Score < agro.Fair {
				continue
			}
			wait := waitOf(o.Sow)
			if wait > MaxWait {
				continue
			}
			if adj := o.Score + bonus - wait/10; later == nil || adj > later.adj {
				later = &pick{crop: c, sow: o.Sow, score: max(0, min(100, o.Score+bonus)), adj: adj, good: good, bad: bad, reasons: o.Reasons, risks: o.Risks}
			}
		}
		chosen := cmp.Or(soon, later)
		if chosen == nil {
			return nil, rest
		}
		if i == 0 && first == "" && soon == nil && parse(chosen.sow).After(soonBy) {
			rest = parse(chosen.sow) // nothing suits well at all: the land waits for this
		}
		var soonNote *agro.Note
		if soon != nil {
			s := agro.Simulate(soon.crop, req, parse(soon.sow))
			soon.reasons, soon.risks = s.Reasons, s.Risks
			if later != nil && later.crop.ID != soon.crop.ID && parse(later.sow).After(soonBy) {
				soonNote = &agro.Note{ID: "sow.soon", Message: fmt.Sprintf("It can be sown within %d weeks; %s would leave the land empty until %s.", SoonDays/7, later.crop.Name, parse(later.sow).Format("January"))}
			}
		}
		plan.Zones[i] = agro.Zone{Crop: chosen.crop.ID, Ring: slicesClone(ring), Sow: chosen.sow}
		notes := []*agro.Note{soonNote, chosen.good, chosen.bad}
		if len(rings) > 1 {
			for k := range notes {
				if notes[k] != nil {
					n := *notes[k]
					n.Message = fmt.Sprintf("Area %d: %s", i+1, n.Message)
					notes[k] = &n
				}
			}
		}
		for _, n := range notes[:2] {
			if n != nil {
				reasons = append(reasons, *n)
			}
		}
		if notes[2] != nil {
			risks = append(risks, *notes[2])
		}
		if i == 0 {
			reasons = append(reasons, chosen.reasons...)
			risks = append(risks, chosen.risks...)
		}
		score += chosen.score
	}
	c, err := in.cycleOf(plan, Suggested, methodOf(in.Record.Slots[pos]), nil)
	if err != nil {
		return nil, rest
	}
	c.Score = score / len(rings)
	c.Reasons, c.Risks = reasons, risks
	return &c, rest
}

// rest says that nothing suits sowing before day's month: the land rests.
func (in Input) rest(day time.Time) *agro.Note {
	where := "on this land"
	if in.Profile.Rainfed() {
		where = "on rainfed land"
	}
	return &agro.Note{ID: "cycle.rest", Message: fmt.Sprintf("Nothing suits sowing %s before %s.", where, day.Format("January"))}
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
			case gap < Turnaround:
				add("cycle.turnaround", agro.Info, i, "Only %d days from the %s harvest to your %s sowing to prepare the land.", gap, pa.CropName, label)
			}
		}
		crop, err := in.Library.Crop(a.Crop)
		if err != nil {
			continue
		}
		if pos > 0 {
			if _, good, bad := rotation(in.previous(prev, i, len(own.Areas)), crop); bad != nil {
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
