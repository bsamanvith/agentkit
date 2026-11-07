package agro

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/sun"
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

// LightBalance is a season's sun on the plot.
type LightBalance struct {
	// Want is how much sun the crop wants: full, part or shade.
	Want string `json:"want"`
	// SunMJ is the mean sunlight a day brings to the plot's ground over
	// the season (on its slope), in MJ/m², and SunHours its bright hours;
	// both 0 when the climate has no sunlight.
	SunMJ    float64 `json:"sun_mj"`
	SunHours float64 `json:"sun_hours"`
	// DimmestMonth is the season's month with the least sun, and
	// DimmestMJ its light.
	DimmestMonth string  `json:"dimmest_month,omitempty"`
	DimmestMJ    float64 `json:"dimmest_mj,omitempty"`
	// SlopeFactor is the slope's light over flat ground's, through the
	// season (1 on flat ground).
	SlopeFactor float64 `json:"slope_factor"`
	// DayLengthH is the season's shortest and longest day, in hours.
	DayLengthH Range `json:"day_length_h"`
}

// The light rules' starter values: a season's mean daily sunlight on the
// ground, in MJ/m², from which a crop grows as well as the light lets it
// (good) and below which it is clearly held back (poor); and the light
// past which a shade crop scorches without cover.
const (
	fullSunGood  = 15.0
	fullSunPoor  = 9.0
	partSunGood  = 10.0
	partSunPoor  = 6.0
	shadeSunMost = 17.0
	// slopeNote is how far the slope's light must differ from flat
	// ground's, as a share, to be worth a note.
	slopeNote = 0.07
	// lightSlack is how far below good, in MJ/m² a day, the light can be
	// before it is a risk.
	lightSlack = 1.5
)

// Season is one crop sown on one date at the plot, as the rules see it.
type Season struct {
	Sow     string       `json:"sow"`
	Harvest string       `json:"harvest"`
	Season  string       `json:"season"` // kharif, rabi or zaid
	Score   int          `json:"score"`  // 0 to 100
	Water   WaterBalance `json:"water"`
	Light   LightBalance `json:"light"`
	Reasons []Note       `json:"reasons"`
	Risks   []Note       `json:"risks"`
}

// Sowing is one sowing date's score, for a calendar.
type Sowing struct {
	Sow    string `json:"sow"`
	Score  int    `json:"score"`
	Season string `json:"season"`
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
	// Calendar is every sowing date tried with its score, when the
	// request asked for it.
	Calendar []Sowing `json:"calendar,omitempty"`
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
	// Slope is how the plot's ground lies; zero for flat.
	Slope sun.Slope
	// Calendar asks for every sowing date's score in each option.
	Calendar bool

	sun *[12]sun.Month // the slope's sun, month by month
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
	if err := r.Slope.Check(); err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	year := r.Climate.Sun(r.Slope)
	r.sun = &year
	if !r.Climate.HasSun() {
		res.Notes = append(res.Notes, Note{"sun.unknown", "The climate has no sunlight figures, so sunlight is not scored."})
	}
	for _, c := range crops {
		seasons := make([]Season, len(dates))
		for i, d := range dates {
			seasons[i] = simulate(c, r, d)
		}
		windows := windowsOf(dates, seasons)
		o := Option{Crop: c.ID, Name: c.Name, Names: c.Names, Kind: c.Kind, Analog: c.ClimateAnalog}
		if r.Calendar {
			o.Calendar = make([]Sowing, len(dates))
			for i := range dates {
				o.Calendar[i] = Sowing{Sow: seasons[i].Sow, Score: seasons[i].Score, Season: seasons[i].Season}
			}
		}
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
	sums := lightSums{dayLo: math.Inf(1), dayHi: math.Inf(-1), dimmest: -1}
	for d := range length {
		t := sow.AddDate(0, 0, d)
		if r.sun != nil {
			sums.add(r, t)
		}
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

	sums.n = n
	lightF := light(c, r, &s, sums, sow, note)

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
	s.Score = int(math.Round(100 * tempF * frostF * waterF * soilF * lightF * seasonF))
	return s
}

// lightSums adds up a season's sun, day by day.
type lightSums struct {
	n                       float64 // days
	sunMJ, sunHours, slopeF float64
	dayLo, dayHi            float64
	dimmest                 int // month index, -1 for none yet
}

func (l *lightSums) add(r Request, t time.Time) {
	sm := r.sun[t.Month()-1]
	l.sunMJ += sm.SlopeMJ
	l.sunHours += sm.SunHours
	l.slopeF += sm.SlopeFactor
	if l.dimmest < 0 || sm.SlopeMJ < r.sun[l.dimmest].SlopeMJ {
		l.dimmest = int(t.Month()) - 1
	}
	dl := sun.DayLength(r.Climate.Point.Lat, t.YearDay())
	l.dayLo, l.dayHi = min(l.dayLo, dl), max(l.dayHi, dl)
}

// light scores a season's sun for c and notes what matters: a dull season
// for a sun crop, strong sun for a shade crop, a slope that gains or loses
// light, and the day length a crop's flowering follows. It fills s.Light
// from the season's sums.
func light(c Crop, r Request, s *Season, l lightSums, sow time.Time, note func(risk bool, id, format string, a ...any)) float64 {
	n, dayLo, dayHi, dimmest := l.n, l.dayLo, l.dayHi, l.dimmest
	s.Light = LightBalance{Want: c.LightNeed(), SlopeFactor: 1}
	if r.sun == nil {
		return 1
	}
	s.Light.DayLengthH = Range{math.Round(dayLo*10) / 10, math.Round(dayHi*10) / 10}
	s.Light.SlopeFactor = math.Round(l.slopeF/n*100) / 100
	f := 1.0
	if r.Climate.HasSun() {
		mean := l.sunMJ / n
		s.Light.SunMJ = math.Round(mean*10) / 10
		s.Light.SunHours = math.Round(l.sunHours/n*10) / 10
		s.Light.DimmestMonth = time.Month(dimmest + 1).String()
		s.Light.DimmestMJ = r.sun[dimmest].SlopeMJ
		switch c.LightNeed() {
		case LightFull, LightPart:
			good, poor := fullSunGood, fullSunPoor
			if c.LightNeed() == LightPart {
				good, poor = partSunGood, partSunPoor
			}
			// Within lightSlack of good the crop barely notices: no risk.
			if mean >= good-lightSlack {
				note(false, "sun.ok", "Sunny enough: about %.0f MJ/m² a day on the ground (%.1f hours of bright sun) through the season.", mean, s.Light.SunHours)
			} else {
				f = 0.8 + 0.2*clamp((mean-poor)/(good-poor))
				note(true, "sun.low", "A dull season for it: about %.0f MJ/m² a day (%.1f hours of bright sun), least in %s; it wants %.0f or more, so expect a smaller crop and more fungus.", mean, s.Light.SunHours, s.Light.DimmestMonth, good)
			}
			if c.LightNeed() == LightPart {
				note(false, "sun.part", "It does well in part shade: it fits under or between trees.")
			}
		case LightShade:
			if mean > shadeSunMost {
				f = 0.7
				note(true, "sun.strong", "Too much sun for it in the open (about %.0f MJ/m² a day): grow it under trees such as coconut or arecanut, or under a shade net.", mean)
			} else {
				note(false, "sun.shade", "A soft enough sun for it (about %.0f MJ/m² a day); it still grows best under some shade.", mean)
			}
		}
	}
	if sf := s.Light.SlopeFactor; r.Slope.TiltDeg > 0 && math.Abs(sf-1) >= slopeNote {
		facing := geo.CompassWords(r.Slope.AspectDeg)
		if sf < 1 {
			note(true, "sun.slope", "The plot's slope faces %s: it gets about %.0f%% less sun than flat ground over this season.", facing, (1-sf)*100)
		} else {
			note(false, "sun.slope", "The plot's slope faces %s: it gets about %.0f%% more sun than flat ground over this season.", facing, (sf-1)*100)
		}
	}
	lat := r.Climate.Point.Lat
	shortening := sun.DayLength(lat, sow.YearDay()+7) < sun.DayLength(lat, sow.YearDay())
	switch c.DayHabit() {
	case DayShort:
		if shortening && dayHi < 12 {
			note(true, "day.short", "Days are already short and shortening at this sowing (%.1f to %.1f h): it is a short-day crop, so it flowers early and small; sow a variety bred for the late season.", dayLo, dayHi)
		} else {
			note(false, "day.short", "Days run %.1f to %.1f h; it is a short-day crop and flowers as they shorten, so sowing on time matters.", dayLo, dayHi)
		}
	case DayLong:
		note(false, "day.long", "Days run %.1f to %.1f h; it responds to long days, so choose a variety bred for Indian day lengths.", dayLo, dayHi)
	}
	return f
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
