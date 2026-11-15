package prices

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"time"
)

// ErrTooLittleHistory is a seasonal index, an advice or a backtest asked of
// fewer years of prices than it needs. It is not a fault: the caller shows
// no timing advice.
var ErrTooLittleHistory = errors.New("prices: too little price history")

// Month is a calendar month of a year.
type Month struct {
	Year  int        `json:"year"`
	Month time.Month `json:"month"`
}

// MonthOf returns the month of d.
func MonthOf(d Day) Month { return Month{d.Time().Year(), d.Time().Month()} }

// Add returns m plus n months (n may be negative).
func (m Month) Add(n int) Month {
	i := m.index() + n
	return Month{floorDiv(i, 12), time.Month(i - floorDiv(i, 12)*12 + 1)}
}

// Since is the number of months from e to m.
func (m Month) Since(e Month) int { return m.index() - e.index() }

// Before reports whether m is before e.
func (m Month) Before(e Month) bool { return m.index() < e.index() }

func (m Month) String() string { return fmt.Sprintf("%d-%02d", m.Year, int(m.Month)) }

func (m Month) index() int { return m.Year*12 + int(m.Month) - 1 }

func floorDiv(a, b int) int {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// MonthlyPrice is one month's price: the median modal price of the quotes
// of that month (every market, variety and day in the area).
type MonthlyPrice struct {
	Month
	Price  float64 `json:"price"`
	Quotes int     `json:"quotes"`
}

// Monthly turns quotes into monthly prices, oldest first: the median modal
// price of each month that has any.
func Monthly(qs []Quote) []MonthlyPrice {
	by := map[Month][]float64{}
	for _, x := range qs {
		if x.Modal > 0 {
			m := MonthOf(x.Date)
			by[m] = append(by[m], x.Modal)
		}
	}
	out := make([]MonthlyPrice, 0, len(by))
	for m, vs := range by {
		out = append(out, MonthlyPrice{Month: m, Price: median(vs), Quotes: len(vs)})
	}
	slices.SortFunc(out, func(a, b MonthlyPrice) int { return a.index() - b.index() })
	return out
}

// IndexOptions tune [NewSeasonalIndex]; the zero value is the default.
type IndexOptions struct {
	// MinYears is the fewest years a month's index must rest on (default
	// 2); with fewer, the month is unknown.
	MinYears int
	// GoodYears is the years for "good" confidence (default 5); from
	// MinYears to 3 it is "low", below GoodYears "fair".
	GoodYears int
	// MaxGap is the longest run of months without prices filled in (on a
	// log scale, between the months either side) for the moving average
	// (default 2). Filled months are never ratios themselves.
	MaxGap int
	// Before keeps only the months before it (zero for all): how a
	// backtest builds an index from the years before the one it tests.
	Before Month
}

func (o IndexOptions) withDefaults() IndexOptions {
	if o.MinYears <= 0 {
		o.MinYears = 2
	}
	if o.GoodYears <= 0 {
		o.GoodYears = 5
	}
	if o.GoodYears < o.MinYears {
		o.GoodYears = o.MinYears
	}
	if o.MaxGap <= 0 {
		o.MaxGap = 2
	}
	return o
}

// MinIndexMonths is the fewest months of the year a seasonal index must
// know.
const MinIndexMonths = 9

// SeasonalIndex is a crop's usual price in each month against its year's
// average, in one area.
type SeasonalIndex struct {
	Commodity string `json:"commodity,omitempty"`
	Area      string `json:"area,omitempty"`
	// Months are January to December.
	Months [12]MonthIndex `json:"months"`
	// Years is the fewest years behind a known month.
	Years int `json:"years"`
	// From and To are the first and last month of prices used.
	From Month `json:"from"`
	To   Month `json:"to"`
	// Confidence is "good", "fair" or "low" (see [IndexOptions]).
	Confidence string   `json:"confidence"`
	Notes      []string `json:"notes,omitempty"`
}

// MonthIndex is one month's index: the median over the years of the month's
// price over its centred 12-month moving average, scaled so that the known
// months average 1. Low and High are the first and third quartiles of those
// ratios on the same scale, and Years how many years there are. Index 0 is
// a month without enough years.
type MonthIndex struct {
	Index float64 `json:"index"`
	Low   float64 `json:"low"`
	High  float64 `json:"high"`
	Years int     `json:"years"`
}

// Known reports whether the month has an index.
func (m MonthIndex) Known() bool { return m.Index > 0 }

// Spread is the month's uncertainty: half the quartile range over the
// index (0.1 means a usual year lands within about ±10%).
func (m MonthIndex) Spread() float64 {
	if !m.Known() {
		return 0
	}
	return (m.High - m.Low) / 2 / m.Index
}

// At returns month m's index.
func (s SeasonalIndex) At(m time.Month) MonthIndex { return s.Months[(int(m)+11)%12] }

// NewSeasonalIndex builds the seasonal index of a monthly price series by
// the ratio to a centred moving average: each month's price over the
// 2x12-month centred moving average around it (which needs the six months
// either side), the median of those ratios over the years for each month,
// scaled so the year averages 1. Medians, so one wild year moves it little.
// It returns [ErrTooLittleHistory] when fewer than [MinIndexMonths] months
// have MinYears years each.
func NewSeasonalIndex(series []MonthlyPrice, o IndexOptions) (SeasonalIndex, error) {
	o = o.withDefaults()
	var kept []MonthlyPrice
	for _, p := range series {
		if p.Price > 0 && !math.IsInf(p.Price, 0) && (o.Before == Month{} || p.Before(o.Before)) {
			kept = append(kept, p)
		}
	}
	slices.SortFunc(kept, func(a, b MonthlyPrice) int { return a.index() - b.index() })
	kept = slices.CompactFunc(kept, func(a, b MonthlyPrice) bool { return a.Month == b.Month })
	var s SeasonalIndex
	if len(kept) == 0 {
		return s, fmt.Errorf("%w: no prices", ErrTooLittleHistory)
	}
	s.From, s.To = kept[0].Month, kept[len(kept)-1].Month
	n := s.To.Since(s.From) + 1
	logs := make([]float64, n)
	observed := make([]bool, n)
	for i := range logs {
		logs[i] = math.NaN()
	}
	for _, p := range kept {
		i := p.Since(s.From)
		logs[i], observed[i] = math.Log(p.Price), true
	}
	// Fill short gaps for the moving average, on a log scale.
	for i := 0; i < n; {
		if observed[i] {
			i++
			continue
		}
		j := i
		for j < n && !observed[j] {
			j++
		}
		if i > 0 && j < n && j-i <= o.MaxGap {
			for k := i; k < j; k++ {
				f := float64(k-i+1) / float64(j-i+1)
				logs[k] = logs[i-1]*(1-f) + logs[j]*f
			}
		}
		i = j
	}
	ratios := make([][]float64, 12)
	for i := 6; i+6 < n; i++ {
		if !observed[i] {
			continue
		}
		sum, ok := 0.0, true
		for k := -6; k <= 6; k++ {
			v := math.Exp(logs[i+k])
			if math.IsNaN(v) {
				ok = false
				break
			}
			if k == -6 || k == 6 {
				v /= 2
			}
			sum += v
		}
		if !ok {
			continue
		}
		m := s.From.Add(i).Month
		ratios[m-1] = append(ratios[m-1], math.Exp(logs[i])/(sum/12))
	}
	known, total, years := 0, 0.0, math.MaxInt
	for m, r := range ratios {
		if len(r) < o.MinYears {
			continue
		}
		mi := MonthIndex{Index: median(r), Low: quantile(r, 0.25), High: quantile(r, 0.75), Years: len(r)}
		s.Months[m] = mi
		known++
		total += mi.Index
		years = min(years, len(r))
	}
	if known < MinIndexMonths {
		return SeasonalIndex{From: s.From, To: s.To}, fmt.Errorf("%w: %d month(s) of the year have %d or more years of prices around them; %d are needed", ErrTooLittleHistory, known, o.MinYears, MinIndexMonths)
	}
	scale := float64(known) / total
	for m := range s.Months {
		if s.Months[m].Known() {
			s.Months[m].Index = round3(s.Months[m].Index * scale)
			s.Months[m].Low = round3(s.Months[m].Low * scale)
			s.Months[m].High = round3(s.Months[m].High * scale)
		}
	}
	s.Years = years
	switch {
	case years >= o.GoodYears && known == 12:
		s.Confidence = "good"
	case years >= 3:
		s.Confidence = "fair"
	default:
		s.Confidence = "low"
	}
	if known < 12 {
		s.Notes = append(s.Notes, fmt.Sprintf("%d month(s) of the year have too few prices for an index.", 12-known))
	}
	s.Notes = append(s.Notes, fmt.Sprintf("Each month's usual price against the year's average, from %d year(s) of prices (%s to %s); the median over the years, so one odd year moves it little.", years, s.From, s.To))
	return s, nil
}

// quantile is the p-quantile of vs (0 <= p <= 1), interpolated.
func quantile(vs []float64, p float64) float64 {
	if len(vs) == 0 {
		return 0
	}
	s := slices.Clone(vs)
	slices.Sort(s)
	i := p * float64(len(s)-1)
	lo := int(math.Floor(i))
	hi := min(lo+1, len(s)-1)
	return s[lo] + (s[hi]-s[lo])*(i-float64(lo))
}
