package planting

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/land"
	"github.com/bsamanvith/agentkit/apps/farm/osm"
	"github.com/bsamanvith/agentkit/apps/farm/sun"
)

// The effects' thresholds: starter values.
const (
	// ShadeHours is how many of the day's hours from 9:00 to 15:00 (seven,
	// sampled on the hour, solar time) a spot must be in shade to count as
	// shaded: more than half of the day's useful sun.
	ShadeHours = 4
	// ShadeTaller: a group shades another only when it is this much taller.
	ShadeTaller = 1.0
	// ShadeWarnShare: shade on more than this share of a group is a warning.
	ShadeWarnShare = 0.10
	// EdgeWarnShare: more than this share of a block within EdgeM of a
	// boundary is a warning; EdgeNoteShare a note.
	EdgeWarnShare = 0.45
	EdgeNoteShare = 0.25
	// MinRows: a piece with fewer rows of its crop is a warning.
	MinRows = 3
	// TreeFit: a block of trees that fits fewer than this share of the
	// ideal count wastes its corners.
	TreeFit = 0.8
	// WaterRatio: crops in one group whose water needs differ by more
	// than this factor cannot share a watering.
	WaterRatio = 2.0
	// WaterFarM: the thirstiest group this much farther from the water
	// than the least thirsty is a warning.
	WaterFarM = 15
	// WindbreakH: a windbreak shelters about this many times its height
	// downwind.
	WindbreakH = 10
)

// waterKinds are the map features a farm draws water from.
var waterKinds = []osm.Kind{osm.Canal, osm.River, osm.Stream, osm.Drain, osm.Water, osm.Well}

func (l *layout) measure(res *Result) {
	res.Plants = make([]Measure, len(l.groups))
	weights := 0.0
	for _, g := range l.groups {
		if g.Place == Block {
			weights += g.Share
		}
	}
	for i, g := range l.groups {
		m := Measure{Plant: g.ID, HeightM: g.height, WaterMM: g.main().WaterMM.Mid(), NarrowestM: math.Inf(1), Counts: []Count{}}
		if g.Place == Block {
			m.Want = math.Round(g.Share/weights*1000) / 1000
		}
		plots := map[int]bool{}
		counts := make([]float64, len(g.crops))
		edge := 0.0
		for _, pc := range l.pieces {
			if pc.group != i {
				continue
			}
			m.Pieces++
			plots[pc.plot] = true
			m.AreaM2 += pc.AreaM2
			if g.Place != Border {
				m.NarrowestM = math.Min(m.NarrowestM, pc.WidthM)
			}
			// Each crop of a mixed group gets an equal share of the rows.
			for j, c := range g.crops {
				if j == 0 && len(pc.Trees) > 0 {
					counts[0] += float64(len(pc.Trees))
					continue
				}
				counts[j] += pc.AreaM2 / float64(len(g.crops)) / (c.RowCM / 100 * c.PlantCM / 100)
			}
			if g.Place != Border {
				boundary := l.planes[pc.plot].ring(l.plots[pc.plot].Ring)
				for _, s := range pc.samples {
					if edgeDistance(boundary, s) < EdgeM {
						edge += pc.cell
					}
				}
			}
		}
		m.Plots = len(plots)
		for j, c := range g.crops {
			m.Counts = append(m.Counts, Count{Crop: c.ID, Plants: int(counts[j])})
		}
		if m.AreaM2 > 0 {
			m.EdgeShare = math.Round(edge/m.AreaM2*1000) / 1000
		}
		if math.IsInf(m.NarrowestM, 1) {
			m.NarrowestM = 0
		}
		res.Plants[i] = m
	}
	for i := range res.Plants {
		if res.AreaM2 > 0 {
			res.Plants[i].Share = math.Round(res.Plants[i].AreaM2/res.AreaM2*1000) / 1000
		}
		res.Plants[i].AreaM2 = round1(res.Plants[i].AreaM2)
	}
}

func (l *layout) effects(res *Result) {
	l.shade(res)
	l.edges(res)
	l.spacing(res)
	l.water(res)
	l.neighbours()
	l.wind()
	l.layoutTakes()
	order := map[Severity]int{Error: 0, Warning: 1, Info: 2}
	slices.SortStableFunc(l.effs, func(a, b Effect) int { return cmp.Compare(order[a.Severity], order[b.Severity]) })
	res.Effects = append(res.Effects, l.effs...)
}

// seasonMonths are the months (1 to 12) a crop sown in an Indian season
// stands in the field: kharif June to October, rabi November to March,
// zaid March to May.
var seasonMonths = map[string][]int{
	agro.Kharif: {6, 7, 8, 9, 10},
	agro.Rabi:   {11, 12, 1, 2, 3},
	agro.Zaid:   {3, 4, 5},
}

// monthsOf returns the months a crop stands in the field, in calendar
// order: all of them for a perennial or a crop of no season.
func monthsOf(c agro.Crop) []int {
	var ms []int
	for _, s := range c.Seasons {
		ms = append(ms, seasonMonths[s]...)
	}
	if c.Perennial || len(ms) == 0 {
		return []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	}
	slices.Sort(ms)
	return slices.Compact(ms)
}

// shade finds, for each pair of a taller and a shorter group in a plot,
// the shorter one's ground in the taller one's shadow for ShadeHours or
// more of a day, month by month through the months the shorter crop
// stands in the field: a spot is shaded at an hour when the line from it
// towards the sun passes over the taller plants within the length of their
// shadow over the shorter crop. The worst month counts; inside the
// tropics the noon sun is north of the plot for part of the year, so
// shade can fall either way.
func (l *layout) shade(res *Result) {
	type pair struct{ tall, short int }
	type month struct {
		area float64
		side xy // where in the shorter group's pieces the shade falls
	}
	byMonth := map[pair]*[13]month{}
	for pi := range l.plots {
		lat := l.plots[pi].Ring[0].Lat
		for t, tg := range l.groups {
			var casters [][2][]xy // ring and hole
			for _, pc := range l.pieces {
				if pc.plot == pi && pc.group == t {
					casters = append(casters, [2][]xy{l.planes[pi].ring(pc.Ring), l.planes[pi].ring(pc.Hole)})
				}
			}
			if len(casters) == 0 {
				continue
			}
			for s, sg := range l.groups {
				if s == t || tg.height-sg.height < ShadeTaller {
					continue
				}
				rise := tg.height - sg.height
				k := pair{t, s}
				if byMonth[k] == nil {
					byMonth[k] = &[13]month{}
				}
				for _, m := range monthsOf(sg.main()) {
					doy := sun.MonthDays[m-1]
					for _, pc := range l.pieces {
						if pc.plot != pi || pc.group != s {
							continue
						}
						c := centroid(l.planes[pi].ring(pc.Ring))
						for _, p := range pc.samples {
							if shadedHours(casters, p, lat, doy, rise) >= ShadeHours {
								byMonth[k][m].area += pc.cell
								byMonth[k][m].side = byMonth[k][m].side.add(p.sub(c).scale(pc.cell))
							}
						}
					}
				}
			}
		}
	}
	type found struct {
		pair
		worst  int // month
		area   float64
		months []int // months past ShadeWarnShare
	}
	var all []found
	for k, ms := range byMonth {
		f := found{pair: k}
		whole := res.Plants[k.short].AreaM2
		for m := 1; m <= 12; m++ {
			if ms[m].area > f.area {
				f.worst, f.area = m, ms[m].area
			}
			if whole > 0 && ms[m].area/whole > ShadeWarnShare {
				f.months = append(f.months, m)
			}
		}
		if f.worst > 0 {
			all = append(all, f)
		}
	}
	slices.SortFunc(all, func(a, b found) int {
		return cmp.Or(cmp.Compare(b.area, a.area), cmp.Compare(a.tall, b.tall), cmp.Compare(a.short, b.short))
	})
	for _, f := range all {
		a := f.area
		res.Plants[f.short].ShadedM2 = round1(res.Plants[f.short].ShadedM2 + a)
		if res.Plants[f.short].ShadeMonth == "" {
			res.Plants[f.short].ShadeMonth = time.Month(f.worst).String()
		}
		tg, sg := l.groups[f.tall], l.groups[f.short]
		whole := res.Plants[f.short].AreaM2
		if whole <= 0 || a < 1 {
			continue
		}
		share := a / whole
		side := ""
		if v := byMonth[f.pair][f.worst].side; v.len() > 0 {
			side = " mostly on its " + land.Words(geo.Compass(math.Mod(math.Atan2(v.x, v.y)*180/math.Pi+360, 360))) + " side,"
		}
		sev := Info
		if share > ShadeWarnShare {
			sev = Warning
		}
		when := "in " + time.Month(f.worst).String() + ", the worst month while it is in the field"
		if len(f.months) > 1 {
			when += " (" + monthRuns(f.months) + ")"
		}
		msg := fmt.Sprintf("The shade of %s (%.0f m tall) covers about %.0f m² of %s (%.0f%%),%s for more than half the day (9 to 3) %s.",
			tg.Name, tg.height, a, sg.Name, share*100, side, when)
		if tg.trees {
			msg += " That is where the trees' crowns close; young trees cast less."
		}
		if sg.main().LightNeed() != agro.LightFull {
			sev = Info
			msg += fmt.Sprintf(" %s bears part shade, and is better for it in the hot months.", sg.main().Name)
		} else if sev == Warning {
			fix := "move the shorter crop to the sunny side, or keep a gap of a shadow's length"
			if l.d.Rows != NorthSouth {
				fix += "; strips running north to south shade each other least"
			}
			msg += " To cut it: " + fix + "."
		}
		l.add(Effect{ID: "shade", Kind: KindShade, Severity: sev, Plant: sg.ID, Other: tg.ID, Message: msg, AreaM2: round1(a)})
	}
}

// shadedHours counts the hours from 9 to 3 (solar time) on day doy that
// point p is in the shadow of casters standing rise metres above it.
func shadedHours(casters [][2][]xy, p xy, lat float64, doy int, rise float64) int {
	hours := 0
	for hour := 9.0; hour <= 15; hour++ {
		e, n, u := sun.Vector(lat, doy, hour)
		if u < 0.05 {
			continue
		}
		flat := math.Hypot(e, n)
		if flat < 1e-9 {
			continue // the sun overhead casts no shadow aside
		}
		reach := math.Min(rise*flat/u, 100)
		dir := xy{e, n}.scale(1 / flat)
		for _, f := range []float64{0.15, 0.35, 0.55, 0.75, 1} {
			if inCasters(casters, p.add(dir.scale(reach*f))) {
				hours++
				break
			}
		}
	}
	return hours
}

// monthRuns names months as runs: "June to August, November".
func monthRuns(ms []int) string {
	var parts []string
	for i := 0; i < len(ms); {
		j := i
		for j+1 < len(ms) && ms[j+1] == ms[j]+1 {
			j++
		}
		switch j - i {
		case 0:
			parts = append(parts, time.Month(ms[i]).String())
		case 1:
			parts = append(parts, time.Month(ms[i]).String(), time.Month(ms[j]).String())
		default:
			parts = append(parts, time.Month(ms[i]).String()+" to "+time.Month(ms[j]).String())
		}
		i = j + 1
	}
	return "shaded " + andList(parts)
}

func inCasters(cs [][2][]xy, q xy) bool {
	for _, c := range cs {
		if contains(c[0], q) && (len(c[1]) < 3 || !contains(c[1], q)) {
			return true
		}
	}
	return false
}

// edges: blocks that are mostly edge, pieces too narrow for rows or too
// small to be worth it.
func (l *layout) edges(res *Result) {
	for i, g := range l.groups {
		m := res.Plants[i]
		if g.Place == Block && m.AreaM2 > 0 {
			switch {
			case m.EdgeShare > EdgeWarnShare:
				l.add(Effect{
					ID: "edge.most", Kind: KindEdge, Severity: Warning, Plant: g.ID,
					Message: fmt.Sprintf("%.0f%% of %s is within %d m of a boundary, where the neighbours' pests and stray cattle reach and border trees' roots compete: give it wider pieces in the middle, or put a hardier crop at the edges.", m.EdgeShare*100, g.Name, EdgeM),
				})
			case m.EdgeShare > EdgeNoteShare:
				l.add(Effect{
					ID: "edge.some", Kind: KindEdge, Severity: Info, Plant: g.ID,
					Message: fmt.Sprintf("%.0f%% of %s is within %d m of a boundary: watch the edges for pests coming in from the neighbours.", m.EdgeShare*100, g.Name, EdgeM),
				})
			}
		}
	}
	for _, pc := range l.pieces {
		g := l.groups[pc.group]
		p := l.plots[pc.plot]
		switch {
		case pc.AreaM2 < agro.MinZoneM2:
			l.add(Effect{
				ID: "piece.small", Kind: KindEdge, Severity: Warning, Plant: g.ID, Plot: p.ID, AreaM2: round1(pc.AreaM2),
				Message: fmt.Sprintf("A piece of %s in %s is only %.0f m²: too small to be worth its own crop; give it to the next strip.", g.Name, p.Name, pc.AreaM2),
			})
		case g.Place == Block && !g.trees && pc.Rows < MinRows:
			l.add(Effect{
				ID: "piece.narrow", Kind: KindEdge, Severity: Warning, Plant: g.ID, Plot: p.ID,
				Message: fmt.Sprintf("A strip of %s in %s is %.1f m wide: only %d rows of %s at %.0f cm. Make the strips wider.", g.Name, p.Name, pc.WidthM, pc.Rows, g.main().Name, g.main().RowCM),
			})
		}
	}
}

// spacing: tree blocks whose shape wastes room, and the plant counts.
func (l *layout) spacing(res *Result) {
	for i, g := range l.groups {
		if !g.trees || g.Place != Block {
			continue
		}
		m := res.Plants[i]
		c := g.main()
		ideal := m.AreaM2 / (c.RowCM / 100 * c.PlantCM / 100)
		got := float64(m.Counts[0].Plants)
		if ideal < 2 || got == 0 {
			if ideal < 1 && m.AreaM2 > 0 {
				l.add(Effect{
					ID: "trees.none", Kind: KindSpacing, Severity: Warning, Plant: g.ID,
					Message: fmt.Sprintf("%s's pieces are too small for even one %s at %.0f × %.0f m.", g.Name, c.Name, c.RowCM/100, c.PlantCM/100),
				})
			}
			continue
		}
		if got/ideal < TreeFit {
			l.add(Effect{
				ID: "trees.fit", Kind: KindSpacing, Severity: Info, Plant: g.ID,
				Message: fmt.Sprintf("%s's pieces fit %.0f %s trees at %.0f × %.0f m, against %.0f on the same area of open ground: narrow strips and corners waste room. Wider pieces, or trees in a triangle (quincunx) pattern, fit more.", g.Name, got, c.Name, c.RowCM/100, c.PlantCM/100, ideal),
			})
		}
	}
}

// waterAt is where plot i draws water: its farm pond, else the nearest
// water on the map within 300 m.
func (l *layout) waterAt(i int) (xy, string, bool) {
	p := l.plots[i]
	pl := l.planes[i]
	if p.Design != nil {
		for _, e := range p.Design.Elements {
			if e.Kind == land.Pond && len(e.Ring) >= 3 {
				return centroid(pl.ring(e.Ring)), "the farm pond", true
			}
		}
	}
	if p.Survey != nil {
		for _, n := range p.Survey.Nearest {
			if slices.Contains(waterKinds, n.Kind) && n.DistanceM <= 300 {
				return pl.xy(n.From), "the " + n.Label(), true
			}
		}
	}
	return xy{}, "", false
}

// water: the thirstiest group should be nearest the water; one group's
// crops should need about the same water.
func (l *layout) water(res *Result) {
	for _, g := range l.groups {
		lo, hi := g.crops[0], g.crops[0]
		for _, c := range g.crops[1:] {
			if c.WaterMM.Mid() < lo.WaterMM.Mid() {
				lo = c
			}
			if c.WaterMM.Mid() > hi.WaterMM.Mid() {
				hi = c
			}
		}
		if lo.WaterMM.Mid() > 0 && hi.WaterMM.Mid()/lo.WaterMM.Mid() > WaterRatio {
			l.add(Effect{
				ID: "water.mixed", Kind: KindWater, Severity: Warning, Plant: g.ID,
				Message: fmt.Sprintf("%s mixes %s (about %.0f mm) with %s (about %.0f mm): one watering drowns one or starves the other. Group crops of like needs.", g.Name, hi.Name, hi.WaterMM.Mid(), lo.Name, lo.WaterMM.Mid()),
			})
		}
	}
	for pi, p := range l.plots {
		at, what, ok := l.waterAt(pi)
		if !ok {
			continue
		}
		// The mean distance from the water of each crop group here.
		type far struct {
			g    int
			d    float64
			need float64
		}
		var fs []far
		for gi, g := range l.groups {
			if g.Place == Border {
				continue
			}
			sumD, n := 0.0, 0.0
			for _, pc := range l.pieces {
				if pc.plot == pi && pc.group == gi {
					for _, s := range pc.samples {
						sumD += s.sub(at).len() * pc.cell
						n += pc.cell
					}
				}
			}
			if n > 0 {
				fs = append(fs, far{gi, sumD / n, g.main().WaterMM.Mid()})
			}
		}
		if len(fs) < 2 {
			continue
		}
		slices.SortStableFunc(fs, func(a, b far) int { return cmp.Compare(b.need, a.need) })
		thirsty, dry := fs[0], fs[len(fs)-1]
		if thirsty.need <= dry.need*1.2 {
			continue
		}
		tg, dg := l.groups[thirsty.g], l.groups[dry.g]
		if thirsty.d > dry.d+WaterFarM {
			l.add(Effect{
				ID: "water.far", Kind: KindWater, Severity: Warning, Plant: tg.ID, Other: dg.ID, Plot: p.ID,
				Message: fmt.Sprintf("In %s, %s needs the most water (about %.0f mm) but is about %.0f m from %s, farther than %s (%.0f m), which needs the least: swap them, so channels and pipes are short and the thirsty crop is watered first.", p.Name, tg.Name, thirsty.need, thirsty.d, what, dg.Name, dry.d),
			})
		} else {
			l.add(Effect{
				ID: "water.near", Kind: KindWater, Severity: Info, Plant: tg.ID, Plot: p.ID,
				Message: fmt.Sprintf("In %s, %s, which needs the most water, is the nearest to %s (about %.0f m).", p.Name, tg.Name, what, thirsty.d),
			})
		}
	}
}

// neighbours: crops of one family side by side or mixed share pests;
// known intercrops help each other.
func (l *layout) neighbours() {
	for _, g := range l.groups {
		for a := 0; a < len(g.crops); a++ {
			for b := a + 1; b < len(g.crops); b++ {
				ca, cb := g.crops[a], g.crops[b]
				switch {
				case ca.Family == cb.Family:
					l.add(Effect{
						ID: "mix.family", Kind: KindNeighbours, Severity: Warning, Plant: g.ID,
						Message: fmt.Sprintf("%s mixes %s and %s, one family (%s): they share pests and diseases and take the same nutrients.", g.Name, ca.Name, cb.Name, ca.Family),
					})
				case slices.Contains(ca.Intercrops, cb.ID):
					l.add(Effect{
						ID: "mix.good", Kind: KindNeighbours, Severity: Info, Plant: g.ID,
						Message: fmt.Sprintf("%s mixes %s and %s, a known good pair.", g.Name, ca.Name, cb.Name),
					})
				default:
					l.add(Effect{
						ID: "mix.unknown", Kind: KindNeighbours, Severity: Info, Plant: g.ID,
						Message: fmt.Sprintf("%s mixes %s and %s: not a pair we know works; try a few rows first.", g.Name, ca.Name, cb.Name),
					})
				}
			}
		}
	}
	type pair struct{ a, b int }
	seen := map[pair]bool{}
	for i, p := range l.pieces {
		for _, q := range l.pieces[i+1:] {
			if p.plot != q.plot || p.group == q.group {
				continue
			}
			k := pair{min(p.group, q.group), max(p.group, q.group)}
			if seen[k] || !l.touch(p, q) {
				continue
			}
			seen[k] = true
			ga, gb := l.groups[k.a], l.groups[k.b]
			if fam := sharedFamily(ga, gb); fam != "" {
				l.add(Effect{
					ID: "next.family", Kind: KindNeighbours, Severity: Warning, Plant: ga.ID, Other: gb.ID,
					Message: fmt.Sprintf("%s and %s grow side by side and both have %s crops: pests and diseases cross between them. Put a different crop between them.", ga.Name, gb.Name, fam),
				})
			} else if good := goodPair(ga, gb); good != "" {
				l.add(Effect{
					ID: "next.good", Kind: KindNeighbours, Severity: Info, Plant: ga.ID, Other: gb.ID,
					Message: fmt.Sprintf("%s and %s side by side: %s, a known good pair.", ga.Name, gb.Name, good),
				})
			}
		}
	}
	if l.d.Arrange == Alternate && len(l.blocks()) > 1 {
		l.add(Effect{
			ID: "alternate.pests", Kind: KindNeighbours, Severity: Info,
			Message: "Strips that take turns slow pests and diseases that spread along one crop, and let each crop's pests' enemies reach the next.",
		})
	}
}

// touch: two pieces of a plot within a metre of each other.
func (l *layout) touch(p, q *piece) bool {
	pl := l.planes[p.plot]
	a, b := pl.ring(p.Ring), pl.ring(q.Ring)
	ha := pl.ring(p.Hole)
	for _, x := range b {
		if edgeDistance(a, x) < 1 || (len(ha) >= 3 && edgeDistance(ha, x) < 1) {
			return true
		}
	}
	for _, x := range a {
		if edgeDistance(b, x) < 1 {
			return true
		}
	}
	return false
}

func sharedFamily(a, b group) string {
	for _, x := range a.crops {
		for _, y := range b.crops {
			if x.Family == y.Family && (!x.Perennial || !y.Perennial) {
				return x.Family
			}
		}
	}
	return ""
}

func goodPair(a, b group) string {
	for _, x := range a.crops {
		for _, y := range b.crops {
			if slices.Contains(x.Intercrops, y.ID) {
				return x.Name + " with " + y.Name
			}
		}
	}
	return ""
}

// wind: a border of tall plants shelters about WindbreakH times its height
// downwind.
func (l *layout) wind() {
	b := l.placed(Border)
	if b < 0 {
		return
	}
	g := l.groups[b]
	if g.height < 3 {
		return
	}
	reach := g.height * WindbreakH
	var covered, partly []string
	for _, p := range l.plots {
		_, across := geo.Extent(p.Ring, l.bearing(p.Ring))
		if across <= reach {
			covered = append(covered, p.Name)
		} else {
			partly = append(partly, p.Name)
		}
	}
	msg := fmt.Sprintf("%s, a border %.0f m tall, shelters the crops about %.0f m downwind from hot, dry winds", g.Name, g.height, reach)
	switch {
	case len(partly) == 0:
		msg += ": all of every plot."
	case len(covered) == 0:
		msg += fmt.Sprintf(": the middle of %s is beyond it.", strings.Join(partly, ", "))
	default:
		msg += fmt.Sprintf(": all of %s; the middle of %s is beyond it.", strings.Join(covered, ", "), strings.Join(partly, ", "))
	}
	l.add(Effect{ID: "wind.shelter", Kind: KindWind, Severity: Info, Plant: g.ID, Message: msg})
}

// layoutTakes: what the farm's pond, shed and path take from each group,
// and a border that doubles the layout's border trees.
func (l *layout) layoutTakes() {
	for pi, p := range l.plots {
		if p.Design == nil {
			continue
		}
		pl := l.planes[pi]
		for _, e := range p.Design.Elements {
			if e.Kind == land.Border && l.placed(Border) >= 0 {
				l.add(Effect{
					ID: "layout.border", Kind: KindLayout, Severity: Warning, Plot: p.ID, Plant: l.groups[l.placed(Border)].ID,
					Message: fmt.Sprintf("%s already has border trees in its layout: the border group plants a second band; keep one.", p.Name),
				})
			}
			if e.Kind != land.Pond && e.Kind != land.Shed && e.Kind != land.Path || len(e.Ring) < 3 {
				continue
			}
			ring := pl.ring(e.Ring)
			for gi, g := range l.groups {
				taken := 0.0
				for _, pc := range l.pieces {
					if pc.plot != pi || pc.group != gi {
						continue
					}
					// The samples left out of the piece: count them back.
					all, cell := sample(pl.ring(pc.Ring), pl.ring(pc.Hole), 1500)
					for _, s := range all {
						if contains(ring, s) {
							taken += cell
						}
					}
				}
				if taken >= 1 {
					l.add(Effect{
						ID: "layout.takes", Kind: KindLayout, Severity: Info, Plant: g.ID, Plot: p.ID, AreaM2: round1(taken),
						Message: fmt.Sprintf("The %s takes about %.0f m² of %s in %s.", e.Kind, taken, g.Name, p.Name),
					})
				}
			}
		}
	}
}

// andList joins words as "a, b and c".
func andList(ws []string) string {
	if len(ws) < 2 {
		return strings.Join(ws, "")
	}
	return strings.Join(ws[:len(ws)-1], ", ") + " and " + ws[len(ws)-1]
}
