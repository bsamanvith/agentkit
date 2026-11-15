package prices

import (
	"fmt"
	"math"
	"time"
)

// BacktestResult is how a timing rule did over past years, replayed as if
// each year were the present: the index built only from the years before,
// the advice made from it, and scored against what prices did.
type BacktestResult struct {
	Commodity string `json:"commodity,omitempty"`
	Area      string `json:"area,omitempty"`
	// Decision is "sell" (sell or hold from the harvest month) or "sow"
	// (which harvest month to sow for).
	Decision string `json:"decision"`
	// Reference is the harvest month the rule starts from.
	Reference time.Month     `json:"reference"`
	Years     []BacktestYear `json:"years"`
	// N is the years scored.
	N int `json:"n"`
	// MeanGainVsHarvestPct and MeanGainVsNaivePct are the advice's mean
	// gain over selling in the harvest month (or sowing at the best date)
	// and over doing what would have been best last year, in percent of
	// the harvest month's price.
	MeanGainVsHarvestPct float64 `json:"mean_gain_vs_harvest_pct"`
	MeanGainVsNaivePct   float64 `json:"mean_gain_vs_naive_pct"`
	// Wins and losses count the years the advice did strictly better and
	// strictly worse than each baseline; HitRate is wins over N.
	WinsVsHarvest, LossesVsHarvest int     `json:"-"`
	WinsVsNaive, LossesVsNaive     int     `json:"-"`
	HitRateVsHarvest               float64 `json:"hit_rate_vs_harvest"`
	HitRateVsNaive                 float64 `json:"hit_rate_vs_naive"`
	// MAPEPct is the mean absolute percentage error of the index's
	// forecast of each later month's price from the harvest month's, and
	// NaiveMAPEPct that of last year's shape ("same as last year").
	MAPEPct      float64 `json:"mape_pct"`
	NaiveMAPEPct float64 `json:"naive_mape_pct"`
	// Passed is the gate: advice is shown only when it is true.
	Passed  bool     `json:"passed"`
	Reasons []string `json:"reasons"`
}

// BacktestYear is one replayed year.
type BacktestYear struct {
	Year int `json:"year"`
	// Advice, Naive and Best are the chosen months' offsets from the
	// reference month: the rule's, last year's best, and this year's
	// best in hindsight.
	Advice int `json:"advice"`
	Naive  int `json:"naive"`
	Best   int `json:"best"`
	// TrainYears is how many years the index rested on.
	TrainYears       int     `json:"train_years"`
	GainVsHarvestPct float64 `json:"gain_vs_harvest_pct"`
	GainVsNaivePct   float64 `json:"gain_vs_naive_pct"`
	MAPEPct          float64 `json:"mape_pct"`
	NaiveMAPEPct     float64 `json:"naive_mape_pct"`
}

// BacktestSell replays sell-or-hold from the harvest month of every past
// year in series: the advice ([AdviseSell]'s rule) against selling in the
// harvest month and against holding as long as would have paid best last
// year.
func BacktestSell(series []MonthlyPrice, harvest time.Month, st Storage, t Tuning) BacktestResult {
	r := BacktestResult{Decision: "sell", Reference: harvest}
	if !st.Storable() {
		r.Reasons = []string{"It does not keep, so there is no holding to test."}
		return r
	}
	return backtest(r, series, 0, sellOptions(st), costsOf(st), t)
}

// BacktestSowing replays the harvest-month advice ([AdviseSowing]'s rule)
// for every past year in series, the window moved to that year: the advice
// against sowing at the best date and against sowing for the harvest month
// that paid best last year. Each year's index is built from the prices
// before that year's best sowing date.
func BacktestSowing(series []MonthlyPrice, daysMin, daysMax int, w SowingWindow, t Tuning) (BacktestResult, error) {
	t = t.withDefaults()
	if daysMin <= 0 || daysMax < daysMin {
		return BacktestResult{}, fmt.Errorf("%w: crop days %d to %d", ErrInvalid, daysMin, daysMax)
	}
	w, err := w.valid()
	if err != nil {
		return BacktestResult{}, err
	}
	cs := sowChoices(daysMin, daysMax, w, t)
	opts := make([]option, len(cs))
	for i, c := range cs {
		opts[i] = c.opt
	}
	lead := MonthOf(cs[0].Harvest).Since(MonthOf(w.Best))
	r := BacktestResult{Decision: "sow", Reference: cs[0].HarvestMonth}
	return backtest(r, series, lead, opts, costs{}, t), nil
}

func backtest(r BacktestResult, series []MonthlyPrice, lead int, opts []option, c costs, t Tuning) BacktestResult {
	t = t.withDefaults()
	price := map[Month]float64{}
	for _, p := range series {
		if p.Price > 0 {
			price[p.Month] = p.Price
		}
	}
	if len(series) == 0 {
		r.Reasons = []string{"No prices to test on."}
		return r
	}
	first, last := series[0].Year, series[len(series)-1].Year
	// prices of every option counted from ref, or false if one is missing.
	ratios := func(ref Month) ([]float64, float64, bool) {
		p0, ok := price[ref]
		if !ok {
			return nil, 0, false
		}
		out := make([]float64, len(opts))
		for i, o := range opts {
			p, ok := price[ref.Add(o.offset)]
			if !ok {
				return nil, 0, false
			}
			out[i] = p / p0
		}
		return out, p0, true
	}
	argmax := func(rs []float64, p0 float64) int {
		best := 0
		for i, o := range opts {
			if c.value(o, rs[i], p0) > c.value(opts[best], rs[best], p0) {
				best = i
			}
		}
		return best
	}
	var sumH, sumN, sumM, sumNM float64
	for y := first; y <= last; y++ {
		ref := Month{y, r.Reference}
		decided := ref.Add(-lead)
		actual, p0, ok := ratios(ref)
		if !ok {
			continue
		}
		prev, pp, ok := ratios(ref.Add(-12))
		if !ok {
			continue
		}
		// Last year's outcome must be known when deciding.
		lastMonth := ref.Add(-12)
		for _, o := range opts {
			lastMonth = maxMonth(lastMonth, ref.Add(-12+o.offset))
		}
		if !lastMonth.Before(decided) {
			continue
		}
		idx, err := NewSeasonalIndex(series, IndexOptions{MinYears: t.MinTrainYears, Before: decided})
		if err != nil {
			continue
		}
		adv, js := decide(idx, r.Reference, opts, c, p0, t)
		if !js[0].known {
			continue
		}
		naive := argmax(prev, pp)
		by := BacktestYear{Year: y, Advice: opts[adv].offset, Naive: opts[naive].offset, Best: opts[argmax(actual, p0)].offset, TrainYears: idx.Years}
		v := func(i int) float64 { return c.value(opts[i], actual[i], p0) }
		gh, gn := v(adv)-v(0), v(adv)-v(naive)
		by.GainVsHarvestPct, by.GainVsNaivePct = round1(gh*100), round1(gn*100)
		const eps = 1e-9
		switch {
		case gh > eps:
			r.WinsVsHarvest++
		case gh < -eps:
			r.LossesVsHarvest++
		}
		switch {
		case gn > eps:
			r.WinsVsNaive++
		case gn < -eps:
			r.LossesVsNaive++
		}
		// The month forecasts: each option's month from the reference's.
		var e, en float64
		k := 0
		base := idx.At(r.Reference)
		for i, o := range opts {
			if o.offset == 0 || !js[i].known {
				continue
			}
			pred := idx.At(monthPlus(r.Reference, o.offset)).Index / base.Index
			e += math.Abs(pred-actual[i]) / actual[i]
			en += math.Abs(prev[i]-actual[i]) / actual[i]
			k++
		}
		if k > 0 {
			by.MAPEPct, by.NaiveMAPEPct = round1(e/float64(k)*100), round1(en/float64(k)*100)
		}
		sumH, sumN, sumM, sumNM = sumH+gh, sumN+gn, sumM+by.MAPEPct, sumNM+by.NaiveMAPEPct
		r.Years = append(r.Years, by)
	}
	r.N = len(r.Years)
	if r.N > 0 {
		n := float64(r.N)
		r.MeanGainVsHarvestPct, r.MeanGainVsNaivePct = round1(sumH/n*100), round1(sumN/n*100)
		r.HitRateVsHarvest, r.HitRateVsNaive = round3(float64(r.WinsVsHarvest)/n), round3(float64(r.WinsVsNaive)/n)
		r.MAPEPct, r.NaiveMAPEPct = round1(sumM/n), round1(sumNM/n)
	}
	r.Passed, r.Reasons = gate(r, t)
	return r
}

func maxMonth(a, b Month) Month {
	if a.Before(b) {
		return b
	}
	return a
}

// Gate reports whether a backtest earns its advice a place in front of the
// farmer: at least [Tuning.MinBacktestYears] years tested, a mean gain
// over both selling at harvest (or sowing at the best date) and "same as
// last year", and no more years lost than won against the first. Against
// last year's choice only the mean counts: it often picks the same month,
// and when it does not, one wrong guess costs it a lot.
func Gate(r BacktestResult, t Tuning) bool {
	ok, _ := gate(r, t)
	return ok
}

func gate(r BacktestResult, t Tuning) (bool, []string) {
	t = t.withDefaults()
	what := "selling in the harvest month"
	if r.Decision == "sow" {
		what = "sowing at the best date"
	}
	switch {
	case r.N < t.MinBacktestYears:
		return false, []string{fmt.Sprintf("Tested on %d past year(s); %d are needed before the advice is shown.", r.N, t.MinBacktestYears)}
	case r.MeanGainVsHarvestPct <= 0 || r.LossesVsHarvest > r.WinsVsHarvest:
		return false, []string{fmt.Sprintf("Over %d past years the rule did not beat %s (mean %+.1f%%, %d year(s) better, %d worse).", r.N, what, r.MeanGainVsHarvestPct, r.WinsVsHarvest, r.LossesVsHarvest)}
	case r.MeanGainVsNaivePct <= 0:
		return false, []string{fmt.Sprintf("Over %d past years the rule did not beat doing what paid best the year before (mean %+.1f%%, %d year(s) better, %d worse).", r.N, r.MeanGainVsNaivePct, r.WinsVsNaive, r.LossesVsNaive)}
	}
	return true, []string{fmt.Sprintf("Tested on %d past years, each with only the years before it: %+.1f%% a year on average over %s (better in %d, worse in %d) and %+.1f%% over doing what paid best the year before.",
		r.N, r.MeanGainVsHarvestPct, what, r.WinsVsHarvest, r.LossesVsHarvest, r.MeanGainVsNaivePct)}
}
