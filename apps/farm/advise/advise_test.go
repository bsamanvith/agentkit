package advise_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/advise"
	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/weather"
	"github.com/bsamanvith/agentkit/apps/farm/weather/weathertest"
)

var today = time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)

func lib(t *testing.T) *agro.Library {
	t.Helper()
	l, err := agro.Default()
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// plot has crops sown das days before today; irrigated from a borewell.
func plot(crops map[string]int) farm.Farm {
	f := farm.Farm{ID: "farm_1", Name: "East field", Profile: &agro.Profile{Water: []agro.Water{{Kind: agro.Borewell, Reliable: true}}}, Plan: &agro.Plan{}}
	ids := make([]string, 0, len(crops))
	for id := range crops {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		f.Plan.Zones = append(f.Plan.Zones, agro.Zone{Crop: id, Sow: today.AddDate(0, 0, -crops[id]).Format(agro.DateLayout)})
	}
	return f
}

func run(t *testing.T, f farm.Farm, days []weather.Day) advise.Report {
	t.Helper()
	return advise.Advise(lib(t), f, weather.Forecast{Days: days, Source: "test"}, today)
}

func rules(r advise.Report) []string {
	var out []string
	for _, a := range r.Advice {
		out = append(out, a.Rule)
	}
	return out
}

// TestQuietWeekSaysNothing: calm weather and a crop between its critical
// stages: no advice, so no notice.
func TestQuietWeekSaysNothing(t *testing.T) {
	t.Parallel()
	r := run(t, plot(map[string]int{"maize": 30}), weathertest.Week("2026-11-09"))
	if r.ActionNeeded() {
		t.Errorf("a quiet week advised %+v", r.Advice)
	}
	if len(r.Crops) != 1 || r.Crops[0].Stage.Name != "vegetative" || len(r.Days) != 7 {
		t.Errorf("report %+v", r)
	}
}

// TestHeavyRain: 90 mm on Wednesday: drains and bunds, no fertiliser
// before it, harvest the maize that is ready, drain for the crops that
// cannot stand water (not rice).
func TestHeavyRain(t *testing.T) {
	t.Parallel()
	days := weathertest.Week("2026-11-09")
	days[2].RainMM, days[2].RainChance = 90, 90
	r := run(t, plot(map[string]int{"maize": 95, "rice": 60}), days)
	if got := rules(r); len(got) == 0 || got[0] != "heavy_rain" {
		t.Fatalf("rules %v, want heavy_rain first", got)
	}
	a := r.Advice[0]
	if a.Severity != advise.Warning || a.Day != "2026-11-11" || a.Key != "heavy_rain:farm_1:2026-11-11" {
		t.Errorf("advice %+v", a)
	}
	for _, want := range []string{"drains", "Harvest Maize", "Maize cannot stand in water"} {
		if !strings.Contains(a.Action, want) {
			t.Errorf("action %q lacks %q", a.Action, want)
		}
	}
	if strings.Contains(a.Action, "Rice (paddy) cannot") {
		t.Errorf("rice tolerates waterlogging: %q", a.Action)
	}
	days[2].RainMM = 130
	if r := run(t, plot(map[string]int{"maize": 30}), days); r.Advice[0].Severity != advise.Urgent {
		t.Errorf("130 mm: %s, want urgent", r.Advice[0].Severity)
	}
	// Three wet days that add up count too.
	days = weathertest.Week("2026-11-09")
	days[1].RainMM, days[2].RainMM, days[3].RainMM = 40, 40, 40
	if got := rules(run(t, plot(map[string]int{"maize": 30}), days)); !slices.Contains(got, "heavy_rain") {
		t.Errorf("120 mm over three days: rules %v", got)
	}
}

// TestNoPlanOnlyHarmToAnyLand: a plot with no plan hears about heavy rain
// and damaging wind, not about heat (no crop to hurt).
func TestNoPlanOnlyHarmToAnyLand(t *testing.T) {
	t.Parallel()
	days := weathertest.Week("2026-11-09")
	days[1].RainMM = 70
	days[3].TMaxC = 45
	days[4].GustKmh = 80
	f := farm.Farm{ID: "farm_2", Name: "Fallow"}
	if got := rules(run(t, f, days)); !slices.Equal(got, []string{"heavy_rain", "strong_wind"}) {
		t.Errorf("rules %v, want heavy rain and wind", got)
	}
	days[4].GustKmh = 65
	if got := rules(run(t, f, days)); !slices.Equal(got, []string{"heavy_rain"}) {
		t.Errorf("65 km/h on a bare plot: rules %v, want heavy rain only", got)
	}
}

// TestCropStageRules: each rule fires on its weather at its stage, and not
// on a crop that is out of the field.
func TestCropStageRules(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		crops map[string]int
		edit  func(d []weather.Day)
		want  string
		text  string
	}{
		"rain at harvest":   {map[string]int{"green_gram": 65}, func(d []weather.Day) { d[1].RainMM = 15 }, "harvest_rain", "Harvest Green gram"},
		"wind on maize":     {map[string]int{"maize": 50}, func(d []weather.Day) { d[2].GustKmh = 65 }, "strong_wind", "Prop or tie Maize"},
		"heat at flowering": {map[string]int{"maize": 50}, func(d []weather.Day) { d[3].TMaxC = 41 }, "heat", "Irrigate lightly"},
		"frost":             {map[string]int{"wheat": 30}, func(d []weather.Day) { d[5].TMinC = 2 }, "cold", "cover nurseries"},
		"dry at flowering": {map[string]int{"maize": 50}, func(d []weather.Day) {
			for i := range d {
				d[i].ET0MM = 5
			}
		}, "dry_week", "about 35 mm"},
		"fungus weather": {map[string]int{"maize": 30}, func(d []weather.Day) {
			for i := 2; i < 5; i++ {
				d[i].HumidityPct = 90
			}
		}, "disease_weather", "leaf spots"},
		"rain on a top-dressing day": {map[string]int{"maize": 25}, func(d []weather.Day) { d[1].RainMM = 30 }, "fertiliser_timing", "to a dry day: Wednesday 11 Nov"},
		"rain on sowing day":         {map[string]int{"groundnut": -2}, func(d []weather.Day) { d[2].RainMM = 60 }, "sowing_rain", "a day or two after the rain"},
	} {
		days := weathertest.Week("2026-11-09")
		c.edit(days)
		r := run(t, plot(c.crops), days)
		got := rules(r)
		if !slices.Contains(got, c.want) {
			t.Errorf("%s: rules %v, want %s", name, got, c.want)
			continue
		}
		for _, a := range r.Advice {
			if a.Rule == c.want && !strings.Contains(a.Action, c.text) {
				t.Errorf("%s: action %q lacks %q", name, a.Action, c.text)
			}
		}
		// The same weather on a crop harvested long ago: only what harms any land.
		gone := map[string]int{}
		for id := range c.crops {
			gone[id] = 400
		}
		for _, rule := range rules(run(t, plot(gone), days)) {
			if rule != "heavy_rain" {
				t.Errorf("%s on a harvested crop: %s", name, rule)
			}
		}
	}
}

// TestRainfedDryWeek: a rainfed crop is told to save moisture, not to
// irrigate.
func TestRainfedDryWeek(t *testing.T) {
	t.Parallel()
	days := weathertest.Week("2026-11-09")
	for i := range days {
		days[i].ET0MM = 5
	}
	f := plot(map[string]int{"maize": 50})
	f.Profile = nil
	r := run(t, f, days)
	if len(r.Advice) != 1 || r.Advice[0].Title != "A dry week ahead" || strings.Contains(r.Advice[0].Action, "Plan to give") {
		t.Errorf("advice %+v", r.Advice)
	}
}

// TestPastDaysSkipped: forecast days before today are not read.
func TestPastDaysSkipped(t *testing.T) {
	t.Parallel()
	days := weathertest.Week("2026-11-07")
	days[0].RainMM = 200
	r := run(t, plot(map[string]int{"maize": 30}), days)
	if r.ActionNeeded() || len(r.Days) != 5 || r.Days[0].Date != "2026-11-09" {
		t.Errorf("report %+v: yesterday's rain read", r)
	}
}

// TestTodayAndTomorrow: advice about today or tomorrow says so.
func TestTodayAndTomorrow(t *testing.T) {
	t.Parallel()
	days := weathertest.Week("2026-11-09")
	days[0].RainMM = 15
	r := run(t, plot(map[string]int{"green_gram": 65}), days)
	if len(r.Advice) == 0 || r.Advice[0].Action != "Harvest Green gram (moong) today if it is ready; cover anything drying in the field." ||
		!strings.Contains(r.Advice[0].Why, "forecast for today") {
		t.Errorf("advice %+v", r.Advice)
	}
	days = weathertest.Week("2026-11-09")
	days[1].RainMM = 70
	if r := run(t, plot(map[string]int{"maize": 30}), days); r.Advice[0].Title != "Heavy rain tomorrow" || !strings.Contains(r.Advice[0].Action, "bunds before tomorrow.") {
		t.Errorf("advice %+v", r.Advice[0])
	}
}

// TestSeveralCropsReadAsPlural: advice naming two crops says "are", not "is".
func TestSeveralCropsReadAsPlural(t *testing.T) {
	t.Parallel()
	days := weathertest.Week("2026-11-09")
	for i := range days {
		days[i].ET0MM = 5
	}
	for want, crops := range map[string]map[string]int{
		"Maize is at a stage":                         {"maize": 2},
		"Green gram (moong) and Maize are at a stage": {"maize": 2, "green_gram": 2},
	} {
		r := run(t, plot(crops), days)
		var why string
		for _, a := range r.Advice {
			if a.Rule == "dry_week" {
				why = a.Why
			}
		}
		if !strings.Contains(why, want) {
			t.Errorf("%v: dry week says %q, want %q", crops, why, want)
		}
	}
}
