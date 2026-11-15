package prices

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// TimingQuery is what a crop cycle knows when it asks when to sow and when
// to sell: the crop, the place, the safe sowing window and today.
type TimingQuery struct {
	// Crop is one of the app's crop ids ("groundnut") or a commodity name.
	Crop     string `json:"crop"`
	State    string `json:"state,omitempty"`
	District string `json:"district,omitempty"`
	Market   string `json:"market,omitempty"`
	// DaysMin and DaysMax are the crop's days from sowing to harvest
	// (agro.Crop.Days).
	DaysMin int `json:"days_min"`
	DaysMax int `json:"days_max"`
	// Window is the agronomically safe sowing window; zero when the crop
	// is already sown (then only selling is advised, from Sown).
	Window SowingWindow `json:"window,omitzero"`
	// Sown is the day it was sown, when it was.
	Sown Day `json:"sown,omitzero"`
	// Today is the day the advice is for; prices after it are not used.
	Today  Day    `json:"today"`
	Tuning Tuning `json:"tuning,omitzero"`
	// Storage is what holding the harvest costs where the plot is (a
	// region pack's); nil for [StorageFor]'s starter values.
	Storage *Storage `json:"-"`
}

// TimingAdvice is advice or nothing: Sowing and Selling are set only when
// their rule beat both naive baselines in a backtest on this area's past
// years ([Gate]); Reasons say why when they are not, in words fit for the
// farmer.
type TimingAdvice struct {
	Commodity string `json:"commodity"`
	// Area is where the prices came from: "Kurnool district, Andhra
	// Pradesh", "Andhra Pradesh".
	Area string `json:"area,omitempty"`
	// Index is the seasonal index the advice rests on.
	Index   *SeasonalIndex `json:"index,omitempty"`
	Sowing  *SowingAdvice  `json:"sowing,omitempty"`
	Selling *SellAdvice    `json:"selling,omitempty"`
	// SowingTest and SellingTest are the backtests behind each gate.
	SowingTest  *BacktestResult `json:"sowing_test,omitempty"`
	SellingTest *BacktestResult `json:"selling_test,omitempty"`
	Reasons     []string        `json:"reasons"`
}

// timingLimit caps the quotes one area's history may give.
const timingLimit = MaxLimit

// AdviseTiming is the one call a crop cycle makes: for a crop, a place, a
// safe sowing window and today, the harvest-month advice (while sowing is
// ahead) and the sell-or-hold advice (for a storable crop), each only when
// its backtest on the area's past years passed the gate. The area is the
// nearest of market, district and state whose history gives a seasonal
// index. It returns an error only for a query it cannot ask; too little
// history is an answer with no advice and the reason.
func AdviseTiming(ctx context.Context, h History, q TimingQuery) (TimingAdvice, error) {
	t := q.Tuning.withDefaults()
	commodity := Commodity(q.Crop)
	if commodity == "" {
		return TimingAdvice{}, fmt.Errorf("%w: name a crop", ErrInvalid)
	}
	if q.Today.IsZero() {
		return TimingAdvice{}, fmt.Errorf("%w: no today", ErrInvalid)
	}
	if q.DaysMin <= 0 || q.DaysMax < q.DaysMin {
		return TimingAdvice{}, fmt.Errorf("%w: crop days %d to %d", ErrInvalid, q.DaysMin, q.DaysMax)
	}
	sowing := q.Window != (SowingWindow{})
	if sowing {
		w, err := q.Window.valid()
		if err != nil {
			return TimingAdvice{}, err
		}
		q.Window = w
	}
	a := TimingAdvice{Commodity: commodity}

	type area struct {
		name string
		q    Query
	}
	var areas []area
	if q.Market != "" {
		areas = append(areas, area{q.Market + " market", Query{State: q.State, District: q.District, Market: q.Market}})
	}
	if q.District != "" {
		areas = append(areas, area{q.District + " district" + suffix(q.State), Query{State: q.State, District: q.District}})
	}
	if q.State != "" {
		areas = append(areas, area{stateName(q.State), Query{State: q.State}})
	}
	if len(areas) == 0 {
		return TimingAdvice{}, fmt.Errorf("%w: name a state, a district or a market", ErrInvalid)
	}
	var (
		quotes []Quote
		series []MonthlyPrice
		idx    SeasonalIndex
		found  bool
	)
	for _, ar := range areas {
		ar.q.Commodity, ar.q.To, ar.q.Limit = commodity, q.Today, timingLimit
		got, err := h.Quotes(ctx, ar.q)
		if err != nil {
			return TimingAdvice{}, err
		}
		s := Monthly(got)
		x, err := NewSeasonalIndex(s, IndexOptions{MinYears: t.MinTrainYears, Before: MonthOf(q.Today).Add(1)})
		if errors.Is(err, ErrTooLittleHistory) {
			continue
		}
		if err != nil {
			return TimingAdvice{}, err
		}
		quotes, series, idx, a.Area, found = got, s, x, ar.name, true
		break
	}
	if !found {
		a.Reasons = append(a.Reasons, fmt.Sprintf("Too few years of %s prices near you to know its usual months, so there is no advice on when to sow or sell for price: sow at the best agronomic date. It comes once a few years of prices are saved.", commodity))
		return a, nil
	}
	idx.Commodity, idx.Area = commodity, a.Area
	a.Index = &idx
	if idx.Confidence == "low" {
		a.Reasons = append(a.Reasons, fmt.Sprintf("The usual months rest on only %d year(s) of prices at %s.", idx.Years, a.Area))
	}

	// Sowing: while the window has not closed.
	harvest := time.Month(0)
	switch {
	case sowing:
		harvest = q.Window.Best.AddDays((q.DaysMin + q.DaysMax) / 2).Time().Month()
	case !q.Sown.IsZero():
		harvest = q.Sown.AddDays((q.DaysMin + q.DaysMax) / 2).Time().Month()
	}
	if sowing && !q.Window.To.Before(q.Today) {
		bt, err := BacktestSowing(series, q.DaysMin, q.DaysMax, q.Window, t)
		if err != nil {
			return TimingAdvice{}, err
		}
		bt.Commodity, bt.Area = commodity, a.Area
		a.SowingTest = &bt
		if bt.Passed {
			adv, err := AdviseSowing(idx, q.DaysMin, q.DaysMax, q.Window, t)
			switch {
			case errors.Is(err, ErrTooLittleHistory):
				a.Reasons = append(a.Reasons, "The index does not know the harvest month: sow at the best agronomic date.")
			case err != nil:
				return TimingAdvice{}, err
			default:
				adv.Reasons = append(adv.Reasons, bt.Reasons...)
				a.Sowing = &adv
			}
		} else {
			a.Reasons = append(a.Reasons, "No advice on the sowing date for price: sow at the best agronomic date. "+join(bt.Reasons))
		}
	}

	// Selling.
	st := StorageFor(commodity)
	if q.Storage != nil {
		st = *q.Storage
	}
	if !st.Storable() {
		a.Reasons = append(a.Reasons, commodity+" does not keep: sell when it is ready, at the best market that day.")
		return a, nil
	}
	from, price, note := sellStart(quotes, series, idx, q, harvest)
	if from == 0 || !idx.At(from).Known() || price <= 0 {
		a.Reasons = append(a.Reasons, "No advice on holding: the harvest month or today's price is not known.")
		return a, nil
	}
	bt := BacktestSell(series, from, st, t)
	bt.Commodity, bt.Area = commodity, a.Area
	a.SellingTest = &bt
	if !bt.Passed {
		a.Reasons = append(a.Reasons, "No advice on holding for price. "+join(bt.Reasons))
		return a, nil
	}
	adv, err := AdviseSell(idx, from, price, st, t)
	if err != nil {
		return TimingAdvice{}, err
	}
	if note != "" {
		adv.Reasons = append([]string{note}, adv.Reasons...)
	}
	adv.Reasons = append(adv.Reasons, bt.Reasons...)
	a.Selling = &adv
	return a, nil
}

// sellStart is the month selling is weighed from and its price: today's
// month and the recent price once the harvest is in (or no harvest month
// is known), else the harvest month and the price it usually has there,
// from the last year's prices.
func sellStart(qs []Quote, series []MonthlyPrice, idx SeasonalIndex, q TimingQuery, harvest time.Month) (time.Month, float64, string) {
	ready := harvest == 0
	if !ready {
		var earliest Day
		switch {
		case !q.Sown.IsZero():
			earliest = q.Sown.AddDays(q.DaysMin)
		default:
			earliest = q.Window.From.AddDays(q.DaysMin)
		}
		ready = !q.Today.Before(earliest)
	}
	if ready {
		if len(qs) == 0 || q.Today.DaysSince(qs[0].Date) > staleDays {
			return 0, 0, ""
		}
		newest := qs[0].Date
		return q.Today.Time().Month(), median(modals(qs, newest.AddDays(-29), newest)), fmt.Sprintf("From the recent price of %s (the median of the 30 days to %s).", INR(median(modals(qs, newest.AddDays(-29), newest))), newest)
	}
	// The usual price in the harvest month: the last twelve months'
	// average, moved by the index.
	var sum float64
	n := 0
	for i := len(series) - 1; i >= 0 && n < 12; i-- {
		if s := idx.At(series[i].Month.Month); s.Known() {
			sum += series[i].Price / s.Index
			n++
		}
	}
	if n == 0 {
		return 0, 0, ""
	}
	p := sum / float64(n) * idx.At(harvest).Index
	return harvest, p, fmt.Sprintf("Planned from the harvest month, %s, at the price it usually has then, about %s.", harvest, INR(p))
}

func join(ss []string) string { return strings.Join(ss, " ") }
