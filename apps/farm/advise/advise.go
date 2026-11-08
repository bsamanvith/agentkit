// Package advise reads a plot's weather forecast against what is growing
// on it and says what the farmer should do this week, if anything.
//
// It is rules, not a model: each [Rule] looks at the forecast and at the
// stage of each crop in the field (from the saved plan, package calendar)
// and gives advice only when there is something to do. A quiet week gives
// none, so a weekly check notifies the farmer only when it matters.
//
// The thresholds follow the India Meteorological Department's categories
// (heavy rain 64.5 mm a day, very heavy 115.6 mm), the crop library's
// temperature limits, and common extension advice; they are starter
// values to review with an agronomist, like the crop library's.
package advise

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/calendar"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/weather"
)

// Severity of advice.
type Severity string

// The severities: urgent is act within a day or two or lose crop or soil;
// warning is plan this week's work around it; Suggest (advice) is worth
// doing.
const (
	Urgent  Severity = "urgent"
	Warning Severity = "warning"
	Suggest Severity = "advice"
)

// Thresholds.
const (
	HeavyRainMM     = 64.5  // IMD heavy rain, a day
	VeryHeavyRainMM = 115.6 // IMD very heavy rain, a day
	HeavyRain3DayMM = 100   // three days together
	WashOffMM       = 20    // enough rain to wash off a spray or fertiliser
	HarvestRainMM   = 10    // enough to spoil a crop lying in the field
	StrongGustKmh   = 60    // lodges tall crops, breaks bananas
	SevereGustKmh   = 75    // damages sheds, nets, polyhouses
	FrostC          = 4     // a screen minimum this low risks frost on the ground
	FloweringHeatC  = 38    // a high this hot at flowering hurts pollination
	DryWeekRainMM   = 10    // a week with less is dry
	DryWeekET0MM    = 25    // and the crop uses more than this
	HumidPct        = 85    // with 20 to 30 °C for three days: fungal disease weather
)

// Item is one piece of advice.
type Item struct {
	// Rule is the rule's id; Key is the rule, plot and first day, so the
	// same warning is not sent twice.
	Rule     string   `json:"rule"`
	Key      string   `json:"key"`
	Severity Severity `json:"severity"`
	// Day is the first day it concerns (YYYY-MM-DD).
	Day   string   `json:"day"`
	Crops []string `json:"crops,omitempty"` // crop names
	Title string   `json:"title"`
	// Action is what to do; Why is the forecast behind it.
	Action string `json:"action"`
	Why    string `json:"why"`
}

// CropNow is a crop of the plan and its stage today.
type CropNow struct {
	Zone  int            `json:"zone"`
	Crop  string         `json:"crop"`
	Name  string         `json:"name"`
	Sow   string         `json:"sow"`
	Stage calendar.Stage `json:"stage"`
}

// Report is a plot's week.
type Report struct {
	Farm     string          `json:"farm"`
	Today    string          `json:"today"`
	Days     []weather.Day   `json:"days"`
	Totals   weather.Totals  `json:"totals"`
	Crops    []CropNow       `json:"crops"`
	Advice   []Item          `json:"advice"`
	Tasks    []calendar.Task `json:"tasks,omitempty"` // the plan's work due over the days
	Source   string          `json:"source"`
	Messages []string        `json:"notes,omitempty"`
}

// ActionNeeded is true when there is advice.
func (r Report) ActionNeeded() bool { return len(r.Advice) > 0 }

// week is what the rules look at.
type week struct {
	f         farm.Farm
	lib       *agro.Library
	days      []weather.Day
	crops     []crop // in the field or sown during the days
	irrigated bool
	tasks     []calendar.Task
	today     string
}

type crop struct {
	CropNow
	c   agro.Crop
	sow time.Time
}

// Rule is one check of the week.
type Rule struct {
	ID    string
	About string
	check func(w week) []Item
}

// Rules returns the rules, in the order their advice is listed.
func Rules() []Rule {
	return []Rule{
		{"heavy_rain", "Heavy rain in the next days: drains, bunds, harvest and no fertiliser before it.", heavyRain},
		{"harvest_rain", "Rain while a crop is ready to harvest.", harvestRain},
		{"sowing_rain", "Heavy rain on a planned sowing day.", sowingRain},
		{"strong_wind", "Gusts that lodge tall crops or damage structures.", wind},
		{"heat", "Heat beyond a crop's limit, or at flowering.", heat},
		{"cold", "Cold beyond a crop's limit, or frost.", cold},
		{"dry_week", "A dry week at a stage when the crop needs water.", dryWeek},
		{"fertiliser_timing", "Fertiliser due on a day rain would wash it away.", fertiliserTiming},
		{"disease_weather", "Humid, warm days that favour fungal disease.", disease},
	}
}

// Advise reads the forecast for plot f against its plan's crops. today is
// the farmer's date; forecast days before it are skipped.
func Advise(lib *agro.Library, f farm.Farm, fc weather.Forecast, today time.Time) Report {
	t := today.Format(weather.DateLayout)
	r := Report{Farm: f.ID, Today: t, Source: fc.Source, Advice: []Item{}, Crops: []CropNow{}}
	for _, d := range fc.Days {
		if d.Date >= t {
			r.Days = append(r.Days, d)
		}
	}
	r.Totals = weather.Sum(r.Days)
	w := week{today: t, f: f, lib: lib, days: r.Days, irrigated: f.Profile != nil && len(f.Profile.Water) > 0}
	if len(r.Days) == 0 {
		r.Messages = append(r.Messages, "The forecast has no days from today.")
		return r
	}
	last, _ := time.Parse(weather.DateLayout, r.Days[len(r.Days)-1].Date)
	if f.Plan != nil {
		for i, z := range f.Plan.Zones {
			c, err := lib.Crop(z.Crop)
			if err != nil {
				continue
			}
			sow, err := time.Parse(agro.DateLayout, z.Sow)
			if err != nil {
				continue
			}
			st := calendar.StageAt(c, sow, today)
			now := CropNow{Zone: i, Crop: c.ID, Name: c.Name, Sow: z.Sow, Stage: st}
			r.Crops = append(r.Crops, now)
			if st.InField || (st.Name == calendar.NotSown && !sow.After(last)) {
				w.crops = append(w.crops, crop{now, c, sow})
			}
		}
		if cal, err := calendar.ForPlan(lib, *f.Plan, w.irrigated); err == nil {
			r.Tasks = calendar.Due(cal, today, last)
			w.tasks = r.Tasks
		}
	} else {
		r.Messages = append(r.Messages, "No crop plan is saved for this plot: only weather that harms any land is checked.")
	}
	for _, rule := range Rules() {
		for _, it := range rule.check(w) {
			it.Rule = rule.ID
			it.Key = rule.ID + ":" + f.ID + ":" + it.Day
			r.Advice = append(r.Advice, it)
		}
	}
	slices.SortStableFunc(r.Advice, func(a, b Item) int { return rank(a.Severity) - rank(b.Severity) })
	return r
}

func rank(s Severity) int {
	switch s {
	case Urgent:
		return 0
	case Warning:
		return 1
	}
	return 2
}

// growing are the crops in the field (sown, not harvested).
func (w week) growing(stages ...string) []crop {
	var out []crop
	for _, c := range w.crops {
		if c.Stage.InField && (len(stages) == 0 || slices.Contains(stages, c.Stage.Name)) {
			out = append(out, c)
		}
	}
	return out
}

// stageOn is a crop's stage on a forecast day.
func (c crop) stageOn(day string) calendar.Stage {
	d, _ := time.Parse(weather.DateLayout, day)
	return calendar.StageAt(c.c, c.sow, d)
}

func names(cs []crop) []string {
	var out []string
	for _, c := range cs {
		if !slices.Contains(out, c.Name) {
			out = append(out, c.Name)
		}
	}
	return out
}

func list(ws []string) string {
	switch len(ws) {
	case 0:
		return ""
	case 1:
		return ws[0]
	}
	return strings.Join(ws[:len(ws)-1], ", ") + " and " + ws[len(ws)-1]
}

// day is "today", "tomorrow" or "Wednesday 12 Nov".
func (w week) day(date string) string {
	switch date {
	case w.today:
		return "today"
	case nextDay(w.today):
		return "tomorrow"
	}
	return dayName(date)
}

// by is "today", or "before tomorrow" or "before Wednesday 12 Nov".
func (w week) by(date string) string {
	if date == w.today {
		return "today"
	}
	return "before " + w.day(date)
}

// dayName is "Wednesday 12 Nov".
func dayName(date string) string {
	d, err := time.Parse(weather.DateLayout, date)
	if err != nil {
		return date
	}
	return d.Format("Monday 2 Jan")
}

func mm(v float64) string { return fmt.Sprintf("%.0f mm", v) }

func heavyRain(w week) []Item {
	for i, d := range w.days {
		if d.RainMM < weather.RainyDayMM {
			continue // a wet spell is dated from its first rainy day
		}
		three := weather.Sum(w.days[i:min(i+3, len(w.days))]).RainMM
		if d.RainMM < HeavyRainMM && three < HeavyRain3DayMM {
			continue
		}
		sev := Warning
		if d.RainMM >= VeryHeavyRainMM || three >= 1.5*HeavyRain3DayMM {
			sev = Urgent
		}
		acts := []string{"Open the field drains and check the bunds " + w.by(d.Date) + "."}
		acts = append(acts, "Do not put fertiliser or spray in the two days before the rain: it would wash away.")
		var ready, wet []crop
		for _, c := range w.growing() {
			st := c.stageOn(d.Date).Name
			if st == calendar.Harvest || st == calendar.Maturing && c.stageOn(d.Date).DAS >= int(c.c.Days[0])-7 {
				ready = append(ready, c)
			}
			if !slices.Contains(c.c.Tolerates, "waterlogging") {
				wet = append(wet, c)
			}
		}
		if len(ready) > 0 {
			acts = append(acts, "Harvest "+list(names(ready))+" before the rain if it is ready, and keep the harvest under cover.")
		}
		if len(wet) > 0 {
			acts = append(acts, list(names(wet))+" cannot stand in water: drain standing water within a day.")
		}
		why := fmt.Sprintf("%s of rain is forecast for %s", mm(d.RainMM), w.day(d.Date))
		if three > d.RainMM+1 {
			why += fmt.Sprintf(" (%s over three days)", mm(three))
		}
		return []Item{{
			Severity: sev, Day: d.Date, Crops: names(w.growing()), Title: "Heavy rain " + w.day(d.Date),
			Action: strings.Join(acts, " "), Why: why + ".",
		}}
	}
	return nil
}

func harvestRain(w week) []Item {
	for _, d := range w.days[:min(4, len(w.days))] {
		if d.RainMM < HarvestRainMM || d.RainMM >= HeavyRainMM {
			continue // heavy rain has its own advice
		}
		var ready []crop
		for _, c := range w.growing() {
			if c.stageOn(d.Date).Name == calendar.Harvest {
				ready = append(ready, c)
			}
		}
		if len(ready) == 0 {
			return nil
		}
		return []Item{{
			Severity: Warning, Day: d.Date, Crops: names(ready), Title: "Rain at harvest time",
			Action: "Harvest " + list(names(ready)) + " " + w.by(d.Date) + " if it is ready; cover anything drying in the field.",
			Why:    fmt.Sprintf("%s of rain is forecast for %s, while the crop is in its harvest window.", mm(d.RainMM), w.day(d.Date)),
		}}
	}
	return nil
}

func sowingRain(w week) []Item {
	var out []Item
	for _, c := range w.crops {
		if c.Stage.Name != calendar.NotSown {
			continue
		}
		for _, d := range w.days {
			if d.Date != c.Sow {
				continue
			}
			if d.RainMM >= HeavyRainMM*0.75 {
				out = append(out, Item{
					Severity: Warning, Day: d.Date, Crops: []string{c.Name}, Title: "Move the sowing of " + c.Name,
					Action: "Sow " + c.Name + " a day or two after the rain, once the soil can be worked, rather than " + w.day(d.Date) + ": heavy rain washes out and crusts over fresh seed.",
					Why:    fmt.Sprintf("%s of rain is forecast on the planned sowing day.", mm(d.RainMM)),
				})
			}
		}
	}
	return out
}

func wind(w week) []Item {
	for _, d := range w.days {
		if d.GustKmh < StrongGustKmh {
			continue
		}
		var tall []crop
		for _, c := range w.growing() {
			if c.c.Tall || c.c.HeightM >= 1.5 || c.c.Perennial {
				tall = append(tall, c)
			}
		}
		why := fmt.Sprintf("Gusts up to %.0f km/h are forecast on %s.", d.GustKmh, w.day(d.Date))
		switch {
		case len(tall) > 0:
			return []Item{{
				Severity: Warning, Day: d.Date, Crops: names(tall), Title: "Strong wind " + w.day(d.Date),
				Action: "Prop or tie " + list(names(tall)) + " " + w.by(d.Date) + ", and do not spray that day: the spray drifts away.",
				Why:    why,
			}}
		case d.GustKmh >= SevereGustKmh:
			return []Item{{
				Severity: Warning, Day: d.Date, Title: "Damaging wind " + w.day(d.Date),
				Action: "Tie down shed roofs, shade nets and anything loose " + w.by(d.Date) + "; do not spray that day.",
				Why:    why,
			}}
		}
		return nil
	}
	return nil
}

func heat(w week) []Item {
	for _, d := range w.days {
		mean := (d.TMaxC + d.TMinC) / 2
		var hit []crop
		for _, c := range w.growing() {
			st := c.stageOn(d.Date).Name
			if mean > c.c.TempAbs[1] || (d.TMaxC >= FloweringHeatC && (st == calendar.Flowering || st == calendar.Establishment)) {
				hit = append(hit, c)
			}
		}
		if len(hit) == 0 {
			continue
		}
		act := "Irrigate lightly in the evening or early morning, keep the soil mulched, and do not spray in the afternoon heat."
		if !w.irrigated {
			act = "Mulch between the rows to keep moisture in, and do not spray in the afternoon heat; young plants can be shaded with leaves or a net."
		}
		return []Item{{
			Severity: Warning, Day: d.Date, Crops: names(hit), Title: "Heat stress " + w.day(d.Date),
			Action: act, Why: fmt.Sprintf("A high of %.0f °C (a day's mean of %.0f °C) is forecast for %s, beyond what %s stands at its stage.", d.TMaxC, mean, w.day(d.Date), list(names(hit))),
		}}
	}
	return nil
}

func cold(w week) []Item {
	for _, d := range w.days {
		mean := (d.TMaxC + d.TMinC) / 2
		var hit []crop
		for _, c := range w.growing() {
			if mean < c.c.TempAbs[0] || d.TMinC <= FrostC {
				hit = append(hit, c)
			}
		}
		if len(hit) == 0 {
			continue
		}
		return []Item{{
			Severity: Warning, Day: d.Date, Crops: names(hit), Title: "Cold night " + w.day(d.Date),
			Action: "Irrigate lightly the evening before (moist soil holds warmth); cover nurseries and young plants; smoke on the windward side on the coldest nights.",
			Why:    fmt.Sprintf("A low of %.0f °C is forecast for %s, colder than %s stands.", d.TMinC, w.day(d.Date), list(names(hit))),
		}}
	}
	return nil
}

func dryWeek(w week) []Item {
	if len(w.days) < 5 {
		return nil
	}
	t := weather.Sum(w.days[:min(7, len(w.days))])
	if t.RainMM >= DryWeekRainMM || t.ET0MM < DryWeekET0MM {
		return nil
	}
	crit := w.growing(calendar.Establishment, calendar.Flowering)
	if len(crit) == 0 {
		return nil
	}
	why := fmt.Sprintf("Only %s of rain is forecast over the week while the crop will use about %s of water; %s is at a stage that cannot go short.", mm(t.RainMM), mm(t.ET0MM), list(names(crit)))
	if w.irrigated {
		return []Item{{
			Severity: Suggest, Day: w.days[0].Date, Crops: names(crit), Title: "Irrigate this week",
			Action: fmt.Sprintf("Plan to give about %s of water this week, in two or three turns; irrigate in the evening or early morning.", mm(t.ET0MM-t.RainMM)),
			Why:    why,
		}}
	}
	return []Item{{
		Severity: Suggest, Day: w.days[0].Date, Crops: names(crit), Title: "A dry week ahead",
		Action: "Hoe lightly and mulch between rows to keep the soil's moisture in; a protective irrigation from a neighbour's well or a farm pond pays most now.",
		Why:    why,
	}}
}

func fertiliserTiming(w week) []Item {
	var due []calendar.Task
	for _, t := range w.tasks {
		if t.Kind == calendar.Fertilise && !strings.HasPrefix(t.Title, "No ") {
			due = append(due, t)
		}
	}
	// washes is the rain on a day and the day after: enough of it washes
	// fertiliser put down that day away.
	washes := func(date string) float64 { return rainOn(w.days, date) + rainOn(w.days, nextDay(date)) }
	for _, t := range due {
		if washes(t.Date) < WashOffMM {
			continue
		}
		var good []string
		for _, d := range w.days {
			if d.Date >= w.today && d.RainMM < weather.RainyDayMM && washes(d.Date) < WashOffMM/2 {
				good = append(good, w.day(d.Date))
			}
		}
		act := "Move \"" + t.Title + "\" to a dry day"
		if len(good) > 0 {
			act += ": " + list(good[:min(3, len(good))])
		}
		act += ", with the soil moist but no heavy rain the day after."
		return []Item{{
			Severity: Suggest, Day: t.Date, Title: "Time the fertiliser around the rain",
			Action: act, Why: fmt.Sprintf("It is due %s, and %s of rain is forecast for that day and the next, which would wash it away.", w.day(t.Date), mm(washes(t.Date))),
		}}
	}
	return nil
}

func nextDay(date string) string {
	d, err := time.Parse(weather.DateLayout, date)
	if err != nil {
		return ""
	}
	return d.AddDate(0, 0, 1).Format(weather.DateLayout)
}

func rainOn(days []weather.Day, date string) float64 {
	for _, d := range days {
		if d.Date == date {
			return d.RainMM
		}
	}
	return 0
}

func disease(w week) []Item {
	run := 0
	for _, d := range w.days {
		mean := (d.TMaxC + d.TMinC) / 2
		if d.HumidityPct >= HumidPct && mean >= 20 && mean <= 30 {
			run++
		} else {
			run = 0
		}
		if run == 3 {
			cs := w.growing(calendar.Vegetative, calendar.Flowering, calendar.Maturing, calendar.Growing)
			if len(cs) == 0 {
				return nil
			}
			return []Item{{
				Severity: Suggest, Day: d.Date, Crops: names(cs), Title: "Watch for fungal disease",
				Action: "Walk the field for leaf spots, blight, blast or mildew this week; if you see it, spray the recommended fungicide on a dry morning.",
				Why:    fmt.Sprintf("Three or more humid (over %d %%) warm days are forecast up to %s: weather fungal disease spreads in.", HumidPct, w.day(d.Date)),
			}}
		}
	}
	return nil
}
