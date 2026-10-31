package agro

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

// DateLayout is how dates are written: 2006-01-02.
const DateLayout = "2006-01-02"

// The recommender's settings.
const (
	// Good is the score from which a sowing date is a good one; Fair the
	// score from which it can work, with care.
	Good = 60
	Fair = 40
	// DefaultSpread is how many days either side of the farmer's "around"
	// date count as around it.
	DefaultSpread = 45
	// Step is the gap, in days, between the sowing dates the recommender
	// tries; Horizon how far ahead it looks.
	Step    = 7
	Horizon = 365
	// effectiveRain is the share of rain a crop can use: the rest runs
	// off or sinks past the roots.
	effectiveRain = 0.8
	// wetMonthMM is the month's rain past which a poorly drained soil
	// stands under water (a very poorly drained one from two thirds of it).
	wetMonthMM = 250
	// shortSlack is the share of a crop's need it can be short of without
	// a risk: within the error of the rules' water balance.
	shortSlack = 0.05
	// sowRainMM is the rain a rainfed sowing needs in its first three
	// weeks to come up: the usual advice is to sow after 50 to 75 mm.
	sowRainMM = 40
)

// storedMM is the water a soil holds for the roots when it was soaked
// before the season (by its water holding): black soils carry a rabi crop
// on what the monsoon left.
var storedMM = map[string]float64{"very_low": 10, "low": 30, "medium": 70, "high": 180, "very_high": 220}

// soakMM is the rain over the two months before sowing that fills the
// soil; less fills it in proportion.
const soakMM = 150

// Note is one reason for or against an option. ID names the rule, for code
// (temp.ok, water.short, ...); Message says it for the farmer.
type Note struct {
	ID      string `json:"id"`
	Message string `json:"message"`
}

// Window is a run of good sowing dates.
type Window struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Best  string `json:"best"`
	Score int    `json:"score"`
}

// WaterBalance is a season's water, in millimetres.
type WaterBalance struct {
	// NeedMM is what the crop needs (the middle of its range).
	NeedMM float64 `json:"need_mm"`
	// RainMM is the rain it can use over the season, with the water the
	// soil held at sowing.
	RainMM float64 `json:"rain_mm"`
	// IrrigationMM is what the plot's sources can give over the season.
	IrrigationMM float64 `json:"irrigation_mm"`
	// GapMM is the need past the rain: what irrigation has to give.
	GapMM float64 `json:"gap_mm"`
	// ShortMM is the crop's minimum need that neither covers.
	ShortMM float64 `json:"short_mm"`
}

// Season is one crop sown on one date at the plot, as the rules see it.
type Season struct {
	Sow     string       `json:"sow"`
	Harvest string       `json:"harvest"`
	Season  string       `json:"season"` // kharif, rabi or zaid
	Score   int          `json:"score"`  // 0 to 100
	Water   WaterBalance `json:"water"`
	Reasons []Note       `json:"reasons"`
	Risks   []Note       `json:"risks"`
}

// Option is one crop the recommender suggests, with when to sow it.
type Option struct {
	Crop   string            `json:"crop"`
	Name   string            `json:"name"`
	Names  map[string]string `json:"names,omitempty"`
	Kind   string            `json:"kind"`
	Analog bool              `json:"analog,omitempty"`
	// Season is the best sowing: in the farmer's range when they gave one,
	// else the best date of the next good window.
	Season
	// InRange is true when the sowing is in the farmer's range.
	InRange bool `json:"in_range,omitempty"`
	// Window is the good window the sowing is in, if any; Next the first
	// good window from today.
	Window *Window `json:"window,omitempty"`
	Next   *Window `json:"next,omitempty"`
}

// Request is what the recommender works from.
type Request struct {
	Climate Climate
	// Soil is the plot's soil; nil when unknown.
	Soil    *Soil
	Profile Profile
	// Today is the day the plan is made on.
	Today time.Time
	// Around is when the farmer wants to sow, roughly; zero when they
	// did not say. Spread is how many days either side count (0:
	// DefaultSpread).
	Around time.Time
	Spread int
	// Crops limits the options to these crop ids; empty for all.
	Crops []string
}

// Result is the recommender's answer, best option first.
type Result struct {
	Today string `json:"today"`
	// From and To are the farmer's range, when they gave one.
	From    string   `json:"from,omitempty"`
	To      string   `json:"to,omitempty"`
	Options []Option `json:"options"`
	// Notes are about the inputs as a whole: an unknown soil, a rainfed
	// plot.
	Notes []Note `json:"notes,omitempty"`
}

// Recommend scores every crop of the library (or of r.Crops) for every
// sowing date a week apart over the next year at the plot, and returns the
// crops best first. The rules are deterministic: the same request gives
// the same result.
func Recommend(l *Library, r Request) (Result, error) {
	if err := r.Climate.Check(); err != nil {
		return Result{}, err
	}
	if r.Today.IsZero() {
		return Result{}, fmt.Errorf("%w: no today", ErrInvalid)
	}
	today := day(r.Today)
	crops := l.Crops()
	if len(r.Crops) > 0 {
		crops = crops[:0]
		for _, id := range r.Crops {
			c, err := l.Crop(id)
			if err != nil {
				return Result{}, err
			}
			crops = append(crops, c)
		}
	}
	res := Result{Today: today.Format(DateLayout), Options: []Option{}}
	var from, to time.Time
	dates := []time.Time{}
	for d := 0; d < Horizon; d += Step {
		dates = append(dates, today.AddDate(0, 0, d))
	}
	if !r.Around.IsZero() {
		spread := r.Spread
		if spread == 0 {
			spread = DefaultSpread
		}
		if spread < 0 || spread > 180 {
			return Result{}, fmt.Errorf("%w: spread %d days: want 0 to 180", ErrInvalid, spread)
		}
		around := day(r.Around)
		from, to = around.AddDate(0, 0, -spread), around.AddDate(0, 0, spread)
		if to.Before(today) {
			return Result{}, fmt.Errorf("%w: %s is in the past", ErrInvalid, around.Format(DateLayout))
		}
		from = later(from, today)
		res.From, res.To = from.Format(DateLayout), to.Format(DateLayout)
		// The range's own dates, so a short range is tried at all.
		for d := from; !d.After(to); d = d.AddDate(0, 0, Step) {
			dates = append(dates, d)
		}
		dates = append(dates, later(around, today))
	}
	slices.SortFunc(dates, func(a, b time.Time) int { return a.Compare(b) })
	dates = slices.Compact(dates)

	if r.Soil == nil {
		res.Notes = append(res.Notes, Note{"soil.unknown", "The soil is not known, so it is not scored: tell us the soil type for a better answer."})
	}
	if r.Profile.Rainfed() {
		res.Notes = append(res.Notes, Note{"water.rainfed", "The plot is rainfed: crops are scored on rain alone."})
	}
	for _, c := range crops {
		seasons := make([]Season, len(dates))
		for i, d := range dates {
			seasons[i] = simulate(c, r, d)
		}
		windows := windowsOf(dates, seasons)
		o := Option{Crop: c.ID, Name: c.Name, Names: c.Names, Kind: c.Kind, Analog: c.ClimateAnalog}
		if len(windows) > 0 {
			o.Next = &windows[0]
		}
		pick := -1
		if !from.IsZero() {
			for i, d := range dates {
				if d.Before(from) || d.After(to) {
					continue
				}
				if pick < 0 || seasons[i].Score > seasons[pick].Score {
					pick = i
				}
			}
			o.InRange = pick >= 0
		}
		if pick < 0 {
			pick = bestIn(dates, seasons, o.Next)
		}
		o.Season = seasons[pick]
		for i := range windows {
			if windows[i].From <= o.Sow && o.Sow <= windows[i].To {
				o.Window = &windows[i]
			}
		}
		res.Options = append(res.Options, o)
	}
	slices.SortStableFunc(res.Options, func(a, b Option) int {
		if a.InRange != b.InRange {
			if a.InRange {
				return -1
			}
			return 1
		}
		return cmp.Or(b.Score-a.Score, strings.Compare(a.Sow, b.Sow), strings.Compare(a.Crop, b.Crop))
	})
	return res, nil
}

// bestIn returns the index of next's best date, or of the best date of all
// when there is no good window.
func bestIn(dates []time.Time, seasons []Season, next *Window) int {
	best := 0
	for i := range dates {
		if next != nil && seasons[i].Sow == next.Best {
			return i
		}
		if seasons[i].Score > seasons[best].Score {
			best = i
		}
	}
	return best
}

// windowsOf returns the runs of good dates, in order. A run breaks where a
// date is not good, or where two tried dates are more than a step apart.
func windowsOf(dates []time.Time, seasons []Season) []Window {
	var out []Window
	var cur *Window
	for i, s := range seasons {
		gap := i > 0 && dates[i].Sub(dates[i-1]) > Step*24*time.Hour
		if s.Score < Good || gap {
			if cur != nil {
				out = append(out, *cur)
				cur = nil
			}
			if s.Score < Good {
				continue
			}
		}
		if cur == nil {
			cur = &Window{From: s.Sow, Best: s.Sow, Score: s.Score}
		}
		cur.To = s.Sow
		if s.Score > cur.Score {
			cur.Best, cur.Score = s.Sow, s.Score
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}

// Simulate scores crop c sown on date sow at the plot of r (r.Around and
// r.Crops play no part): the plan checker's view of one zone.
func Simulate(c Crop, r Request, sow time.Time) Season { return simulate(c, r, day(sow)) }

func simulate(c Crop, r Request, sow time.Time) Season {
	length := int(math.Round(c.Days.Mid()))
	if c.Perennial {
		length = 365 // a year of the orchard, from planting
	}
	s := Season{
		Sow: sow.Format(DateLayout), Harvest: sow.AddDate(0, 0, length).Format(DateLayout),
		Season: SeasonOf(sow), Reasons: []Note{}, Risks: []Note{},
	}
	var tempSum, rain, irrigation float64
	var cold, hot, frost int
	lo, hi := math.Inf(1), math.Inf(-1)
	coldest, hottest := sow, sow
	wettest, firstRain := 0.0, 0.0
	irrigationPerMonth := r.Profile.IrrigationMM()
	for d := range length {
		t := sow.AddDate(0, 0, d)
		temp := r.Climate.tempOn(t)
		tempSum += tempScore(c, temp)
		if temp < lo {
			lo, coldest = temp, t
		}
		if temp > hi {
			hi, hottest = temp, t
		}
		switch {
		case temp < c.TempAbs[0]:
			cold++
		case temp > c.TempAbs[1]:
			hot++
		}
		m := r.Climate.Months[t.Month()-1]
		if m.TempMin <= 2 && !c.Tolerant(TolerateFrost) {
			frost++
		}
		wettest = max(wettest, m.RainMM)
		rain += r.Climate.rainOn(t)
		if d < 21 {
			firstRain += r.Climate.rainOn(t)
		}
		irrigation += irrigationPerMonth / float64(daysIn(t))
	}
	n := float64(length)
	tempF := tempSum / n
	note := func(risk bool, id, format string, a ...any) {
		if risk {
			s.Risks = append(s.Risks, Note{id, fmt.Sprintf(format, a...)})
		} else {
			s.Reasons = append(s.Reasons, Note{id, fmt.Sprintf(format, a...)})
		}
	}
	switch {
	case float64(cold) > n/10:
		tempF *= 0.3
		note(true, "temp.cold", "Too cold around %s (%.0f °C); it needs at least %.0f °C.", coldest.Month(), lo, c.TempAbs[0])
	case float64(hot) > n/10:
		tempF *= 0.3
		note(true, "temp.hot", "Too hot around %s (%.0f °C); it stands at most %.0f °C.", hottest.Month(), hi, c.TempAbs[1])
	case tempF >= 0.85:
		note(false, "temp.ok", "Temperatures through the season suit it (%.0f to %.0f °C).", lo, hi)
	default:
		note(true, "temp.off", "Part of the season is outside its best temperatures (%.0f to %.0f °C; best %.0f to %.0f °C).", lo, hi, c.TempOpt[0], c.TempOpt[1])
	}
	frostF := 1.0
	if frost > 0 {
		frostF = 0.5
		note(true, "frost", "Nights can fall to frost in the season, and it does not stand frost.")
	}

	// Water.
	eff := effectiveRain * rain
	stored := 0.0
	if r.Soil != nil {
		before := 0.0
		for d := 1; d <= 60; d++ {
			before += r.Climate.rainOn(sow.AddDate(0, 0, -d))
		}
		stored = round(storedMM[r.Soil.WaterHolding] * min(1, before/soakMM))
		eff += stored
	}
	need := c.WaterMM[0]
	s.Water = WaterBalance{
		NeedMM: round(c.WaterMM.Mid()), RainMM: round(eff), IrrigationMM: round(irrigation),
		GapMM: round(max(0, c.WaterMM.Mid()-eff)), ShortMM: round(max(0, need-eff-irrigation)),
	}
	waterF := math.Pow(min(1, (eff+irrigation)/need), 1.5)
	switch {
	case eff >= need:
		note(false, "water.rain", "Rain covers it: about %.0f mm in the season; it needs %.0f to %.0f mm.", eff, c.WaterMM[0], c.WaterMM[1])
	case s.Water.ShortMM <= shortSlack*need:
		note(false, "water.irrigation", "Rain gives about %.0f mm of the %.0f mm it needs; your water sources can cover the rest.", eff, need)
	default:
		note(true, "water.short", "Short of water: rain gives about %.0f mm and irrigation %.0f mm, but it needs at least %.0f mm.", eff, irrigation, need)
	}
	if eff > c.WaterMM[1]*1.3 && !c.Tolerant(TolerateWaterlogging) {
		waterF *= 0.85
		note(true, "water.excess", "More rain than it likes (about %.0f mm; at most %.0f): watch for rot and fungus.", eff, c.WaterMM[1])
	}
	if r.Profile.Rainfed() && !c.Perennial && firstRain < sowRainMM && stored < 50 {
		waterF *= 0.7
		note(true, "sow.dry", "Little rain in the three weeks after this date (about %.0f mm): on a rainfed plot, sow after the first good rains.", firstRain)
	}

	// Soil.
	soilF := 1.0
	ph, measured := r.Profile.PH, r.Profile.PH != 0
	if soil := r.Soil; soil != nil || measured {
		var dist float64
		switch {
		case measured:
			dist = max(0, c.PH[0]-ph, ph-c.PH[1])
		default:
			dist = max(0, c.PH[0]-soil.PH[1], soil.PH[0]-c.PH[1])
		}
		if dist > 0 {
			soilF *= max(0.3, 1-0.25*dist)
			note(true, "soil.ph", "The soil's pH is off its range (%.1f to %.1f).", c.PH[0], c.PH[1])
		}
	}
	if soil := r.Soil; soil != nil {
		if c.Suits(soil.Texture) {
			note(false, "soil.texture", "%s suits it.", soil.Name)
		} else {
			soilF *= 0.7
			note(true, "soil.texture", "%s is not its best soil.", soil.Name)
		}
		if soil.Salty && !c.Tolerant(TolerateSalinity) {
			soilF *= 0.4
			note(true, "soil.salt", "The soil is salty, and it does not stand salt.")
		}
		wet := wetMonthMM
		if soil.Drainage == "very_poor" {
			wet = wetMonthMM * 2 / 3
		}
		if soil.DrainsPoorly() && wettest > float64(wet) && !c.Tolerant(TolerateWaterlogging) {
			soilF *= 0.75
			note(true, "waterlogging", "Water stands on this soil in the wettest month (%.0f mm): raise beds or open drains.", wettest)
		}
	}

	seasonF := 1.0
	if slices.Contains(c.Seasons, s.Season) {
		note(false, "season.usual", "%s is a usual season for it in India.", titleCase(s.Season))
	} else if !c.Perennial {
		seasonF = 0.8
		note(true, "season.unusual", "It is not usually sown in %s in India.", s.Season)
	}
	if c.ClimateAnalog {
		note(true, "analog", "New to India (grown in %s): the climate here matches, but there is little local experience, so try a small area first.", strings.Join(c.GrownIn, ", "))
	}
	s.Score = int(math.Round(100 * tempF * frostF * waterF * soilF * seasonF))
	return s
}

// tempScore is how well a day's mean temperature suits c: 1 in its best
// range, falling to 0 at the edges of what it survives.
func tempScore(c Crop, t float64) float64 {
	switch {
	case c.TempOpt.Contains(t):
		return 1
	case t < c.TempOpt[0]:
		return clamp((t - c.TempAbs[0]) / (c.TempOpt[0] - c.TempAbs[0]))
	default:
		return clamp((c.TempAbs[1] - t) / (c.TempAbs[1] - c.TempOpt[1]))
	}
}

func clamp(v float64) float64 { return max(0, min(1, v)) }

func round(v float64) float64 { return math.Round(v) }

func day(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func later(a, b time.Time) time.Time {
	if a.Before(b) {
		return b
	}
	return a
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
