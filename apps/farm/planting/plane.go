package planting

import (
	"math"

	"github.com/bsamanvith/agentkit/apps/farm/geo"
)

// The pieces are measured on a local plane: metres east (x) and north (y)
// of a plot's centre. Over a farm's few hundred metres it is as good as the
// ellipsoid to a few centimetres.

type xy struct{ x, y float64 }

func (a xy) sub(b xy) xy        { return xy{a.x - b.x, a.y - b.y} }
func (a xy) add(b xy) xy        { return xy{a.x + b.x, a.y + b.y} }
func (a xy) scale(f float64) xy { return xy{a.x * f, a.y * f} }
func (a xy) len() float64       { return math.Hypot(a.x, a.y) }
func dot(a, b xy) float64       { return a.x*b.x + a.y*b.y }

func rad(d float64) float64 { return d * math.Pi / 180 }

// plane is the plane at a point: metres per degree of latitude and
// longitude there (WGS 84).
type plane struct {
	at         geo.Point
	mLat, mLng float64
}

func planeAt(p geo.Point) plane {
	φ := rad(p.Lat)
	return plane{
		at:   p,
		mLat: 111132.954 - 559.822*math.Cos(2*φ) + 1.175*math.Cos(4*φ),
		mLng: 111412.84*math.Cos(φ) - 93.5*math.Cos(3*φ),
	}
}

func (pl plane) xy(p geo.Point) xy {
	return xy{(p.Lng - pl.at.Lng) * pl.mLng, (p.Lat - pl.at.Lat) * pl.mLat}
}

func (pl plane) point(q xy) geo.Point {
	return geo.Point{Lat: pl.at.Lat + q.y/pl.mLat, Lng: pl.at.Lng + q.x/pl.mLng}
}

func (pl plane) ring(r geo.Ring) []xy {
	if len(r) == 0 {
		return nil
	}
	r = r.Open()
	out := make([]xy, len(r))
	for i, p := range r {
		out[i] = pl.xy(p)
	}
	return out
}

func area(r []xy) float64 {
	s := 0.0
	for i := range r {
		a, b := r[i], r[(i+1)%len(r)]
		s += a.x*b.y - b.x*a.y
	}
	return math.Abs(s) / 2
}

func centroid(r []xy) xy {
	var c xy
	for _, p := range r {
		c = c.add(p)
	}
	return c.scale(1 / math.Max(1, float64(len(r))))
}

// contains: even-odd rule.
func contains(r []xy, p xy) bool {
	in := false
	for i, j := 0, len(r)-1; i < len(r); j, i = i, i+1 {
		a, b := r[i], r[j]
		if (a.y > p.y) != (b.y > p.y) && p.x < (b.x-a.x)*(p.y-a.y)/(b.y-a.y)+a.x {
			in = !in
		}
	}
	return in
}

func insideAny(rs [][]xy, p xy) bool {
	for _, r := range rs {
		if contains(r, p) {
			return true
		}
	}
	return false
}

// edgeDistance is the distance from p to the nearest edge of r.
func edgeDistance(r []xy, p xy) float64 {
	best := math.Inf(1)
	for i := range r {
		a, b := r[i], r[(i+1)%len(r)]
		d := b.sub(a)
		t := 0.0
		if l2 := dot(d, d); l2 > 0 {
			t = math.Max(0, math.Min(1, dot(p.sub(a), d)/l2))
		}
		best = math.Min(best, p.sub(a.add(d.scale(t))).len())
	}
	return best
}

// extent is how far r reaches along u and v.
func extent(r []xy, u, v xy) (aLo, aHi, bLo, bHi float64) {
	aLo, aHi, bLo, bHi = math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	for _, p := range r {
		a, b := dot(p, u), dot(p, v)
		aLo, aHi = math.Min(aLo, a), math.Max(aHi, a)
		bLo, bHi = math.Min(bLo, b), math.Max(bHi, b)
	}
	return
}

// sample covers ring less hole with points on a square grid, about n of
// them (at least a metre apart), and returns them with the area each
// stands for.
func sample(ring, hole []xy, n int) ([]xy, float64) {
	if len(ring) < 3 {
		return nil, 0
	}
	a := area(ring) - area(hole)
	step := math.Max(1, math.Sqrt(math.Max(a, 1)/float64(n)))
	lo, hi := xy{math.Inf(1), math.Inf(1)}, xy{math.Inf(-1), math.Inf(-1)}
	for _, p := range ring {
		lo = xy{math.Min(lo.x, p.x), math.Min(lo.y, p.y)}
		hi = xy{math.Max(hi.x, p.x), math.Max(hi.y, p.y)}
	}
	var out []xy
	for y := lo.y + step/2; y < hi.y; y += step {
		for x := lo.x + step/2; x < hi.x; x += step {
			p := xy{x, y}
			if contains(ring, p) && (len(hole) < 3 || !contains(hole, p)) {
				out = append(out, p)
			}
		}
	}
	if len(out) == 0 { // a sliver thinner than the grid: its centre
		out = append(out, centroid(ring))
	}
	return out, a / float64(len(out))
}
