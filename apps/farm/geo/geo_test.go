package geo_test

import (
	"math"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/geo"
)

// quadArea is the exact area of the WGS84 quadrangle between two latitudes
// and two longitudes (degrees): b²Δλ/2 · [q(φ2) − q(φ1)], the reference the
// projection is checked against.
func quadArea(lat1, lat2, dlng float64) float64 {
	const a = 6378137.0
	const f = 1 / 298.257223563
	e2 := f * (2 - f)
	e := math.Sqrt(e2)
	b := a * (1 - f)
	q := func(lat float64) float64 {
		s := math.Sin(lat * math.Pi / 180)
		return s/(1-e2*s*s) + math.Log((1+e*s)/(1-e*s))/(2*e)
	}
	return b * b * dlng * math.Pi / 180 / 2 * (q(lat2) - q(lat1))
}

func rect(lat, lng, dlat, dlng float64) geo.Ring {
	return geo.Ring{{lat, lng}, {lat, lng + dlng}, {lat + dlat, lng + dlng}, {lat + dlat, lng}}
}

func TestAreaMatchesTheEllipsoid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		lat, lng, dl, d float64
	}{
		{"one hectare-ish near Hyderabad", 17.385, 78.4867, 0.0009, 0.00094},
		{"two acres at the equator", 0, 30, 0.0006, 0.00075},
		{"a large estate in Punjab", 30.9, 75.85, 0.03, 0.03},
		{"a field in Norway", 60.4, 10.6, 0.001, 0.002},
		{"southern hemisphere", -33.9, 18.4, 0.002, 0.002},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			want := quadArea(tt.lat, tt.lat+tt.dl, tt.d)
			// The quadrangle's top and bottom follow parallels, the ring's
			// edges are straight: allow for the bulge on the large one.
			got := geo.Area(rect(tt.lat, tt.lng, tt.dl, tt.d))
			if rel := math.Abs(got-want) / want; rel > 0.001 {
				t.Errorf("Area = %.3f m², want %.3f m² (off by %.4f%%)", got, want, rel*100)
			}
		})
	}
}

// TestAgainstGeographicLib: area and perimeter agree with GeographicLib's
// geodesic polygon (Karney's algorithm, the reference for WGS84), computed
// with geographiclib 2.x in Python: Geodesic.WGS84.Polygon().
func TestAgainstGeographicLib(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		ring            geo.Ring
		area, perimeter float64
	}{
		{"two acres in Ranga Reddy", geo.Ring{{17.0712, 78.2021}, {17.0712, 78.2030}, {17.0720, 78.2030}, {17.0720, 78.2021}}, 8481.820753902197, 368.67362076614893},
		{"950 ha in Punjab", geo.Ring{{30.9, 75.85}, {30.9, 75.88}, {30.93, 75.88}, {30.93, 75.85}}, 9537972.455055237, 12387.412242490938},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := geo.Area(tt.ring); math.Abs(got-tt.area)/tt.area > 1e-5 {
				t.Errorf("Area = %.6f, GeographicLib %.6f", got, tt.area)
			}
			if got := geo.Perimeter(tt.ring); math.Abs(got-tt.perimeter)/tt.perimeter > 1e-6 {
				t.Errorf("Perimeter = %.6f, GeographicLib %.6f", got, tt.perimeter)
			}
		})
	}
}

func TestAreaIgnoresOrderAndClosingCorner(t *testing.T) {
	t.Parallel()
	r := rect(17.385, 78.4867, 0.001, 0.001)
	want := geo.Area(r)
	rev := geo.Ring{r[3], r[2], r[1], r[0]}
	closed := append(append(geo.Ring{}, r...), r[0])
	for name, ring := range map[string]geo.Ring{"clockwise": rev, "closed": closed} {
		if got := geo.Area(ring); math.Abs(got-want) > 1e-6 {
			t.Errorf("%s: Area = %v, want %v", name, got, want)
		}
	}
	if got := geo.Area(r[:2]); got != 0 {
		t.Errorf("two corners: Area = %v, want 0", got)
	}
}

func TestPerimeterAndDistance(t *testing.T) {
	t.Parallel()
	// One minute of latitude is about 1844.5 m at 17°N (M(φ)·π/10800);
	// one degree of longitude at the equator is
	// 111319.49 m.
	if d := geo.Distance(geo.Point{Lat: 0, Lng: 0}, geo.Point{Lat: 0, Lng: 1}); math.Abs(d-111319.49) > 0.5 {
		t.Errorf("1° of longitude at the equator = %.2f m, want 111319.49", d)
	}
	if d := geo.Distance(geo.Point{Lat: 17, Lng: 78}, geo.Point{Lat: 17 + 1.0/60, Lng: 78}); math.Abs(d-1844.49) > 0.05 {
		t.Errorf("1' of latitude at 17°N = %.2f m, want about 1844.49", d)
	}
	r := rect(0, 30, 0.001, 0.001)
	want := 2*geo.Distance(r[0], r[1]) + 2*geo.Distance(r[1], r[2])
	if got := geo.Perimeter(r); math.Abs(got-want) > 1e-6 {
		t.Errorf("Perimeter = %v, want %v", got, want)
	}
}

func TestCentroid(t *testing.T) {
	t.Parallel()
	c := geo.Centroid(rect(17.0, 78.0, 0.002, 0.002))
	if math.Abs(c.Lat-17.001) > 1e-6 || math.Abs(c.Lng-78.001) > 1e-6 {
		t.Errorf("Centroid = %+v, want about 17.001, 78.001", c)
	}
	// An L shape's centroid is pulled toward its heavy arm.
	l := geo.Ring{{0, 0}, {0, 0.002}, {0.001, 0.002}, {0.001, 0.001}, {0.002, 0.001}, {0.002, 0}}
	c = geo.Centroid(l)
	if !(c.Lat < 0.001 && c.Lng < 0.001) || !geo.Contains(l, c) {
		t.Errorf("L centroid = %+v, want inside the L, below and left of its corner", c)
	}
}

func TestSelfIntersections(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ring geo.Ring
		want bool // some crossing
	}{
		{"square", rect(17, 78, 0.001, 0.001), false},
		{"concave L", geo.Ring{{0, 0}, {0, 2e-3}, {1e-3, 2e-3}, {1e-3, 1e-3}, {2e-3, 1e-3}, {2e-3, 0}}, false},
		{"bow tie", geo.Ring{{0, 0}, {1e-3, 1e-3}, {1e-3, 0}, {0, 1e-3}}, true},
		{"corner touches an edge", geo.Ring{{0, 0}, {0, 2e-3}, {2e-3, 2e-3}, {0, 1e-3}, {2e-3, 0}}, true},
		{"doubles back", geo.Ring{{0, 0}, {0, 2e-3}, {0, 1e-3}, {1e-3, 1e-3}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := geo.SelfIntersections(tt.ring); (len(got) > 0) != tt.want {
				t.Errorf("SelfIntersections = %v, want some: %v", got, tt.want)
			}
		})
	}
}

func TestAngles(t *testing.T) {
	t.Parallel()
	for _, ring := range []geo.Ring{rect(10, 10, 0.001, 0.001), {{10, 10}, {10.001, 10}, {10.001, 10.001}, {10, 10.001}}} {
		for i, a := range geo.Angles(ring) {
			// A degree box is not a square in metres, but its corners stay right angles.
			if math.Abs(a-90) > 0.01 {
				t.Errorf("corner %d: angle %v, want 90", i, a)
			}
		}
	}
	l := geo.Ring{{0, 0}, {0, 2e-3}, {1e-3, 2e-3}, {1e-3, 1e-3}, {2e-3, 1e-3}, {2e-3, 0}}
	if a := geo.Angles(l)[3]; math.Abs(a-270) > 0.5 {
		t.Errorf("L's inner corner: angle %v, want 270", a)
	}
	// A square with a long thin spike out of its east side, as a GPS jump
	// makes.
	spike := geo.Ring{{0, 0}, {1e-3, 0}, {1e-3, 1e-3}, {0.5e-3 + 1e-6, 1e-3}, {0.5e-3, 0.02}, {0.5e-3 - 1e-6, 1e-3}, {0, 1e-3}}
	if c := geo.SelfIntersections(spike); len(c) != 0 {
		t.Fatalf("spike ring is not simple: %v", c)
	}
	if a := geo.Angles(spike)[4]; a > 1 {
		t.Errorf("spike tip: angle %v, want under 1°", a)
	}
}

func TestContainsAndOverlap(t *testing.T) {
	t.Parallel()
	a := rect(17, 78, 0.001, 0.001)
	if !geo.Contains(a, geo.Point{Lat: 17.0005, Lng: 78.0005}) || geo.Contains(a, geo.Point{Lat: 17.002, Lng: 78.0005}) {
		t.Error("Contains: wrong for a point inside or outside")
	}
	half := rect(17, 78.0005, 0.001, 0.001) // shares half of a
	got, want := geo.Overlap(a, half), geo.Area(a)/2
	if math.Abs(got-want)/want > 0.02 {
		t.Errorf("Overlap = %v, want about %v", got, want)
	}
	if got := geo.Overlap(a, rect(17, 78.001, 0.001, 0.001)); got != 0 {
		t.Errorf("neighbours sharing an edge: Overlap = %v, want 0", got)
	}
	if got := geo.Overlap(a, rect(18, 78, 0.001, 0.001)); got != 0 {
		t.Errorf("far apart: Overlap = %v, want 0", got)
	}
}

func TestPointValid(t *testing.T) {
	t.Parallel()
	for _, p := range []geo.Point{{91, 0}, {0, 181}, {math.NaN(), 0}, {0, math.Inf(1)}} {
		if p.Valid() {
			t.Errorf("%+v: Valid, want not", p)
		}
	}
	if !(geo.Point{Lat: -90, Lng: 180}).Valid() {
		t.Error("edge of range: not Valid")
	}
}
