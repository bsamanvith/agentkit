package farm

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/pkg/clock"
)

// Status sums up a [Report].
type Status string

// A report's statuses.
const (
	// StatusVerified: every check passed.
	StatusVerified Status = "verified"
	// StatusNeedsReview: the boundary is usable, with warnings the farmer
	// should look at.
	StatusNeedsReview Status = "needs_review"
	// StatusInvalid: the boundary cannot be used.
	StatusInvalid Status = "invalid"
)

// Severity is how much a [Check] matters.
type Severity string

// Check severities.
const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// Check is one finding about a boundary. ID names the check (such as
// "shape.crossing"), for code and tests; Message is for people. Corners
// lists the corners it is about, by index, if any.
type Check struct {
	ID       string   `json:"id"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	Corners  []int    `json:"corners,omitempty"`
}

// The checks' IDs.
const (
	CheckCorners      = "corners.count"     // fewer than three distinct corners
	CheckRange        = "corners.range"     // a coordinate out of range
	CheckTooMany      = "corners.max"       // more than MaxCorners
	CheckDuplicate    = "corners.duplicate" // corners on top of each other, merged
	CheckCrossing     = "shape.crossing"    // edges cross or touch
	CheckSpike        = "shape.spike"       // a needle-thin corner
	CheckSliver       = "shape.sliver"      // a long thin strip
	CheckTiny         = "area.tiny"         // too small to be a plot
	CheckHuge         = "area.huge"         // larger than a farm is likely to be
	CheckDeclared     = "area.declared"     // measured against the declared size
	CheckGPS          = "gps.accuracy"      // GPS fixes too rough for the plot
	CheckLocation     = "location.found"    // where the plot is
	CheckNoLocation   = "location.none"     // the plot is nowhere on land
	CheckCountry      = "location.country"  // outside the expected country
	CheckLocateFailed = "location.failed"   // the lookup failed: not checked
	CheckOverlap      = "overlap.farm"      // overlaps another of the farms
)

// Limits of the checks.
const (
	// MaxCorners bounds a boundary's corners: a walked boundary with a fix
	// every few metres around a large plot stays well under it.
	MaxCorners = 2000
	// DuplicateM is how close two corners in a row may be before they are
	// taken for one: closer than a phone's GPS can tell apart.
	DuplicateM = 0.5
	// MinAreaM2 is the smallest plot: about the floor of a room.
	MinAreaM2 = 50
	// LargeAreaM2 is where a plot gets a warning (1,000 ha).
	LargeAreaM2 = 1000 * 10000
	// MaxAreaM2 is the largest plot (50,000 ha), past any farm and past
	// where the area is accurate.
	MaxAreaM2 = 50000 * 10000
	// SpikeDegrees: a corner sharper than this, or a notch deeper, is
	// likely a GPS jump or a mistyped coordinate.
	SpikeDegrees = 10
	// SliverRatio is the compactness (4πA/P², 1 for a circle, 0.79 for a
	// square) under which a plot is a strip; 0.05 is about 1:65.
	SliverRatio = 0.05
)

// Measures are a boundary's sizes.
type Measures struct {
	AreaM2     float64   `json:"area_m2"`
	Hectares   float64   `json:"hectares"`
	Acres      float64   `json:"acres"`
	Guntas     float64   `json:"guntas"`
	Cents      float64   `json:"cents"`
	AcresText  string    `json:"acres_text"` // "2 acres 15 guntas"
	PerimeterM float64   `json:"perimeter_m"`
	Centroid   geo.Point `json:"centroid"`
	Corners    int       `json:"corners"` // distinct corners measured
}

// Location is where a point is, from a [Locator].
type Location struct {
	Village     string `json:"village,omitempty"`
	SubDistrict string `json:"sub_district,omitempty"` // mandal, taluk, tehsil
	District    string `json:"district,omitempty"`
	State       string `json:"state,omitempty"`
	Country     string `json:"country,omitempty"`
	CountryCode string `json:"country_code,omitempty"` // ISO 3166-1 alpha-2, upper case
	DisplayName string `json:"display_name,omitempty"`
}

// Report is the verification of a boundary.
type Report struct {
	Status    Status    `json:"status"`
	Measures  *Measures `json:"measures,omitempty"` // nil when the shape is invalid
	Location  *Location `json:"location,omitempty"` // nil when not looked up or not found
	Checks    []Check   `json:"checks"`
	CheckedAt time.Time `json:"checked_at"`
}

// ErrNoLocation is what a [Locator] returns for a point that is nowhere it
// knows of, such as the open sea.
var ErrNoLocation = errors.New("farm: no location at this point")

// Locator finds where a point is (reverse geocoding).
type Locator interface {
	// Locate returns the location of p, or ErrNoLocation.
	Locate(ctx context.Context, p geo.Point) (Location, error)
}

// Verifier checks boundaries. The zero value is not usable: use
// [NewVerifier].
type Verifier struct {
	locator Locator
	country string
	timeout time.Duration
	clock   clock.Clock
}

// VerifierOption configures [NewVerifier].
type VerifierOption func(*Verifier)

// WithLocator looks each plot up with l; without it, locations are not
// checked.
func WithLocator(l Locator) VerifierOption { return func(v *Verifier) { v.locator = l } }

// WithCountry expects plots in the country with this ISO 3166-1 alpha-2
// code ("IN"); a plot elsewhere gets a warning.
func WithCountry(code string) VerifierOption {
	return func(v *Verifier) { v.country = strings.ToUpper(code) }
}

// WithLocateTimeout bounds a lookup (default 5s); a lookup that takes
// longer is reported as not checked.
func WithLocateTimeout(d time.Duration) VerifierOption {
	return func(v *Verifier) { v.timeout = d }
}

// WithClock sets the clock that stamps reports (default the machine's).
func WithClock(c clock.Clock) VerifierOption { return func(v *Verifier) { v.clock = c } }

// NewVerifier returns a Verifier.
func NewVerifier(opts ...VerifierOption) *Verifier {
	v := &Verifier{timeout: 5 * time.Second, clock: clock.Real()}
	for _, o := range opts {
		o(v)
	}
	return v
}

// Request is what [Verifier.Verify] checks.
type Request struct {
	Boundary Boundary
	// Declared is the size the farmer declared, if any.
	Declared *Area
	// Others are the farmer's other plots, checked for overlaps.
	Others []Farm
}

// Verify checks a boundary. It never fails: what is wrong is in the
// report's checks. A declared area with an unknown unit is an error check.
func (v *Verifier) Verify(ctx context.Context, req Request) Report {
	rep := Report{CheckedAt: v.clock.Now().UTC(), Checks: []Check{}}
	v.verify(ctx, req, &rep)
	rep.Status = StatusVerified
	for _, c := range rep.Checks {
		switch c.Severity {
		case SeverityError:
			rep.Status = StatusInvalid
		case SeverityWarning:
			if rep.Status != StatusInvalid {
				rep.Status = StatusNeedsReview
			}
		}
	}
	return rep
}

func (v *Verifier) verify(ctx context.Context, req Request, rep *Report) {
	add := func(id string, s Severity, corners []int, format string, args ...any) {
		rep.Checks = append(rep.Checks, Check{ID: id, Severity: s, Message: fmt.Sprintf(format, args...), Corners: corners})
	}
	cs := req.Boundary.Corners
	if len(cs) > MaxCorners {
		add(CheckTooMany, SeverityError, nil, "%d corners: at most %d", len(cs), MaxCorners)
		return
	}
	var bad []int
	for i, c := range cs {
		if !c.Valid() {
			bad = append(bad, i)
		}
	}
	if len(bad) > 0 {
		add(CheckRange, SeverityError, bad, "%s out of range: latitude must be within ±90 and longitude within ±180", plural(len(bad), "corner is", "corners are"))
		return
	}

	// Merge corners on top of each other, keeping the first of each run;
	// idx maps the merged ring back to the farmer's corners.
	var ring geo.Ring
	var idx []int
	var dups []int
	for i, c := range cs {
		if len(ring) > 0 && geo.Distance(ring[len(ring)-1], c.Point) < DuplicateM {
			dups = append(dups, i)
			continue
		}
		ring = append(ring, c.Point)
		idx = append(idx, i)
	}
	if len(ring) > 1 && geo.Distance(ring[0], ring[len(ring)-1]) < DuplicateM {
		// The last corner repeats the first: a closed ring, which is fine,
		// unless it is a near miss rather than the same point.
		if ring[0] != ring[len(ring)-1] {
			dups = append(dups, idx[len(idx)-1])
		}
		ring, idx = ring[:len(ring)-1], idx[:len(idx)-1]
	}
	if len(dups) > 0 {
		add(CheckDuplicate, SeverityWarning, dups, "%s within %.1f m of the one before and %s merged with it", plural(len(dups), "corner is", "corners are"), DuplicateM, pick(len(dups), "was", "were"))
	}
	if len(ring) < 3 {
		add(CheckCorners, SeverityError, nil, "%s: a plot needs at least 3", plural(len(ring), "distinct corner", "distinct corners"))
		return
	}
	if xs := geo.SelfIntersections(ring); len(xs) > 0 {
		var corners []int
		for _, x := range xs {
			corners = append(corners, idx[x.A], idx[(x.A+1)%len(idx)], idx[x.B], idx[(x.B+1)%len(idx)])
		}
		add(CheckCrossing, SeverityError, unique(corners), "the boundary crosses itself: put the corners in order around the plot")
		return
	}

	area := geo.Area(ring)
	perim := geo.Perimeter(ring)
	m := &Measures{
		AreaM2: area, Hectares: area / 10000, PerimeterM: perim,
		Centroid: geo.Centroid(ring), Corners: len(ring), AcresText: AcresAndGuntas(area),
	}
	m.Acres, _ = Convert(area, Acres)
	m.Guntas, _ = Convert(area, Guntas)
	m.Cents, _ = Convert(area, Cents)
	rep.Measures = m

	switch {
	case area < MinAreaM2:
		add(CheckTiny, SeverityError, nil, "%.0f m² is too small for a plot (at least %d m²): are the corners right?", area, MinAreaM2)
		return
	case area > MaxAreaM2:
		add(CheckHuge, SeverityError, nil, "%.0f ha is too large for one plot (at most %d ha)", m.Hectares, MaxAreaM2/10000)
		return
	case area > LargeAreaM2:
		add(CheckHuge, SeverityWarning, nil, "%.0f ha is unusually large for one plot: check the corners", m.Hectares)
	}

	var spikes []int
	for i, a := range geo.Angles(ring) {
		if a < SpikeDegrees || a > 360-SpikeDegrees {
			spikes = append(spikes, idx[i])
		}
	}
	if len(spikes) > 0 {
		add(CheckSpike, SeverityWarning, spikes, "%s a needle-thin point, often a GPS jump or a mistyped coordinate", plural(len(spikes), "corner makes", "corners make"))
	}
	if c := 4 * math.Pi * area / (perim * perim); c < SliverRatio {
		add(CheckSliver, SeverityWarning, nil, "the plot is a long thin strip (compactness %.3f): check that no corner is missing", c)
	}

	v.checkDeclared(req.Declared, area, add)
	checkGPS(cs, area, add)
	v.checkOverlaps(ring, req.Others, add)
	if !hasErrors(rep.Checks) {
		v.locate(ctx, m.Centroid, rep, add)
	}
}

type addFunc func(id string, s Severity, corners []int, format string, args ...any)

func (v *Verifier) checkDeclared(d *Area, area float64, add addFunc) {
	if d == nil {
		return
	}
	want, err := d.SquareMetres()
	if err != nil || want == 0 {
		add(CheckDeclared, SeverityError, nil, "the declared size %v %s is not a size", d.Value, d.Unit)
		return
	}
	got, _ := Convert(area, d.Unit)
	diff := (area - want) / want
	pct := math.Abs(diff) * 100
	more := "larger"
	if diff < 0 {
		more = "smaller"
	}
	switch {
	case pct <= 10:
		add(CheckDeclared, SeverityInfo, nil, "matches the declared %s (measured %s)", d.Unit.Format(d.Value), d.Unit.Format(got))
	default:
		msg := fmt.Sprintf("measured %s, %.0f%% %s than the declared %s", d.Unit.Format(got), pct, more, d.Unit.Format(d.Value))
		// A size that fits well in another unit is likely the wrong unit.
		for _, u := range Units() {
			if u == d.Unit {
				continue
			}
			s, _ := u.SquareMetres()
			if r := area / (d.Value * s); r > 0.9 && r < 1.1 {
				msg += fmt.Sprintf(": did you mean %s?", u.Format(d.Value))
				break
			}
		}
		if pct > 25 {
			msg += " Check the corners, or the declared size and its unit."
		}
		add(CheckDeclared, SeverityWarning, nil, "%s", msg)
	}
}

// checkGPS flags fixes whose accuracy is coarse for the plot: worse than a
// tenth of its side, and worse than 10 m.
func checkGPS(cs []Corner, area float64, add addFunc) {
	limit := math.Max(10, math.Sqrt(area)/10)
	var rough []int
	for i, c := range cs {
		if c.AccuracyM > limit {
			rough = append(rough, i)
		}
	}
	if len(rough) > 0 {
		add(CheckGPS, SeverityWarning, rough, "%s a GPS fix worse than %.0f m: wait for a better fix and record %s again", plural(len(rough), "corner has", "corners have"), limit, pick(len(rough), "it", "them"))
	}
}

func (v *Verifier) checkOverlaps(ring geo.Ring, others []Farm, add addFunc) {
	area := geo.Area(ring)
	for _, o := range others {
		r := o.Boundary.Ring()
		shared := geo.Overlap(ring, r)
		if shared > 0.01*math.Min(area, geo.Area(r)) {
			add(CheckOverlap, SeverityWarning, nil, "overlaps your plot %q by about %s (%.0f m²): the shared land would be counted twice", o.Name, AcresAndGuntas(shared), shared)
		}
	}
}

func (v *Verifier) locate(ctx context.Context, p geo.Point, rep *Report, add addFunc) {
	if v.locator == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()
	loc, err := v.locator.Locate(ctx, p)
	switch {
	case errors.Is(err, ErrNoLocation):
		add(CheckNoLocation, SeverityWarning, nil, "the plot's centre is not on any known land (the sea?): check the coordinates")
		return
	case err != nil:
		add(CheckLocateFailed, SeverityInfo, nil, "could not look up where the plot is: not checked")
		return
	}
	rep.Location = &loc
	add(CheckLocation, SeverityInfo, nil, "in %s", place(loc))
	if v.country != "" && loc.CountryCode != "" && !strings.EqualFold(loc.CountryCode, v.country) {
		add(CheckCountry, SeverityWarning, nil, "the plot is in %s, not %s: are latitude and longitude swapped?", loc.Country, v.country)
	}
}

func place(l Location) string {
	var parts []string
	for _, s := range []string{l.Village, l.SubDistrict, l.District, l.State, l.Country} {
		if s != "" && (len(parts) == 0 || parts[len(parts)-1] != s) {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return l.DisplayName
	}
	return strings.Join(parts, ", ")
}

func hasErrors(cs []Check) bool {
	for _, c := range cs {
		if c.Severity == SeverityError {
			return true
		}
	}
	return false
}

// pick returns one for n == 1, else many.
func pick(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// plural returns n and the word for it: "1 corner", "3 corners".
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// num formats an area for people: two decimals at most, no trailing zeros.
func num(f float64) string {
	s := fmt.Sprintf("%.2f", f)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	return s
}

func unique(xs []int) []int {
	seen := map[int]bool{}
	var out []int
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
