package prices

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// EstimateQuery asks what a crop may sell for.
type EstimateQuery struct {
	Commodity string `json:"commodity"`
	State     string `json:"state,omitempty"`
	District  string `json:"district,omitempty"`
	Market    string `json:"market,omitempty"`
	// SellMonth is the month the farmer expects to sell (1 to 12); 0 for
	// now.
	SellMonth time.Month `json:"sell_month,omitempty"`
	// YieldQPerAcre is the farmer's expected yield in quintals an acre;
	// with it the estimate says what the harvest may bring. Acres scales
	// that to the field.
	YieldQPerAcre float64 `json:"yield_q_per_acre,omitempty"`
	Acres         float64 `json:"acres,omitempty"`
}

// Estimate is a price estimate with what it rests on. Prices are rupees a
// quintal.
type Estimate struct {
	Commodity string     `json:"commodity"`
	SellMonth time.Month `json:"sell_month"`
	// Area is where the prices came from: "Rangareddy district,
	// Telangana", "Telangana", "India".
	Area string `json:"area"`
	// Low, Likely and High are the estimate; all 0 when there is no price
	// history to estimate from.
	Low    float64 `json:"low"`
	Likely float64 `json:"likely"`
	High   float64 `json:"high"`
	// Now is the recent level: the median modal price of the last 30 days
	// of data, up to AsOf.
	Now  float64 `json:"now"`
	AsOf Day     `json:"as_of,omitzero"`
	// TrendPct is the change of that level from the 30 days before, in
	// percent; 0 when unknown.
	TrendPct float64 `json:"trend_pct"`
	// Seasonal is the crop's usual price in each month as a share of its
	// year's average (index 0 is January), from the history; nil when
	// there is less than a year to go on.
	Seasonal []float64 `json:"seasonal,omitempty"`
	// Confidence is "good", "fair" or "low".
	Confidence string `json:"confidence"`
	Basis      Basis  `json:"basis"`
	// MSP are the commodity's minimum support prices, if it has any.
	MSP []SupportPrice `json:"msp,omitempty"`
	// Revenue is what the harvest may bring, with a yield given.
	Revenue *Revenue `json:"revenue,omitempty"`
	// Notes say how the numbers were reached and what to watch.
	Notes []string `json:"notes"`
}

// Basis is the history an estimate rests on.
type Basis struct {
	Quotes  int    `json:"quotes"`
	Markets int    `json:"markets"`
	Months  int    `json:"months"`
	From    Day    `json:"from,omitzero"`
	To      Day    `json:"to,omitzero"`
	Sources string `json:"sources,omitempty"`
}

// Revenue is what a harvest may bring at the estimate's prices, in rupees.
type Revenue struct {
	YieldQPerAcre float64 `json:"yield_q_per_acre"`
	PerAcre       Range   `json:"per_acre"`
	Acres         float64 `json:"acres,omitempty"`
	Total         *Range  `json:"total,omitempty"`
}

// Range is a low, likely and high amount.
type Range struct {
	Low    float64 `json:"low"`
	Likely float64 `json:"likely"`
	High   float64 `json:"high"`
}

// The estimate's thresholds.
const (
	// enoughQuotes and enoughMonths are the least history an area needs
	// before the estimate uses it rather than the wider area.
	enoughQuotes = 20
	enoughMonths = 3
	// staleDays is how old the newest price may be before the estimate
	// says so and lowers its confidence.
	staleDays = 45
	// estimateLimit caps the quotes one estimate reads.
	estimateLimit = 50000
)

// EstimatePrice estimates what a crop may sell for in a month from the
// history h holds, near the farmer first (market, then district, then
// state, then India, whichever is the nearest with enough history). today
// is the day to estimate from.
//
// The estimate is plain arithmetic, so it can be checked: the recent level
// (median modal price of the newest 30 days), moved by the crop's usual
// seasonal swing between this month and the selling month (median over the
// years in the history), with a range from how much that month has varied
// and how far ahead it is. Minimum support prices are shown beside it.
func EstimatePrice(ctx context.Context, h History, q EstimateQuery, today Day) (Estimate, error) {
	q.Commodity = Commodity(q.Commodity)
	if q.Commodity == "" {
		return Estimate{}, fmt.Errorf("%w: name a commodity", ErrInvalid)
	}
	if q.SellMonth < 0 || q.SellMonth > 12 {
		return Estimate{}, fmt.Errorf("%w: sell month %d", ErrInvalid, q.SellMonth)
	}
	if q.YieldQPerAcre < 0 || q.Acres < 0 {
		return Estimate{}, fmt.Errorf("%w: negative yield or area", ErrInvalid)
	}
	if q.SellMonth == 0 {
		q.SellMonth = today.Time().Month()
	}
	e := Estimate{Commodity: q.Commodity, SellMonth: q.SellMonth, MSP: MSP(q.Commodity), Confidence: "low"}

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
	areas = append(areas, area{"India", Query{}})
	// The nearest area with enough history; failing that, the one with
	// the most.
	var qs []Quote
	for _, a := range areas {
		a.q.Commodity, a.q.Limit = q.Commodity, estimateLimit
		got, err := h.Quotes(ctx, a.q)
		if err != nil {
			return Estimate{}, err
		}
		if len(got) >= enoughQuotes && countMonths(got) >= enoughMonths {
			qs, e.Area = got, a.name
			break
		}
		if len(got) > len(qs) {
			qs, e.Area = got, a.name
		}
	}
	if len(qs) == 0 {
		e.Notes = append(e.Notes, "No mandi prices for "+q.Commodity+" are saved yet, so there is no estimate: ask for today's prices first, or add price files.")
		e.Notes = append(e.Notes, mspNotes(e.MSP, 0)...)
		return e, nil
	}
	e.Basis = basis(qs)

	// The recent level, and the trend against the 30 days before it.
	newest := qs[0].Date
	e.AsOf = newest
	e.Now = median(modals(qs, newest.AddDays(-29), newest))
	if before := modals(qs, newest.AddDays(-59), newest.AddDays(-30)); len(before) >= 3 {
		if b := median(before); b > 0 {
			e.TrendPct = round1((e.Now - b) / b * 100)
		}
	}

	// The seasonal swing, from the years with at least six months of data.
	monthly := monthlyMedians(qs)
	ratios := make([][]float64, 12)
	years := 0
	for _, ms := range monthly {
		if len(ms) < 6 {
			continue
		}
		years++
		var sum float64
		for _, v := range ms {
			sum += v
		}
		mean := sum / float64(len(ms))
		for m, v := range ms {
			ratios[m-1] = append(ratios[m-1], v/mean)
		}
	}
	covered := 0
	for _, r := range ratios {
		if len(r) > 0 {
			covered++
		}
	}
	nowMonth := newest.Time().Month()
	if years > 0 && covered >= 9 && len(ratios[nowMonth-1]) > 0 && len(ratios[q.SellMonth-1]) > 0 {
		e.Seasonal = make([]float64, 12)
		for m, r := range ratios {
			if len(r) > 0 {
				e.Seasonal[m] = round3(median(r))
			}
		}
	}

	ahead := monthsAhead(today.Time().Month(), q.SellMonth)
	spread := 0.08 + 0.04*float64(ahead)
	e.Likely = e.Now
	if e.Seasonal != nil {
		e.Likely = e.Now * e.Seasonal[q.SellMonth-1] / e.Seasonal[nowMonth-1]
		if r := ratios[q.SellMonth-1]; len(r) >= 2 {
			lo, hi := slices.Min(r), slices.Max(r)
			if s := (hi - lo) / 2 / median(r); s > spread {
				spread = s
			}
		}
		e.Notes = append(e.Notes, fmt.Sprintf("From the recent level of %s, moved by %s's usual price in %s against %s (%d year(s) of history).",
			INR(e.Now), q.Commodity, q.SellMonth, nowMonth, years))
	} else {
		e.Notes = append(e.Notes, fmt.Sprintf("Less than a year of prices is saved, so the estimate is the recent level of %s with no seasonal swing; it gets better as prices are saved day by day.", INR(e.Now)))
	}
	// A day's prices across markets set the least spread.
	if recent := modals(qs, newest.AddDays(-29), newest); len(recent) >= 4 {
		if s := iqr(recent) / 2 / e.Now; s > spread {
			spread = s
		}
	}
	spread = math.Min(spread, 0.5)
	e.Low, e.Likely, e.High = round0(e.Likely*(1-spread)), round0(e.Likely), round0(e.Likely*(1+spread))

	stale := today.DaysSince(newest) > staleDays
	switch {
	case stale:
		e.Notes = append(e.Notes, fmt.Sprintf("The newest saved price is from %s, %d days ago: today's prices may differ.", newest, today.DaysSince(newest)))
	case e.Seasonal != nil && years >= 2:
		e.Confidence = "good"
	default:
		e.Confidence = "fair"
	}
	if e.TrendPct != 0 {
		dir := "up"
		if e.TrendPct < 0 {
			dir = "down"
		}
		e.Notes = append(e.Notes, fmt.Sprintf("Prices are %s %.1f%% on the 30 days before.", dir, math.Abs(e.TrendPct)))
	}
	if ahead > 0 {
		e.Notes = append(e.Notes, fmt.Sprintf("Selling %d month(s) ahead widens the range; rain, arrivals at harvest and export rules move prices more than any history shows.", ahead))
	}
	e.Notes = append(e.Notes, mspNotes(e.MSP, e.Likely)...)

	if q.YieldQPerAcre > 0 {
		y := q.YieldQPerAcre
		r := &Revenue{YieldQPerAcre: y, PerAcre: Range{round0(e.Low * y), round0(e.Likely * y), round0(e.High * y)}}
		if q.Acres > 0 {
			r.Acres = q.Acres
			r.Total = &Range{round0(e.Low * y * q.Acres), round0(e.Likely * y * q.Acres), round0(e.High * y * q.Acres)}
		}
		e.Revenue = r
		e.Notes = append(e.Notes, "Revenue is before costs (seed, inputs, labour, transport to the mandi, commission).")
	}
	return e, nil
}

func suffix(state string) string {
	if state == "" {
		return ""
	}
	return ", " + stateName(state)
}

func mspNotes(msp []SupportPrice, likely float64) []string {
	if len(msp) == 0 {
		return nil
	}
	low := msp[0].Price
	for _, p := range msp {
		low = math.Min(low, p.Price)
	}
	n := []string{fmt.Sprintf("Minimum support price (%s): %s a quintal; it is paid only where the government procures.", msp[0].Season, INR(low))}
	if likely > 0 && likely < low {
		n = append(n, "The likely mandi price is below the MSP: ask about procurement centres (and PM-AASHA for pulses and oilseeds) before selling.")
	}
	return n
}

func basis(qs []Quote) Basis {
	b := Basis{Quotes: len(qs), From: qs[len(qs)-1].Date, To: qs[0].Date, Months: countMonths(qs)}
	markets, sources := map[string]bool{}, map[string]bool{}
	for _, x := range qs {
		markets[Fold(x.Market)] = true
		sources[x.Source] = true
	}
	b.Markets = len(markets)
	names := make([]string, 0, len(sources))
	for s := range sources {
		names = append(names, s)
	}
	slices.Sort(names)
	b.Sources = strings.Join(names, ", ")
	return b
}

func countMonths(qs []Quote) int {
	m := map[[2]int]bool{}
	for _, x := range qs {
		t := x.Date.Time()
		m[[2]int{t.Year(), int(t.Month())}] = true
	}
	return len(m)
}

// monthlyMedians is the median modal price of each month, by year.
func monthlyMedians(qs []Quote) map[int]map[time.Month]float64 {
	by := map[int]map[time.Month][]float64{}
	for _, x := range qs {
		t := x.Date.Time()
		if by[t.Year()] == nil {
			by[t.Year()] = map[time.Month][]float64{}
		}
		by[t.Year()][t.Month()] = append(by[t.Year()][t.Month()], x.Modal)
	}
	out := map[int]map[time.Month]float64{}
	for y, ms := range by {
		out[y] = map[time.Month]float64{}
		for m, vs := range ms {
			out[y][m] = median(vs)
		}
	}
	return out
}

// modals is the modal prices of the quotes from from to to, both included.
func modals(qs []Quote, from, to Day) []float64 {
	var out []float64
	for _, x := range qs {
		if !x.Date.Before(from) && !to.Before(x.Date) {
			out = append(out, x.Modal)
		}
	}
	return out
}

func monthsAhead(from, to time.Month) int { return (int(to) - int(from) + 12) % 12 }

// Median is the median of vs, 0 for none.
func Median(vs []float64) float64 { return median(vs) }

func median(vs []float64) float64 {
	if len(vs) == 0 {
		return 0
	}
	s := slices.Clone(vs)
	slices.Sort(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// iqr is the spread between the first and third quartiles.
func iqr(vs []float64) float64 {
	s := slices.Clone(vs)
	slices.Sort(s)
	q := func(p float64) float64 {
		i := p * float64(len(s)-1)
		lo := int(math.Floor(i))
		hi := min(lo+1, len(s)-1)
		return s[lo] + (s[hi]-s[lo])*(i-float64(lo))
	}
	return q(0.75) - q(0.25)
}

func round0(v float64) float64 { return math.Round(v) }
func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// INR writes rupees in the Indian grouping: ₹1,23,456.
func INR(v float64) string {
	n := int64(math.Round(math.Abs(v)))
	s := strconv.FormatInt(n, 10)
	if len(s) > 3 {
		head, tail := s[:len(s)-3], s[len(s)-3:]
		var parts []string
		for len(head) > 2 {
			parts = append([]string{head[len(head)-2:]}, parts...)
			head = head[:len(head)-2]
		}
		if head != "" {
			parts = append([]string{head}, parts...)
		}
		s = strings.Join(parts, ",") + "," + tail
	}
	if v < 0 {
		return "-₹" + s
	}
	return "₹" + s
}
