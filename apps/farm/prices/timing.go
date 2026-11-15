package prices

import (
	"fmt"
	"math"
	"slices"
	"time"
)

// Tuning holds the thresholds of the timing advice and its backtest; the
// zero value is the default. They are starter values, explained in
// apps/farm/docs/PRICES.md ("When to sow and sell").
type Tuning struct {
	// MinGainPct is the least expected gain, in percent of the harvest
	// month's price, that advice to wait or to shift must promise (default
	// 3).
	MinGainPct float64 `json:"min_gain_pct,omitempty"`
	// UncertaintyWeight is how much of the index's spread (the months'
	// half quartile ranges, combined) is added to that margin (default
	// 0.5).
	UncertaintyWeight float64 `json:"uncertainty_weight,omitempty"`
	// YieldRiskPctPerWeek is the yield given up for each week a sowing
	// moves away from the best agronomic date (default 2).
	YieldRiskPctPerWeek float64 `json:"yield_risk_pct_per_week,omitempty"`
	// VarietyRiskPct is the yield given up for a shorter or longer variety
	// than the crop's usual (default 3).
	VarietyRiskPct float64 `json:"variety_risk_pct,omitempty"`
	// MinBacktestYears is the fewest past years a rule must have been
	// tested on before its advice is shown (default 3).
	MinBacktestYears int `json:"min_backtest_years,omitempty"`
	// MinTrainYears is the fewest years an index needs (default 2;
	// [IndexOptions.MinYears]).
	MinTrainYears int `json:"min_train_years,omitempty"`
}

func (t Tuning) withDefaults() Tuning {
	if t.MinGainPct <= 0 {
		t.MinGainPct = 3
	}
	if t.UncertaintyWeight <= 0 {
		t.UncertaintyWeight = 0.5
	}
	if t.YieldRiskPctPerWeek <= 0 {
		t.YieldRiskPctPerWeek = 2
	}
	if t.VarietyRiskPct <= 0 {
		t.VarietyRiskPct = 3
	}
	if t.MinBacktestYears <= 0 {
		t.MinBacktestYears = 3
	}
	if t.MinTrainYears <= 0 {
		t.MinTrainYears = 2
	}
	return t
}

// option is one choice a timing decision weighs: harvest or sell offset
// months after the reference month, with yield the share of the reference
// harvest it keeps, held store months in store. Option 0 of every decision
// is the reference itself: offset 0, yield 1, no storage.
type option struct {
	offset int
	yield  float64
	store  int
}

// costs are the costs of holding a harvest.
type costs struct{ lossPct, costPerQ, interestPct float64 }

func costsOf(s Storage) costs {
	return costs{s.LossPctPerMonth, s.CostPerQMonth, s.InterestPctPerMonth}
}

// value is what option o brings for each rupee the reference brings, when
// its month's price is r times the reference month's price p.
func (c costs) value(o option, r, p float64) float64 {
	v := r * o.yield * math.Pow(1-c.lossPct/100, float64(o.store))
	if o.store > 0 && p > 0 {
		v -= c.costPerQ * float64(o.store) / p
	}
	return v - (math.Pow(1+c.interestPct/100, float64(o.store)) - 1)
}

// judged is a decision's view of each option from an index: its expected
// gain over option 0 and the margin it must clear (both as shares of the
// reference's value), and whether the index knows its month.
type judged struct {
	gain, margin float64
	known        bool
}

// decide picks, of the options whose expected gain clears their margin, the
// one with the best gain; option 0 when none clears it.
func decide(idx SeasonalIndex, ref time.Month, opts []option, c costs, p float64, t Tuning) (int, []judged) {
	base := idx.At(ref)
	js := make([]judged, len(opts))
	best, bestGain := 0, 0.0
	for i, o := range opts {
		m := idx.At(monthPlus(ref, o.offset))
		if !base.Known() || !m.Known() {
			continue
		}
		u := math.Hypot(m.Spread(), base.Spread())
		if o.offset == 0 {
			u = 0
		}
		j := judged{gain: c.value(o, m.Index/base.Index, p) - 1, margin: t.MinGainPct/100 + t.UncertaintyWeight*u, known: true}
		js[i] = j
		if i > 0 && j.gain > j.margin && j.gain > bestGain {
			best, bestGain = i, j.gain
		}
	}
	return best, js
}

func monthPlus(m time.Month, n int) time.Month {
	return time.Month(((int(m)-1+n)%12+12)%12 + 1)
}

// SowingWindow is the agronomically safe run of sowing dates, and the best
// date in it (zero for its middle).
type SowingWindow struct {
	From Day `json:"from"`
	To   Day `json:"to"`
	Best Day `json:"best,omitzero"`
}

// Valid returns the window with its best date set, or an error.
func (w SowingWindow) valid() (SowingWindow, error) {
	if w.From.IsZero() || w.To.IsZero() || w.To.Before(w.From) {
		return w, fmt.Errorf("%w: sowing window %s to %s", ErrInvalid, w.From, w.To)
	}
	if w.Best.IsZero() {
		w.Best = w.From.AddDays(w.To.DaysSince(w.From) / 2)
	}
	if w.Best.Before(w.From) || w.To.Before(w.Best) {
		return w, fmt.Errorf("%w: best sowing date %s outside the window", ErrInvalid, w.Best)
	}
	return w, nil
}

// SowingOption is one sowing date and crop duration, with what its harvest
// month promises against the best agronomic date's.
type SowingOption struct {
	Sow          Day        `json:"sow"`
	Days         int        `json:"days"`
	Harvest      Day        `json:"harvest"`
	HarvestMonth time.Month `json:"harvest_month"`
	// Index is the harvest month's seasonal index.
	Index float64 `json:"index"`
	// YieldRiskPct is the yield given up for leaving the best date or the
	// usual variety.
	YieldRiskPct float64 `json:"yield_risk_pct"`
	// ExpectedPct is the expected revenue against the best date's, in
	// percent: price times yield (+4.5 is 4.5% more).
	ExpectedPct float64 `json:"expected_pct"`
	// MarginPct is what ExpectedPct must pass to be advised.
	MarginPct float64 `json:"margin_pct"`
}

// SowingAdvice says whether to move the sowing for a better harvest month.
type SowingAdvice struct {
	// Shift is true when moving the sowing (or the variety) is advised;
	// Sow, Days and Harvest are then the advice, else the best agronomic
	// date's.
	Shift   bool `json:"shift"`
	Sow     Day  `json:"sow"`
	Days    int  `json:"days"`
	Harvest Day  `json:"harvest"`
	// GainPct is the advised option's expected gain, MarginPct the margin
	// it cleared (0 without a shift).
	GainPct   float64 `json:"gain_pct"`
	MarginPct float64 `json:"margin_pct"`
	// Options are the sowings weighed, one per harvest month, best
	// expected first.
	Options []SowingOption `json:"options"`
	Reasons []string       `json:"reasons"`
}

type sowChoice struct {
	SowingOption
	opt option
}

// sowChoices lists the sowings in the window (every week, and the best
// date) and the crop's durations (shortest, usual, longest), and keeps per
// harvest month the one that gives up the least yield. The best date with
// the usual duration comes first.
func sowChoices(daysMin, daysMax int, w SowingWindow, t Tuning) []sowChoice {
	mid := int(math.Round(float64(daysMin+daysMax) / 2))
	durations := slices.Compact([]int{mid, daysMin, daysMax})
	baseHarvest := w.Best.AddDays(mid)
	dates := []Day{w.Best}
	for d := w.From; !w.To.Before(d); d = d.AddDays(7) {
		if d != w.Best {
			dates = append(dates, d)
		}
	}
	if w.To != w.Best && w.To.DaysSince(w.From)%7 != 0 {
		dates = append(dates, w.To)
	}
	byMonth := map[int]sowChoice{}
	var order []int
	for _, sow := range dates {
		for _, days := range durations {
			risk := t.YieldRiskPctPerWeek * math.Abs(float64(sow.DaysSince(w.Best))) / 7
			if days != mid {
				risk += t.VarietyRiskPct
			}
			risk = math.Min(risk, 90)
			h := sow.AddDays(days)
			off := MonthOf(h).Since(MonthOf(baseHarvest))
			c := sowChoice{SowingOption{Sow: sow, Days: days, Harvest: h, HarvestMonth: h.Time().Month(), YieldRiskPct: round1(risk)}, option{offset: off, yield: 1 - risk/100}}
			old, ok := byMonth[off]
			if !ok {
				order = append(order, off)
			}
			if !ok || c.opt.yield > old.opt.yield {
				byMonth[off] = c
			}
		}
	}
	out := make([]sowChoice, 0, len(order))
	for _, off := range order {
		out = append(out, byMonth[off])
	}
	// The reference: the best date with the usual duration, at yield 1.
	out[0].opt.yield, out[0].YieldRiskPct = 1, 0
	return out
}

// AdviseSowing ranks the sowings in a safe window (and the crop's shorter
// and longer durations, daysMin to daysMax) by their harvest month's
// seasonal index, less the yield they give up, and advises a shift only
// when the best one's expected gain clears [Tuning.MinGainPct] plus the
// index's uncertainty. Otherwise it says to sow at the best agronomic date.
func AdviseSowing(idx SeasonalIndex, daysMin, daysMax int, w SowingWindow, t Tuning) (SowingAdvice, error) {
	t = t.withDefaults()
	if daysMin <= 0 || daysMax < daysMin {
		return SowingAdvice{}, fmt.Errorf("%w: crop days %d to %d", ErrInvalid, daysMin, daysMax)
	}
	w, err := w.valid()
	if err != nil {
		return SowingAdvice{}, err
	}
	cs := sowChoices(daysMin, daysMax, w, t)
	base := cs[0]
	opts := make([]option, len(cs))
	for i, c := range cs {
		opts[i] = c.opt
	}
	pick, js := decide(idx, base.HarvestMonth, opts, costs{}, 0, t)
	if !js[0].known {
		return SowingAdvice{}, fmt.Errorf("%w: the index has no %s, the best date's harvest month", ErrTooLittleHistory, base.HarvestMonth)
	}
	a := SowingAdvice{Sow: base.Sow, Days: base.Days, Harvest: base.Harvest}
	for i, c := range cs {
		if !js[i].known {
			continue
		}
		o := c.SowingOption
		o.Index = idx.At(o.HarvestMonth).Index
		o.ExpectedPct, o.MarginPct = round1(js[i].gain*100), round1(js[i].margin*100)
		if i == 0 {
			o.MarginPct = 0
		}
		a.Options = append(a.Options, o)
	}
	slices.SortStableFunc(a.Options, func(x, y SowingOption) int { return cmpFloat(y.ExpectedPct, x.ExpectedPct) })
	bm := idx.At(base.HarvestMonth)
	if pick > 0 {
		p := cs[pick]
		a.Shift, a.Sow, a.Days, a.Harvest = true, p.Sow, p.Days, p.Harvest
		a.GainPct, a.MarginPct = round1(js[pick].gain*100), round1(js[pick].margin*100)
		a.Reasons = append(a.Reasons,
			fmt.Sprintf("Sowing on %s with a %d-day variety brings the harvest into %s, when prices usually run at %.0f%% of the year's average, against %.0f%% in %s for the best sowing date (%s).",
				p.Sow, p.Days, p.HarvestMonth, idx.At(p.HarvestMonth).Index*100, bm.Index*100, base.HarvestMonth, base.Sow),
			fmt.Sprintf("After the %.1f%% of yield that sowing gives up, it is expected to bring %.1f%% more, past the %.1f%% margin for how much the months vary from year to year.", p.YieldRiskPct, a.GainPct, a.MarginPct))
	} else {
		a.Reasons = append(a.Reasons, fmt.Sprintf("Sow at the best agronomic date (%s): no other date in the window or variety promises a harvest month enough dearer to pay for the yield it risks (prices in %s usually run at %.0f%% of the year's average).", base.Sow, base.HarvestMonth, bm.Index*100))
	}
	a.Reasons = append(a.Reasons, "A usual year only: rain, the area sown and export rules move a season's prices more than any history shows.")
	return a, nil
}

// SellMonth is one month on a held harvest's expected path.
type SellMonth struct {
	Month       time.Month `json:"month"`
	MonthsAhead int        `json:"months_ahead"`
	// Price is the expected price, Low and High a usual year's range, in
	// rupees a quintal.
	Price float64 `json:"price"`
	Low   float64 `json:"low"`
	High  float64 `json:"high"`
	// NetPerQ is what a quintal put in store brings then, after the loss
	// in store, the storage cost and the interest.
	NetPerQ float64 `json:"net_per_q"`
	// GainPct is NetPerQ against selling now, in percent.
	GainPct float64 `json:"gain_pct"`
}

// SellAdvice says whether to sell now or hold, and until when.
type SellAdvice struct {
	SellNow     bool       `json:"sell_now"`
	Month       time.Month `json:"month"`
	MonthsAhead int        `json:"months_ahead"`
	// From is the month the advice starts from and Price its price level
	// (rupees a quintal).
	From      time.Month  `json:"from"`
	Price     float64     `json:"price"`
	GainPct   float64     `json:"gain_pct"`
	MarginPct float64     `json:"margin_pct"`
	Path      []SellMonth `json:"path"`
	Storage   Storage     `json:"storage"`
	Reasons   []string    `json:"reasons"`
}

func sellOptions(st Storage) []option {
	opts := make([]option, st.MaxMonths+1)
	for k := range opts {
		opts[k] = option{offset: k, yield: 1, store: k}
	}
	return opts
}

// AdviseSell weighs selling in month from at price (rupees a quintal)
// against holding a storable crop month by month up to its
// [Storage.MaxMonths], each month's expected price being price moved by
// the seasonal index, less the loss in store, the storage cost and the
// interest. It advises holding only when the best month's net gain clears
// [Tuning.MinGainPct] plus the index's uncertainty; otherwise to sell now.
func AdviseSell(idx SeasonalIndex, from time.Month, price float64, st Storage, t Tuning) (SellAdvice, error) {
	t = t.withDefaults()
	if from < time.January || from > time.December || !(price > 0) {
		return SellAdvice{}, fmt.Errorf("%w: month %d, price %v", ErrInvalid, from, price)
	}
	a := SellAdvice{SellNow: true, Month: from, From: from, Price: round0(price), Storage: st}
	if !st.Storable() {
		a.Reasons = []string{"It does not keep: sell when it is ready."}
		return a, nil
	}
	base := idx.At(from)
	if !base.Known() {
		return SellAdvice{}, fmt.Errorf("%w: the index has no %s", ErrTooLittleHistory, from)
	}
	opts := sellOptions(st)
	c := costsOf(st)
	pick, js := decide(idx, from, opts, c, price, t)
	for k, o := range opts {
		if !js[k].known || k == 0 {
			continue
		}
		mi := idx.At(monthPlus(from, k))
		r := mi.Index / base.Index
		a.Path = append(a.Path, SellMonth{
			Month: monthPlus(from, k), MonthsAhead: k,
			Price: round0(price * r), Low: round0(price * mi.Low / base.Index), High: round0(price * mi.High / base.Index),
			NetPerQ: round0(price * c.value(o, r, price)), GainPct: round1(js[k].gain * 100),
		})
	}
	if pick > 0 {
		a.SellNow, a.Month, a.MonthsAhead = false, monthPlus(from, pick), pick
		a.GainPct, a.MarginPct = round1(js[pick].gain*100), round1(js[pick].margin*100)
		a.Reasons = append(a.Reasons, fmt.Sprintf("Holding till %s usually pays: about %s a quintal then against %s now, %.1f%% more after %.1f%% a month lost in store, %s a quintal a month for storage and %.1f%% a month interest; that clears the %.1f%% margin for how much the months vary.",
			a.Month, INR(price*idx.At(a.Month).Index/base.Index), INR(price), a.GainPct, st.LossPctPerMonth, INR(st.CostPerQMonth), st.InterestPctPerMonth, a.MarginPct))
	} else {
		a.Reasons = append(a.Reasons, fmt.Sprintf("Sell now: in a usual year no later month pays enough more than %s to cover the loss in store, the storage cost and the interest with a margin to spare.", INR(price)))
	}
	a.Reasons = append(a.Reasons, "Storage costs are starter values ("+st.Class+"); a warehouse receipt or your own store may cost less or more.")
	return a, nil
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
