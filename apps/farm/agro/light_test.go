package agro_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/agro/agrotest"
	"github.com/bsamanvith/agentkit/apps/farm/sun"
)

// around asks for one crop sown close to a date.
func around(t *testing.T, l *agro.Library, crop, day string, r agro.Request) agro.Option {
	t.Helper()
	r.Crops, r.Around, r.Spread = []string{crop}, date(t, day), 3
	return option(t, recommend(t, l, r), crop)
}

func dimmed(f float64) agro.Climate {
	c := agrotest.Hyderabad()
	for i := range c.Months {
		c.Months[i].SunMJ *= f
	}
	return c
}

func has(notes []agro.Note, id string) bool { return slices.Contains(ids(notes), id) }

// The Deccan's sun is enough for a sun crop all year; the season's light,
// sun hours and day length come back with the option.
func TestSunnyEnoughAtHyderabad(t *testing.T) {
	t.Parallel()
	l := library(t)
	o := around(t, l, "maize", "2027-06-20", agro.Request{})
	lb := o.Light
	if !has(o.Reasons, "sun.ok") || has(o.Risks, "sun.low") {
		t.Errorf("maize in kharif: reasons %v risks %v", ids(o.Reasons), ids(o.Risks))
	}
	if lb.Want != agro.LightFull || lb.SunMJ < 15 || lb.SunMJ > 20 || lb.SunHours < 5 || lb.SlopeFactor != 1 {
		t.Errorf("light %+v", lb)
	}
	// Kharif's dullest month is the monsoon's.
	if lb.DimmestMonth != "August" && lb.DimmestMonth != "July" {
		t.Errorf("dimmest month %s, want July or August", lb.DimmestMonth)
	}
	if lb.DayLengthH[0] < 11.5 || lb.DayLengthH[1] > 13.1 || lb.DayLengthH[0] >= lb.DayLengthH[1] {
		t.Errorf("day length %v h", lb.DayLengthH)
	}
}

// The same plot under a duller sky: a sun crop scores lower and says why;
// nothing else changes.
func TestDullSkyLowersASunCrop(t *testing.T) {
	t.Parallel()
	l := library(t)
	bright := around(t, l, "rice", "2027-07-01", agro.Request{})
	dull := around(t, l, "rice", "2027-07-01", agro.Request{Climate: dimmed(0.55)})
	if !has(dull.Risks, "sun.low") || dull.Score >= bright.Score {
		t.Errorf("dull rice: score %d (bright %d), risks %v", dull.Score, bright.Score, ids(dull.Risks))
	}
	if dull.Score < bright.Score*3/4 {
		t.Errorf("dull rice score %d: light alone takes at most a fifth of %d", dull.Score, bright.Score)
	}
	if dull.Water != bright.Water {
		t.Errorf("water changed with the light: %+v vs %+v", dull.Water, bright.Water)
	}
}

// A shade crop in the open Deccan sun is a risk; a part-shade crop is fine
// in the sun and says it fits under trees.
func TestShadeCrops(t *testing.T) {
	t.Parallel()
	l := library(t)
	cocoa := around(t, l, "cocoa", "2027-06-20", agro.Request{})
	if !has(cocoa.Risks, "sun.strong") || cocoa.Light.Want != agro.LightShade {
		t.Errorf("cocoa: risks %v, light %+v", ids(cocoa.Risks), cocoa.Light)
	}
	soft := around(t, l, "cocoa", "2027-06-20", agro.Request{Climate: dimmed(0.7)})
	if has(soft.Risks, "sun.strong") || soft.Score <= cocoa.Score {
		t.Errorf("cocoa under a softer sun: score %d (open %d), risks %v", soft.Score, cocoa.Score, ids(soft.Risks))
	}
	turmeric := around(t, l, "turmeric", "2027-06-20", agro.Request{})
	if !has(turmeric.Reasons, "sun.part") || has(turmeric.Risks, "sun.low") {
		t.Errorf("turmeric: reasons %v risks %v", ids(turmeric.Reasons), ids(turmeric.Risks))
	}
}

// A steep slope facing away from the winter sun loses light for a rabi
// crop, one facing it gains; flat ground is neither.
func TestSlope(t *testing.T) {
	t.Parallel()
	l := library(t)
	flat := around(t, l, "wheat", "2026-11-15", agro.Request{})
	north := around(t, l, "wheat", "2026-11-15", agro.Request{Slope: sun.FromPercent(35, 0)})
	south := around(t, l, "wheat", "2026-11-15", agro.Request{Slope: sun.FromPercent(35, 180)})
	if has(flat.Risks, "sun.slope") || has(flat.Reasons, "sun.slope") {
		t.Errorf("flat wheat has a slope note")
	}
	if !has(north.Risks, "sun.slope") || north.Light.SlopeFactor >= 0.9 || north.Light.SunMJ >= flat.Light.SunMJ {
		t.Errorf("north slope: light %+v (flat %+v), risks %v", north.Light, flat.Light, ids(north.Risks))
	}
	if !has(south.Reasons, "sun.slope") || south.Light.SlopeFactor <= 1.07 {
		t.Errorf("south slope: light %+v, reasons %v", south.Light, ids(south.Reasons))
	}
	if north.Score > flat.Score || south.Score < flat.Score {
		t.Errorf("scores: north %d, flat %d, south %d", north.Score, flat.Score, south.Score)
	}
}

// A short-day crop sown as the days shorten flowers early: a risk; on time
// it is a reason. A long-day crop names its day lengths.
func TestDayLength(t *testing.T) {
	t.Parallel()
	l := library(t)
	late := around(t, l, "soybean", "2026-11-20", agro.Request{})
	if !has(late.Risks, "day.short") {
		t.Errorf("soybean in late November: risks %v", ids(late.Risks))
	}
	onTime := around(t, l, "soybean", "2027-06-25", agro.Request{})
	if !has(onTime.Reasons, "day.short") || has(onTime.Risks, "day.short") {
		t.Errorf("soybean in June: reasons %v risks %v", ids(onTime.Reasons), ids(onTime.Risks))
	}
	onion := around(t, l, "onion", "2026-11-20", agro.Request{})
	if !has(onion.Reasons, "day.long") {
		t.Errorf("onion: reasons %v", ids(onion.Reasons))
	}
	maize := around(t, l, "maize", "2027-06-20", agro.Request{})
	if has(maize.Reasons, "day.short") || has(maize.Reasons, "day.long") {
		t.Errorf("day-neutral maize has a day note")
	}
}

// Without sunlight in the climate the light is not scored, and the
// result says so; the day lengths still come from the latitude.
func TestNoSunlight(t *testing.T) {
	t.Parallel()
	l := library(t)
	c := agrotest.Hyderabad()
	for i := range c.Months {
		c.Months[i].SunMJ, c.Months[i].ClearSunMJ = 0, 0
	}
	res := recommend(t, l, agro.Request{Climate: c, Crops: []string{"rice", "cocoa"}})
	if !has(res.Notes, "sun.unknown") {
		t.Errorf("notes %v", ids(res.Notes))
	}
	for _, o := range res.Options {
		if o.Light.SunMJ != 0 || has(o.Risks, "sun.low") || has(o.Risks, "sun.strong") || o.Light.DayLengthH[0] == 0 {
			t.Errorf("%s: light %+v risks %v", o.Crop, o.Light, ids(o.Risks))
		}
	}
	withSun := recommend(t, l, agro.Request{Crops: []string{"rice"}})
	if has(withSun.Notes, "sun.unknown") {
		t.Error("sun.unknown with sunlight")
	}
}

// A calendar has every sowing date tried, in order, the best one among them.
func TestCalendar(t *testing.T) {
	t.Parallel()
	l := library(t)
	res := recommend(t, l, agro.Request{Crops: []string{"maize"}, Calendar: true})
	o := option(t, res, "maize")
	if len(o.Calendar) < 52 {
		t.Fatalf("%d sowings, want a year of weeks", len(o.Calendar))
	}
	best := slices.IndexFunc(o.Calendar, func(s agro.Sowing) bool { return s.Sow == o.Sow })
	if best < 0 || o.Calendar[best].Score != o.Score {
		t.Errorf("best sowing %s (%d) not in the calendar", o.Sow, o.Score)
	}
	if !slices.IsSortedFunc(o.Calendar, func(a, b agro.Sowing) int { return compare(a.Sow, b.Sow) }) {
		t.Error("calendar out of order")
	}
	if none := option(t, recommend(t, l, agro.Request{Crops: []string{"maize"}}), "maize"); none.Calendar != nil {
		t.Error("a calendar nobody asked for")
	}
}

func compare(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func TestRefusesABadSlope(t *testing.T) {
	t.Parallel()
	_, err := agro.Recommend(library(t), agro.Request{Climate: agrotest.Hyderabad(), Today: time.Now(), Slope: sun.Slope{TiltDeg: 75}})
	if !errors.Is(err, agro.ErrInvalid) {
		t.Errorf("err %v, want ErrInvalid", err)
	}
}

// Every crop says how much sun it wants and how day length sets it.
func TestEveryCropHasLightNeeds(t *testing.T) {
	t.Parallel()
	for _, c := range library(t).Crops() {
		if c.Light == "" || c.DayLength == "" {
			t.Errorf("%s: light %q, day_length %q", c.ID, c.Light, c.DayLength)
		}
	}
	bad := []byte(`{"crops":[{"id":"x","days":[1,2],"temp_opt":[1,2],"temp_abs":[0,3],"water_mm":[1,2],"ph":[5,6],"textures":["loam"],"row_cm":1,"plant_cm":1,"height_m":1,"light":"dim","day_length":"odd"}]}`)
	if _, err := agro.Load([]byte(`{"soils":[]}`), bad); !errors.Is(err, agro.ErrInvalid) {
		t.Errorf("err %v, want ErrInvalid", err)
	}
}
