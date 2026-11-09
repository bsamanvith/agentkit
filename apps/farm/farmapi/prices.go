package farmapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/prices"
)

// WithPrices serves the price routes with p; without it, they are 503.
func WithPrices(p *prices.Service) Option { return func(x *api) { x.prices = p } }

// maxAnswerQuotes bounds the quotes GET /api/prices returns.
const maxAnswerQuotes = 300

func (a *api) pricing(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.prices == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: "prices are not configured"})
			return
		}
		h(w, r)
	}
}

// place fills a query's state and district from ?farm= (the plot's
// location), unless the request names them.
func (a *api) place(r *http.Request) (state, district string, acres float64, err error) {
	q := r.URL.Query()
	state, district = strings.TrimSpace(q.Get("state")), strings.TrimSpace(q.Get("district"))
	id := q.Get("farm")
	if id == "" {
		return state, district, 0, nil
	}
	f, err := a.svc.Get(r.Context(), id)
	if err != nil {
		return "", "", 0, err
	}
	if loc := f.Report.Location; loc != nil {
		if state == "" {
			state = loc.State
		}
		if district == "" {
			district = loc.District
		}
	}
	if m := f.Report.Measures; m != nil {
		acres = m.AreaM2 / 4046.8564224
	}
	return state, district, acres, nil
}

// pricesToday answers GET /api/prices?commodity=&farm=&state=&district=&market=.
func (a *api) pricesToday(w http.ResponseWriter, r *http.Request) {
	state, district, _, err := a.place(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ans, err := a.prices.Today(r.Context(), prices.Query{
		State: state, District: district, Market: r.URL.Query().Get("market"), Commodity: r.URL.Query().Get("commodity"),
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if len(ans.Quotes) > maxAnswerQuotes {
		ans.Quotes = ans.Quotes[:maxAnswerQuotes]
	}
	writeJSON(w, http.StatusOK, map[string]any{"answer": ans, "msp": prices.MSP(ans.Query.Commodity)})
}

// priceHistory answers GET /api/prices/history?commodity=&farm=&state=&district=&days=:
// the median modal price of each month.
func (a *api) priceHistory(w http.ResponseWriter, r *http.Request) {
	state, district, _, err := a.place(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 || days > 3660 {
		days = 365
	}
	to := prices.DayOf(time.Now())
	qs, err := a.prices.History(r.Context(), prices.Query{
		State: state, District: district, Market: r.URL.Query().Get("market"), Commodity: r.URL.Query().Get("commodity"),
		From: to.AddDays(-days), Limit: prices.MaxLimit,
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	type month struct {
		Month  string  `json:"month"`
		Modal  float64 `json:"median_modal"`
		Low    float64 `json:"low"`
		High   float64 `json:"high"`
		Quotes int     `json:"quotes"`
	}
	by := map[string][]float64{}
	var order []string
	for i := len(qs) - 1; i >= 0; i-- {
		k := qs[i].Date.Time().Format("2006-01")
		if _, ok := by[k]; !ok {
			order = append(order, k)
		}
		by[k] = append(by[k], qs[i].Modal)
	}
	out := make([]month, 0, len(order))
	for _, k := range order {
		vs := by[k]
		lo, hi := vs[0], vs[0]
		for _, v := range vs {
			lo, hi = min(lo, v), max(hi, v)
		}
		out = append(out, month{Month: k, Modal: prices.Median(vs), Low: lo, High: hi, Quotes: len(vs)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"months": out, "quotes": len(qs)})
}

type estimateBody struct {
	prices.EstimateQuery
	Farm string `json:"farm,omitempty"`
}

// priceEstimate answers POST /api/prices/estimate.
func (a *api) priceEstimate(w http.ResponseWriter, r *http.Request) {
	var body estimateBody
	if err := decode(w, r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	q := body.EstimateQuery
	if body.Farm != "" {
		f, err := a.svc.Get(r.Context(), body.Farm)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if loc := f.Report.Location; loc != nil {
			if q.State == "" {
				q.State = loc.State
			}
			if q.District == "" {
				q.District = loc.District
			}
		}
		if m := f.Report.Measures; m != nil && q.Acres == 0 && q.YieldQPerAcre > 0 {
			q.Acres = float64(int(m.AreaM2/4046.8564224*100+0.5)) / 100
		}
	}
	e, err := a.prices.Estimate(r.Context(), q)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

// priceSources answers GET /api/prices/sources: the providers in order
// and the minimum support prices.
func (a *api) priceSources(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"providers": a.prices.Providers(), "msp": prices.SupportPrices()})
}
