package geo_test

import (
	"errors"
	"math"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/geo"
)

// square is a 100 m by 100 m plot near Hyderabad, corners anticlockwise
// from the south-west.
func square(t *testing.T) geo.Ring {
	t.Helper()
	sw := geo.Point{Lat: 17.385, Lng: 78.4867}
	se := geo.Offset(sw, 90, 100)
	ne := geo.Offset(se, 0, 100)
	nw := geo.Offset(sw, 0, 100)
	return geo.Ring{sw, se, ne, nw}
}

func near(t *testing.T, what string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.4f, want %.4f ± %g", what, got, want, tol)
	}
}

func TestBearingAndCompass(t *testing.T) {
	t.Parallel()
	p := geo.Point{Lat: 17.385, Lng: 78.4867}
	for _, b := range []float64{0, 45, 90, 135, 180, 225, 270, 315, 10, 359} {
		q := geo.Offset(p, b, 250)
		near(t, "distance", geo.Distance(p, q), 250, 0.01)
		got := geo.Bearing(p, q)
		diff := math.Mod(math.Abs(got-b)+180, 360) - 180
		near(t, "bearing", diff, 0, 0.01)
	}
	for b, want := range map[float64]string{0: "N", 22: "N", 23: "NE", 90: "E", 180: "S", 200: "S", 225: "SW", 270: "W", 337.4: "NW", 337.6: "N", -90: "W", 720: "N"} {
		if got := geo.Compass(b); got != want {
			t.Errorf("Compass(%v) = %s, want %s", b, got, want)
		}
	}
	if geo.Bearing(p, p) != 0 {
		t.Error("Bearing of a point to itself is not 0")
	}
}

func TestRect(t *testing.T) {
	t.Parallel()
	c := geo.Point{Lat: 17.385, Lng: 78.4867}
	r := geo.Rect(c, 30, 40, 10)
	near(t, "area", geo.Area(r), 400, 0.01)
	near(t, "perimeter", geo.Perimeter(r), 100, 0.01)
	b := geo.MinBox(r)
	near(t, "length", b.LengthM, 40, 0.01)
	near(t, "width", b.WidthM, 10, 0.01)
	near(t, "bearing", b.Bearing, 30, 0.01)
}

func TestClosestTo(t *testing.T) {
	t.Parallel()
	sq := square(t)
	sw, se := sq[0], sq[1]
	// A road running north-south 30 m east of the plot.
	roadS, roadN := geo.Offset(geo.Offset(se, 90, 30), 180, 200), geo.Offset(geo.Offset(se, 90, 30), 0, 300)
	c := geo.ClosestTo(sq, []geo.Point{roadS, roadN}, false)
	near(t, "road distance", c.DistanceM, 30, 0.05)
	if c.Edge != 1 {
		t.Errorf("road nearest to edge %d, want 1 (the east side)", c.Edge)
	}
	near(t, "bearing to road", geo.Bearing(c.OnRing, c.OnShape), 90, 0.1)
	if c.Crosses || c.Inside || c.Encloses {
		t.Errorf("road 30 m away: %+v", c)
	}
	// A canal crossing the plot.
	c = geo.ClosestTo(sq, []geo.Point{geo.Offset(sw, 270, 20), geo.Offset(se, 90, 20)}, false)
	if !c.Crosses || c.DistanceM != 0 {
		t.Errorf("crossing canal: %+v", c)
	}
	// A well (a point) inside the plot.
	mid := geo.Centroid(sq)
	c = geo.ClosestTo(sq, []geo.Point{mid}, false)
	if !c.Inside || c.DistanceM != 0 {
		t.Errorf("well inside: %+v", c)
	}
	near(t, "well to the edge", c.BoundaryM, 50, 0.05)
	// A building 12 m north of the plot.
	nw := sq[3]
	b := geo.Rect(geo.Offset(geo.Offset(nw, 0, 17), 90, 50), 90, 20, 10)
	c = geo.ClosestTo(sq, b, true)
	near(t, "building distance", c.DistanceM, 12, 0.05)
	if c.Edge != 2 {
		t.Errorf("building nearest edge %d, want 2 (the north side)", c.Edge)
	}
	// A lake around the plot.
	lake := geo.Rect(mid, 0, 500, 500)
	c = geo.ClosestTo(sq, lake, true)
	if !c.Encloses || c.DistanceM != 0 {
		t.Errorf("plot in a lake: %+v", c)
	}
	// Nothing to compare.
	if c := geo.ClosestTo(sq, nil, false); !math.IsInf(c.DistanceM, 1) || c.Edge != -1 {
		t.Errorf("empty shape: %+v", c)
	}
	near(t, "line length", geo.LineLength([]geo.Point{roadS, roadN}), 500, 0.05)
}

func TestHullAndMinBox(t *testing.T) {
	t.Parallel()
	sq := square(t)
	// An L-shape: the square with its north-east quarter cut out.
	sw, se, ne, nw := sq[0], sq[1], sq[2], sq[3]
	_ = ne
	l := geo.Ring{sw, se, geo.Offset(se, 0, 50), geo.Offset(geo.Offset(se, 0, 50), 270, 50), geo.Offset(nw, 90, 50), nw}
	h := geo.Hull(l)
	if len(h) != 5 {
		t.Fatalf("hull of the L has %d corners, want 5: %v", len(h), h)
	}
	near(t, "hull area", geo.Area(h), 8750, 1)
	near(t, "L area", geo.Area(l), 7500, 1)
	b := geo.MinBox(sq)
	near(t, "box length", b.LengthM, 100, 0.05)
	near(t, "box width", b.WidthM, 100, 0.05)
	near(t, "box area", geo.Area(b.Corners), 10000, 1)
	if geo.MinBox(geo.Ring{sw}).LengthM != 0 {
		t.Error("box of one point")
	}
	// Hull of a triangle is the triangle; of two points, the points.
	if len(geo.Hull(geo.Ring{sw, se, nw})) != 3 || len(geo.Hull(geo.Ring{sw, se})) != 2 {
		t.Error("small hulls")
	}
}

func TestInset(t *testing.T) {
	t.Parallel()
	sq := square(t)
	in, err := geo.Inset(sq, 5)
	if err != nil {
		t.Fatal(err)
	}
	near(t, "inset area", geo.Area(in), 90*90, 1)
	if !geo.RingInside(in, sq) {
		t.Error("inset ring is not inside")
	}
	// Either way round.
	rev := geo.Ring{sq[3], sq[2], sq[1], sq[0]}
	in2, err := geo.Inset(rev, 5)
	if err != nil {
		t.Fatal(err)
	}
	near(t, "inset of the reversed ring", geo.Area(in2), 8100, 1)
	// The L keeps its shape.
	sw, se, nw := sq[0], sq[1], sq[3]
	l := geo.Ring{sw, se, geo.Offset(se, 0, 50), geo.Offset(geo.Offset(se, 0, 50), 270, 50), geo.Offset(nw, 90, 50), nw}
	lin, err := geo.Inset(l, 5)
	if err != nil {
		t.Fatal(err)
	}
	near(t, "inset L", geo.Area(lin), 90*40+40*50, 2)
	if !geo.RingInside(lin, l) {
		t.Error("inset L is not inside")
	}
	for _, d := range []float64{50, 60, -1, math.NaN()} {
		if _, err := geo.Inset(sq, d); !errors.Is(err, geo.ErrInset) {
			t.Errorf("Inset by %v: %v, want ErrInset", d, err)
		}
	}
	if same, err := geo.Inset(sq, 0); err != nil || len(same) != 4 {
		t.Errorf("Inset by 0: %v %v", same, err)
	}
}

func TestReachAndRingInside(t *testing.T) {
	t.Parallel()
	sq := square(t)
	c := geo.Centroid(sq)
	near(t, "reach north", geo.Reach(sq, c, 0), 50, 0.05)
	near(t, "reach east", geo.Reach(sq, c, 90), 50, 0.05)
	near(t, "reach north-east", geo.Reach(sq, c, 45), 50*math.Sqrt2, 0.1)
	if geo.Reach(sq, geo.Offset(sq[0], 225, 10), 45) != 0 {
		t.Error("reach from outside is not 0")
	}
	if geo.RingInside(geo.Rect(c, 0, 120, 10), sq) {
		t.Error("a 120 m strip is inside a 100 m square")
	}
	if !geo.RingInside(geo.Rect(c, 0, 90, 10), sq) {
		t.Error("a 90 m strip is not inside a 100 m square")
	}
}
