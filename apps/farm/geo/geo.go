package geo

import (
	"math"
)

// Point is a WGS84 position in decimal degrees.
type Point struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// Valid reports whether p is a real position: finite, latitude within ±90
// and longitude within ±180.
func (p Point) Valid() bool {
	return !math.IsNaN(p.Lat) && !math.IsNaN(p.Lng) &&
		p.Lat >= -90 && p.Lat <= 90 && p.Lng >= -180 && p.Lng <= 180
}

// Ring is a polygon's boundary, its corners in order.
type Ring []Point

// Open returns r without a closing corner: if the last corner equals the
// first, it is dropped. It never changes r.
func (r Ring) Open() Ring {
	if len(r) > 1 && r[0] == r[len(r)-1] {
		return r[:len(r)-1]
	}
	return r
}

// WGS84 ellipsoid.
const (
	semiMajor  = 6378137.0
	flattening = 1 / 298.257223563
	e2         = flattening * (2 - flattening) // first eccentricity squared
)

// primeVertical is N(φ), the radius of curvature in the prime vertical.
func primeVertical(lat float64) float64 {
	s := math.Sin(lat)
	return semiMajor / math.Sqrt(1-e2*s*s)
}

// meridional is M(φ), the radius of curvature along the meridian.
func meridional(lat float64) float64 {
	s := math.Sin(lat)
	return semiMajor * (1 - e2) / math.Pow(1-e2*s*s, 1.5)
}

// meridianArc is the distance along a meridian from the equator to φ.
func meridianArc(lat float64) float64 {
	e4 := e2 * e2
	e6 := e4 * e2
	return semiMajor * ((1-e2/4-3*e4/64-5*e6/256)*lat -
		(3*e2/8+3*e4/32+45*e6/1024)*math.Sin(2*lat) +
		(15*e4/256+45*e6/1024)*math.Sin(4*lat) -
		(35*e6/3072)*math.Sin(6*lat))
}

func rad(deg float64) float64 { return deg * math.Pi / 180 }

// plane is the ellipsoidal sinusoidal projection centred on a point: equal
// area, so the shoelace formula on its coordinates gives areas on the
// ellipsoid. x and y are metres east and north of the centre.
type plane struct {
	lng0 float64 // radians
	y0   float64 // meridian arc of the centre's latitude
	lat0 float64 // radians
}

func planeAt(c Point) plane {
	return plane{lng0: rad(c.Lng), lat0: rad(c.Lat), y0: meridianArc(rad(c.Lat))}
}

func (pl plane) xy(p Point) (x, y float64) {
	lat := rad(p.Lat)
	return primeVertical(lat) * math.Cos(lat) * (rad(p.Lng) - pl.lng0), meridianArc(lat) - pl.y0
}

// point is the inverse of xy, by Newton's method on the meridian arc.
func (pl plane) point(x, y float64) Point {
	lat := pl.lat0 + y/meridional(pl.lat0)
	for range 8 {
		lat -= (meridianArc(lat) - pl.y0 - y) / meridional(lat)
	}
	lng := pl.lng0 + x/(primeVertical(lat)*math.Cos(lat))
	return Point{Lat: lat * 180 / math.Pi, Lng: lng * 180 / math.Pi}
}

// project returns r's corners in a plane centred on its first corner.
func project(pl plane, r Ring) []xy {
	out := make([]xy, len(r))
	for i, p := range r {
		out[i].x, out[i].y = pl.xy(p)
	}
	return out
}

type xy struct{ x, y float64 }

func (a xy) sub(b xy) xy           { return xy{a.x - b.x, a.y - b.y} }
func cross(a, b xy) float64        { return a.x*b.y - a.y*b.x }
func dot(a, b xy) float64          { return a.x*b.x + a.y*b.y }
func (a xy) len() float64          { return math.Hypot(a.x, a.y) }
func orient(a, b, c xy) float64    { return cross(b.sub(a), c.sub(a)) }
func between(a, b, v float64) bool { return math.Min(a, b) <= v && v <= math.Max(a, b) }

// signedArea is the shoelace area of a projected ring: positive when it
// runs anticlockwise.
func signedArea(ps []xy) float64 {
	var s float64
	for i := range ps {
		j := (i + 1) % len(ps)
		s += cross(ps[i], ps[j])
	}
	return s / 2
}

// Area returns the area enclosed by r in square metres; zero for fewer than
// three corners. A self-intersecting ring's area is not meaningful: check
// [SelfIntersections] first.
func Area(r Ring) float64 {
	r = r.Open()
	if len(r) < 3 {
		return 0
	}
	return math.Abs(signedArea(project(planeAt(r[0]), r)))
}

// Distance returns the length in metres of the short line between a and b,
// in a plane tangent at their midpoint: within millimetres of the geodesic
// for points a few kilometres apart.
func Distance(a, b Point) float64 {
	mid := rad((a.Lat + b.Lat) / 2)
	dy := rad(b.Lat-a.Lat) * meridional(mid)
	dx := rad(b.Lng-a.Lng) * primeVertical(mid) * math.Cos(mid)
	return math.Hypot(dx, dy)
}

// Perimeter returns the length of r's boundary in metres, closing edge
// included.
func Perimeter(r Ring) float64 {
	r = r.Open()
	if len(r) < 2 {
		return 0
	}
	var s float64
	for i := range r {
		s += Distance(r[i], r[(i+1)%len(r)])
	}
	return s
}

// Centroid returns the centre of mass of the area r encloses; for a ring
// with no area, the mean of its corners.
func Centroid(r Ring) Point {
	r = r.Open()
	if len(r) == 0 {
		return Point{}
	}
	pl := planeAt(r[0])
	ps := project(pl, r)
	a := signedArea(ps)
	if len(r) < 3 || math.Abs(a) < 1e-9 {
		var c xy
		for _, p := range ps {
			c.x += p.x / float64(len(ps))
			c.y += p.y / float64(len(ps))
		}
		return pl.point(c.x, c.y)
	}
	var cx, cy float64
	for i := range ps {
		j := (i + 1) % len(ps)
		f := cross(ps[i], ps[j])
		cx += (ps[i].x + ps[j].x) * f
		cy += (ps[i].y + ps[j].y) * f
	}
	return pl.point(cx/(6*a), cy/(6*a))
}

// Angles returns the interior angle at each corner of r, in degrees, in
// (0, 360). A needle-thin spike has an angle near 0.
func Angles(r Ring) []float64 {
	r = r.Open()
	if len(r) < 3 {
		return nil
	}
	ps := project(planeAt(r[0]), r)
	ccw := signedArea(ps) > 0
	out := make([]float64, len(ps))
	for i := range ps {
		prev := ps[(i+len(ps)-1)%len(ps)]
		next := ps[(i+1)%len(ps)]
		a, b := prev.sub(ps[i]), next.sub(ps[i])
		ang := math.Atan2(cross(b, a), dot(b, a)) * 180 / math.Pi // from b to a
		if !ccw {
			ang = -ang
		}
		if ang < 0 {
			ang += 360
		}
		out[i] = ang
	}
	return out
}

// Crossing is a pair of edges of a ring that meet where they should not.
// Edge i runs from corner i to corner i+1 (the last edge back to corner 0).
type Crossing struct{ A, B int }

// SelfIntersections returns every pair of edges of r that cross or touch,
// other than neighbours meeting at their shared corner. A simple polygon has
// none.
func SelfIntersections(r Ring) []Crossing {
	r = r.Open()
	n := len(r)
	if n < 3 {
		return nil
	}
	ps := project(planeAt(r[0]), r)
	var out []Crossing
	for i := range n {
		a1, a2 := ps[i], ps[(i+1)%n]
		for j := i + 1; j < n; j++ {
			if j == i+1 || (i == 0 && j == n-1) {
				// Neighbours share a corner; they only go wrong if they
				// fold back over each other.
				if overlapsCollinear(a1, a2, ps[j], ps[(j+1)%n]) {
					out = append(out, Crossing{i, j})
				}
				continue
			}
			if segmentsMeet(a1, a2, ps[j], ps[(j+1)%n]) {
				out = append(out, Crossing{i, j})
			}
		}
	}
	return out
}

// eps is the tolerance, in square metres of cross product, for collinearity.
const eps = 1e-6

func segmentsMeet(p1, p2, q1, q2 xy) bool {
	d1, d2 := orient(q1, q2, p1), orient(q1, q2, p2)
	d3, d4 := orient(p1, p2, q1), orient(p1, p2, q2)
	if ((d1 > eps && d2 < -eps) || (d1 < -eps && d2 > eps)) &&
		((d3 > eps && d4 < -eps) || (d3 < -eps && d4 > eps)) {
		return true
	}
	on := func(a, b, p xy, d float64) bool {
		return math.Abs(d) <= eps && between(a.x, b.x, p.x) && between(a.y, b.y, p.y)
	}
	return on(q1, q2, p1, d1) || on(q1, q2, p2, d2) || on(p1, p2, q1, d3) || on(p1, p2, q2, d4)
}

// overlapsCollinear reports whether two edges sharing a corner lie on one
// line and overlap for more than that corner: the ring doubles back.
func overlapsCollinear(p1, p2, q1, q2 xy) bool {
	if math.Abs(orient(p1, p2, q1)) > eps || math.Abs(orient(p1, p2, q2)) > eps {
		return false
	}
	d := p2.sub(p1)
	l := d.len()
	if l == 0 {
		return false
	}
	t := func(p xy) float64 { return dot(p.sub(p1), d) / (l * l) }
	lo, hi := math.Min(t(q1), t(q2)), math.Max(t(q1), t(q2))
	return math.Min(hi, 1)-math.Max(lo, 0) > 1e-9
}

// Contains reports whether p is inside r (on the boundary counts as
// outside).
func Contains(r Ring, p Point) bool {
	r = r.Open()
	if len(r) < 3 {
		return false
	}
	pl := planeAt(r[0])
	var q xy
	q.x, q.y = pl.xy(p)
	return containsXY(project(pl, r), q)
}

// Overlap returns the area in square metres shared by two simple rings,
// estimated by sampling a grid over the smaller one's bounding box (to about
// 1% of the smaller ring's area); zero when they do not overlap.
func Overlap(a, b Ring) float64 {
	a, b = a.Open(), b.Open()
	if len(a) < 3 || len(b) < 3 {
		return 0
	}
	if Area(b) < Area(a) {
		a, b = b, a
	}
	pl := planeAt(a[0])
	pa, pb := project(pl, a), project(pl, b)
	minA, maxA := bounds(pa)
	minB, maxB := bounds(pb)
	if maxA.x < minB.x || maxB.x < minA.x || maxA.y < minB.y || maxB.y < minA.y {
		return 0
	}
	const n = 100
	dx, dy := (maxA.x-minA.x)/n, (maxA.y-minA.y)/n
	hits := 0
	for i := range n {
		for j := range n {
			q := xy{minA.x + (float64(i)+0.5)*dx, minA.y + (float64(j)+0.5)*dy}
			if containsXY(pa, q) && containsXY(pb, q) {
				hits++
			}
		}
	}
	return float64(hits) * dx * dy
}

func bounds(ps []xy) (lo, hi xy) {
	lo, hi = ps[0], ps[0]
	for _, p := range ps[1:] {
		lo.x, lo.y = math.Min(lo.x, p.x), math.Min(lo.y, p.y)
		hi.x, hi.y = math.Max(hi.x, p.x), math.Max(hi.y, p.y)
	}
	return lo, hi
}

func containsXY(ps []xy, q xy) bool {
	in := false
	for i := range ps {
		a, b := ps[i], ps[(i+1)%len(ps)]
		if (a.y > q.y) != (b.y > q.y) && q.x < a.x+(q.y-a.y)*(b.x-a.x)/(b.y-a.y) {
			in = !in
		}
	}
	return in
}
