package prices_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/feeds"
	"github.com/bsamanvith/agentkit/apps/farm/prices"
	"github.com/bsamanvith/agentkit/apps/farm/prices/pricesqlite"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

// dataGovAnswer is shaped like api.data.gov.in's answer for the daily mandi
// resource (synthetic values): lower-case fields, prices as strings or
// numbers, dates as dd/mm/yyyy.
const dataGovAnswer = `{
 "status": "ok", "total": 4, "count": 4, "limit": "1000", "offset": "0",
 "records": [
  {"state": "Telangana", "district": "Rangareddi", "market": "Shamshabad", "commodity": "Tomato", "variety": "Deshi", "grade": "FAQ", "arrival_date": "07/11/2026", "min_price": "900", "max_price": "1400", "modal_price": "1200"},
  {"state": "Telangana", "district": "Hyderabad", "market": "Bowenpally", "commodity": "Tomato", "variety": "Hybrid", "grade": "FAQ", "arrival_date": "07/11/2026", "min_price": 1000, "max_price": 1600, "modal_price": 1300},
  {"state": "Telangana", "district": "Rangareddi", "market": "Shamshabad", "commodity": "Tomato", "variety": "Deshi", "grade": "FAQ", "arrival_date": "06/11/2026", "min_price": "800", "max_price": "1300", "modal_price": "1100"},
  {"state": "Telangana", "district": "Warangal", "market": "Warangal", "commodity": "Tomato", "variety": "Deshi", "grade": "FAQ", "arrival_date": "bad", "min_price": "1", "max_price": "2", "modal_price": "1"}
 ]}`

func TestDataGovAsksByStateAndCommodityAndFiltersTheRest(t *testing.T) {
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.String()
		if r.URL.Path != "/resource/"+prices.DataGovDaily || r.URL.Query().Get("api-key") != "secret-key" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		if r.URL.Query().Get("filters[state.keyword]") != "Telangana" || r.URL.Query().Get("filters[commodity]") != "Tomato" {
			_, _ = w.Write([]byte(`{"status":"ok","records":[]}`))
			return
		}
		_, _ = w.Write([]byte(dataGovAnswer))
	}))
	defer srv.Close()
	src, err := prices.DataGov("secret-key", prices.WithDataGovURL(srv.URL), prices.WithDataGovHTTPClient(srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	got, err := src.Fetch(context.Background(), prices.Query{State: "telangana", District: "Rangareddy", Commodity: "tamata"})
	if err != nil {
		t.Fatalf("Fetch: %v (asked %s)", err, asked)
	}
	if len(got) != 2 || got[0].Date.String() != "2026-11-07" || got[0].Modal != 1200 || got[1].Modal != 1100 {
		t.Fatalf("got %+v; want Shamshabad's two days, newest first (Rangareddy matches Rangareddi)", got)
	}
	if got[0].Source != "Agmarknet via data.gov.in" {
		t.Errorf("source %q", got[0].Source)
	}
	all, err := src.Fetch(context.Background(), prices.Query{State: "TS", Commodity: "tomato"})
	if err != nil || len(all) != 3 {
		t.Fatalf("state-wide: %d quotes, %v; want 3 (the bad date skipped)", len(all), err)
	}
	if _, err := src.Fetch(context.Background(), prices.Query{State: "Telangana", Commodity: "onion"}); !errors.Is(err, feeds.ErrNoData) {
		t.Fatalf("no records: err %v; want ErrNoData", err)
	}
}

func TestDataGovNeedsAKeyAndKeepsItOutOfErrors(t *testing.T) {
	if _, err := prices.DataGov(" "); !errors.Is(err, feeds.ErrNotConfigured) {
		t.Fatalf("no key: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	src, _ := prices.DataGov("secret-key", prices.WithDataGovURL(url))
	_, err := src.Fetch(context.Background(), prices.Query{State: "Karnataka"})
	if !errors.Is(err, feeds.ErrUnavailable) {
		t.Fatalf("refused: %v", err)
	}
	if strings.Contains(err.Error(), "secret-key") {
		t.Fatalf("the key is in the error: %v", err)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFilesReadEachSourcesExportAndSeeChanges(t *testing.T) {
	dir := t.TempDir()
	// Agmarknet's report export: no state column, so the folder names it.
	write(t, filepath.Join(dir, "karnataka", "onion-2026.csv"), "\uFEFFSl no.,District Name,Market Name,Commodity,Variety,Grade,Min Price (Rs./Quintal),Max Price (Rs./Quintal),Modal Price (Rs./Quintal),Price Date\n"+
		"1,Bangalore,Bangalore,Onion,Big,FAQ,\"1,800\",\"2,600\",\"2,200\",05 Nov 2026\n"+
		"2,Kolar,Kolar,Onion,Big,FAQ,1700,2400,2100,05-Nov-2026\n"+
		"3,Kolar,Kolar,Onion,Big,FAQ,,,,05 Nov 2026\n")
	// data.gov.in's CSV.
	write(t, filepath.Join(dir, "datagov.csv"), "State,District,Market,Commodity,Variety,Grade,Arrival_Date,Min_x0020_Price,Max_x0020_Price,Modal_x0020_Price\n"+
		"Tamil Nadu,Coimbatore,Coimbatore(Uzhavar Sandhai ),Tomato,Hybrid,FAQ,04/11/2026,1500,2000,1800\n")
	write(t, filepath.Join(dir, "notes.txt"), "not a price file")
	write(t, filepath.Join(dir, "broken.csv"), "a,b\n1,2\n")

	f, err := prices.Files(dir)
	if err != nil {
		t.Fatal(err)
	}
	all, stats, err := f.All(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || stats.Files != 3 || stats.Quotes != 3 || stats.Skipped != 1 || len(stats.Errors) != 1 || !strings.Contains(stats.Errors[0], "broken.csv") {
		t.Fatalf("all %d, stats %+v", len(all), stats)
	}
	got, err := f.Fetch(context.Background(), prices.Query{State: "Karnataka", Commodity: "onion"})
	if err != nil || len(got) != 2 || got[0].State != "Karnataka" || got[0].Modal != 2200 || got[0].Min != 1800 {
		t.Fatalf("Karnataka onion: %+v, %v", got, err)
	}
	// A new file is seen without a restart; a removed one is forgotten.
	write(t, filepath.Join(dir, "telangana", "more.csv"), "date,market,district,commodity,modal\n2026-11-06,Bowenpally,Hyderabad,Tomato,1250\n")
	if err := os.Remove(filepath.Join(dir, "datagov.csv")); err != nil {
		t.Fatal(err)
	}
	got, err = f.Fetch(context.Background(), prices.Query{Commodity: "tomato"})
	if err != nil || len(got) != 1 || got[0].State != "Telangana" || got[0].Market != "Bowenpally" {
		t.Fatalf("after the change: %+v, %v", got, err)
	}
	if _, err := prices.Files(""); !errors.Is(err, feeds.ErrNotConfigured) {
		t.Fatalf("no folder: %v", err)
	}
}

func histories(t *testing.T) map[string]prices.History {
	s, err := pricesqlite.Open(context.Background(), filepath.Join(t.TempDir(), "prices.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return map[string]prices.History{"memory": prices.NewMemoryHistory(), "sqlite": s}
}

func quote(day prices.Day, district, market, commodity string, modal float64) prices.Quote {
	return prices.Quote{
		Date: day, State: "Telangana", District: district, Market: market, Commodity: commodity, Variety: "Other", Grade: "FAQ",
		Min: modal * 0.8, Max: modal * 1.2, Modal: modal, Source: "test",
	}
}

func TestHistoriesKeepOneQuoteADayAndMatchLikeAQuery(t *testing.T) {
	ctx := context.Background()
	d := prices.NewDay(2026, 11, 7)
	for name, h := range histories(t) {
		t.Run(name, func(t *testing.T) {
			n, err := h.Save(ctx, []prices.Quote{
				quote(d, "Rangareddi", "Shamshabad", "Tomato", 1200),
				quote(d.AddDays(-1), "Rangareddi", "Shamshabad", "Tomato", 1100),
				quote(d, "Hyderabad", "Bowenpally", "Cotton(Unginned)", 7400),
				{Date: d, Market: "x", Commodity: "Tomato"}, // no price: not kept
			})
			if err != nil || n != 3 {
				t.Fatalf("Save = %d, %v; want 3", n, err)
			}
			n, err = h.Save(ctx, []prices.Quote{quote(d, "Rangareddi", "Shamshabad", "Tomato", 1250)})
			if err != nil || n != 0 {
				t.Fatalf("again = %d, %v; want 0 new", n, err)
			}
			got, err := h.Quotes(ctx, prices.Query{State: "telangana", District: "rangareddy", Commodity: "tomato"})
			if err != nil || len(got) != 2 || got[0].Modal != 1250 || got[1].Date != d.AddDays(-1) {
				t.Fatalf("tomato: %+v, %v", got, err)
			}
			got, _ = h.Quotes(ctx, prices.Query{State: "Telangana", Commodity: "cotton"})
			if len(got) != 1 || got[0].Commodity != "Cotton(Unginned)" {
				t.Fatalf("cotton matches Cotton(Unginned): %+v", got)
			}
			got, _ = h.Quotes(ctx, prices.Query{State: "Telangana", From: d, To: d, Limit: 1})
			if len(got) != 1 || got[0].Date != d {
				t.Fatalf("one day, limit 1: %+v", got)
			}
			if _, err := h.Quotes(ctx, prices.Query{}); !errors.Is(err, prices.ErrInvalid) {
				t.Fatalf("empty query: %v", err)
			}
		})
	}
}

func TestCommodityNamesFromCropsAndLocalNames(t *testing.T) {
	for in, want := range map[string]string{
		"pigeon_pea": "Arhar (Tur/Red Gram)(Whole)", "Kandulu": "Arhar (Tur/Red Gram)(Whole)", "rice": "Paddy(Dhan)(Common)",
		"okra": "Bhindi(Ladies Finger)", "Tomato": "Tomato", "Dragon fruit": "Dragon fruit",
	} {
		if got := prices.Commodity(in); got != want {
			t.Errorf("Commodity(%q) = %q; want %q", in, got, want)
		}
	}
	if !prices.SameCommodity("cotton", "Cotton(Unginned)") || prices.SameCommodity("Green Chilli", "Green Gram (Moong)(Whole)") {
		t.Error("SameCommodity")
	}
	if m := prices.MSP("tur"); len(m) != 1 || m[0].Price != 8450 {
		t.Errorf("MSP(tur) = %+v", m)
	}
	if m := prices.MSP("tomato"); m != nil {
		t.Errorf("tomato has no MSP: %+v", m)
	}
}

// seasonal saves two years of daily tomato prices in three Telangana
// markets: 1000 a quintal on average, twice as dear in July as in January
// (a cosine over the year), plus a market's offset.
func seasonal(t *testing.T, h prices.History, from, to prices.Day) {
	t.Helper()
	var qs []prices.Quote
	for d := from; !to.Before(d); d = d.AddDays(1) {
		m := float64(d.Time().Month() - 1)
		base := 1000 * (1 - 0.33*math.Cos(2*math.Pi*m/12))
		for i, mk := range []string{"Shamshabad", "Bowenpally", "Gudimalkapur"} {
			qs = append(qs, quote(d, "Rangareddi", mk, "Tomato", math.Round(base*(0.95+0.05*float64(i)))))
		}
	}
	if _, err := h.Save(context.Background(), qs); err != nil {
		t.Fatal(err)
	}
}

func TestEstimateMovesTheRecentLevelByTheSeasonalSwing(t *testing.T) {
	ctx := context.Background()
	today := prices.NewDay(2026, 11, 8)
	for name, h := range histories(t) {
		t.Run(name, func(t *testing.T) {
			seasonal(t, h, prices.NewDay(2024, 11, 1), prices.NewDay(2026, 11, 7))
			e, err := prices.EstimatePrice(ctx, h, prices.EstimateQuery{Commodity: "tomato", State: "Telangana", District: "Rangareddy", SellMonth: time.July, YieldQPerAcre: 100, Acres: 2}, today)
			if err != nil {
				t.Fatal(err)
			}
			// November's factor is 1-0.33*cos(2π·10/12) ≈ 0.835, July's
			// 1.33; the level is November's ≈ 835.
			if e.Area != "Rangareddy district, Telangana" || e.Confidence != "good" || e.Seasonal == nil {
				t.Fatalf("area %q, confidence %q, seasonal %v", e.Area, e.Confidence, e.Seasonal)
			}
			wantLikely := e.Now * 1.33 / 0.835
			if math.Abs(e.Likely-wantLikely)/wantLikely > 0.03 {
				t.Fatalf("likely %v; want about %v (now %v)", e.Likely, wantLikely, e.Now)
			}
			if !(e.Low < e.Likely && e.Likely < e.High) || (e.High-e.Low)/e.Likely < 0.5 {
				t.Fatalf("range %v..%v..%v; selling 8 months ahead should be wide", e.Low, e.Likely, e.High)
			}
			if e.Revenue == nil || e.Revenue.Total == nil || e.Revenue.Total.Likely != math.Round(e.Likely*100*2) {
				t.Fatalf("revenue %+v", e.Revenue)
			}
			if e.Basis.Markets != 3 || e.Basis.Months < 24 || e.AsOf != prices.NewDay(2026, 11, 7) {
				t.Fatalf("basis %+v as of %s", e.Basis, e.AsOf)
			}
			// Selling now is the level, with a narrow range.
			n, _ := prices.EstimatePrice(ctx, h, prices.EstimateQuery{Commodity: "tomato", State: "Telangana"}, today)
			if n.SellMonth != time.November || math.Abs(n.Likely-n.Now) > 1 || (n.High-n.Low)/n.Likely > 0.25 {
				t.Fatalf("now: %+v", n)
			}
		})
	}
}

func TestEstimateWidensToTheStateAndSaysWhenHistoryIsThinOrOld(t *testing.T) {
	ctx := context.Background()
	h := prices.NewMemoryHistory()
	// Two months of tomato in Hyderabad only, ending in September.
	seasonal(t, h, prices.NewDay(2026, 8, 1), prices.NewDay(2026, 9, 30))
	e, err := prices.EstimatePrice(ctx, h, prices.EstimateQuery{Commodity: "tomato", State: "Telangana", District: "Nalgonda"}, prices.NewDay(2026, 11, 8))
	if err != nil {
		t.Fatal(err)
	}
	if e.Area != "Telangana" || e.Seasonal != nil || e.Confidence != "fair" || e.Likely != e.Now || !strings.Contains(strings.Join(e.Notes, " "), "Less than a year") {
		t.Fatalf("estimate %+v", e)
	}
	// Two months on, the newest price is old: confidence drops and the
	// notes say so.
	old, _ := prices.EstimatePrice(ctx, h, prices.EstimateQuery{Commodity: "tomato", State: "Telangana"}, prices.NewDay(2026, 12, 1))
	if old.Confidence != "low" || !strings.Contains(strings.Join(old.Notes, " "), "62 days ago") {
		t.Fatalf("old: %q, %q", old.Confidence, old.Notes)
	}

	none, err := prices.EstimatePrice(ctx, h, prices.EstimateQuery{Commodity: "tur", State: "Karnataka"}, prices.NewDay(2026, 11, 8))
	if err != nil || none.Likely != 0 || len(none.MSP) != 1 || !strings.Contains(strings.Join(none.Notes, " "), "₹8,450") {
		t.Fatalf("no history: %+v, %v", none, err)
	}
	if _, err := prices.EstimatePrice(ctx, h, prices.EstimateQuery{Commodity: "tur", SellMonth: 13}, prices.NewDay(2026, 11, 8)); !errors.Is(err, prices.ErrInvalid) {
		t.Fatalf("month 13: %v", err)
	}
}

func TestServiceFallsBackToSavedPricesAndWidensToTheState(t *testing.T) {
	ctx := context.Background()
	k := clocktest.NewFake(time.Date(2026, 11, 8, 6, 0, 0, 0, time.UTC))
	h := prices.NewMemoryHistory()
	up := true
	calls := 0
	live := feeds.Func[prices.Query, []prices.Quote]{N: "live", F: func(_ context.Context, q prices.Query) ([]prices.Quote, error) {
		calls++
		if !up {
			return nil, feeds.ErrUnavailable
		}
		x := quote(prices.NewDay(2026, 11, 7), "Hyderabad", "Bowenpally", "Tomato", 1300)
		x.Source = "live"
		if !q.Matches(x) {
			return nil, feeds.ErrNoData
		}
		return []prices.Quote{x}, nil
	}}
	s := prices.New(h, []prices.Source{live}, prices.WithClock(k))
	a, err := s.Today(ctx, prices.Query{State: "Telangana", District: "Nalgonda", Commodity: "tomato"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Source != "live" || a.Widened == "" || a.DaysOld != 1 || len(a.Markets) != 1 {
		t.Fatalf("answer %+v", a)
	}
	// The live answer was saved: with the provider down (and the cache
	// expired) the saved price answers.
	up = false
	k.Advance(prices.CacheTTL + time.Minute)
	a, err = s.Today(ctx, prices.Query{State: "Telangana", Commodity: "tomato"})
	if err != nil || a.Source != "live" || calls != 3 {
		t.Fatalf("saved: %+v, %v, calls %d", a, err, calls)
	}
	if _, err := s.Today(ctx, prices.Query{State: "Telangana", Commodity: "onion"}); !errors.Is(err, feeds.ErrNoData) {
		t.Fatalf("onion: %v", err)
	}
	if got := s.Providers(); strings.Join(got, ",") != "live,saved prices" {
		t.Fatalf("providers %v", got)
	}
}

func TestServiceImportsPriceFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "tamil-nadu", "groundnut.csv"), "Market Name,District Name,Commodity,Modal Price (Rs./Quintal),Price Date\nVillupuram,Villupuram,Groundnut,6900,03 Nov 2026\n")
	f, err := prices.Files(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := prices.NewMemoryHistory()
	s := prices.New(h, nil, prices.WithFiles(f))
	stats, n, err := s.Import(context.Background())
	if err != nil || n != 1 || stats.Quotes != 1 {
		t.Fatalf("Import = %+v, %d, %v", stats, n, err)
	}
	a, err := s.Today(context.Background(), prices.Query{State: "Tamil Nadu", Commodity: "verusenaga"})
	if err != nil || a.Markets[0].Modal != 6900 || a.Source != "price files" {
		t.Fatalf("Today = %+v, %v", a, err)
	}
	b, _ := json.Marshal(a.Quotes[0])
	if !strings.Contains(string(b), `"date":"2026-11-03"`) {
		t.Fatalf("JSON %s", b)
	}
}

func TestTodayAddsTheRestOfTheStatesMarkets(t *testing.T) {
	ctx := context.Background()
	h := prices.NewMemoryHistory()
	d := prices.NewDay(2026, 11, 6)
	if _, err := h.Save(ctx, []prices.Quote{
		quote(d, "Rangareddi", "Shadnagar", "Tomato", 1200),
		quote(d, "Vikarabad", "Tandur", "Tomato", 1350),
		quote(d.AddDays(-30), "Medak", "Siddipet", "Tomato", 900), // too old to sit beside today's
	}); err != nil {
		t.Fatal(err)
	}
	s := prices.New(h, nil, prices.WithClock(clocktest.NewFake(time.Date(2026, 11, 8, 6, 0, 0, 0, time.UTC))))
	a, err := s.Today(ctx, prices.Query{State: "Telangana", District: "Ranga Reddy", Commodity: "tomato"})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Markets) != 1 || a.Markets[0].Market != "Shadnagar" || len(a.Elsewhere) != 1 || a.Elsewhere[0].Market != "Tandur" {
		t.Fatalf("markets %+v elsewhere %+v", a.Markets, a.Elsewhere)
	}
}

func TestINR(t *testing.T) {
	for v, want := range map[float64]string{0: "₹0", 999: "₹999", 1000: "₹1,000", 123456.4: "₹1,23,456", 12345678: "₹1,23,45,678", -2500: "-₹2,500"} {
		if got := prices.INR(v); got != want {
			t.Errorf("INR(%v) = %q; want %q", v, got, want)
		}
	}
}
