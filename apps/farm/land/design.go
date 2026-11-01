package land

import (
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/osm"
)

// ElementKind is a fixed part of a farm.
type ElementKind string

// The elements a design can have.
const (
	Gate   ElementKind = "gate"   // where the plot is entered: a point on the boundary
	Path   ElementKind = "path"   // a cart path from the gate across the plot
	Shed   ElementKind = "shed"   // a store or cattle shed by the gate
	Pond   ElementKind = "pond"   // a farm pond at the low end
	Border ElementKind = "border" // a band of trees (or a bund) inside the boundary
)

// ElementKinds lists them in the order a design holds them.
func ElementKinds() []ElementKind { return []ElementKind{Gate, Path, Shed, Pond, Border} }

// Element is one part of a design.
type Element struct {
	Kind ElementKind `json:"kind"`
	// Ring is its outline (not for a gate). A border is the band between
	// Ring (the plot's boundary) and Hole.
	Ring geo.Ring `json:"ring,omitempty"`
	Hole geo.Ring `json:"hole,omitempty"`
	// At is a gate's position, on the boundary.
	At *geo.Point `json:"at,omitempty"`
	// Size: length and width (a pond's sides, a shed's, a path's length and
	// width, a border's width), and a pond's depth, in metres.
	LengthM float64 `json:"length_m,omitempty"`
	WidthM  float64 `json:"width_m,omitempty"`
	DepthM  float64 `json:"depth_m,omitempty"`
	// Trees on a border, at TreeSpacingM.
	Trees        int     `json:"trees,omitempty"`
	TreeSpacingM float64 `json:"tree_spacing_m,omitempty"`
	// Why it is where it is.
	Why string `json:"why,omitempty"`
}

// Design is a farm's fixed parts.
type Design struct {
	Elements []Element `json:"elements"`
}

// ElementMeasure is what an element takes.
type ElementMeasure struct {
	AreaM2 float64 `json:"area_m2"`
	// Share of the plot, 0 to 1.
	Share float64 `json:"share"`
	// VolumeM3 is a pond's capacity: what it holds when full.
	VolumeM3 float64 `json:"volume_m3,omitempty"`
}

// DesignStatus is the verdict of a design's checks.
type DesignStatus string

// The verdicts.
const (
	DesignSound   DesignStatus = "sound"        // no warnings or errors
	DesignReview  DesignStatus = "needs_review" // warnings
	DesignInvalid DesignStatus = "invalid"      // errors: it cannot be saved
)

// DesignReport is a design's check.
type DesignReport struct {
	Status   DesignStatus     `json:"status"`
	Checks   []Note           `json:"checks"`
	Elements []ElementMeasure `json:"elements"`
	PlotM2   float64          `json:"plot_m2"`
	// FieldsM2 is what is left to crop.
	FieldsM2 float64 `json:"fields_m2"`
}

// Want is what a farmer wants in a design. Sizes left at zero are
// chosen from the plot.
type Want struct {
	Gate   bool `json:"gate,omitempty" jsonschema:"a gate on the road side (implied by path and shed)"`
	Path   bool `json:"path,omitempty" jsonschema:"a cart path from the gate across the plot"`
	Shed   bool `json:"shed,omitempty" jsonschema:"a store or cattle shed by the gate"`
	Pond   bool `json:"pond,omitempty" jsonschema:"a farm pond at the low end"`
	Border bool `json:"border,omitempty" jsonschema:"a band of trees all round the inside of the boundary"`

	PathWidthM   float64 `json:"path_width_m,omitempty" jsonschema:"cart path width, default 3 m"`
	ShedLengthM  float64 `json:"shed_length_m,omitempty" jsonschema:"shed length, default 6 m"`
	ShedWidthM   float64 `json:"shed_width_m,omitempty" jsonschema:"shed width, default 4 m"`
	PondSideM    float64 `json:"pond_side_m,omitempty" jsonschema:"farm pond side (square), default from the plot's size: 10, 15, 20 or 30 m"`
	PondDepthM   float64 `json:"pond_depth_m,omitempty" jsonschema:"farm pond depth, default 3 m"`
	BorderWidthM float64 `json:"border_width_m,omitempty" jsonschema:"border band width, default 3 m"`
	TreeSpacingM float64 `json:"tree_spacing_m,omitempty" jsonschema:"spacing of border trees along the boundary, default 4 m"`
}

// Starter values of designs.
const (
	DefaultPathWidthM   = 3
	DefaultShedLengthM  = 6
	DefaultShedWidthM   = 4
	DefaultPondDepthM   = 3
	DefaultBorderWidthM = 3
	DefaultTreeSpacingM = 4
	// PondSetbackM keeps a pond's edge this far inside the boundary (its
	// spoil bund, and the neighbour's field).
	PondSetbackM = 3
	// MaxPondShare: a pond larger than this share of the plot takes too
	// much cropland.
	MaxPondShare = 0.10
	// MinFieldsShare: less than this left to crop is a warning.
	MinFieldsShare = 0.70
	// ShedFromGateM: a shed farther than this from the gate means carrying.
	ShedFromGateM = 40
)

// PondSide is the default side of a square farm pond for a plot of area
// m2, from the MGNREGA standard sizes (10, 15, 20, 30 m, 3 m deep); 0 when
// the plot is too small for even the smallest within MaxPondShare.
func PondSide(m2 float64) float64 {
	for _, c := range []struct{ upTo, side float64 }{{5000, 10}, {15000, 15}, {30000, 20}} {
		if m2 <= c.upTo {
			if c.side*c.side > MaxPondShare*m2 {
				return 0
			}
			return c.side
		}
	}
	return 30
}

// ErrDesign is a request [Suggest] cannot meet.
var ErrDesign = errors.New("land: cannot lay that out")

func (w *Want) defaults(area float64) {
	if w.PathWidthM == 0 {
		w.PathWidthM = DefaultPathWidthM
	}
	if w.ShedLengthM == 0 {
		w.ShedLengthM = DefaultShedLengthM
	}
	if w.ShedWidthM == 0 {
		w.ShedWidthM = DefaultShedWidthM
	}
	if w.PondDepthM == 0 {
		w.PondDepthM = DefaultPondDepthM
	}
	if w.PondSideM == 0 {
		w.PondSideM = PondSide(area)
	}
	if w.BorderWidthM == 0 {
		w.BorderWidthM = DefaultBorderWidthM
	}
	if w.TreeSpacingM == 0 {
		w.TreeSpacingM = DefaultTreeSpacingM
	}
	if w.Path || w.Shed {
		w.Gate = true
	}
}

func (w Want) check() error {
	for name, v := range map[string]float64{
		"path_width_m": w.PathWidthM, "shed_length_m": w.ShedLengthM, "shed_width_m": w.ShedWidthM,
		"pond_side_m": w.PondSideM, "pond_depth_m": w.PondDepthM, "border_width_m": w.BorderWidthM, "tree_spacing_m": w.TreeSpacingM,
	} {
		if v < 0 || v > 200 || math.IsNaN(v) {
			return fmt.Errorf("%w: %s %v must be between 0 and 200 m", ErrDesign, name, v)
		}
	}
	if !w.Gate && !w.Path && !w.Shed && !w.Pond && !w.Border {
		return fmt.Errorf("%w: ask for at least one of gate, path, shed, pond, border", ErrDesign)
	}
	return nil
}

// Suggest lays out what w asks for on plot r, using the survey (nil for
// none) to place the gate on the road side and the pond at the low end,
// and checks the result.
//
//   - The gate goes where the plot is nearest a road or track; with no
//     road, at the middle of the longest side.
//   - The path runs from the gate towards the plot's centre to the far
//     side.
//   - The shed stands just inside the gate, beside the path.
//   - The pond goes in the lowest corner when the land has a measurable
//     slope, else the corner nearest a drain or water, else the corner
//     farthest from the gate; PondSetbackM inside the boundary (and the
//     border).
//   - The border is a band of trees all round the inside of the boundary.
func Suggest(r geo.Ring, s *Survey, w Want) (Design, DesignReport, error) {
	r = r.Open()
	shape, err := Describe(r)
	if err != nil {
		return Design{}, DesignReport{}, err
	}
	w.defaults(shape.AreaM2)
	if err := w.check(); err != nil {
		return Design{}, DesignReport{}, err
	}
	var d Design
	inner := r // the area inside the border
	if w.Border {
		in, err := geo.Inset(r, w.BorderWidthM)
		if err != nil {
			return Design{}, DesignReport{}, fmt.Errorf("%w: a %.0f m border does not fit this plot", ErrDesign, w.BorderWidthM)
		}
		inner = in
		trees := int(math.Floor(geo.Perimeter(mustInset(r, w.BorderWidthM/2)) / w.TreeSpacingM))
		d.Elements = append(d.Elements, Element{
			Kind: Border, Ring: r, Hole: in, WidthM: w.BorderWidthM, Trees: trees, TreeSpacingM: w.TreeSpacingM,
			Why: "a band of trees all round: shade and wind break for the crops, fruit, fodder or timber, and a living fence",
		})
	}
	var gate geo.Point
	var gateEdge int
	if w.Gate {
		why := "at the middle of the longest side: no road on the map"
		gate, gateEdge = middleOfLongest(shape, r)
		if s != nil {
			if road, ok := nearestOf(s, osm.Road, osm.Track); ok && road.DistanceM <= 500 && road.Edge >= 0 {
				gate, gateEdge = road.From, road.Edge
				why = fmt.Sprintf("where the plot is nearest the %s (%.0f m)", road.Label(), road.DistanceM)
				if road.Touches {
					why = fmt.Sprintf("on the %s side, where the plot meets the %s", Words(road.Direction), road.Label())
				}
			}
		}
		// Keep the gate off the corners, so the path and shed fit inside.
		a, b := r[gateEdge], r[(gateEdge+1)%len(r)]
		edgeLen, along := geo.Distance(a, b), geo.Bearing(a, b)
		margin := w.BorderWidthM*boolF(w.Border) + w.PathWidthM/2 + (w.ShedLengthM+2)*boolF(w.Shed) + 2
		t := math.Max(margin, math.Min(edgeLen-margin, geo.Distance(a, gate)))
		if edgeLen < 2*margin {
			t = edgeLen / 2
		}
		gate = geo.Offset(a, along, t)
		g := gate
		d.Elements = append(d.Elements, Element{Kind: Gate, At: &g, Why: why})
	}
	inward := inwardBearing(shape, gateEdge)
	var taken []geo.Ring // what the pond must not overlap
	if w.Path {
		// Straight in from the gate, square to its side, to the far side.
		start := geo.Offset(gate, inward, 0.05)
		reach := geo.Reach(r, start, inward)
		var ring geo.Ring
		length := 0.0
		for l := reach - 0.1; l >= 5; l-- {
			cand := geo.Rect(geo.Offset(start, inward, l/2), inward, l, w.PathWidthM)
			if geo.RingInside(cand, r) {
				ring, length = cand, l
				break
			}
		}
		if ring == nil {
			return Design{}, DesignReport{}, fmt.Errorf("%w: no room for a path from the gate", ErrDesign)
		}
		d.Elements = append(d.Elements, Element{
			Kind: Path, Ring: ring, LengthM: length, WidthM: w.PathWidthM,
			Why: "a cart path from the gate across the plot, so a bullock cart or tractor reaches every field",
		})
		taken = append(taken, ring)
	}
	if w.Shed {
		ring, ok := placeNearGate(inner, gate, inward, shape.Edges[gateEdge].Bearing, w, taken)
		if !ok {
			return Design{}, DesignReport{}, fmt.Errorf("%w: no room for a %.0f × %.0f m shed by the gate", ErrDesign, w.ShedLengthM, w.ShedWidthM)
		}
		d.Elements = append(d.Elements, Element{
			Kind: Shed, Ring: ring, LengthM: w.ShedLengthM, WidthM: w.ShedWidthM,
			Why: "just inside the gate, beside the path: inputs and produce are loaded there without crossing the fields",
		})
		taken = append(taken, ring)
	}
	if w.Pond {
		if w.PondSideM == 0 {
			return Design{}, DesignReport{}, fmt.Errorf("%w: the plot is too small for a farm pond (a 10 m pond would take over %.0f%% of it)", ErrDesign, MaxPondShare*100)
		}
		corner, why := pondCorner(r, shape, s, gate, w.Gate)
		ring, ok := placeInCorner(inner, r[corner], shape, w.PondSideM, taken)
		if !ok {
			// Try the other corners, farthest from the gate first.
			order := cornersByDistance(r, r[corner])
			for _, c := range order {
				if ring, ok = placeInCorner(inner, r[c], shape, w.PondSideM, taken); ok {
					why += fmt.Sprintf("; it did not fit there, so it went to corner %d", c+1)
					break
				}
			}
		}
		if !ok {
			return Design{}, DesignReport{}, fmt.Errorf("%w: no room for a %.0f m pond", ErrDesign, w.PondSideM)
		}
		d.Elements = append(d.Elements, Element{
			Kind: Pond, Ring: ring, LengthM: w.PondSideM, WidthM: w.PondSideM, DepthM: w.PondDepthM, Why: why,
		})
	}
	// Keep the order of ElementKinds.
	slices.SortStableFunc(d.Elements, func(a, b Element) int {
		return slices.Index(ElementKinds(), a.Kind) - slices.Index(ElementKinds(), b.Kind)
	})
	return d, CheckDesign(r, s, d, nil), nil
}

func boolF(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func mustInset(r geo.Ring, d float64) geo.Ring {
	in, err := geo.Inset(r, d)
	if err != nil {
		return r
	}
	return in
}

func middleOfLongest(s Shape, r geo.Ring) (geo.Point, int) {
	best := 0
	for i, e := range s.Edges {
		if e.LengthM > s.Edges[best].LengthM {
			best = i
		}
	}
	a, b := r[best], r[(best+1)%len(r)]
	return geo.Offset(a, geo.Bearing(a, b), geo.Distance(a, b)/2), best
}

// inwardBearing is the direction into the plot across side edge.
func inwardBearing(s Shape, edge int) float64 {
	e := s.Edges[edge]
	out := e.Bearing + 90
	if geo.Compass(out) != e.Faces {
		out = e.Bearing - 90
	}
	return math.Mod(out+180+360, 360)
}

// placeNearGate puts a shed beside the path, just inside the gate.
func placeNearGate(inner geo.Ring, gate geo.Point, inward, along float64, w Want, taken []geo.Ring) (geo.Ring, bool) {
	for depth := 1.0; depth < 60; depth++ {
		for _, side := range []float64{1, -1, 2, -2, 3, -3} {
			sideways := (w.PathWidthM/2 + w.ShedLengthM/2 + 1) * math.Abs(side)
			side = math.Copysign(1, side)
			c := geo.Offset(geo.Offset(gate, inward, depth+w.ShedWidthM/2), along, side*sideways)
			ring := geo.Rect(c, along, w.ShedLengthM, w.ShedWidthM)
			if geo.RingInside(ring, inner) && free(ring, taken) {
				return ring, true
			}
		}
	}
	return nil, false
}

// placeInCorner puts a square of side, aligned with the plot's box, as
// close to corner as it fits inside inner, PondSetbackM in.
func placeInCorner(inner geo.Ring, corner geo.Point, s Shape, side float64, taken []geo.Ring) (geo.Ring, bool) {
	toward := geo.Bearing(corner, s.Centroid)
	total := geo.Distance(corner, s.Centroid)
	for d := side / 2; d < total; d++ {
		c := geo.Offset(corner, toward, d)
		ring := geo.Rect(c, s.Box.Bearing, side, side)
		if !geo.RingInside(ring, inner) || !free(ring, taken) {
			continue
		}
		if geo.ClosestTo(inner, ring, true).BoundaryM < PondSetbackM {
			continue
		}
		return ring, true
	}
	return nil, false
}

func free(r geo.Ring, taken []geo.Ring) bool {
	for _, t := range taken {
		if geo.Overlap(r, t) > 0.5 || geo.ClosestTo(t, r, true).DistanceM < 1 {
			return false
		}
	}
	return true
}

func terrainOf(s *Survey) *Terrain {
	if s == nil || s.Terrain == nil || len(s.Terrain.Points) == 0 || s.Terrain.Lowest >= len(s.Terrain.Points) {
		return nil
	}
	return s.Terrain
}

func cornersByDistance(r geo.Ring, from geo.Point) []int {
	idx := make([]int, len(r))
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(a, b int) int {
		return cmpF(geo.Distance(r[a], from), geo.Distance(r[b], from))
	})
	return idx[1:]
}

// pondCorner picks the corner for a pond and says why.
func pondCorner(r geo.Ring, shape Shape, s *Survey, gate geo.Point, haveGate bool) (int, string) {
	if t := terrainOf(s); t != nil && !t.Flat {
		low := t.Points[t.Lowest]
		best := 0
		for i, p := range r {
			if geo.Distance(p, low) < geo.Distance(r[best], low) {
				best = i
			}
		}
		return best, fmt.Sprintf("in the lowest corner (%.0f m below the highest point): run-off collects there", t.FallM)
	}
	if s != nil {
		best, bestD, label := -1, math.Inf(1), ""
		for _, n := range s.Nearby {
			if n.Kind != osm.Drain && n.Kind != osm.Canal && n.Kind != osm.Stream && n.Kind != osm.River && n.Kind != osm.Water {
				continue
			}
			for i, p := range r {
				if d := geo.Distance(p, n.At); d < bestD {
					best, bestD, label = i, d, n.Label()
				}
			}
		}
		if best >= 0 && bestD <= 300 {
			return best, fmt.Sprintf("in the corner nearest the %s: the land usually drains towards it (the elevation data cannot tell on flat land)", label)
		}
	}
	far, farD := 0, -1.0
	for i, p := range r {
		ref := shape.Centroid
		if haveGate {
			ref = gate
		}
		if d := geo.Distance(p, ref); d > farD {
			far, farD = i, d
		}
	}
	return far, "in the corner farthest from the gate, out of the way of carts (no slope or water on the map to go by)"
}

// The ids of design checks.
const (
	CheckOutside     = "outside_plot"
	CheckOverlap     = "overlap"
	CheckPondSetback = "pond_setback"
	CheckPondShare   = "pond_share"
	CheckPondPower   = "pond_under_power"
	CheckPondHouses  = "pond_near_houses"
	CheckPondNotLow  = "pond_not_lowest"
	CheckShedFar     = "shed_far_from_gate"
	CheckGateRoad    = "gate_not_road_side"
	CheckBorderShade = "border_on_narrow_plot"
	CheckTreesPower  = "trees_under_power"
	CheckFields      = "little_left_to_crop"
	CheckPondZone    = "pond_in_crop_zone"
	CheckGateMissing = "gate_off_boundary"
	CheckBadElement  = "bad_element"
	CheckPondVolume  = "pond_volume"
)

// Zone is a zone of a crop plan, for the design check.
type Zone struct {
	Name string
	Ring geo.Ring
}

// CheckDesign checks a design on plot r against the rules, with the
// survey (nil for none) and the saved plan's zones (for what a pond or a
// shed takes from them).
func CheckDesign(r geo.Ring, s *Survey, d Design, zones []Zone) DesignReport {
	r = r.Open()
	plot := geo.Area(r)
	rep := DesignReport{Checks: []Note{}, Elements: make([]ElementMeasure, len(d.Elements)), PlotM2: plot}
	add := func(id, sev string, el int, format string, args ...any) {
		rep.Checks = append(rep.Checks, Note{ID: id, Severity: sev, Message: fmt.Sprintf(format, args...), Element: el})
	}
	var shape *Shape
	if sh, err := Describe(r); err == nil {
		shape = &sh
	}
	used := 0.0
	gateAt := -1
	for i, e := range d.Elements {
		m := &rep.Elements[i]
		switch e.Kind {
		case Gate:
			if e.At == nil {
				add(CheckBadElement, Error, i, "The gate has no position.")
				continue
			}
			gateAt = i
			if c := geo.ClosestTo(r, []geo.Point{*e.At}, false); c.BoundaryM > 2 {
				add(CheckGateMissing, Warning, i, "The gate is %.0f m off the boundary.", c.BoundaryM)
			}
			continue
		case Border:
			if len(e.Ring) < 3 || len(e.Hole) < 3 {
				add(CheckBadElement, Error, i, "The border needs its outer and inner outlines.")
				continue
			}
			m.AreaM2 = geo.Area(e.Ring) - geo.Area(e.Hole)
			if !geo.RingInside(e.Hole, r) {
				add(CheckOutside, Error, i, "The border's inner edge goes outside the plot.")
			}
		case Path, Shed, Pond:
			if len(e.Ring) < 3 {
				add(CheckBadElement, Error, i, "The %s has no outline.", e.Kind)
				continue
			}
			m.AreaM2 = geo.Area(e.Ring)
			if !geo.RingInside(e.Ring, r) {
				add(CheckOutside, Error, i, "The %s goes outside the plot.", e.Kind)
			}
		default:
			add(CheckBadElement, Error, i, "Unknown element %q.", e.Kind)
			continue
		}
		m.Share = m.AreaM2 / plot
		used += m.AreaM2
	}
	// Overlaps, but a path may cross the border to reach the gate.
	for i := range d.Elements {
		for j := i + 1; j < len(d.Elements); j++ {
			a, b := d.Elements[i], d.Elements[j]
			if len(a.Ring) < 3 || len(b.Ring) < 3 || a.Kind == Gate || b.Kind == Gate {
				continue
			}
			var ov float64
			switch {
			case a.Kind == Border || b.Kind == Border:
				band, other := a, b
				if b.Kind == Border {
					band, other = b, a
				}
				ov = geo.Overlap(other.Ring, band.Ring) - geo.Overlap(other.Ring, band.Hole)
				if other.Kind == Path {
					used -= math.Max(ov, 0)
					continue
				}
			default:
				ov = geo.Overlap(a.Ring, b.Ring)
			}
			if ov > 1 {
				if a.Kind == Path || b.Kind == Path {
					used -= ov
				}
				sev := Error
				if a.Kind == Path || b.Kind == Path {
					sev = Warning
				}
				add(CheckOverlap, sev, j, "The %s and the %s overlap by %.0f m².", a.Kind, b.Kind, ov)
			}
		}
	}
	rep.FieldsM2 = math.Max(plot-used, 0)
	if plot > 0 && rep.FieldsM2/plot < MinFieldsShare {
		add(CheckFields, Warning, -1, "Only %.0f%% of the plot is left to crop.", rep.FieldsM2/plot*100)
	}
	var gate *geo.Point
	if gateAt >= 0 {
		gate = d.Elements[gateAt].At
	}
	for i, e := range d.Elements {
		m := &rep.Elements[i]
		switch e.Kind {
		case Pond:
			if len(e.Ring) < 3 {
				continue
			}
			if e.DepthM > 0 {
				m.VolumeM3 = math.Round(m.AreaM2 * e.DepthM)
			}
			if c := geo.ClosestTo(r, e.Ring, true); c.BoundaryM < PondSetbackM {
				add(CheckPondSetback, Warning, i, "The pond is %.1f m from the boundary; keep at least %d m for its bund and the neighbour.", c.BoundaryM, PondSetbackM)
			}
			if m.Share > MaxPondShare {
				add(CheckPondShare, Warning, i, "The pond takes %.0f%% of the plot; more than %.0f%% costs a lot of cropland.", m.Share*100, MaxPondShare*100)
			}
			if e.DepthM > 0 {
				add(CheckPondVolume, Info, i, "The pond holds about %.0f m³ (%.0f litres) when full.", m.VolumeM3, m.VolumeM3*1000)
			}
			if s != nil {
				pondNearFeatures(s, e, i, add)
				if t := s.Terrain; t != nil && !t.Flat && t.Lowest < len(t.Points) {
					low := t.Points[t.Lowest]
					if c := geo.ClosestTo(e.Ring, []geo.Point{low}, false); c.DistanceM > 30 {
						add(CheckPondNotLow, Warning, i, "The pond is %.0f m from the lowest point of the plot; run-off will not reach it on its own.", c.DistanceM)
					}
				}
			}
			for _, z := range zones {
				if ov := geo.Overlap(e.Ring, z.Ring); ov > 1 {
					add(CheckPondZone, Info, i, "The pond takes %.0f m² from the %s zone of the season plan.", ov, z.Name)
				}
			}
		case Shed:
			if gate != nil && len(e.Ring) >= 3 {
				if c := geo.ClosestTo(e.Ring, []geo.Point{*gate}, false); c.DistanceM > ShedFromGateM {
					add(CheckShedFar, Warning, i, "The shed is %.0f m from the gate: produce and inputs will be carried far.", c.DistanceM)
				}
			}
		case Border:
			if shape != nil && shape.Box.WidthM < NarrowM {
				add(CheckBorderShade, Warning, i, "The plot is only %.0f m wide: trees along both long sides will shade much of it; plant the north side only, or low shrubs.", shape.Box.WidthM)
			}
			if s != nil {
				if n, ok := s.Find(osm.Power); ok && n.DistanceM <= 5 {
					add(CheckTreesPower, Warning, i, "An overhead power line runs %.0f m from the boundary on the %s side: plant low shrubs, not tall trees, under it.", n.DistanceM, Words(n.Direction))
				}
			}
		case Gate:
			if s != nil && e.At != nil {
				if road, ok := nearestOf(s, osm.Road, osm.Track); ok && road.Touches {
					if geo.ClosestTo(r, []geo.Point{*e.At}, false).Edge != road.Edge && geo.Distance(*e.At, road.From) > 15 {
						add(CheckGateRoad, Warning, i, "The plot meets the %s on its %s side, but the gate is elsewhere.", road.Label(), Words(road.Direction))
					}
				}
			}
		}
	}
	rep.Status = DesignSound
	for _, c := range rep.Checks {
		switch c.Severity {
		case Error:
			rep.Status = DesignInvalid
		case Warning:
			if rep.Status == DesignSound {
				rep.Status = DesignReview
			}
		}
	}
	return rep
}

func pondNearFeatures(s *Survey, e Element, i int, add func(id, sev string, el int, format string, args ...any)) {
	houses := 0
	for _, f := range s.Features {
		c := geo.ClosestTo(e.Ring, f.Points, f.Closed)
		switch {
		case f.Kind == osm.Power && c.DistanceM <= 10:
			add(CheckPondPower, Warning, i, "An overhead power line passes %.0f m from the pond: keep digging machines clear of it.", c.DistanceM)
		case f.Kind == osm.Building && c.DistanceM <= 15:
			houses++
		}
	}
	if houses > 0 {
		add(CheckPondHouses, Warning, i, "%d building(s) within 15 m of the pond: open water near houses needs a fence for children's safety.", houses)
	}
}
