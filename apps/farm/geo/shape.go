package geo

import (
	"errors"
	"math"
	"sort"
)

// Hull returns the convex hull of r's corners, anticlockwise; r itself
// (open) when it has fewer than three corners.
func Hull(r Ring) Ring {
	r = r.Open()
	if len(r) < 3 {
		return r
	}
	pl := planeAt(Centroid(r))
	ps := project(pl, r)
	idx := make([]int, len(ps))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool {
		pa, pb := ps[idx[a]], ps[idx[b]]
		return pa.x < pb.x || (pa.x == pb.x && pa.y < pb.y)
	})
	var h []int
	for pass := range 2 {
		start := len(h)
		for k := range idx {
			i := idx[k]
			if pass == 1 {
				i = idx[len(idx)-1-k]
			}
			for len(h) >= start+2 && orient(ps[h[len(h)-2]], ps[h[len(h)-1]], ps[i]) <= eps {
				h = h[:len(h)-1]
			}
			h = append(h, i)
		}
		h = h[:len(h)-1]
	}
	out := make(Ring, len(h))
	for k, i := range h {
		out[k] = r[i]
	}
	return out
}

// Box is a rectangle around a ring: the smallest one whose sides follow
// one of the ring's hull edges.
type Box struct {
	Center Point `json:"center"`
	// LengthM and WidthM are its long and short sides in metres.
	LengthM float64 `json:"length_m"`
	WidthM  float64 `json:"width_m"`
	// Bearing is the long side's direction, degrees from north in [0, 180).
	Bearing float64 `json:"bearing"`
	// Corners are its four corners, anticlockwise.
	Corners Ring `json:"corners"`
}

// MinBox returns the smallest rectangle around r; the zero Box for fewer
// than two corners.
func MinBox(r Ring) Box {
	h := Hull(r)
	if len(h) < 2 {
		return Box{}
	}
	pl := planeAt(Centroid(r))
	ps := project(pl, h)
	best := math.Inf(1)
	var box Box
	for i := range ps {
		e := ps[(i+1)%len(ps)].sub(ps[i])
		l := e.len()
		if l < 1e-9 {
			continue
		}
		d := xy{e.x / l, e.y / l}
		n := xy{-d.y, d.x}
		aLo, aHi, bLo, bHi := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
		for _, p := range ps {
			a, b := dot(p, d), dot(p, n)
			aLo, aHi = math.Min(aLo, a), math.Max(aHi, a)
			bLo, bHi = math.Min(bLo, b), math.Max(bHi, b)
		}
		area := (aHi - aLo) * (bHi - bLo)
		if area >= best-1e-9 {
			continue
		}
		best = area
		ca, cb := (aLo+aHi)/2, (bLo+bHi)/2
		c := xy{ca*d.x + cb*n.x, ca*d.y + cb*n.y}
		long, short, u := aHi-aLo, bHi-bLo, d
		if short > long {
			long, short, u = short, long, n
		}
		bearing := math.Mod(math.Atan2(u.x, u.y)*180/math.Pi+360, 180)
		center := pl.point(c.x, c.y)
		box = Box{Center: center, LengthM: long, WidthM: short, Bearing: bearing, Corners: Rect(center, bearing, long, short)}
	}
	return box
}

// ErrInset is a ring [Inset] cannot shrink by that much: the margin eats
// it, or folds it over itself.
var ErrInset = errors.New("geo: the ring is too small or too narrow for that margin")

// Inset returns r shrunk by d metres on every side: each edge moved d
// metres inwards, the corners where the moved edges meet. It suits plots
// whose corners are not much sharper than a right angle; a margin that
// would fold the ring over itself is [ErrInset].
func Inset(r Ring, d float64) (Ring, error) {
	r = r.Open()
	if len(r) < 3 || d < 0 || math.IsNaN(d) {
		return nil, ErrInset
	}
	if d == 0 {
		return append(Ring(nil), r...), nil
	}
	pl := planeAt(Centroid(r))
	ps := project(pl, r)
	area := signedArea(ps)
	if math.Abs(area) < 1e-6 {
		return nil, ErrInset
	}
	sign := 1.0 // inwards is to the left of an anticlockwise ring
	if area < 0 {
		sign = -1
	}
	n := len(ps)
	type line struct{ p, d xy }
	lines := make([]line, n)
	for i := range ps {
		e := ps[(i+1)%n].sub(ps[i])
		l := e.len()
		if l < 1e-9 {
			return nil, ErrInset
		}
		u := xy{e.x / l, e.y / l}
		in := xy{-u.y * sign, u.x * sign}
		lines[i] = line{xy{ps[i].x + in.x*d, ps[i].y + in.y*d}, u}
	}
	out := make([]xy, n)
	for i := range ps {
		a, b := lines[(i+n-1)%n], lines[i]
		den := cross(a.d, b.d)
		if math.Abs(den) < 1e-9 { // straight on: the corner just moves in
			out[i] = b.p
			continue
		}
		t := cross(b.p.sub(a.p), b.d) / den
		out[i] = xy{a.p.x + t*a.d.x, a.p.y + t*a.d.y}
	}
	inner := signedArea(out)
	if inner*area <= 0 || math.Abs(inner) >= math.Abs(area) {
		return nil, ErrInset
	}
	res := make(Ring, n)
	for i, p := range out {
		res[i] = pl.point(p.x, p.y)
	}
	if len(SelfIntersections(res)) > 0 {
		return nil, ErrInset
	}
	for _, p := range out { // every corner is inside, at least about d from the edge
		if !containsXY(ps, p) || nearestBoundary(ps, p) < d*0.98 {
			return nil, ErrInset
		}
	}
	return res, nil
}

// nearestBoundary is the distance from p to the polygon ps's boundary.
func nearestBoundary(ps []xy, p xy) float64 {
	best := math.Inf(1)
	for i := range ps {
		q := nearestOnSegment(ps[i], ps[(i+1)%len(ps)], p)
		best = math.Min(best, q.sub(p).len())
	}
	return best
}

// RingInside reports whether every corner of inner is inside outer (and
// no edges cross): inner lies within outer.
func RingInside(inner, outer Ring) bool {
	inner, outer = inner.Open(), outer.Open()
	if len(inner) == 0 || len(outer) < 3 {
		return false
	}
	pl := planeAt(Centroid(outer))
	po, pi := project(pl, outer), project(pl, inner)
	for _, p := range pi {
		if !containsXY(po, p) {
			return false
		}
	}
	for i := range pi {
		a1, a2 := pi[i], pi[(i+1)%len(pi)]
		for j := range po {
			if segmentsMeet(a1, a2, po[j], po[(j+1)%len(po)]) {
				return false
			}
		}
	}
	return true
}

// Reach returns how far from p, inside r, one can go along a bearing
// before leaving r: the distance to the first crossing of r's boundary.
// Zero when p is outside r.
func Reach(r Ring, p Point, bearing float64) float64 {
	r = r.Open()
	if len(r) < 3 {
		return 0
	}
	pl := planeAt(p)
	ps := project(pl, r)
	if !containsXY(ps, xy{}) {
		return 0
	}
	b := rad(bearing)
	d := xy{math.Sin(b), math.Cos(b)}
	best := math.Inf(1)
	for i := range ps {
		a, c := ps[i], ps[(i+1)%len(ps)]
		s := c.sub(a)
		den := cross(d, s)
		if math.Abs(den) < 1e-12 {
			continue
		}
		t := cross(a, s) / den // along the ray
		u := cross(a, d) / den // along the edge
		if t > 1e-9 && u >= 0 && u <= 1 {
			best = math.Min(best, t)
		}
	}
	if math.IsInf(best, 1) {
		return 0
	}
	return best
}
