package agro

import (
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/geo"
)

// Plan is a season's plan for one plot: which crop goes where, and when.
type Plan struct {
	Zones []Zone `json:"zones"`
}

// Zone is one part of a plot with one crop, or two crops in rows.
type Zone struct {
	// Crop is a crop id of the library.
	Crop string `json:"crop"`
	// With is a second crop sown in the same zone, in rows between the
	// first (an intercrop); Pattern is the rows of Crop to rows of With,
	// such as [4, 1].
	With    string `json:"with,omitempty"`
	Pattern [2]int `json:"pattern,omitempty"`
	// Ring is the zone's boundary, inside the plot.
	Ring geo.Ring `json:"ring"`
	// RowCM and PlantCM are the spacing; 0 for the crop's usual.
	RowCM   float64 `json:"row_cm,omitempty"`
	PlantCM float64 `json:"plant_cm,omitempty"`
	// RowBearing is the rows' direction, in degrees from north (0: north
	// to south).
	RowBearing float64 `json:"row_bearing"`
	// Sow is the sowing date, as DateLayout.
	Sow string `json:"sow"`
}

// Part is one crop's share of a plot, for [Layout].
type Part struct {
	Crop    string  `json:"crop"`
	With    string  `json:"with,omitempty"`
	Pattern [2]int  `json:"pattern,omitempty"`
	Share   float64 `json:"share"` // of the plot's area, 0 to 1
	Sow     string  `json:"sow"`
}

// Layout cuts the plot into strips along its long side, one per part in
// order, sized by their shares, and returns the plan with the usual
// spacing and rows running north to south.
func Layout(l *Library, plot geo.Ring, parts []Part) (Plan, error) {
	shares := make([]float64, len(parts))
	for i, p := range parts {
		if _, err := l.Crop(p.Crop); err != nil {
			return Plan{}, err
		}
		if p.With != "" {
			if _, err := l.Crop(p.With); err != nil {
				return Plan{}, err
			}
		}
		shares[i] = p.Share
	}
	rings, _, err := geo.Strips(plot, shares)
	if err != nil {
		return Plan{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	plan := Plan{Zones: make([]Zone, len(parts))}
	for i, p := range parts {
		z := Zone{Crop: p.Crop, With: p.With, Pattern: p.Pattern, Ring: rings[i], Sow: p.Sow}
		if z.With != "" && z.Pattern == [2]int{} {
			z.Pattern = [2]int{2, 1}
		}
		plan.Zones[i] = z
	}
	return plan, nil
}

// Plot is the plot a plan is for, as the checker sees it.
type Plot struct {
	Ring    geo.Ring
	Climate *Climate // nil when unavailable: sowing and water go unchecked
	Soil    *Soil
	Profile Profile
}

// Severity of a plan check.
type Severity string

// The severities, from good news to a plan that cannot work.
const (
	Info    Severity = "info"
	Warning Severity = "warning"
	Error   Severity = "error"
)

// PlanStatus is a checked plan's verdict.
type PlanStatus string

// The verdicts.
const (
	PlanSound       PlanStatus = "sound"
	PlanNeedsReview PlanStatus = "needs_review"
	PlanInvalid     PlanStatus = "invalid"
)

// PlanCheck is one finding about a plan. Zone is the zone's index, -1 for
// the plan as a whole; Other is a second zone it concerns, -1 for none.
type PlanCheck struct {
	ID       string   `json:"id"`
	Severity Severity `json:"severity"`
	Zone     int      `json:"zone"`
	Other    int      `json:"other"`
	Message  string   `json:"message"`
}

// ZoneMeasures are a zone's numbers, from its ring and spacing.
type ZoneMeasures struct {
	AreaM2 float64 `json:"area_m2"`
	// Share is of the plot's area.
	Share float64 `json:"share"`
	// Length and Width are along and across the rows, in metres; Rows
	// how many rows fit across.
	LengthM float64 `json:"length_m"`
	WidthM  float64 `json:"width_m"`
	Rows    int     `json:"rows"`
	// Plants of Crop and of With.
	Plants     int `json:"plants"`
	WithPlants int `json:"with_plants,omitempty"`
	// Season is the crop's sowing as the recommender scores it; nil
	// without a climate or a sowing date.
	Season *Season `json:"season,omitempty"`
}

// PlanReport is a checked plan.
type PlanReport struct {
	Status PlanStatus     `json:"status"`
	Checks []PlanCheck    `json:"checks"`
	Zones  []ZoneMeasures `json:"zones"`
	// UsedM2 is the area the zones cover, of the plot's PlotM2.
	UsedM2 float64 `json:"used_m2"`
	PlotM2 float64 `json:"plot_m2"`
}

// The plan checker's limits.
const (
	// MaxZones caps a plan.
	MaxZones = 50
	// MinZoneM2 is the smallest zone worth a crop.
	MinZoneM2 = 20
	// insideShare is the share of a zone that must be inside the plot,
	// and overlapShare the share past which two zones overlap: room for
	// the corners of a map drawing.
	insideShare  = 0.98
	overlapShare = 0.02
	// spacingFactor bounds a zone's spacing around the crop's usual.
	spacingFactor = 2.0
	// rowsOffDegrees is how far rows may run from north-south before
	// tall crops shade their neighbours' rows.
	rowsOffDegrees = 30
	// seasonDays is the most days between the sowings of one plan.
	seasonDays = 120
)

// CheckPlan checks a plan against the plot and the rules: zones inside the
// plot and apart, crops known, spacing within reason, sowing in a good
// window, water enough, soil suitable, rotation after the previous crop,
// known intercrops, and tall crops not shading short ones. today is the
// day the plan is checked on.
func CheckPlan(l *Library, plot Plot, plan Plan, today time.Time) PlanReport {
	rep := PlanReport{Checks: []PlanCheck{}, Zones: make([]ZoneMeasures, len(plan.Zones)), PlotM2: round1(geo.Area(plot.Ring))}
	add := func(id string, sev Severity, zone, other int, format string, a ...any) {
		rep.Checks = append(rep.Checks, PlanCheck{ID: id, Severity: sev, Zone: zone, Other: other, Message: fmt.Sprintf(format, a...)})
	}
	switch {
	case len(plan.Zones) == 0:
		add("plan.empty", Error, -1, -1, "The plan has no zones.")
	case len(plan.Zones) > MaxZones:
		add("plan.zones", Error, -1, -1, "The plan has %d zones; at most %d.", len(plan.Zones), MaxZones)
		rep.Zones = nil
	}
	if rep.Status = status(rep.Checks); rep.Status == PlanInvalid {
		return rep
	}
	plotRing := plot.Ring.Open()
	crops := make([]*Crop, len(plan.Zones))
	for i, z := range plan.Zones {
		c, err := l.Crop(z.Crop)
		if err != nil {
			add("zone.crop", Error, i, -1, "Zone %d: no crop %q in the library.", i+1, z.Crop)
			continue
		}
		crops[i] = &c
		m := &rep.Zones[i]
		ring := z.Ring.Open()
		if len(ring) < 3 || len(geo.SelfIntersections(ring)) > 0 {
			add("zone.shape", Error, i, -1, "Zone %d (%s): its boundary needs three or more corners and edges that do not cross.", i+1, c.Name)
			continue
		}
		m.AreaM2 = round1(geo.Area(ring))
		rep.UsedM2 += m.AreaM2
		if rep.PlotM2 > 0 {
			m.Share = math.Round(m.AreaM2/rep.PlotM2*1000) / 1000
		}
		if m.AreaM2 < MinZoneM2 {
			add("zone.small", Warning, i, -1, "Zone %d (%s) is only %.0f m².", i+1, c.Name, m.AreaM2)
		}
		if len(plotRing) >= 3 && m.AreaM2 > 0 && shareInside(ring, plotRing) < insideShare {
			add("zone.outside", Error, i, -1, "Zone %d (%s) reaches outside the plot.", i+1, c.Name)
		}

		// Spacing and counts.
		row, plant := orDefault(z.RowCM, c.RowCM), orDefault(z.PlantCM, c.PlantCM)
		for _, s := range []struct {
			id        string
			got, want float64
		}{{"spacing.row", row, c.RowCM}, {"spacing.plant", plant, c.PlantCM}} {
			if s.got < s.want/spacingFactor || s.got > s.want*spacingFactor {
				add(s.id, Warning, i, -1, "Zone %d (%s): %.0f cm spacing; the usual is %.0f cm.", i+1, c.Name, s.got, s.want)
			}
		}
		m.LengthM, m.WidthM = geo.Extent(ring, z.RowBearing)
		m.LengthM, m.WidthM = round1(m.LengthM), round1(m.WidthM)
		m.Rows = int(m.WidthM / (row / 100))
		plants := m.AreaM2 / (row / 100 * plant / 100)
		if z.With == "" {
			m.Plants = int(plants)
		} else {
			w, err := l.Crop(z.With)
			switch {
			case err != nil:
				add("zone.crop", Error, i, -1, "Zone %d: no crop %q in the library.", i+1, z.With)
			case z.Pattern[0] <= 0 || z.Pattern[1] <= 0:
				add("intercrop.pattern", Error, i, -1, "Zone %d: give the rows of each crop, such as 4 rows of %s to 1 of %s.", i+1, c.Name, w.Name)
			default:
				a, b := float64(z.Pattern[0]), float64(z.Pattern[1])
				m.Plants = int(plants * a / (a + b))
				m.WithPlants = int(m.AreaM2 * b / (a + b) / (w.RowCM / 100 * w.PlantCM / 100))
				if slices.Contains(c.Intercrops, w.ID) {
					add("intercrop.good", Info, i, -1, "Zone %d: %s with %s is a known good intercrop.", i+1, c.Name, w.Name)
				} else {
					add("intercrop.unknown", Warning, i, -1, "Zone %d: %s with %s is not a pair we know works.", i+1, c.Name, w.Name)
				}
				if c.Family == w.Family {
					add("intercrop.family", Warning, i, -1, "Zone %d: %s and %s are one family: they share pests and take the same nutrients.", i+1, c.Name, w.Name)
				}
			}
		}

		// Sowing, water and soil, through the recommender's rules.
		sow, err := time.Parse(DateLayout, z.Sow)
		if err != nil {
			add("zone.sow", Error, i, -1, "Zone %d (%s): give the sowing date as YYYY-MM-DD.", i+1, c.Name)
		} else if plot.Climate != nil {
			s := Simulate(c, Request{Climate: *plot.Climate, Soil: plot.Soil, Profile: plot.Profile, Today: today}, sow)
			m.Season = &s
			switch {
			case s.Score >= Good:
				add("sow.good", Info, i, -1, "Zone %d (%s): sowing on %s scores %d.", i+1, c.Name, s.Sow, s.Score)
			case s.Score >= Fair:
				add("sow.fair", Warning, i, -1, "Zone %d (%s): sowing on %s scores only %d.", i+1, c.Name, s.Sow, s.Score)
			default:
				add("sow.poor", Warning, i, -1, "Zone %d (%s): sowing on %s scores %d: pick another date or crop.", i+1, c.Name, s.Sow, s.Score)
			}
			for _, risk := range s.Risks {
				add(risk.ID, Warning, i, -1, "Zone %d (%s): %s", i+1, c.Name, risk.Message)
			}
		}

		// Rotation.
		if prev, err := l.Crop(plot.Profile.PreviousCrop); err == nil {
			switch {
			case prev.Family == c.Family && !c.Perennial:
				add("rotation.same", Warning, i, -1, "Zone %d: %s after %s, the same family: pests carry over and the soil tires; rotate if you can.", i+1, c.Name, prev.Name)
			case c.Kind == "pulse" && prev.Kind != "pulse":
				add("rotation.pulse", Info, i, -1, "Zone %d: a pulse after %s puts nitrogen back in the soil.", i+1, prev.Name)
			}
		}

		// Rows across the sun's path let tall crops shade their rows.
		if c.Tall && z.With != "" && offNorthSouth(z.RowBearing) > rowsOffDegrees {
			add("rows.direction", Warning, i, -1, "Zone %d: rows of tall %s should run north to south so they do not shade the crop between them.", i+1, c.Name)
		}
	}

	// A plan is one season: zones sown months apart are two plans.
	var first, last time.Time
	for _, z := range plan.Zones {
		if d, err := time.Parse(DateLayout, z.Sow); err == nil {
			if first.IsZero() || d.Before(first) {
				first = d
			}
			if d.After(last) {
				last = d
			}
		}
	}
	if days := last.Sub(first).Hours() / 24; days > seasonDays {
		add("plan.seasons", Warning, -1, -1, "The zones are sown %.0f days apart (%s to %s): that is two seasons; plan each season on its own.", days, first.Format(DateLayout), last.Format(DateLayout))
	}

	// Zones against each other.
	for i := range plan.Zones {
		for j := i + 1; j < len(plan.Zones); j++ {
			a, b := plan.Zones[i].Ring.Open(), plan.Zones[j].Ring.Open()
			if len(a) < 3 || len(b) < 3 {
				continue
			}
			o := geo.Overlap(a, b)
			if o > overlapShare*math.Min(rep.Zones[i].AreaM2, rep.Zones[j].AreaM2) {
				add("zone.overlap", Error, i, j, "Zones %d and %d overlap by about %.0f m².", i+1, j+1, o)
				continue
			}
			ci, cj := crops[i], crops[j]
			if ci == nil || cj == nil || ci.Tall == cj.Tall || !touch(a, b) {
				continue
			}
			tall, short, ti, si := ci, cj, i, j
			if cj.Tall {
				tall, short, ti, si = cj, ci, j, i
			}
			// The midday sun is to the south in the northern hemisphere,
			// and to the north in the southern.
			tc, sc := geo.Centroid(plan.Zones[ti].Ring), geo.Centroid(plan.Zones[si].Ring)
			if sunward(tc, sc) {
				add("shade.tall", Warning, ti, si, "Zone %d's %s is tall and on the sunny side of zone %d's %s: it will shade it; swap them.", ti+1, tall.Name, si+1, short.Name)
			}
		}
	}
	rep.UsedM2 = round1(rep.UsedM2)
	if rep.PlotM2 > 0 && rep.UsedM2 > rep.PlotM2*1.02 {
		add("plan.over", Error, -1, -1, "The zones add up to %.0f m², more than the plot's %.0f m².", rep.UsedM2, rep.PlotM2)
	}
	rep.Status = status(rep.Checks)
	return rep
}

func status(checks []PlanCheck) PlanStatus {
	s := PlanSound
	for _, c := range checks {
		switch c.Severity {
		case Error:
			return PlanInvalid
		case Warning:
			s = PlanNeedsReview
		}
	}
	return s
}

// sunward reports whether a is on the midday sun's side of b, by more than
// a few metres.
func sunward(a, b geo.Point) bool {
	dist := geo.Distance(geo.Point{Lat: a.Lat, Lng: b.Lng}, b)
	if dist < 5 {
		return false
	}
	if (a.Lat+b.Lat)/2 >= 0 {
		return a.Lat < b.Lat
	}
	return a.Lat > b.Lat
}

// touch reports whether two zones come within a few metres of each other:
// a corner of one near an edge's corner of the other, or overlapping.
func touch(a, b geo.Ring) bool {
	for _, p := range a {
		for _, q := range b {
			if geo.Distance(p, q) < 3 {
				return true
			}
		}
	}
	return geo.Overlap(a, b) > 0
}

// shareInside is the share of ring inside plot. Both overlaps sample the
// same grid over ring, so their ratio does not carry the sampling error.
func shareInside(ring, plot geo.Ring) float64 {
	self := geo.Overlap(ring, ring)
	if self == 0 {
		return 0
	}
	return geo.Overlap(ring, plot) / self
}

func offNorthSouth(bearing float64) float64 {
	b := math.Mod(math.Abs(bearing), 180)
	return math.Min(b, 180-b)
}

func orDefault(v, d float64) float64 {
	if v > 0 {
		return v
	}
	return d
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
