package geo_test

import (
	"errors"
	"math"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/geo"
)

func TestStrips(t *testing.T) {
	t.Parallel()
	// A plot about 200 m east-west by 100 m north-south; a tilted one; an
	// L-shaped (concave) one; a triangle.
	plots := map[string]geo.Ring{
		"rectangle": rect(17.07, 78.20, 0.0009, 0.00188),
		"tilted":    {{17.0700, 78.2000}, {17.0715, 78.2012}, {17.0709, 78.2020}, {17.0694, 78.2008}},
		"L-shape":   {{17.07, 78.20}, {17.07, 78.202}, {17.0705, 78.202}, {17.0705, 78.201}, {17.071, 78.201}, {17.071, 78.20}},
		"triangle":  {{17.07, 78.20}, {17.07, 78.203}, {17.0715, 78.2005}},
	}
	for name, plot := range plots {
		for _, shares := range [][]float64{{1}, {0.6, 0.4}, {0.25, 0.25, 0.5}, {0.3, 0.2}} {
			strips, bearing, err := geo.Strips(plot, shares)
			if err != nil {
				t.Fatalf("%s %v: %v", name, shares, err)
			}
			if len(strips) != len(shares) || bearing < 0 || bearing >= 180 {
				t.Fatalf("%s %v: %d strips, bearing %v", name, shares, len(strips), bearing)
			}
			whole := geo.Area(plot)
			for i, s := range strips {
				got, want := geo.Area(s), shares[i]*whole
				if math.Abs(got-want) > 0.002*whole {
					t.Errorf("%s %v: strip %d is %.1f m², want %.1f", name, shares, i, got, want)
				}
				if inside := geo.Overlap(s, plot) / geo.Overlap(s, s); inside < 0.99 {
					t.Errorf("%s %v: strip %d is %.0f%% inside the plot", name, shares, i, inside*100)
				}
				for j := i + 1; j < len(strips); j++ {
					if o := geo.Overlap(s, strips[j]); o > 0.01*want {
						t.Errorf("%s %v: strips %d and %d overlap by %.1f m²", name, shares, i, j, o)
					}
				}
			}
		}
	}
	// The rectangle's strips run along its long side, east-west.
	if _, bearing, _ := geo.Strips(plots["rectangle"], []float64{0.5, 0.5}); math.Abs(bearing-90) > 1 {
		t.Errorf("rectangle strips bear %v°, want 90 (east-west)", bearing)
	}
}

func TestStripsRefuses(t *testing.T) {
	t.Parallel()
	plot := rect(17.07, 78.20, 0.001, 0.001)
	for _, shares := range [][]float64{nil, {0}, {-0.2, 0.5}, {0.7, 0.5}, {math.NaN()}} {
		if _, _, err := geo.Strips(plot, shares); !errors.Is(err, geo.ErrShares) {
			t.Errorf("%v: err %v, want ErrShares", shares, err)
		}
	}
	line := geo.Ring{{17.07, 78.20}, {17.071, 78.20}, {17.072, 78.20}}
	for _, r := range []geo.Ring{line, line[:2]} {
		if _, _, err := geo.Strips(r, []float64{1}); !errors.Is(err, geo.ErrShape) {
			t.Errorf("%d corners: err %v, want ErrShape", len(r), err)
		}
	}
}

func TestExtent(t *testing.T) {
	t.Parallel()
	plot := rect(17.07, 78.20, 0.0009, 0.00188) // ~100 m north-south, ~200 m east-west
	along, across := geo.Extent(plot, 0)
	if math.Abs(along-99.6) > 1 || math.Abs(across-200) > 2 {
		t.Errorf("north-south rows: %.1f m along, %.1f across", along, across)
	}
	along, across = geo.Extent(plot, 90)
	if math.Abs(along-200) > 2 || math.Abs(across-99.6) > 1 {
		t.Errorf("east-west rows: %.1f m along, %.1f across", along, across)
	}
}
