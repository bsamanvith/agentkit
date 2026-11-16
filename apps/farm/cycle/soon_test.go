package cycle_test

import (
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/agro/agrotest"
	"github.com/bsamanvith/agentkit/apps/farm/cycle"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
)

func plotRing() geo.Ring {
	var r geo.Ring
	for _, c := range field {
		r = append(r, c.Point)
	}
	return append(r, field[0].Point)
}

var borewell = []agro.Water{{Kind: agro.Borewell, Reliable: true}}

// current computes the current cycle alone at the Hyderabad plot on today,
// on black soil (soil "" for unknown) with water.
func current(t *testing.T, today time.Time, soil string, water []agro.Water) (cycle.Line, agro.Request) {
	t.Helper()
	lib, err := agro.Default()
	if err != nil {
		t.Fatal(err)
	}
	clim := agrotest.Hyderabad()
	in := cycle.Input{
		Library: lib, Farm: "f", Ring: plotRing(), Climate: &clim, Profile: agro.Profile{Soil: soil, Water: water},
		Today: today, Record: cycle.Record{FutureOff: true},
	}
	if soil != "" {
		s, err := lib.Soil(soil)
		if err != nil {
			t.Fatal(err)
		}
		in.Soil = &s
	}
	l, err := cycle.Compute(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Cycles) != 1 || l.Cycles[0].Suggested == nil {
		t.Fatalf("%+v; want a suggestion", l.Cycles)
	}
	return l, agro.Request{Climate: clim, Soil: in.Soil, Profile: in.Profile, Today: today}
}

// suits is the first sowing date the recommender tries (a week apart from
// today) on which any annual crop scores SoonScore or more; zero when none
// does in a year.
func suits(t *testing.T, r agro.Request) time.Time {
	t.Helper()
	lib, _ := agro.Default()
	r.Calendar = true
	for _, c := range lib.Crops() {
		if !c.Perennial && !c.ClimateAnalog {
			r.Crops = append(r.Crops, c.ID)
		}
	}
	res, err := agro.Recommend(lib, r)
	if err != nil {
		t.Fatal(err)
	}
	var first string
	for _, o := range res.Options {
		for _, s := range o.Calendar {
			if s.Score >= cycle.SoonScore && (first == "" || s.Sow < first) {
				first = s.Sow
			}
		}
	}
	d, _ := time.Parse(agro.DateLayout, first)
	return d
}

func daysAfter(t *testing.T, from time.Time, day string) int {
	t.Helper()
	d, err := time.Parse(agro.DateLayout, day)
	if err != nil {
		t.Fatal(err)
	}
	return int(d.Sub(from).Hours() / 24)
}

// A crop that suits sowing within SoonDays is this cycle, ahead of a
// better one months later, and says why.
func TestSuggestionPrefersASowingSoon(t *testing.T) {
	t.Parallel()
	// Mid-April, rainfed: black gram after the June rains scores best, but
	// something suits sowing before the monsoon.
	today := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	l, r := current(t, today, "black", nil)
	p := l.Cycles[0]
	s := p.Suggested
	if wait := daysAfter(t, today, s.Sow); wait < 0 || wait > cycle.SoonDays {
		t.Fatalf("suggested %s sown %s, %d days away; want within %d", s.MainCrop(), s.Sow, wait, cycle.SoonDays)
	}
	lib, _ := agro.Default()
	r.Calendar, r.Crops = true, []string{s.MainCrop()}
	res, err := agro.Recommend(lib, r)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res.Options[0].Calendar {
		if c.Sow == s.Sow && c.Score < cycle.SoonScore {
			t.Errorf("%s on %s scores %d; a sowing soon must score %d or more", s.MainCrop(), s.Sow, c.Score, cycle.SoonScore)
		}
	}
	if p.Rest != nil {
		t.Errorf("rest %v; want none when something can be sown soon", p.Rest)
	}
	if len(s.Reasons) == 0 || s.Reasons[0].ID != "sow.soon" || !strings.Contains(s.Reasons[0].Message, "June") {
		t.Errorf("reasons %v; want first why it beats the better crop that waits until June", s.Reasons)
	}
	// With water, the same day: a sowing now, no rest.
	l, _ = current(t, today, "black", borewell)
	if wait := daysAfter(t, today, l.Cycles[0].Suggested.Sow); wait < cycle.ReadyDays || wait > cycle.SoonDays || l.Cycles[0].Rest != nil {
		t.Errorf("irrigated: sown %d days away, rest %v; want soon, with %d days to get ready, and no rest", wait, l.Cycles[0].Rest, cycle.ReadyDays)
	}
}

// When nothing suits sowing soon, the line says so plainly, names the
// month from which something does, and still suggests the next season.
func TestNothingSoonTheLandRests(t *testing.T) {
	t.Parallel()
	// Mid-December, rainfed: too late for rabi on what the monsoon left.
	today := time.Date(2026, 12, 15, 0, 0, 0, 0, time.UTC)
	l, r := current(t, today, "black", nil)
	p := l.Cycles[0]
	first := suits(t, r)
	if first.IsZero() || daysAfter(t, today, first.Format(agro.DateLayout)) <= cycle.SoonDays {
		t.Fatalf("the first good sowing is %v: the case needs none within %d days", first, cycle.SoonDays)
	}
	if p.Rest == nil {
		t.Fatalf("no rest note; want one: nothing suits before %s", first.Format("January"))
	}
	want := "Nothing suits sowing on rainfed land before " + first.Format("January") + "."
	if p.Rest.Message != want {
		t.Errorf("rest %q; want %q", p.Rest.Message, want)
	}
	if wait := daysAfter(t, today, p.Suggested.Sow); wait <= cycle.SoonDays || wait > cycle.MaxWait {
		t.Errorf("suggested sowing %s, %d days away; want the next season, after %d days", p.Suggested.Sow, wait, cycle.SoonDays)
	}
	// Irrigation changes the answer completely in rabi.
	l, _ = current(t, today, "black", borewell)
	if wait := daysAfter(t, today, l.Cycles[0].Suggested.Sow); wait < cycle.ReadyDays || wait > cycle.SoonDays || l.Cycles[0].Rest != nil {
		t.Errorf("with a borewell: sown %d days away, rest %v; want a sowing soon, after the time to get ready", wait, l.Cycles[0].Rest)
	}
}

// The line says when it assumes the land: no water and no soil given.
func TestAssumedLand(t *testing.T) {
	t.Parallel()
	today := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		soil  string
		water []agro.Water
		want  bool
	}{{"", nil, true}, {"black", nil, false}, {"", borewell, false}} {
		if l, _ := current(t, today, c.soil, c.water); l.Assumed != c.want {
			t.Errorf("soil %q, water %v: assumed %v; want %v", c.soil, c.water, l.Assumed, c.want)
		}
	}
}

// Taking the suggestion leaves nothing to say: the suggestion beside the
// farmer's own is the same cycle, so "we would have suggested" never
// shows for the crop just taken.
func TestTakenSuggestionIsStillTheSuggestion(t *testing.T) {
	t.Parallel()
	today := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	l, _ := current(t, today, "black", borewell)
	s := l.Cycles[0].Suggested
	lib, _ := agro.Default()
	clim := agrotest.Hyderabad()
	soil, _ := lib.Soil("black")
	got, err := cycle.Compute(cycle.Input{
		Library: lib, Farm: "f", Ring: plotRing(), Climate: &clim, Soil: &soil, Profile: agro.Profile{Soil: "black", Water: borewell},
		Today: today, Record: cycle.Record{FutureOff: true}, Current: &s.Plan,
	})
	if err != nil {
		t.Fatal(err)
	}
	p := got.Cycles[0]
	if p.Own == nil || p.Suggested == nil || p.Suggested.MainCrop() != p.Own.MainCrop() || p.Suggested.Sow != p.Own.Sow {
		t.Errorf("taken %s on %s; suggested now %+v", s.MainCrop(), s.Sow, p.Suggested)
	}
}

// The app never plans a crop new to the country by itself, whatever the
// month, soil or water: such a crop has no local seed, market or
// experience (a Kadapa plot with a borewell was offered chia in November).
func TestSuggestionIsNeverACropNewToTheCountry(t *testing.T) {
	lib, err := agro.Default()
	if err != nil {
		t.Fatal(err)
	}
	for m := time.January; m <= time.December; m++ {
		for _, soil := range []string{"", "red_sandy", "black"} {
			for _, water := range [][]agro.Water{nil, borewell} {
				l, _ := current(t, time.Date(2026, m, 16, 0, 0, 0, 0, time.UTC), soil, water)
				for _, a := range l.Cycles[0].Suggested.Areas {
					c, err := lib.Crop(a.Crop)
					if err != nil {
						t.Fatal(err)
					}
					if c.ClimateAnalog {
						t.Errorf("%s, soil %q, water %v: suggested %s, a crop new to the country", m, soil, water, c.Name)
					}
				}
			}
		}
	}
}
