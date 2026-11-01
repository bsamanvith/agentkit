// Package land reads a plot and what is around it, and shapes the farm:
//
//   - [Describe] measures a plot's shape: its size, its box, how long and
//     narrow, how regular, and which way each side faces.
//   - [Analyse] turns the map's features (package osm) and the ground's
//     heights (package elevation) into a [Survey]: the nearest road, water,
//     buildings and village, what crosses the plot, which way the land
//     falls, and notes on what that means for farming.
//   - [Suggest] lays out a farm's fixed parts — the gate, a cart path, a
//     shed, a farm pond, a border of trees — from the shape and the survey,
//     and [CheckDesign] checks a layout against rules.
//
// It does no I/O: the callers fetch the features and heights. Every number
// a farmer sees comes from here, so the assistant never estimates one.
// The rules' values (setbacks, pond sizes) are starter values from Indian
// practice (MGNREGA farm pond sizes, state agriculture department
// guidance) that need review by an agronomist before farmers rely on them.
package land

import (
	"fmt"
	"math"

	"github.com/bsamanvith/agentkit/apps/farm/geo"
)

// ShapeKind names a plot's shape.
type ShapeKind string

// The shapes.
const (
	Square    ShapeKind = "square"
	Rectangle ShapeKind = "rectangle"
	Strip     ShapeKind = "long_strip"
	Triangle  ShapeKind = "triangle"
	Trapezium ShapeKind = "trapezium"
	Irregular ShapeKind = "irregular"
)

// Edge is one side of a plot: from corner From to the next.
type Edge struct {
	From    int     `json:"from"`
	LengthM float64 `json:"length_m"`
	// Bearing is the side's direction from corner From, degrees from north.
	Bearing float64 `json:"bearing"`
	// Faces is the compass point the side faces, out of the plot.
	Faces string `json:"faces"`
}

// Shape is a plot's shape, measured.
type Shape struct {
	AreaM2     float64   `json:"area_m2"`
	PerimeterM float64   `json:"perimeter_m"`
	Corners    int       `json:"corners"`
	Centroid   geo.Point `json:"centroid"`
	// Box is the smallest rectangle around the plot: its length, width and
	// the direction of its long side.
	Box geo.Box `json:"box"`
	// Elongation is the box's length over its width: 1 for a square.
	Elongation float64 `json:"elongation"`
	// Compactness is 4πA/P², the Polsby-Popper score: 1 for a circle,
	// 0.785 for a square, lower for long or ragged plots.
	Compactness float64 `json:"compactness"`
	// Rectangularity is the area over the box's area: 1 for a rectangle.
	Rectangularity float64 `json:"rectangularity"`
	// Convexity is the area over its convex hull's: 1 when no side bends
	// in.
	Convexity float64   `json:"convexity"`
	Kind      ShapeKind `json:"kind"`
	Edges     []Edge    `json:"edges"`
	// LongSide is the direction of the long side in words ("north-east to
	// south-west").
	LongSide string `json:"long_side"`
	// Notes say what the shape means for farming it.
	Notes []string `json:"notes,omitempty"`
}

var compassWords = map[string]string{
	"N": "north", "NE": "north-east", "E": "east", "SE": "south-east",
	"S": "south", "SW": "south-west", "W": "west", "NW": "north-west",
}

// Words names a compass point in words: "north-east" for "NE".
func Words(point string) string {
	if w, ok := compassWords[point]; ok {
		return w
	}
	return point
}

// Describe measures r's shape. It needs a simple ring of at least three
// corners.
func Describe(r geo.Ring) (Shape, error) {
	r = r.Open()
	if len(r) < 3 || len(geo.SelfIntersections(r)) > 0 {
		return Shape{}, fmt.Errorf("land: a plot needs at least three corners and sides that do not cross")
	}
	area, per := geo.Area(r), geo.Perimeter(r)
	if area < 1 {
		return Shape{}, fmt.Errorf("land: the plot has no area")
	}
	box := geo.MinBox(r)
	s := Shape{
		AreaM2: area, PerimeterM: per, Corners: len(r), Centroid: geo.Centroid(r), Box: box,
		Compactness:    4 * math.Pi * area / (per * per),
		Rectangularity: area / (box.LengthM * box.WidthM),
		Convexity:      area / geo.Area(geo.Hull(r)),
	}
	if box.WidthM > 0 {
		s.Elongation = box.LengthM / box.WidthM
	}
	ccw := signedCCW(r)
	for i := range r {
		a, b := r[i], r[(i+1)%len(r)]
		bearing := geo.Bearing(a, b)
		out := bearing + 90 // right of the direction: outside an anticlockwise ring
		if !ccw {
			out = bearing - 90
		}
		s.Edges = append(s.Edges, Edge{From: i, LengthM: geo.Distance(a, b), Bearing: bearing, Faces: geo.Compass(out)})
	}
	s.Kind = kindOf(s)
	s.LongSide = Words(geo.Compass(box.Bearing)) + " to " + Words(geo.Compass(box.Bearing+180))
	s.Notes = shapeNotes(s)
	return s, nil
}

// signedCCW reports whether r runs anticlockwise.
func signedCCW(r geo.Ring) bool {
	// The shoelace sum on longitude and latitude has the same sign as the
	// projected one for a plot.
	var s float64
	for i := range r {
		j := (i + 1) % len(r)
		s += r[i].Lng*r[j].Lat - r[j].Lng*r[i].Lat
	}
	return s > 0
}

func kindOf(s Shape) ShapeKind {
	switch {
	case s.Corners == 3 || (s.Convexity > 0.97 && s.Rectangularity < 0.6):
		return Triangle
	case s.Rectangularity >= 0.9 && s.Elongation >= 3:
		return Strip
	case s.Rectangularity >= 0.9 && s.Elongation < 1.25:
		return Square
	case s.Rectangularity >= 0.9:
		return Rectangle
	case s.Corners == 4 && s.Convexity > 0.97 && s.Rectangularity >= 0.7:
		return Trapezium
	case s.Elongation >= 4 && s.Convexity > 0.9:
		return Strip
	}
	return Irregular
}

// Starter thresholds of the shape notes.
const (
	// NarrowM: narrower than this, a tractor and its implement turn with
	// difficulty, and borders shade much of the plot.
	NarrowM = 20
	// SmallM2 is about a quarter acre.
	SmallM2 = 1000
)

func shapeNotes(s Shape) []string {
	var out []string
	switch s.Kind {
	case Strip:
		out = append(out, fmt.Sprintf("A long strip (%.0f times as long as wide): sow and plough in rows along its length, %s, to save turns.", s.Elongation, s.LongSide))
	case Rectangle, Square:
		out = append(out, fmt.Sprintf("A regular %s: easy to plough, lay out in rows and share between crops in strips along its long side (%s).", s.Kind, s.LongSide))
	case Trapezium:
		out = append(out, "Four-sided but not square-cornered: rows along the longest side keep short rows to one end.")
	case Triangle:
		out = append(out, "Triangular: the sharp corners are hard to plough; trees, a pond or fodder suit them.")
	case Irregular:
		if s.Convexity < 0.85 {
			out = append(out, fmt.Sprintf("Irregular, with sides that bend in (%.0f%% of its outline's hull): treat the parts as separate blocks.", s.Convexity*100))
		} else {
			out = append(out, "Irregular: the odd corners suit trees, a pond, a shed or fodder, and the regular middle suits field crops.")
		}
	}
	if s.Box.WidthM < NarrowM {
		out = append(out, fmt.Sprintf("Only %.0f m wide: a tractor turns with difficulty; border trees would shade much of it.", s.Box.WidthM))
	}
	if s.AreaM2 < SmallM2 {
		out = append(out, "A small plot: hand tools or a power tiller suit it better than a tractor.")
	}
	return out
}
