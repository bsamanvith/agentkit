package geo

import (
	"errors"
	"math"
)

// ErrShares is a list of shares [Strips] cannot cut: empty, a share that
// is not positive, or shares that add up to more than the whole.
var ErrShares = errors.New("geo: shares must be positive and add up to at most 1")

// ErrShape is a ring [Strips] cannot cut: fewer than three corners, or no
// area.
var ErrShape = errors.New("geo: the ring has no area to cut")

// Strips cuts r into strips that run along its long axis (the long side of
// its smallest bounding rectangle), one per share, side by side across the
// plot. Strip i holds shares[i] of r's area, to within a square metre or
// so; when the shares add up to less than 1, the rest of r is left over
// past the last strip. It also returns the strips' direction as a bearing
// in degrees from north, from 0 up to 180.
//
// Strips are easy to walk, plough and sow in rows. A strip of a concave
// plot can come out in two parts joined along a cut line.
func Strips(r Ring, shares []float64) ([]Ring, float64, error) {
	r = r.Open()
	if len(r) < 3 {
		return nil, 0, ErrShape
	}
	total := 0.0
	for _, s := range shares {
		if !(s > 0) {
			return nil, 0, ErrShares
		}
		total += s
	}
	if len(shares) == 0 || total > 1+1e-9 {
		return nil, 0, ErrShares
	}
	pl := planeAt(Centroid(r))
	ps := project(pl, r)
	whole := math.Abs(signedArea(ps))
	if whole < 1e-6 {
		return nil, 0, ErrShape
	}
	u := longAxis(ps)
	v := xy{-u.y, u.x}
	// The ring in the axis frame: a along u, b along v.
	ab := make([]xy, len(ps))
	lo, hi := math.Inf(1), math.Inf(-1)
	for i, p := range ps {
		ab[i] = xy{dot(p, u), dot(p, v)}
		lo, hi = math.Min(lo, ab[i].y), math.Max(hi, ab[i].y)
	}
	below := func(t float64) float64 { return math.Abs(signedArea(clipBelow(ab, t))) }
	cuts := []float64{lo}
	acc := 0.0
	for i, s := range shares {
		acc += s
		if i == len(shares)-1 && acc > 1-1e-9 {
			cuts = append(cuts, hi)
			break
		}
		target := acc * whole
		a, b := cuts[len(cuts)-1], hi
		for range 64 {
			m := (a + b) / 2
			if below(m) < target {
				a = m
			} else {
				b = m
			}
		}
		cuts = append(cuts, (a+b)/2)
	}
	out := make([]Ring, len(shares))
	for i := range shares {
		band := clipAbove(clipBelow(ab, cuts[i+1]), cuts[i])
		ring := make(Ring, 0, len(band))
		for _, q := range band {
			// Back from the axis frame to east and north.
			x, y := q.x*u.x+q.y*v.x, q.x*u.y+q.y*v.y
			p := pl.point(x, y)
			if n := len(ring); n > 0 && Distance(ring[n-1], p) < 0.01 {
				continue
			}
			ring = append(ring, p)
		}
		if n := len(ring); n > 1 && Distance(ring[0], ring[n-1]) < 0.01 {
			ring = ring[:n-1]
		}
		out[i] = ring
	}
	bearing := math.Mod(math.Atan2(u.x, u.y)*180/math.Pi+360, 180)
	return out, bearing, nil
}

// longAxis returns the unit vector along the long side of the smallest
// rectangle around ps whose sides follow one of its edges.
func longAxis(ps []xy) xy {
	best, bestArea := xy{1, 0}, math.Inf(1)
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
		if area := (aHi - aLo) * (bHi - bLo); area < bestArea-1e-9 {
			bestArea = area
			if aHi-aLo >= bHi-bLo {
				best = d
			} else {
				best = n
			}
		}
	}
	return best
}

// clipBelow keeps the part of the polygon ps with y at most t
// (Sutherland-Hodgman against one half-plane).
func clipBelow(ps []xy, t float64) []xy { return clip(ps, func(p xy) float64 { return t - p.y }) }

// clipAbove keeps the part with y at least t.
func clipAbove(ps []xy, t float64) []xy { return clip(ps, func(p xy) float64 { return p.y - t }) }

// clip keeps the part of ps where side is at least zero.
func clip(ps []xy, side func(xy) float64) []xy {
	var out []xy
	for i := range ps {
		p, q := ps[i], ps[(i+1)%len(ps)]
		sp, sq := side(p), side(q)
		if sp >= 0 {
			out = append(out, p)
		}
		if (sp >= 0) != (sq >= 0) {
			f := sp / (sp - sq)
			out = append(out, xy{p.x + f*(q.x-p.x), p.y + f*(q.y-p.y)})
		}
	}
	return out
}

// Extent returns how far r reaches along a bearing (degrees from north)
// and across it, in metres: the length and width of a plot sown in rows
// that run that way.
func Extent(r Ring, bearing float64) (along, across float64) {
	r = r.Open()
	if len(r) < 2 {
		return 0, 0
	}
	ps := project(planeAt(Centroid(r)), r)
	b := rad(bearing)
	d := xy{math.Sin(b), math.Cos(b)}
	n := xy{-d.y, d.x}
	aLo, aHi, bLo, bHi := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	for _, p := range ps {
		a, c := dot(p, d), dot(p, n)
		aLo, aHi = math.Min(aLo, a), math.Max(aHi, a)
		bLo, bHi = math.Min(bLo, c), math.Max(bHi, c)
	}
	return aHi - aLo, bHi - bLo
}
