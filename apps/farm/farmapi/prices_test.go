package farmapi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farmapi"
	"github.com/bsamanvith/agentkit/apps/farm/feeds"
	"github.com/bsamanvith/agentkit/apps/farm/prices"
)

func TestPriceRoutes(t *testing.T) {
	t.Parallel()
	h := prices.NewMemoryHistory()
	d := prices.DayOf(prices.NewDay(2026, 11, 7).Time())
	var qs []prices.Quote
	for i := range 200 {
		qs = append(qs, prices.Quote{
			Date: d.AddDays(-i), State: "Telangana", District: "Hyderabad", Market: "Bowenpally",
			Commodity: "Tomato", Min: 900, Max: 1500, Modal: 1200, Source: "price files",
		})
	}
	if _, err := h.Save(context.Background(), qs); err != nil {
		t.Fatal(err)
	}
	down := feeds.Func[prices.Query, []prices.Quote]{N: "Agmarknet via data.gov.in", F: func(context.Context, prices.Query) ([]prices.Quote, error) {
		return nil, feeds.ErrUnavailable
	}}
	srv := server(t, farmapi.WithPrices(prices.New(h, []prices.Source{down})))
	var created farm.Farm
	if code := call(t, srv, "POST", "/api/farms", `{"name":"East field","boundary":`+boundary+`}`, &created); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}

	var today struct {
		Answer prices.Answer
		MSP    []prices.SupportPrice
	}
	if code := call(t, srv, "GET", "/api/prices?farm="+created.ID+"&commodity=tamata", "", &today); code != http.StatusOK ||
		today.Answer.Source != "price files" || len(today.Answer.Markets) != 1 || today.Answer.Query.State != "Telangana" {
		t.Fatalf("today: %d %+v", code, today)
	}
	var hist struct {
		Months []struct {
			Month  string
			Quotes int
		}
	}
	if code := call(t, srv, "GET", "/api/prices/history?state=Telangana&commodity=tomato&days=3650", "", &hist); code != http.StatusOK ||
		len(hist.Months) < 6 || hist.Months[0].Month > hist.Months[1].Month {
		t.Fatalf("history: %d %+v", code, hist)
	}
	var est prices.Estimate
	if code := call(t, srv, "POST", "/api/prices/estimate", `{"farm":"`+created.ID+`","commodity":"tomato","sell_month":12,"yield_q_per_acre":80}`, &est); code != http.StatusOK ||
		est.Likely == 0 || est.Revenue == nil || est.Revenue.Total == nil || est.Revenue.Acres == 0 {
		t.Fatalf("estimate: %d %+v", code, est)
	}
	var sources struct {
		Providers []string
		MSP       []prices.SupportPrice
	}
	if code := call(t, srv, "GET", "/api/prices/sources", "", &sources); code != http.StatusOK || len(sources.Providers) != 2 || len(sources.MSP) < 20 {
		t.Fatalf("sources: %d %+v", code, sources)
	}
	var e struct{ Error string }
	if code := call(t, srv, "GET", "/api/prices?state=Karnataka&commodity=tomato", "", &e); code != http.StatusNotFound || e.Error != "no mandi prices found for Tomato in Karnataka" {
		t.Fatalf("Karnataka: %d %+v", code, e)
	}
	// The geocoder names the district "Guntur District": not "Guntur District district".
	if code := call(t, srv, "GET", "/api/prices?state=Karnataka&district=Kolar%20District&commodity=tomato", "", &e); code != http.StatusNotFound ||
		e.Error != "no mandi prices found for Tomato in Kolar District, Karnataka" {
		t.Fatalf("Kolar District: %d %+v", code, e)
	}
	if code := call(t, srv, "GET", "/api/prices", "", nil); code != http.StatusBadRequest {
		t.Fatalf("empty: %d", code)
	}
	if code := call(t, srv, "POST", "/api/prices/estimate", `{"commodity":"tomato","sell_month":13}`, nil); code != http.StatusBadRequest {
		t.Fatalf("month 13: %d", code)
	}
	if code := call(t, srv, "GET", "/api/prices?farm=nope&commodity=tomato", "", nil); code != http.StatusNotFound {
		t.Fatalf("no farm: %d", code)
	}
	if code := call(t, server(t), "GET", "/api/prices?state=Telangana", "", nil); code != http.StatusServiceUnavailable {
		t.Fatalf("not configured: %d", code)
	}
}
