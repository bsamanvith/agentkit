package planting

import (
	"cmp"
	"fmt"
	"math"
	"slices"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/land"
)

// group is a plant group with what the rules know of its crops.
type group struct {
	PlantGroup
	crops  []agro.Crop
	height float64 // the tallest crop's
	trees  bool    // its main crop is planted as trees
}

func (g group) main() agro.Crop { return g.crops[0] }

// piece is a region being measured, with its samples.
type piece struct {
	Region
	plot    int
	group   int
	samples []xy // points sampling it, each standing for cell m²
	cell    float64
}

// Distribute lays d out on the plots with lib's crops and works out its
// effects. An invalid distribution is [ErrInvalid]; a plot the
// distribution does not fit (a border wider than the plot) is an effect,
// not an error.
func Distribute(lib *agro.Library, plots []Plot, d Distribution) (Result, error) {
	d, err := normalise(lib, d)
	if err != nil {
		return Result{}, err
	}
	if len(plots) == 0 || len(plots) > MaxPlots {
		return Result{}, fmt.Errorf("%w: a group needs 1 to %d plots, not %d", ErrInvalid, MaxPlots, len(plots))
	}
	plots = slices.Clone(plots)
	groups := make([]group, len(d.Plants))
	for i, p := range d.Plants {
		g := group{PlantGroup: p}
		for _, id := range p.Crops {
			c, _ := lib.Crop(id) // normalise checked
			g.crops = append(g.crops, c)
			g.height = math.Max(g.height, c.HeightM)
		}
		g.trees = g.main().Tree()
		groups[i] = g
	}
	l := &layout{lib: lib, d: d, groups: groups, plots: plots}
	for i, p := range plots {
		if len(p.Ring.Open()) < 3 || geo.Area(p.Ring) < 1 {
			return Result{}, fmt.Errorf("%w: plot %s has no area", ErrInvalid, p.ID)
		}
		plots[i].Ring = p.Ring.Open()
	}
	l.assign()
	res := Result{Distribution: d, Plots: make([]PlotLayout, len(plots)), Effects: []Effect{}}
	for i := range plots {
		res.Plots[i] = l.cut(i)
		res.AreaM2 += res.Plots[i].AreaM2
	}
	l.measure(&res)
	l.effects(&res)
	res.Status = Sound
	for _, e := range res.Effects {
		switch e.Severity {
		case Error:
			res.Status = Invalid
		case Warning:
			if res.Status == Sound {
				res.Status = NeedsReview
			}
		}
	}
	for i := range res.Plots {
		for j := range res.Plots[i].Regions {
			res.Plots[i].Regions[j].AreaM2 = round1(res.Plots[i].Regions[j].AreaM2)
		}
		res.Plots[i].AreaM2 = round1(res.Plots[i].AreaM2)
	}
	res.AreaM2 = round1(res.AreaM2)
	return res, nil
}

type layout struct {
	lib    *agro.Library
	d      Distribution
	groups []group
	plots  []Plot
	// owner[i] is the block group a plot goes to under ByPlot.
	owner  []int
	pieces []*piece
	// planes[i] projects plot i.
	planes []plane
	effs   []Effect
}

func (l *layout) add(e Effect) { l.effs = append(l.effs, e) }

func (l *layout) blocks() []int {
	var out []int
	for i, g := range l.groups {
		if g.Place == Block {
			out = append(out, i)
		}
	}
	return out
}

func (l *layout) placed(p Place) int {
	return slices.IndexFunc(l.groups, func(g group) bool { return g.Place == p })
}

// assign shares the plots out to the blocks for ByPlot: the largest plot
// first, each to the block furthest below its share.
func (l *layout) assign() {
	if l.d.Arrange != ByPlot {
		return
	}
	blocks := l.blocks()
	total, weights := 0.0, 0.0
	areas := make([]float64, len(l.plots))
	for i, p := range l.plots {
		areas[i] = geo.Area(p.Ring)
		total += areas[i]
	}
	for _, b := range blocks {
		weights += l.groups[b].Share
	}
	order := make([]int, len(l.plots))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(areas[b], areas[a]) })
	got := map[int]float64{}
	l.owner = make([]int, len(l.plots))
	for _, p := range order {
		best, bestGap := blocks[0], math.Inf(-1)
		for _, b := range blocks {
			if gap := l.groups[b].Share/weights*total - got[b]; gap > bestGap {
				best, bestGap = b, gap
			}
		}
		l.owner[p] = best
		got[best] += areas[p]
	}
	for _, b := range blocks {
		if got[b] == 0 {
			l.add(Effect{
				ID: "by_plot.no_land", Kind: KindLayout, Severity: Warning, Plant: l.groups[b].ID,
				Message: fmt.Sprintf("%s gets no plot: there are %d plots for %d blocks. Cut the plots into strips instead, or add plots.", l.groups[b].Name, len(l.plots), len(blocks)),
			})
		}
	}
}

// slice is one band of a plot's inner land, for one group.
type slice struct {
	group int
	width float64
}

// bearing is the plot's row direction and whether it is the long axis.
func (l *layout) bearing(r geo.Ring) float64 {
	switch l.d.Rows {
	case NorthSouth:
		return 0
	case EastWest:
		return 90
	}
	_, b, _ := geo.Strips(r, []float64{1})
	return b
}

// cut lays the distribution out on plot i.
func (l *layout) cut(i int) PlotLayout {
	p := l.plots[i]
	pl := planeAt(geo.Centroid(p.Ring))
	l.planes = append(l.planes, pl)
	out := PlotLayout{Plot: p.ID, Name: p.Name, AreaM2: geo.Area(p.Ring), Regions: []Region{}}
	inner := p.Ring
	if b := l.placed(Border); b >= 0 {
		g := l.groups[b]
		in, err := geo.Inset(p.Ring, g.WidthM)
		if err != nil {
			l.add(Effect{
				ID: "border.no_fit", Kind: KindEdge, Severity: Warning, Plant: g.ID, Plot: p.ID,
				Message: fmt.Sprintf("A %.0f m border of %s does not fit %s: it is too small or too narrow; it has no border.", g.WidthM, g.Name, p.Name),
			})
		} else {
			inner = in
			l.region(i, b, Region{Ring: p.Ring, Hole: in})
		}
	}
	bearing := l.bearing(inner)
	out.RowBearing = math.Round(bearing*10) / 10
	_, across, err := geo.BandsAlong(inner, bearing, []float64{1e9})
	if err != nil {
		return out
	}
	blocks := l.blocks()
	if l.d.Arrange == ByPlot {
		blocks = []int{l.owner[i]}
	}
	weights := 0.0
	for _, b := range blocks {
		weights += l.groups[b].Share
	}
	var plan []slice
	alley := l.placed(Alley)
	switch {
	case alley >= 0:
		plan = l.alleys(alley, blocks, weights, across)
	case l.d.Arrange == Alternate:
		for at, k := 0.0, 0; at < across && len(plan) < MaxSlices; k++ {
			b := blocks[k%len(blocks)]
			w := l.d.StripM * l.groups[b].Share / weights * float64(len(blocks))
			plan = append(plan, slice{b, w})
			at += w
		}
	default:
		shares := make([]float64, len(blocks))
		for j, b := range blocks {
			shares[j] = l.groups[b].Share / weights
		}
		// Rounding can push the sum a hair over the whole.
		shares[len(shares)-1] = math.Max(1e-9, 1-sum(shares[:len(shares)-1]))
		strips, err := geo.StripsAlong(inner, bearing, shares)
		if err == nil {
			for j, s := range strips {
				l.region(i, blocks[j], Region{Ring: s})
			}
			l.finish(i, &out, bearing)
			return out
		}
	}
	widths := make([]float64, len(plan))
	for j, s := range plan {
		widths[j] = s.width
	}
	bands, _, err := geo.BandsAlong(inner, bearing, widths)
	if err == nil {
		for j, b := range bands {
			if len(b) >= 3 {
				l.region(i, plan[j].group, Region{Ring: b})
			}
		}
	}
	l.finish(i, &out, bearing)
	return out
}

// alleys is the bands across a plot of width across: a row of trees every
// EveryM, the first half that in, and the alleys between shared out to
// the blocks, by share (strips) or in turn (alternate).
func (l *layout) alleys(alley int, blocks []int, weights, across float64) []slice {
	g := l.groups[alley]
	var plan []slice
	type gap struct{ from, to float64 }
	var gaps []gap
	at := 0.0
	for c := g.EveryM / 2; c+g.WidthM/2 <= across && len(plan) < MaxSlices/2; c += g.EveryM {
		gaps = append(gaps, gap{at, c - g.WidthM/2})
		at = c + g.WidthM/2
	}
	gaps = append(gaps, gap{at, across})
	total := 0.0
	for _, x := range gaps {
		total += x.to - x.from
	}
	done := 0.0
	for k, x := range gaps {
		w := x.to - x.from
		b := blocks[k%len(blocks)]
		if l.d.Arrange != Alternate {
			// The block whose share takes in the middle of this alley.
			mid := (done + w/2) / math.Max(total, 1e-9)
			acc := 0.0
			for _, bb := range blocks {
				acc += l.groups[bb].Share / weights
				b = bb
				if mid <= acc+1e-9 {
					break
				}
			}
		}
		done += w
		if w > 0.01 {
			plan = append(plan, slice{b, w})
		}
		if k < len(gaps)-1 {
			plan = append(plan, slice{alley, g.WidthM})
		}
	}
	return plan
}

func sum(v []float64) float64 {
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s
}

// region adds a piece of plot i for group g.
func (l *layout) region(i, g int, r Region) {
	r.Plant = l.groups[g].ID
	l.pieces = append(l.pieces, &piece{Region: r, plot: i, group: g})
}

// finish measures plot i's pieces: samples, area less the layout's
// elements, rows and trees.
func (l *layout) finish(i int, out *PlotLayout, bearing float64) {
	pl := l.planes[i]
	var taken [][]xy // the layout's pond, shed and path
	if d := l.plots[i].Design; d != nil {
		for _, e := range d.Elements {
			if (e.Kind == land.Pond || e.Kind == land.Shed || e.Kind == land.Path) && len(e.Ring) >= 3 {
				taken = append(taken, pl.ring(e.Ring))
			}
		}
	}
	for _, pc := range l.pieces {
		if pc.plot != i {
			continue
		}
		g := l.groups[pc.group]
		ring, hole := pl.ring(pc.Ring), pl.ring(pc.Hole)
		pc.samples, pc.cell = sample(ring, hole, 1500)
		kept := pc.samples[:0]
		for _, s := range pc.samples {
			if !insideAny(taken, s) {
				kept = append(kept, s)
			}
		}
		lost := float64(len(pc.samples)-len(kept)) * pc.cell
		pc.samples = kept
		pc.AreaM2 = math.Max(0, area(ring)-area(hole)-lost)
		row := g.main().RowCM / 100
		if g.Place == Border {
			// A border's rows run round the plot.
			pc.LengthM = math.Round(geo.Perimeter(pc.Ring)*10) / 10
			pc.WidthM = g.WidthM
		} else {
			pc.LengthM, pc.WidthM = geo.Extent(pc.Ring, bearing)
			pc.LengthM, pc.WidthM = math.Round(pc.LengthM*10)/10, math.Round(pc.WidthM*10)/10
		}
		pc.Rows = max(1, int(pc.WidthM/row))
		if g.trees {
			pc.Trees = l.trees(pl, pc, g, bearing, taken)
		}
		out.Regions = append(out.Regions, pc.Region)
	}
}

// trees places a tree group's trees in a piece: in rows at the crop's
// spacing, half a spacing in from the piece's edges; one row down the
// middle of a band narrower than the spacing, and round the middle of a
// border.
func (l *layout) trees(pl plane, pc *piece, g group, bearing float64, taken [][]xy) []geo.Point {
	row, along := g.main().RowCM/100, g.main().PlantCM/100
	var pts []xy
	if g.Place == Border {
		mid, err := geo.Inset(pc.Ring, g.WidthM/2)
		if err != nil {
			return nil
		}
		pts = alongRing(pl.ring(mid), along)
	} else {
		ring := pl.ring(pc.Ring)
		u := xy{math.Sin(rad(bearing)), math.Cos(rad(bearing))}
		v := xy{-u.y, u.x}
		aLo, aHi, bLo, bHi := extent(ring, u, v)
		var across []float64
		clear := math.Min(math.Min(along, row), bHi-bLo) / 2
		if bHi-bLo < row {
			across = []float64{(bLo + bHi) / 2}
		} else {
			for b := bLo + row/2; b <= bHi-row/2+1e-9; b += row {
				across = append(across, b)
			}
		}
		for _, b := range across {
			for a := aLo + along/2; a <= aHi-along/2+1e-9; a += along {
				p := xy{u.x*a + v.x*b, u.y*a + v.y*b}
				if contains(ring, p) && edgeDistance(ring, p) >= clear-0.01 {
					pts = append(pts, p)
				}
				if len(pts) > 5000 {
					return nil // too many to draw: counted by area
				}
			}
		}
	}
	out := make([]geo.Point, 0, len(pts))
	for _, p := range pts {
		if !insideAny(taken, p) {
			out = append(out, pl.point(p))
		}
	}
	return out
}

// alongRing puts points every step metres round a ring, the first half a
// step from its first corner.
func alongRing(r []xy, step float64) []xy {
	var out []xy
	next, walked := step/2, 0.0
	for i := range r {
		a, b := r[i], r[(i+1)%len(r)]
		d := b.sub(a)
		n := d.len()
		for ; next <= walked+n; next += step {
			t := (next - walked) / n
			out = append(out, xy{a.x + d.x*t, a.y + d.y*t})
			if len(out) > 5000 {
				return nil
			}
		}
		walked += n
	}
	return out
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
