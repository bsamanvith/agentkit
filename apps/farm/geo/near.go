package geo

import "math"

// Bearing returns the direction from a to b in degrees clockwise from
// north, in [0, 360): 0 is north, 90 east. It is the direction in a plane
// tangent at their midpoint, so it is meant for points a few kilometres
// apart, like the rest of the package. It is 0 when a and b are the same.
func Bearing(a, b Point) float64 {
	mid := rad((a.Lat + b.Lat) / 2)
	dy := rad(b.Lat-a.Lat) * meridional(mid)
	dx := rad(b.Lng-a.Lng) * primeVertical(mid) * math.Cos(mid)
	if dx == 0 && dy == 0 {
		return 0
	}
	return math.Mod(math.Atan2(dx, dy)*180/math.Pi+360, 360)
}

// compassPoints are the eight points of the compass, clockwise from north.
var compassPoints = [8]string{"N", "NE", "E", "SE", "S", "SW", "W", "NW"}

// Compass names a bearing (degrees from north) as the nearest of the eight
// points of the compass: N, NE, E, SE, S, SW, W or NW.
func Compass(bearing float64) string {
	b := math.Mod(math.Mod(bearing, 360)+360, 360)
	return compassPoints[int(math.Floor((b+22.5)/45))%8]
}

// CompassWords names a bearing as the nearest of the eight points of the
// compass in words: "north", "north-east", and so on.
func CompassWords(bearing float64) string {
	return compassWords[int(math.Floor((math.Mod(math.Mod(bearing, 360)+360, 360)+22.5)/45))%8]
}

var compassWords = [8]string{"north", "north-east", "east", "south-east", "south", "south-west", "west", "north-west"}

// Offset returns the point d metres from p along a bearing (degrees from
// north).
func Offset(p Point, bearing, d float64) Point {
	b := rad(bearing)
	return planeAt(p).point(d*math.Sin(b), d*math.Cos(b))
}

// Rect returns the rectangle centred on c with its long side, length
// metres, along a bearing (degrees from north) and its short side width
// metres; corners anticlockwise.
func Rect(c Point, bearing, length, width float64) Ring {
	pl := planeAt(c)
	b := rad(bearing)
	u := xy{math.Sin(b), math.Cos(b)} // along
	v := xy{-u.y, u.x}                // across, to the left
	hl, hw := length/2, width/2
	out := make(Ring, 0, 4)
	for _, s := range [4][2]float64{{-1, -1}, {1, -1}, {1, 1}, {-1, 1}} {
		out = append(out, pl.point(s[0]*hl*u.x+s[1]*hw*v.x, s[0]*hl*u.y+s[1]*hw*v.y))
	}
	return out
}

// Closest is where a ring and another shape come closest.
type Closest struct {
	// DistanceM is the shortest distance between them in metres: zero when
	// they touch, cross, or one is inside the other.
	DistanceM float64
	// OnRing and OnShape are the points where they come closest: on the
	// ring's boundary and on the shape. When the shape is inside the ring
	// (or crosses it), OnShape is a point of it inside the ring.
	OnRing, OnShape Point
	// Edge is the ring's edge OnRing lies on: edge i runs from corner i to
	// corner i+1.
	Edge int
	// BoundaryM is the shortest distance between the ring's boundary and
	// the shape's line or boundary, whether or not one is inside the
	// other: how far a pond inside a plot is from the plot's edge. Zero
	// when they cross.
	BoundaryM float64
	// Crosses: the shape crosses the ring's boundary. Inside: some of the
	// shape is inside the ring. Encloses: the ring is inside the shape (a
	// closed shape only).
	Crosses, Inside, Encloses bool
}

// ClosestTo returns where ring r and a shape come closest. The shape is a
// line through its points, or, when closed, the polygon they bound (a lake,
// a building). Points are within a few kilometres of each other.
func ClosestTo(r Ring, shape []Point, closed bool) Closest {
	r = r.Open()
	if closed {
		shape = Ring(shape).Open()
	}
	if len(r) == 0 || len(shape) == 0 {
		return Closest{DistanceM: math.Inf(1), BoundaryM: math.Inf(1), Edge: -1}
	}
	pl := planeAt(Centroid(r))
	pr := project(pl, r)
	ps := project(pl, shape)
	segs := func(ps []xy, closed bool) int {
		switch {
		case len(ps) < 2:
			return len(ps) // a point is a segment from itself to itself
		case closed:
			return len(ps)
		default:
			return len(ps) - 1
		}
	}
	seg := func(ps []xy, i int) (xy, xy) {
		if len(ps) == 1 {
			return ps[0], ps[0]
		}
		return ps[i], ps[(i+1)%len(ps)]
	}
	out := Closest{DistanceM: math.Inf(1), BoundaryM: math.Inf(1), Edge: -1}
	nr, ns := segs(pr, true), segs(ps, closed)
	for i := range nr {
		a1, a2 := seg(pr, i)
		for j := range ns {
			b1, b2 := seg(ps, j)
			if segmentsMeet(a1, a2, b1, b2) {
				out.Crosses, out.BoundaryM = true, 0
				if out.DistanceM > 0 {
					p := intersection(a1, a2, b1, b2)
					out.DistanceM, out.Edge = 0, i
					out.OnRing, out.OnShape = pl.point(p.x, p.y), pl.point(p.x, p.y)
				}
				continue
			}
			d, pa, pb := segmentDistance(a1, a2, b1, b2)
			out.BoundaryM = math.Min(out.BoundaryM, d)
			if d < out.DistanceM {
				out.DistanceM, out.Edge = d, i
				out.OnRing, out.OnShape = pl.point(pa.x, pa.y), pl.point(pb.x, pb.y)
			}
		}
	}
	for _, q := range ps {
		if len(pr) >= 3 && containsXY(pr, q) {
			out.Inside = true
			if !out.Crosses {
				out.DistanceM = 0
				out.OnShape = pl.point(q.x, q.y)
			}
			break
		}
	}
	if closed && len(ps) >= 3 && !out.Crosses && !out.Inside && containsXY(ps, pr[0]) {
		out.Encloses, out.DistanceM = true, 0
		out.OnRing, out.OnShape, out.Edge = r[0], r[0], 0
	}
	return out
}

// segmentDistance returns the distance between segments a1a2 and b1b2,
// which do not meet, and the closest point on each.
func segmentDistance(a1, a2, b1, b2 xy) (float64, xy, xy) {
	best := math.Inf(1)
	var pa, pb xy
	try := func(d float64, p, q xy) {
		if d < best {
			best, pa, pb = d, p, q
		}
	}
	q := nearestOnSegment(a1, a2, b1)
	try(q.sub(b1).len(), q, b1)
	q = nearestOnSegment(a1, a2, b2)
	try(q.sub(b2).len(), q, b2)
	q = nearestOnSegment(b1, b2, a1)
	try(q.sub(a1).len(), a1, q)
	q = nearestOnSegment(b1, b2, a2)
	try(q.sub(a2).len(), a2, q)
	return best, pa, pb
}

// nearestOnSegment returns the point of segment ab nearest to p.
func nearestOnSegment(a, b, p xy) xy {
	d := b.sub(a)
	l2 := dot(d, d)
	if l2 == 0 {
		return a
	}
	t := math.Max(0, math.Min(1, dot(p.sub(a), d)/l2))
	return xy{a.x + t*d.x, a.y + t*d.y}
}

// intersection returns where segments that meet do; for collinear
// overlapping ones, an end of one that lies on the other.
func intersection(p1, p2, q1, q2 xy) xy {
	r, s := p2.sub(p1), q2.sub(q1)
	den := cross(r, s)
	if math.Abs(den) > 1e-12 {
		t := cross(q1.sub(p1), s) / den
		return xy{p1.x + t*r.x, p1.y + t*r.y}
	}
	for _, c := range []xy{q1, q2, p1, p2} {
		if nearestOnSegment(p1, p2, c).sub(c).len() < 1e-6 && nearestOnSegment(q1, q2, c).sub(c).len() < 1e-6 {
			return c
		}
	}
	return p1
}

// LineLength returns the length in metres of a line through points.
func LineLength(line []Point) float64 {
	var s float64
	for i := 1; i < len(line); i++ {
		s += Distance(line[i-1], line[i])
	}
	return s
}
