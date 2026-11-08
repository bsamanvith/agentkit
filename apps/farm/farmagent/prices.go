package farmagent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/feeds"
	"github.com/bsamanvith/agentkit/apps/farm/prices"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// The price tools' names.
const (
	MandiPricesTool   = "mandi_prices"
	PriceEstimateTool = "price_estimate"
	// maxMarkets is the most markets mandi_prices lists.
	maxMarkets = 15
)

// PriceSystem is added to the system prompt with the price tools.
const PriceSystem = `
You know crop prices through the price tools.

- Prices come from mandi_prices (today's mandi prices) and price_estimate (what a crop may fetch in a month); never state a price, a trend or an MSP from memory. Prices are rupees a quintal (100 kg): min, max and modal (the most common price) of a day's trade in one market.
- Say which day the prices are from and where they come from; when they are days old or from a wider area than asked, say so.
- An estimate is a range, not a promise: give low, likely and high with the tool's notes in plain words, and its confidence. Revenue is before costs.
- MSP is paid only where the government procures; do not tell a farmer they will get it at the mandi.`

type pricesIn struct {
	Commodity string `json:"commodity" jsonschema:"the crop: a crop id from list_crops (pigeon_pea) or its common name (tur, tomato, cotton)"`
	Farm      string `json:"farm,omitempty" jsonschema:"a farm's id: look near its plot (its district and state)"`
	State     string `json:"state,omitempty" jsonschema:"the state, when no farm is given: Karnataka, Andhra Pradesh, Telangana, Tamil Nadu, ..."`
	District  string `json:"district,omitempty" jsonschema:"the district, to narrow the state"`
	Market    string `json:"market,omitempty" jsonschema:"one market (mandi, APMC) by name"`
	Days      int    `json:"days,omitempty" jsonschema:"also show the price history of the last this many days (at most 730) as monthly medians"`
}

type marketOut struct {
	Market   string  `json:"market"`
	District string  `json:"district"`
	Variety  string  `json:"variety,omitempty"`
	Date     string  `json:"date"`
	Min      float64 `json:"min"`
	Max      float64 `json:"max"`
	Modal    float64 `json:"modal"`
}

type monthOut struct {
	Month  string  `json:"month"`
	Modal  float64 `json:"median_modal"`
	Quotes int     `json:"quotes"`
}

type pricesOut struct {
	Commodity string                `json:"commodity"`
	Area      string                `json:"area"`
	AsOf      string                `json:"as_of"`
	DaysOld   int                   `json:"days_old"`
	Source    string                `json:"source"`
	Widened   string                `json:"widened,omitempty"`
	Median    float64               `json:"median_modal"`
	Markets   []marketOut           `json:"markets"`
	More      int                   `json:"more_markets,omitempty"`
	Elsewhere []marketOut           `json:"elsewhere_in_state,omitempty"`
	History   []monthOut            `json:"history,omitempty"`
	MSP       []prices.SupportPrice `json:"msp,omitempty"`
	Unit      string                `json:"unit"`
}

type estimateIn struct {
	Commodity     string  `json:"commodity" jsonschema:"the crop: a crop id from list_crops or its common name"`
	Farm          string  `json:"farm,omitempty" jsonschema:"a farm's id: estimate near its plot"`
	State         string  `json:"state,omitempty" jsonschema:"the state, when no farm is given"`
	District      string  `json:"district,omitempty"`
	Market        string  `json:"market,omitempty"`
	SellMonth     int     `json:"sell_month,omitempty" jsonschema:"the month the farmer expects to sell, 1 to 12 (default this month)"`
	YieldQPerAcre float64 `json:"yield_q_per_acre,omitempty" jsonschema:"the farmer's expected yield in quintals an acre, for what the harvest may bring"`
	Acres         float64 `json:"acres,omitempty" jsonschema:"the area to scale that to (default the farm's area when a farm is given)"`
}

// PriceTools returns mandi_prices and price_estimate over p; svc finds a
// farm's district and state.
func PriceTools(p *prices.Service, svc *farm.Service) ([]tool.Tool, error) {
	where := func(ctx context.Context, id, state, district string) (string, string, float64, error) {
		if id == "" {
			if state == "" && district == "" {
				return "", "", 0, &tool.Error{Tool: MandiPricesTool, Kind: tool.ErrInvalidInput, Err: errors.New("give a farm or a state")}
			}
			return state, district, 0, nil
		}
		f, err := svc.Get(ctx, id)
		if err != nil {
			return "", "", 0, err
		}
		acres := 0.0
		if m := f.Report.Measures; m != nil {
			acres = m.AreaM2 / 4046.8564224
		}
		if loc := f.Report.Location; loc != nil {
			if state == "" {
				state = loc.State
			}
			if district == "" {
				district = loc.District
			}
		}
		if state == "" {
			return "", "", 0, fmt.Errorf("farm %s has no known state (its location was not looked up): give the state", id)
		}
		return state, district, acres, nil
	}
	var tools []tool.Tool
	add := func(t tool.Tool, err error) error {
		if err == nil {
			tools = append(tools, t)
		}
		return err
	}
	err := errors.Join(
		add(tool.Func(MandiPricesTool, "Today's mandi prices of a crop near a farm (its district, else its state) or in a state, district or market: each market's newest min, max and modal price in rupees a quintal, the day they are from and the source, and the minimum support price if the crop has one. With days, also the monthly median price over that many days of saved history.",
			func(ctx context.Context, in pricesIn) (pricesOut, error) {
				state, district, _, err := where(ctx, in.Farm, in.State, in.District)
				if err != nil {
					return pricesOut{}, err
				}
				a, err := p.Today(ctx, prices.Query{State: state, District: district, Market: in.Market, Commodity: in.Commodity})
				if errors.Is(err, feeds.ErrNoData) {
					return pricesOut{}, fmt.Errorf("%w; the price providers may not cover it, or the commodity has another name in the mandis", err)
				}
				if errors.Is(err, prices.ErrInvalid) {
					return pricesOut{}, &tool.Error{Tool: MandiPricesTool, Kind: tool.ErrInvalidInput, Err: err}
				}
				if err != nil {
					return pricesOut{}, err
				}
				out := pricesOut{
					Commodity: a.Query.Commodity, Area: area(a.Query), AsOf: a.AsOf.String(), DaysOld: a.DaysOld, Source: a.Source,
					Widened: a.Widened, Markets: []marketOut{}, MSP: prices.MSP(a.Query.Commodity), Unit: "rupees per quintal (100 kg)",
				}
				var modal []float64
				for i, x := range a.Markets {
					modal = append(modal, x.Modal)
					if i >= maxMarkets {
						out.More++
						continue
					}
					out.Markets = append(out.Markets, marketOut{Market: x.Market, District: x.District, Variety: x.Variety, Date: x.Date.String(), Min: x.Min, Max: x.Max, Modal: x.Modal})
				}
				out.Median = medianOf(modal)
				for _, x := range a.Elsewhere {
					out.Elsewhere = append(out.Elsewhere, marketOut{Market: x.Market, District: x.District, Variety: x.Variety, Date: x.Date.String(), Min: x.Min, Max: x.Max, Modal: x.Modal})
				}
				if in.Days > 0 {
					days := min(in.Days, 730)
					to := prices.DayOf(time.Now())
					hist, err := p.History(ctx, prices.Query{
						State: a.Query.State, District: a.Query.District, Market: a.Query.Market,
						Commodity: a.Query.Commodity, From: to.AddDays(-days), Limit: prices.MaxLimit,
					})
					if err != nil {
						return pricesOut{}, err
					}
					out.History = monthly(hist)
				}
				return out, nil
			}, read)),
		add(tool.Func(PriceEstimateTool, "Estimate what a crop may sell for in a month near a farm or in a state: low, likely and high in rupees a quintal from the saved mandi price history (recent level, the crop's usual seasonal swing, the spread), with today's level, the trend, the minimum support price, how confident it is and why. With a yield, also what the harvest may bring (before costs).",
			func(ctx context.Context, in estimateIn) (prices.Estimate, error) {
				state, district, acres, err := where(ctx, in.Farm, in.State, in.District)
				if err != nil {
					return prices.Estimate{}, err
				}
				if in.Acres > 0 {
					acres = in.Acres
				}
				if in.YieldQPerAcre <= 0 {
					acres = 0
				}
				e, err := p.Estimate(ctx, prices.EstimateQuery{
					Commodity: in.Commodity, State: state, District: district, Market: in.Market,
					SellMonth: time.Month(in.SellMonth), YieldQPerAcre: in.YieldQPerAcre, Acres: round2(acres),
				})
				if errors.Is(err, prices.ErrInvalid) {
					return e, &tool.Error{Tool: PriceEstimateTool, Kind: tool.ErrInvalidInput, Err: err}
				}
				return e, err
			}, read)),
	)
	return tools, err
}

func area(q prices.Query) string {
	switch {
	case q.Market != "":
		return q.Market + " market"
	case q.District != "":
		return q.District + " district, " + q.State
	case q.State != "":
		return q.State
	}
	return "India"
}

func monthly(qs []prices.Quote) []monthOut {
	by := map[string][]float64{}
	for _, x := range qs {
		k := x.Date.Time().Format("2006-01")
		by[k] = append(by[k], x.Modal)
	}
	out := make([]monthOut, 0, len(by))
	for k, vs := range by {
		out = append(out, monthOut{Month: k, Modal: medianOf(vs), Quotes: len(vs)})
	}
	slices.SortFunc(out, func(a, b monthOut) int { return compareStrings(a.Month, b.Month) })
	return out
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func medianOf(vs []float64) float64 {
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

func round2(v float64) float64 { return float64(int(v*100+0.5)) / 100 }
