package prices_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/prices"
	"github.com/bsamanvith/agentkit/apps/farm/prices/synthetic"
)

// Every price in these tests is synthetic (synthetic), with a known
// season, so the right answer is known.

func dip(seed uint64, years int, noise float64) synthetic.Series {
	return synthetic.Series{
		Commodity: "Groundnut", State: "Andhra Pradesh", District: "Kurnool", Markets: 2,
		From: synthetic.Start, Years: years, Base: 5000, Season: synthetic.PostHarvestDip,
		TrendPct: 4, Noise: noise, MarketNoise: 0.02, Seed: seed,
	}
}

// checkIndex holds for every index: known months average 1, each lies in
// its quartiles, the years and confidence agree, and it survives JSON.
func checkIndex(t *testing.T, idx prices.SeasonalIndex) {
	t.Helper()
	sum, n := 0.0, 0
	for m, mi := range idx.Months {
		if !mi.Known() {
			continue
		}
		sum += mi.Index
		n++
		if mi.Low > mi.Index+1e-9 || mi.Index > mi.High+1e-9 || mi.Years < idx.Years {
			t.Errorf("month %d: %+v (index years %d)", m+1, mi, idx.Years)
		}
	}
	if n < prices.MinIndexMonths || math.Abs(sum/float64(n)-1) > 0.005 {
		t.Errorf("%d known months averaging %.4f; want >= %d averaging 1", n, sum/float64(n), prices.MinIndexMonths)
	}
	if !slices.Contains([]string{"good", "fair", "low"}, idx.Confidence) || idx.Confidence == "good" && idx.Years < 5 {
		t.Errorf("confidence %q with %d years", idx.Confidence, idx.Years)
	}
	roundTrip(t, idx)
}

func roundTrip[T any](t *testing.T, v T) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var back T
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	b2, _ := json.Marshal(back)
	if string(b) != string(b2) {
		t.Errorf("JSON round trip changed it:\n%s\n%s", b, b2)
	}
}

func TestSeasonalIndexRecoversAKnownSeason(t *testing.T) {
	t.Parallel()
	for seed := range uint64(5) {
		idx, err := prices.NewSeasonalIndex(dip(seed, 9, 0.04).Monthly(), prices.IndexOptions{})
		if err != nil {
			t.Fatal(err)
		}
		checkIndex(t, idx)
		for m, want := range synthetic.PostHarvestDip {
			if got := idx.Months[m].Index; math.Abs(got-want) > 0.04 {
				t.Errorf("seed %d, month %d: index %.3f, want %.2f", seed, m+1, got, want)
			}
		}
		if idx.Years != 8 || idx.Confidence != "good" {
			t.Errorf("seed %d: %d years, %s; want 8 years (the moving average needs half a year each end), good", seed, idx.Years, idx.Confidence)
		}
	}
}

func TestSeasonalIndexShrugsOffOneWildYearAndFillsShortGaps(t *testing.T) {
	t.Parallel()
	s := dip(7, 9, 0.03)
	clean, err := prices.NewSeasonalIndex(s.Monthly(), prices.IndexOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// One July three times dearer than usual, and two months missing.
	wild := s.Monthly()
	for i := range wild {
		if wild[i].Month == (prices.Month{Year: 2014, Month: time.July}) {
			wild[i].Price *= 3
		}
	}
	wild = slices.DeleteFunc(wild, func(p prices.MonthlyPrice) bool {
		return p.Month == prices.Month{Year: 2016, Month: time.March} || p.Month == prices.Month{Year: 2016, Month: time.April}
	})
	got, err := prices.NewSeasonalIndex(wild, prices.IndexOptions{})
	if err != nil {
		t.Fatal(err)
	}
	checkIndex(t, got)
	// The filled months help the moving average but are not ratios.
	if got.Months[time.March-1].Years != clean.Months[time.March-1].Years-1 || got.Months[time.May-1].Years != clean.Months[time.May-1].Years {
		t.Errorf("years behind March %d (was %d), May %d (was %d); a filled month must not count", got.Months[time.March-1].Years, clean.Months[time.March-1].Years, got.Months[time.May-1].Years, clean.Months[time.May-1].Years)
	}
	for m := range got.Months {
		if d := math.Abs(got.Months[m].Index - clean.Months[m].Index); d > 0.03 {
			t.Errorf("month %d moved %.3f with one wild July and a gap", m+1, d)
		}
	}
}

func TestSeasonalIndexRefusesThinHistoryAndMarksItLow(t *testing.T) {
	t.Parallel()
	s := dip(1, 4, 0.03).Monthly()
	if _, err := prices.NewSeasonalIndex(s[:20], prices.IndexOptions{}); !errors.Is(err, prices.ErrTooLittleHistory) {
		t.Fatalf("20 months: %v; want ErrTooLittleHistory", err)
	}
	if _, err := prices.NewSeasonalIndex(nil, prices.IndexOptions{}); !errors.Is(err, prices.ErrTooLittleHistory) {
		t.Fatalf("no prices: %v", err)
	}
	idx, err := prices.NewSeasonalIndex(s[:38], prices.IndexOptions{})
	if err != nil {
		t.Fatal(err)
	}
	checkIndex(t, idx)
	if idx.Years != 2 || idx.Confidence != "low" {
		t.Fatalf("38 months: %d years, %s; want 2, low", idx.Years, idx.Confidence)
	}
	// A crop traded only part of the year: too few months known.
	part := slices.DeleteFunc(dip(1, 8, 0.03).Monthly(), func(p prices.MonthlyPrice) bool { return p.Month.Month >= time.May && p.Month.Month <= time.August })
	if _, err := prices.NewSeasonalIndex(part, prices.IndexOptions{}); !errors.Is(err, prices.ErrTooLittleHistory) {
		t.Fatalf("four months never traded: %v", err)
	}
}

func TestSeasonalIndexUsesNothingFromOrAfterItsCutoff(t *testing.T) {
	t.Parallel()
	s := dip(3, 10, 0.05).Monthly()
	cut := prices.Month{Year: 2015, Month: time.October}
	a, err := prices.NewSeasonalIndex(s, prices.IndexOptions{Before: cut})
	if err != nil {
		t.Fatal(err)
	}
	scrambled := slices.Clone(s)
	for i := range scrambled {
		if !scrambled[i].Before(cut) {
			scrambled[i].Price *= float64(1 + i%5)
		}
	}
	b, err := prices.NewSeasonalIndex(scrambled, prices.IndexOptions{Before: cut})
	if err != nil {
		t.Fatal(err)
	}
	if a.Months != b.Months || a.To != (prices.Month{Year: 2015, Month: time.September}) {
		t.Fatalf("prices from %s on changed the index (to %s)", cut, a.To)
	}
}

// index makes an index by hand: each month's value and spread (half the
// quartile range over the index).
func index(vals [12]float64, spread float64) prices.SeasonalIndex {
	var idx prices.SeasonalIndex
	for m, v := range vals {
		idx.Months[m] = prices.MonthIndex{Index: v, Low: v * (1 - spread), High: v * (1 + spread), Years: 6}
	}
	idx.Years, idx.Confidence = 6, "good"
	return idx
}

func flat() [12]float64 { return [12]float64{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1} }

// checkSowing holds for every sowing advice: options best first, the
// reference among them at 0%, a shift only past its margin and within
// the window, reasons given.
func checkSowing(t *testing.T, a prices.SowingAdvice, w prices.SowingWindow) {
	t.Helper()
	if !slices.IsSortedFunc(a.Options, func(x, y prices.SowingOption) int { return -cmpF(x.ExpectedPct, y.ExpectedPct) }) {
		t.Errorf("options not best first: %+v", a.Options)
	}
	if !slices.ContainsFunc(a.Options, func(o prices.SowingOption) bool { return o.ExpectedPct == 0 && o.YieldRiskPct == 0 }) {
		t.Errorf("no reference option: %+v", a.Options)
	}
	if a.Shift && !(a.GainPct > a.MarginPct) || !a.Shift && a.GainPct != 0 {
		t.Errorf("shift %v with gain %.1f, margin %.1f", a.Shift, a.GainPct, a.MarginPct)
	}
	if a.Sow.Before(w.From) || w.To.Before(a.Sow) || a.Harvest != a.Sow.AddDays(a.Days) || len(a.Reasons) == 0 {
		t.Errorf("advice %+v outside window %+v or without reasons", a, w)
	}
	roundTrip(t, a)
}

func cmpF(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func TestAdviseSowingShiftsOnlyWhenTheHarvestMonthPaysForTheYieldRisk(t *testing.T) {
	t.Parallel()
	// Groundnut, 100 to 130 days, sown 15 June to 31 July, best 1 July:
	// the usual harvest falls in late October.
	w := prices.SowingWindow{From: prices.NewDay(2027, 6, 15), To: prices.NewDay(2027, 7, 31), Best: prices.NewDay(2027, 7, 1)}

	steep := flat()
	steep[time.October-1], steep[time.November-1], steep[time.December-1] = 0.80, 1.20, 1.20
	a, err := prices.AdviseSowing(index(steep, 0.03), 100, 130, w, prices.Tuning{})
	if err != nil {
		t.Fatal(err)
	}
	checkSowing(t, a, w)
	if !a.Shift || a.Harvest.Time().Month() < time.November || a.GainPct < 30 {
		t.Fatalf("a dearer November: %+v; want a shift into November or later", a)
	}

	for name, idx := range map[string]prices.SeasonalIndex{
		"flat":   index(flat(), 0.03),
		"modest": index(func() [12]float64 { v := flat(); v[time.November-1] = 1.06; return v }(), 0.03),
		"noisy":  index(steep, 0.8),
	} {
		a, err := prices.AdviseSowing(idx, 100, 130, w, prices.Tuning{})
		if err != nil {
			t.Fatal(err)
		}
		checkSowing(t, a, w)
		if a.Shift || a.Sow != w.Best || a.Days != 115 || !strings.Contains(a.Reasons[0], "best agronomic date") {
			t.Errorf("%s: %+v; want the best agronomic date", name, a)
		}
	}

	if _, err := prices.AdviseSowing(index(flat(), 0), 130, 100, w, prices.Tuning{}); !errors.Is(err, prices.ErrInvalid) {
		t.Errorf("days backwards: %v", err)
	}
	if _, err := prices.AdviseSowing(index(flat(), 0), 100, 130, prices.SowingWindow{From: w.To, To: w.From}, prices.Tuning{}); !errors.Is(err, prices.ErrInvalid) {
		t.Errorf("window backwards: %v", err)
	}
}

func checkSell(t *testing.T, a prices.SellAdvice) {
	t.Helper()
	for _, p := range a.Path {
		if p.NetPerQ > p.Price || p.Low > p.Price || p.Price > p.High || p.MonthsAhead < 1 || p.MonthsAhead > a.Storage.MaxMonths {
			t.Errorf("path month %+v", p)
		}
	}
	if a.SellNow != (a.MonthsAhead == 0) || !a.SellNow && !(a.GainPct > a.MarginPct) || len(a.Reasons) == 0 {
		t.Errorf("advice %+v", a)
	}
	roundTrip(t, a)
}

func TestAdviseSellHoldsOnlyWhenItPaysNetOfStorage(t *testing.T) {
	t.Parallel()
	idx := index(synthetic.PostHarvestDip, 0.03)
	a, err := prices.AdviseSell(idx, time.November, 6000, prices.StorageFor("groundnut"), prices.Tuning{})
	if err != nil {
		t.Fatal(err)
	}
	checkSell(t, a)
	if a.SellNow || a.Month != time.May || a.GainPct < 15 {
		t.Fatalf("groundnut from November: %+v; want to hold till May, the last month it keeps", a)
	}
	// The same months, but onion loses 8% a month in store; tomato does
	// not keep; a dear store eats the gain.
	dear := prices.StorageFor("groundnut")
	dear.CostPerQMonth = 400
	for name, st := range map[string]prices.Storage{"onion": prices.StorageFor("onion"), "tomato": prices.StorageFor("tomato"), "dear store": dear} {
		a, err := prices.AdviseSell(idx, time.November, 6000, st, prices.Tuning{})
		if err != nil {
			t.Fatal(err)
		}
		checkSell(t, a)
		if !a.SellNow {
			t.Errorf("%s: %+v; want sell now", name, a)
		}
	}
	if _, err := prices.AdviseSell(idx, time.November, 0, dear, prices.Tuning{}); !errors.Is(err, prices.ErrInvalid) {
		t.Errorf("no price: %v", err)
	}
}

// checkBacktest holds for every backtest: its sums agree with its years
// and the gate is the gate.
func checkBacktest(t *testing.T, r prices.BacktestResult) {
	t.Helper()
	if r.N != len(r.Years) || r.HitRateVsHarvest < 0 || r.HitRateVsHarvest > 1 || r.HitRateVsNaive < 0 || r.HitRateVsNaive > 1 {
		t.Errorf("result %+v", r)
	}
	var sum float64
	for _, y := range r.Years {
		sum += y.GainVsHarvestPct
		if y.TrainYears < 2 {
			t.Errorf("year %d advised from %d year(s)", y.Year, y.TrainYears)
		}
	}
	if r.N > 0 && math.Abs(sum/float64(r.N)-r.MeanGainVsHarvestPct) > 0.1 {
		t.Errorf("mean gain %.1f, years average %.2f", r.MeanGainVsHarvestPct, sum/float64(r.N))
	}
	if r.Passed != prices.Gate(r, prices.Tuning{}) || len(r.Reasons) == 0 {
		t.Errorf("passed %v, gate %v, reasons %v", r.Passed, prices.Gate(r, prices.Tuning{}), r.Reasons)
	}
	roundTrip(t, r)
}

func TestBacktestPassesASeasonThatHoldsAndFailsOneThatDoesNot(t *testing.T) {
	t.Parallel()
	st := prices.StorageFor("groundnut")
	w := prices.SowingWindow{From: prices.NewDay(2010, 6, 15), To: prices.NewDay(2010, 7, 31)}
	rev := synthetic.Reversed
	for _, tc := range []struct {
		name string
		s    synthetic.Series
		pass bool
	}{
		{"noisy dip", dip(2, 15, 0.15), true},
		{"no season", func() synthetic.Series { s := dip(3, 15, 0.05); s.Season = [12]float64{}; return s }(), false},
		{"season turns round", func() synthetic.Series {
			s := dip(4, 15, 0.04)
			s.Breaks = []synthetic.Break{{At: s.From.Add(7 * 12), Level: 1.3, Season: &rev}}
			return s
		}(), false},
	} {
		r := prices.BacktestSell(tc.s.Monthly(), time.October, st, prices.Tuning{})
		checkBacktest(t, r)
		if r.N < 10 || r.Passed != tc.pass {
			t.Errorf("%s: sell rule passed %v over %d years (%+v); want %v", tc.name, r.Passed, r.N, r, tc.pass)
		}
		sow, err := prices.BacktestSowing(tc.s.Monthly(), 100, 130, w, prices.Tuning{})
		if err != nil {
			t.Fatal(err)
		}
		checkBacktest(t, sow)
		if !tc.pass && sow.Passed {
			t.Errorf("%s: the sowing rule passed (%+v)", tc.name, sow)
		}
	}
	if r := prices.BacktestSell(dip(2, 15, 0.15).Monthly(), time.October, prices.StorageFor("tomato"), prices.Tuning{}); r.Passed || r.N != 0 {
		t.Errorf("a crop that does not keep: %+v", r)
	}
}

func TestBacktestAdvisesEachYearOnlyFromTheYearsBefore(t *testing.T) {
	t.Parallel()
	s := dip(5, 14, 0.1).Monthly()
	st := prices.StorageFor("groundnut")
	before := prices.BacktestSell(s, time.October, st, prices.Tuning{})
	// Scramble everything from October 2018: the advice for every year up
	// to and including 2018 must not change.
	cut := prices.Month{Year: 2018, Month: time.October}
	for i := range s {
		if !s[i].Before(cut) {
			s[i].Price *= 1 + float64(i%7)/3
		}
	}
	after := prices.BacktestSell(s, time.October, st, prices.Tuning{})
	n := 0
	for i, y := range before.Years {
		if y.Year > 2018 {
			break
		}
		n++
		if after.Years[i].Year != y.Year || after.Years[i].Advice != y.Advice || after.Years[i].Naive != y.Naive {
			t.Errorf("%d: advice %d, naive %d became %d, %d when later prices changed", y.Year, y.Advice, y.Naive, after.Years[i].Advice, after.Years[i].Naive)
		}
	}
	if n < 5 {
		t.Fatalf("only %d years before the cut", n)
	}
}

func TestGateNeedsEnoughYearsAndBothBaselinesBeaten(t *testing.T) {
	t.Parallel()
	good := prices.BacktestResult{Decision: "sell", N: 5, MeanGainVsHarvestPct: 4, MeanGainVsNaivePct: 1, WinsVsHarvest: 4, LossesVsHarvest: 1, WinsVsNaive: 2, LossesVsNaive: 1}
	if !prices.Gate(good, prices.Tuning{}) {
		t.Fatal("a rule that beat both over 5 years failed the gate")
	}
	for name, r := range map[string]func(prices.BacktestResult) prices.BacktestResult{
		"two years":             func(r prices.BacktestResult) prices.BacktestResult { r.N = 2; return r },
		"no gain vs harvest":    func(r prices.BacktestResult) prices.BacktestResult { r.MeanGainVsHarvestPct = 0; return r },
		"loses vs last year":    func(r prices.BacktestResult) prices.BacktestResult { r.MeanGainVsNaivePct = -0.5; return r },
		"more losses than wins": func(r prices.BacktestResult) prices.BacktestResult { r.LossesVsHarvest = 5; return r },
	} {
		if prices.Gate(r(good), prices.Tuning{}) {
			t.Errorf("%s passed the gate", name)
		}
	}
	if prices.Gate(good, prices.Tuning{MinBacktestYears: 6}) {
		t.Error("5 years passed a gate of 6")
	}
}

func saved(t *testing.T, ss ...synthetic.Series) prices.History {
	t.Helper()
	h := prices.NewMemoryHistory()
	for _, s := range ss {
		if _, err := h.Save(context.Background(), s.Quotes()); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func checkTiming(t *testing.T, a prices.TimingAdvice) {
	t.Helper()
	if len(a.Reasons) == 0 && a.Sowing == nil && a.Selling == nil {
		t.Error("neither advice nor a reason")
	}
	if a.Sowing != nil && (a.SowingTest == nil || !a.SowingTest.Passed) || a.Selling != nil && (a.SellingTest == nil || !a.SellingTest.Passed) {
		t.Errorf("advice shown without a passed backtest: %+v", a)
	}
	if a.Index != nil {
		checkIndex(t, *a.Index)
	}
	roundTrip(t, a)
}

func TestAdviseTimingGivesAdviceOrNothingWithReasons(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// 15 years of a noisy post-harvest dip in Kurnool, 2010 to 2024.
	h := saved(t, dip(2, 15, 0.15))
	q := prices.TimingQuery{
		Crop: "groundnut", State: "Andhra Pradesh", District: "Kurnool", DaysMin: 100, DaysMax: 130,
		Window: prices.SowingWindow{From: prices.NewDay(2024, 6, 15), To: prices.NewDay(2024, 7, 31)},
		Today:  prices.NewDay(2024, 11, 20),
	}
	// Harvest is in: sell or hold from today's price.
	sown := q
	sown.Window, sown.Sown = prices.SowingWindow{}, prices.NewDay(2024, 7, 1)
	a, err := prices.AdviseTiming(ctx, h, sown)
	if err != nil {
		t.Fatal(err)
	}
	checkTiming(t, a)
	if a.Area != "Kurnool district, Andhra Pradesh" || a.Selling == nil || a.Selling.SellNow || a.Selling.From != time.November || a.Sowing != nil {
		t.Fatalf("after harvest: %+v", a)
	}
	// Before sowing: the sowing rule is tested, and selling is planned
	// from the harvest month.
	before := q
	before.Today = prices.NewDay(2024, 5, 20)
	a, err = prices.AdviseTiming(ctx, h, before)
	if err != nil {
		t.Fatal(err)
	}
	checkTiming(t, a)
	if a.SowingTest == nil || a.Selling == nil || a.Selling.From != time.October {
		t.Fatalf("before sowing: %+v", a)
	}
	// Another district of the state has no prices: the state's are used.
	elsewhere := sown
	elsewhere.District = "Anantapur"
	if a, err = prices.AdviseTiming(ctx, h, elsewhere); err != nil || a.Area != "Andhra Pradesh" {
		t.Fatalf("Anantapur: %+v, %v", a, err)
	}
	// Two years of prices: no advice, and why.
	thin := saved(t, dip(2, 2, 0.15))
	a, err = prices.AdviseTiming(ctx, thin, prices.TimingQuery{Crop: "groundnut", State: "Andhra Pradesh", DaysMin: 100, DaysMax: 130, Today: prices.NewDay(2011, 12, 1)})
	if err != nil {
		t.Fatal(err)
	}
	checkTiming(t, a)
	if a.Sowing != nil || a.Selling != nil || a.Index != nil || !strings.Contains(a.Reasons[0], "Too few years") {
		t.Fatalf("thin history: %+v", a)
	}
	// A season with no pattern: tested, and no advice.
	none := dip(3, 15, 0.05)
	none.Season = [12]float64{}
	if a, err = prices.AdviseTiming(ctx, saved(t, none), sown); err != nil || a.Selling != nil || a.SellingTest == nil {
		t.Fatalf("no season: %+v, %v", a, err)
	}
	// Tomato does not keep.
	tom := dip(2, 15, 0.15)
	tom.Commodity = "Tomato"
	tq := sown
	tq.Crop = "tomato"
	if a, err = prices.AdviseTiming(ctx, saved(t, tom), tq); err != nil || a.Selling != nil || !strings.Contains(strings.Join(a.Reasons, " "), "does not keep") {
		t.Fatalf("tomato: %+v, %v", a, err)
	}
	for name, bad := range map[string]prices.TimingQuery{
		"no crop":  {State: "Andhra Pradesh", DaysMin: 100, DaysMax: 130, Today: q.Today},
		"no place": {Crop: "groundnut", DaysMin: 100, DaysMax: 130, Today: q.Today},
		"no today": {Crop: "groundnut", State: "Andhra Pradesh", DaysMin: 100, DaysMax: 130},
		"no days":  {Crop: "groundnut", State: "Andhra Pradesh", Today: q.Today},
	} {
		if _, err := prices.AdviseTiming(ctx, h, bad); !errors.Is(err, prices.ErrInvalid) {
			t.Errorf("%s: %v; want ErrInvalid", name, err)
		}
	}
}

func TestScanFilesReadsIndiaDataPortalAndCEDAFiles(t *testing.T) {
	t.Parallel()
	var got []prices.Quote
	stats, err := prices.ScanFiles(context.Background(), "testdata/synthetic-bulk", func(x prices.Quote) error {
		got = append(got, x)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Files != 3 || stats.Quotes != 4 || stats.Skipped != 1 || len(stats.Errors) != 0 {
		t.Fatalf("stats %+v", stats)
	}
	slices.SortFunc(got, func(a, b prices.Quote) int {
		return strings.Compare(a.Date.String()+a.Commodity, b.Date.String()+b.Commodity)
	})
	want := []string{
		"2019-11-04 Andhra Pradesh/Anantapur/Synthetic Market Groundnut 4450",
		"2023-10-02 Andhra Pradesh/Kurnool/Synthetic Yard Arhar (Tur/Red Gram)(Whole) 8600",
		"2023-10-02 Andhra Pradesh/Kurnool/Synthetic Yard Groundnut 5800",
		"2025-10-27 Andhra Pradesh/Y.S.R./Synthetic Apmc Groundnut 6783",
	}
	for i, x := range got {
		if s := x.Date.String() + " " + x.State + "/" + x.District + "/" + x.Market + " " + x.Commodity + " " + strings.TrimSuffix(strings.TrimSuffix(jsonNum(x.Modal), ".0"), ".00"); i >= len(want) || s != want[i] {
			t.Errorf("quote %d: %s", i, s)
		}
	}
	stop := errors.New("stop")
	if _, err := prices.ScanFiles(context.Background(), "testdata/synthetic-bulk", func(prices.Quote) error { return stop }); !errors.Is(err, stop) {
		t.Errorf("an error from each: %v", err)
	}
	// The kept-in-memory provider reads the same layouts.
	f, err := prices.Files("testdata/synthetic-bulk")
	if err != nil {
		t.Fatal(err)
	}
	if qs, err := f.Fetch(context.Background(), prices.Query{District: "Y.S.R.", Commodity: "groundnut"}); err != nil || len(qs) != 1 || qs[0].Market != "Synthetic Apmc" {
		t.Fatalf("Files: %+v, %v", qs, err)
	}
}

func jsonNum(v float64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// TestGateShowsASeasonOftenAndNoSeasonRarely measures the gate on 40
// synthetic histories of each kind (15 years, a 10% monthly shock): how
// often it lets the sell-or-hold advice through for a real post-harvest
// dip (its power: 17 of 40 when written) and for prices with no season at
// all (its false alarms: 4 of 40). A change that makes either much worse
// fails here.
func TestGateShowsASeasonOftenAndNoSeasonRarely(t *testing.T) {
	t.Parallel()
	st := prices.StorageFor("groundnut")
	dips, nones := 0, 0
	for seed := uint64(100); seed < 140; seed++ {
		s := dip(seed, 15, 0.10)
		r := prices.BacktestSell(s.Monthly(), time.October, st, prices.Tuning{})
		checkBacktest(t, r)
		if r.Passed {
			dips++
		}
		s.Season = [12]float64{}
		if prices.BacktestSell(s.Monthly(), time.October, st, prices.Tuning{}).Passed {
			nones++
		}
	}
	if dips < 12 || nones > 6 {
		t.Fatalf("the gate passed %d of 40 seasonal histories (want 12 or more) and %d of 40 without a season (want 6 or fewer)", dips, nones)
	}
}
