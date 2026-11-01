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
	return strips(r, -1, shares)
}

// StripsAlong is [Strips] with the strips running along bearing (degrees
// from north, such as 0 for north-south strips) instead of the long axis.
func StripsAlong(r Ring, bearing float64, shares []float64) ([]Ring, error) {
	if math.IsNaN(bearing) {
		return nil, ErrShape
	}
	out, _, err := strips(r, math.Mod(math.Mod(bearing, 180)+180, 180), shares)
	return out, err
}

// strips cuts along bearing, or the long axis when bearing is negative.
func strips(r Ring, bearing float64, shares []float64) ([]Ring, float64, error) {
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
	f, err := newFrame(r, bearing)
	if err != nil {
		return nil, 0, err
	}
	cuts := []float64{f.lo}
	acc := 0.0
	for i, s := range shares {
		acc += s
		if i == len(shares)-1 && acc > 1-1e-9 {
			cuts = append(cuts, f.hi)
			break
		}
		target := acc * f.whole
		a, b := cuts[len(cuts)-1], f.hi
		for range 64 {
			m := (a + b) / 2
			if f.below(m) < target {
				a = m
			} else {
				b = m
			}
		}
		cuts = append(cuts, (a+b)/2)
	}
	out := make([]Ring, len(shares))
	for i := range shares {
		out[i] = f.band(cuts[i], cuts[i+1])
	}
	return out, f.bearing(), nil
}

// Bands cuts r like [Strips], along its long axis, but by width: band i is
// widths[i] metres wide, measured square to the long axis, the first
// along one long side. Bands are straight-sided whatever the plot's
// shape, so a row of trees 2 m wide stays 2 m wide. A band that reaches
// past the far side is cut short, and one that starts past it is nil;
// when the widths add up to less than r is across, the rest is left
// over. It also returns the bands' bearing, as Strips does, and how wide
// r is across its long axis.
func Bands(r Ring, widths []float64) ([]Ring, float64, float64, error) {
	return bands(r, -1, widths)
}

// BandsAlong is [Bands] with the bands running along bearing (degrees from
// north) instead of the long axis; it returns the bands and how wide r is
// across that bearing.
func BandsAlong(r Ring, bearing float64, widths []float64) ([]Ring, float64, error) {
	if math.IsNaN(bearing) {
		return nil, 0, ErrShape
	}
	out, _, across, err := bands(r, math.Mod(math.Mod(bearing, 180)+180, 180), widths)
	return out, across, err
}

func bands(r Ring, bearing float64, widths []float64) ([]Ring, float64, float64, error) {
	r = r.Open()
	if len(r) < 3 {
		return nil, 0, 0, ErrShape
	}
	for _, w := range widths {
		if !(w > 0) {
			return nil, 0, 0, ErrShares
		}
	}
	f, err := newFrame(r, bearing)
	if err != nil {
		return nil, 0, 0, err
	}
	out := make([]Ring, len(widths))
	at := f.lo
	for i, w := range widths {
		if at >= f.hi-1e-6 {
			break
		}
		next := math.Min(at+w, f.hi)
		if band := f.band(at, next); len(band) >= 3 {
			out[i] = band
		}
		at = next
	}
	return out, f.bearing(), f.hi - f.lo, nil
}

// frame is a ring in its long axis's frame: a along the axis, b across.
type frame struct {
	pl     plane
	u, v   xy
	ab     []xy
	lo, hi float64 // the ring's reach across the axis
	whole  float64 // its area
}

// newFrame puts r in the frame of bearing, or of its long axis when
// bearing is negative.
func newFrame(r Ring, bearing float64) (frame, error) {
	pl := planeAt(Centroid(r))
	ps := project(pl, r)
	whole := math.Abs(signedArea(ps))
	if whole < 1e-6 {
		return frame{}, ErrShape
	}
	u := longAxis(ps)
	if bearing >= 0 {
		u = xy{math.Sin(rad(bearing)), math.Cos(rad(bearing))}
	}
	f := frame{pl: pl, u: u, v: xy{-u.y, u.x}, ab: make([]xy, len(ps)), lo: math.Inf(1), hi: math.Inf(-1), whole: whole}
	for i, p := range ps {
		f.ab[i] = xy{dot(p, f.u), dot(p, f.v)}
		f.lo, f.hi = math.Min(f.lo, f.ab[i].y), math.Max(f.hi, f.ab[i].y)
	}
	return f, nil
}

// below is the ring's area across the axis up to t.
func (f frame) below(t float64) float64 { return math.Abs(signedArea(clipBelow(f.ab, t))) }

// bearing is the axis's direction, from 0 up to 180 degrees.
func (f frame) bearing() float64 { return math.Mod(math.Atan2(f.u.x, f.u.y)*180/math.Pi+360, 180) }

// band is the part of the ring between from and to across the axis.
func (f frame) band(from, to float64) Ring {
	band := clipAbove(clipBelow(f.ab, to), from)
	ring := make(Ring, 0, len(band))
	for _, q := range band {
		// Back from the axis frame to east and north.
		x, y := q.x*f.u.x+q.y*f.v.x, q.x*f.u.y+q.y*f.v.y
		p := f.pl.point(x, y)
		if n := len(ring); n > 0 && Distance(ring[n-1], p) < 0.01 {
			continue
		}
		ring = append(ring, p)
	}
	if n := len(ring); n > 1 && Distance(ring[0], ring[n-1]) < 0.01 {
		ring = ring[:n-1]
	}
	return ring
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
