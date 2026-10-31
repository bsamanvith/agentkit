package farm_test

import (
	"context"
	"errors"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farm/farmtest"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

var t0 = time.Date(2026, 10, 31, 6, 0, 0, 0, time.UTC)

// Degrees per metre near Hyderabad (17.385°N).
const (
	latPerM = 1 / 110_712.0
	lngPerM = 1 / 106_216.0
)

// plot returns the corners of a north-up rectangle of about w by h metres
// whose south-west corner is at lat, lng.
func plot(lat, lng, w, h float64) []farm.Corner {
	return corners(geo.Point{Lat: lat, Lng: lng}, geo.Point{Lat: lat, Lng: lng + w*lngPerM},
		geo.Point{Lat: lat + h*latPerM, Lng: lng + w*lngPerM}, geo.Point{Lat: lat + h*latPerM, Lng: lng})
}

func corners(ps ...geo.Point) []farm.Corner {
	out := make([]farm.Corner, len(ps))
	for i, p := range ps {
		out[i] = farm.Corner{Point: p}
	}
	return out
}

// acre is a square of one acre (63.6 m a side) in Hyderabad.
func acre() []farm.Corner { return plot(17.385, 78.4867, 63.6149, 63.6149) }

var india = farm.Location{Village: "Nampally", District: "Hyderabad", State: "Telangana", Country: "India", CountryCode: "IN"}

func verifier(opts ...farm.VerifierOption) *farm.Verifier {
	return farm.NewVerifier(append([]farm.VerifierOption{farm.WithClock(clocktest.NewFake(t0))}, opts...)...)
}

// ids returns the IDs of a report's checks of a severity.
func ids(r farm.Report, s farm.Severity) []string {
	var out []string
	for _, c := range r.Checks {
		if c.Severity == s {
			out = append(out, c.ID)
		}
	}
	return out
}

func check(r farm.Report, id string) (farm.Check, bool) {
	i := slices.IndexFunc(r.Checks, func(c farm.Check) bool { return c.ID == id })
	if i < 0 {
		return farm.Check{}, false
	}
	return r.Checks[i], true
}

func TestVerifyGoodPlot(t *testing.T) {
	t.Parallel()
	loc := &farmtest.Locator{Location: india}
	v := verifier(farm.WithLocator(loc), farm.WithCountry("in"))
	rep := v.Verify(context.Background(), farm.Request{
		Boundary: farm.Boundary{Corners: acre(), Source: farm.SourceMap},
		Declared: &farm.Area{Value: 40, Unit: farm.Guntas},
	})
	if rep.Status != farm.StatusVerified {
		t.Fatalf("Status = %s, want verified; checks %+v", rep.Status, rep.Checks)
	}
	m := rep.Measures
	if m == nil || math.Abs(m.Acres-1) > 0.005 || math.Abs(m.Guntas-40) > 0.2 || math.Abs(m.Cents-100) > 0.5 ||
		math.Abs(m.Hectares-0.40469) > 0.002 || m.AcresText != "1 acre" || m.Corners != 4 {
		t.Errorf("Measures = %+v, want about 1 acre", m)
	}
	if m != nil && math.Abs(m.PerimeterM-4*63.6149) > 1 {
		t.Errorf("PerimeterM = %v, want about %v", m.PerimeterM, 4*63.6149)
	}
	if diff := cmp.Diff(&india, rep.Location); diff != "" {
		t.Errorf("Location (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{farm.CheckDeclared, farm.CheckLocation}, ids(rep, farm.SeverityInfo)); diff != "" {
		t.Errorf("info checks (-want +got):\n%s", diff)
	}
	if !rep.CheckedAt.Equal(t0) {
		t.Errorf("CheckedAt = %v, want %v", rep.CheckedAt, t0)
	}
}

func TestVerifyInvalidShapes(t *testing.T) {
	t.Parallel()
	bowTie := corners(geo.Point{Lat: 17, Lng: 78}, geo.Point{Lat: 17.001, Lng: 78.001}, geo.Point{Lat: 17.001, Lng: 78}, geo.Point{Lat: 17, Lng: 78.001})
	many := make([]farm.Corner, farm.MaxCorners+1)
	for i := range many {
		a := 2 * math.Pi * float64(i) / float64(len(many))
		many[i] = farm.Corner{Point: geo.Point{Lat: 17 + 0.01*math.Sin(a), Lng: 78 + 0.01*math.Cos(a)}}
	}
	tests := []struct {
		name     string
		corners  []farm.Corner
		want     string
		measured bool // the report still has measures
	}{
		{"no corners", nil, farm.CheckCorners, false},
		{"two corners", acre()[:2], farm.CheckCorners, false},
		{"three corners, two the same", append(acre()[:2], acre()[1]), farm.CheckCorners, false},
		{"latitude out of range", corners(geo.Point{Lat: 97, Lng: 78}, geo.Point{Lat: 17, Lng: 78.001}, geo.Point{Lat: 17.001, Lng: 78}), farm.CheckRange, false},
		{"not a number", corners(geo.Point{Lat: math.NaN(), Lng: 78}, geo.Point{Lat: 17, Lng: 78.001}, geo.Point{Lat: 17.001, Lng: 78}), farm.CheckRange, false},
		{"bow tie", bowTie, farm.CheckCrossing, false},
		{"too many corners", many, farm.CheckTooMany, false},
		{"a few square metres", plot(17, 78, 5, 5), farm.CheckTiny, true},
		{"a whole district", plot(17, 78, 30000, 30000), farm.CheckHuge, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			loc := &farmtest.Locator{Location: india}
			rep := verifier(farm.WithLocator(loc)).Verify(context.Background(), farm.Request{Boundary: farm.Boundary{Corners: tt.corners}})
			if rep.Status != farm.StatusInvalid {
				t.Errorf("Status = %s, want invalid", rep.Status)
			}
			if diff := cmp.Diff([]string{tt.want}, ids(rep, farm.SeverityError)); diff != "" {
				t.Errorf("error checks (-want +got):\n%s", diff)
			}
			if (rep.Measures != nil) != tt.measured {
				t.Errorf("Measures = %+v, want present: %v", rep.Measures, tt.measured)
			}
			if loc.Calls() != 0 {
				t.Errorf("an invalid plot was looked up %d times", loc.Calls())
			}
		})
	}
}

func TestVerifyCrossingNamesTheCorners(t *testing.T) {
	t.Parallel()
	bowTie := corners(geo.Point{Lat: 17, Lng: 78}, geo.Point{Lat: 17.001, Lng: 78.001}, geo.Point{Lat: 17.001, Lng: 78}, geo.Point{Lat: 17, Lng: 78.001})
	rep := verifier().Verify(context.Background(), farm.Request{Boundary: farm.Boundary{Corners: bowTie}})
	c, ok := check(rep, farm.CheckCrossing)
	if !ok {
		t.Fatal("no crossing check")
	}
	slices.Sort(c.Corners)
	if diff := cmp.Diff([]int{0, 1, 2, 3}, c.Corners); diff != "" {
		t.Errorf("crossing corners (-want +got):\n%s", diff)
	}
}

func TestVerifyDeclaredArea(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		declared farm.Area
		want     farm.Severity
	}{
		{"exact in acres", farm.Area{Value: 1, Unit: farm.Acres}, farm.SeverityInfo},
		{"within 10% in cents", farm.Area{Value: 92, Unit: farm.Cents}, farm.SeverityInfo},
		{"20% off", farm.Area{Value: 1.25, Unit: farm.Acres}, farm.SeverityWarning},
		{"the wrong unit", farm.Area{Value: 1, Unit: farm.Hectares}, farm.SeverityWarning},
		{"zero", farm.Area{Value: 0, Unit: farm.Acres}, farm.SeverityError},
		{"unknown unit", farm.Area{Value: 1, Unit: "bigha"}, farm.SeverityError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rep := verifier().Verify(context.Background(), farm.Request{Boundary: farm.Boundary{Corners: acre()}, Declared: &tt.declared})
			c, ok := check(rep, farm.CheckDeclared)
			if !ok || c.Severity != tt.want {
				t.Errorf("declared check = %+v (found %v), want severity %s", c, ok, tt.want)
			}
		})
	}
	rep := verifier().Verify(context.Background(), farm.Request{Boundary: farm.Boundary{Corners: acre()}})
	if _, ok := check(rep, farm.CheckDeclared); ok || rep.Status != farm.StatusVerified {
		t.Errorf("no declared size: %+v, want verified with no declared check", rep)
	}
}

func TestVerifyWarnings(t *testing.T) {
	t.Parallel()
	sq := acre()
	// A spike: a corner 300 m east of the plot's east side, 1 m wide.
	mid := (sq[1].Lat + sq[2].Lat) / 2
	spike := slices.Concat(sq[:2], corners(
		geo.Point{Lat: mid - 0.5*latPerM, Lng: sq[1].Lng},
		geo.Point{Lat: mid, Lng: sq[1].Lng + 300*lngPerM},
		geo.Point{Lat: mid + 0.5*latPerM, Lng: sq[1].Lng}), sq[2:])
	rough := acre()
	rough[2].AccuracyM = 25
	tests := []struct {
		name    string
		corners []farm.Corner
		want    string
		at      []int // the corners the check names, if any
	}{
		{"spike", spike, farm.CheckSpike, []int{3}},
		{"strip", plot(17, 78, 2000, 20), farm.CheckSliver, nil},
		{"very large", plot(17, 78, 4000, 4000), farm.CheckHuge, nil},
		{"rough GPS fix", rough, farm.CheckGPS, []int{2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rep := verifier().Verify(context.Background(), farm.Request{Boundary: farm.Boundary{Corners: tt.corners}})
			if rep.Status != farm.StatusNeedsReview {
				t.Errorf("Status = %s, want needs_review; checks %+v", rep.Status, rep.Checks)
			}
			if diff := cmp.Diff([]string{tt.want}, ids(rep, farm.SeverityWarning)); diff != "" {
				t.Errorf("warnings (-want +got):\n%s", diff)
			}
			if c, _ := check(rep, tt.want); !cmp.Equal(c.Corners, tt.at) {
				t.Errorf("check corners = %v, want %v", c.Corners, tt.at)
			}
		})
	}
}

func TestVerifyMergesRepeatedCorners(t *testing.T) {
	t.Parallel()
	sq := acre()
	want := verifier().Verify(context.Background(), farm.Request{Boundary: farm.Boundary{Corners: sq}}).Measures.AreaM2

	closed := append(slices.Clone(sq), sq[0])
	rep := verifier().Verify(context.Background(), farm.Request{Boundary: farm.Boundary{Corners: closed}})
	if rep.Status != farm.StatusVerified || rep.Measures.Corners != 4 {
		t.Errorf("closed ring: %s with %d corners, want verified with 4; checks %+v", rep.Status, rep.Measures.Corners, rep.Checks)
	}

	twice := slices.Concat(sq[:2], []farm.Corner{{Point: geo.Point{Lat: sq[1].Lat + 0.1*latPerM, Lng: sq[1].Lng}}}, sq[2:])
	rep = verifier().Verify(context.Background(), farm.Request{Boundary: farm.Boundary{Corners: twice}})
	c, ok := check(rep, farm.CheckDuplicate)
	if !ok || c.Severity != farm.SeverityWarning || !cmp.Equal(c.Corners, []int{2}) {
		t.Errorf("duplicate check = %+v, want a warning on corner 2", c)
	}
	if math.Abs(rep.Measures.AreaM2-want) > 1 {
		t.Errorf("area with a repeated corner = %v, want %v", rep.Measures.AreaM2, want)
	}
}

func TestVerifyOverlaps(t *testing.T) {
	t.Parallel()
	mine := farm.Farm{Name: "Mango orchard", Boundary: farm.Boundary{Corners: plot(17.385, 78.4867, 100, 100)}}
	tests := []struct {
		name    string
		corners []farm.Corner
		want    bool
	}{
		{"half over it", plot(17.385, 78.4867+50*lngPerM, 100, 100), true},
		{"inside it", plot(17.385+10*latPerM, 78.4867+10*lngPerM, 50, 50), true},
		{"next to it", plot(17.385, 78.4867+100*lngPerM, 100, 100), false},
		{"far away", plot(18, 79, 100, 100), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rep := verifier().Verify(context.Background(), farm.Request{Boundary: farm.Boundary{Corners: tt.corners}, Others: []farm.Farm{mine}})
			if _, got := check(rep, farm.CheckOverlap); got != tt.want {
				t.Errorf("overlap check: %v, want %v; checks %+v", got, tt.want, rep.Checks)
			}
		})
	}
}

// blocking is a locator that answers only when its context ends.
type blocking struct{}

func (blocking) Locate(ctx context.Context, _ geo.Point) (farm.Location, error) {
	<-ctx.Done()
	return farm.Location{}, ctx.Err()
}

func TestVerifyLocation(t *testing.T) {
	t.Parallel()
	nepal := farm.Location{Village: "Bharatpur", Country: "Nepal", CountryCode: "NP"}
	tests := []struct {
		name    string
		locator farm.Locator
		want    []string
		status  farm.Status
	}{
		{"in India", &farmtest.Locator{Location: india}, []string{farm.CheckLocation}, farm.StatusVerified},
		{"in the sea", &farmtest.Locator{Err: farm.ErrNoLocation}, []string{farm.CheckNoLocation}, farm.StatusNeedsReview},
		{"another country", &farmtest.Locator{Location: nepal}, []string{farm.CheckLocation, farm.CheckCountry}, farm.StatusNeedsReview},
		{"lookup down", &farmtest.Locator{Err: errors.New("503")}, []string{farm.CheckLocateFailed}, farm.StatusVerified},
		{"lookup too slow", blocking{}, []string{farm.CheckLocateFailed}, farm.StatusVerified},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v := verifier(farm.WithLocator(tt.locator), farm.WithCountry("IN"), farm.WithLocateTimeout(10*time.Millisecond))
			rep := v.Verify(context.Background(), farm.Request{Boundary: farm.Boundary{Corners: acre()}})
			var got []string
			for _, c := range rep.Checks {
				got = append(got, c.ID)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("checks (-want +got):\n%s", diff)
			}
			if rep.Status != tt.status {
				t.Errorf("Status = %s, want %s", rep.Status, tt.status)
			}
		})
	}
}

func FuzzVerify(f *testing.F) {
	f.Add(17.385, 78.4867, 17.386, 78.4867, 17.386, 78.4877, 17.385, 78.4877, 4.0)
	f.Add(0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0)
	f.Add(89.9, 179.9, -89.9, -179.9, 0.0, 180.0, 90.0, 0.0, -1.0)
	f.Fuzz(func(t *testing.T, a, b, c, d, e, g, h, i, acc float64) {
		cs := corners(geo.Point{Lat: a, Lng: b}, geo.Point{Lat: c, Lng: d}, geo.Point{Lat: e, Lng: g}, geo.Point{Lat: h, Lng: i})
		cs[1].AccuracyM = acc
		rep := verifier().Verify(context.Background(), farm.Request{Boundary: farm.Boundary{Corners: cs}, Declared: &farm.Area{Value: 1, Unit: farm.Acres}})
		switch rep.Status {
		case farm.StatusVerified, farm.StatusNeedsReview:
			if rep.Measures == nil || !(rep.Measures.AreaM2 >= farm.MinAreaM2) || hasError(rep) {
				t.Fatalf("usable report without a sane area: %+v", rep)
			}
		case farm.StatusInvalid:
			if !hasError(rep) {
				t.Fatalf("invalid report with no error: %+v", rep)
			}
		default:
			t.Fatalf("Status = %q", rep.Status)
		}
	})
}

func hasError(r farm.Report) bool { return len(ids(r, farm.SeverityError)) > 0 }

func TestAcresAndGuntas(t *testing.T) {
	t.Parallel()
	g := 4046.8564224 / 40
	for m2, want := range map[float64]string{
		4046.8564224: "1 acre", 2*4046.8564224 + 15*g: "2 acres 15 guntas", g: "1 gunta",
		39.6 * g: "1 acre", 3 * g: "3 guntas", 4046.8564224 + g: "1 acre 1 gunta",
	} {
		if got := farm.AcresAndGuntas(m2); got != want {
			t.Errorf("AcresAndGuntas(%v) = %q, want %q", m2, got, want)
		}
	}
}
