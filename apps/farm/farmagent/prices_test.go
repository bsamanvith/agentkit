package farmagent_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/farmagent"
	"github.com/bsamanvith/agentkit/apps/farm/feeds"
	"github.com/bsamanvith/agentkit/apps/farm/prices"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// priceService has a live provider with today's tomato and tur prices in
// Telangana (synthetic), and a year of saved tur prices.
func priceService(t *testing.T) *prices.Service {
	t.Helper()
	today := prices.NewDay(2026, 11, 7)
	live := feeds.Func[prices.Query, []prices.Quote]{N: "test feed", F: func(_ context.Context, q prices.Query) ([]prices.Quote, error) {
		var out []prices.Quote
		for i, m := range []string{"Bowenpally", "Shamshabad", "Gudimalkapur"} {
			for _, c := range []struct {
				name  string
				modal float64
			}{{"Tomato", 1200}, {"Arhar (Tur/Red Gram)(Whole)", 7900}} {
				x := prices.Quote{
					Date: today, State: "Telangana", District: "Hyderabad", Market: m, Commodity: c.name,
					Min: c.modal * 0.9, Max: c.modal * 1.1, Modal: c.modal + float64(100*i), Source: "test feed",
				}
				if q.Matches(x) {
					out = append(out, x)
				}
			}
		}
		if len(out) == 0 {
			return nil, feeds.ErrNoData
		}
		return out, nil
	}}
	h := prices.NewMemoryHistory()
	var past []prices.Quote
	for d := today.AddDays(-400); d.Before(today); d = d.AddDays(1) {
		past = append(past, prices.Quote{
			Date: d, State: "Telangana", District: "Hyderabad", Market: "Bowenpally",
			Commodity: "Arhar (Tur/Red Gram)(Whole)", Modal: 7500, Min: 7000, Max: 8000, Source: "files",
		})
	}
	if _, err := h.Save(context.Background(), past); err != nil {
		t.Fatal(err)
	}
	return prices.New(h, []prices.Source{live}, prices.WithClock(clocktest.NewFake(time.Date(2026, 11, 8, 6, 0, 0, 0, time.UTC))))
}

func TestPriceToolsFindTheFarmsStateAndAnswerFromTheTools(t *testing.T) {
	t.Parallel()
	svc, _ := service(t)
	tools, err := farmagent.PriceTools(priceService(t), svc)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tool.NewRegistry(tools)
	if err != nil {
		t.Fatal(err)
	}
	defs, _ := reg.List(context.Background())
	for _, d := range defs {
		if d.Kind != tool.KindRead {
			t.Errorf("%s: kind %s", d.Name, d.Kind)
		}
	}
	type out struct {
		Commodity string
		Area      string
		AsOf      string  `json:"as_of"`
		DaysOld   int     `json:"days_old"`
		Median    float64 `json:"median_modal"`
		Markets   []struct {
			Market string
			Modal  float64
		}
		History []struct{ Month string }
		MSP     []prices.SupportPrice
	}
	got := invoke[out](t, reg, farmagent.MandiPricesTool, `{"farm":"farm_1","commodity":"kandulu","days":365}`)
	if got.Commodity != "Arhar (Tur/Red Gram)(Whole)" || got.Area != "Telangana" || got.AsOf != "2026-11-07" || got.DaysOld != 1 {
		t.Fatalf("got %+v", got)
	}
	if len(got.Markets) != 3 || got.Markets[0].Modal != 8100 || got.Median != 8000 || len(got.MSP) != 1 || len(got.History) < 12 {
		t.Fatalf("markets %+v median %v msp %v history %d", got.Markets, got.Median, got.MSP, len(got.History))
	}
	est := invoke[prices.Estimate](t, reg, farmagent.PriceEstimateTool, `{"farm":"farm_1","commodity":"tur","sell_month":1,"yield_q_per_acre":4}`)
	if est.Likely == 0 || est.Revenue == nil || est.Revenue.Total == nil || est.Revenue.Acres == 0 || est.Area != "Telangana" {
		t.Fatalf("estimate %+v", est)
	}
	if _, err := reg.Invoke(context.Background(), farmagent.MandiPricesTool, json.RawMessage(`{"commodity":"tomato"}`)); err == nil {
		t.Fatal("no farm or state: want an error")
	}
	if _, err := reg.Invoke(context.Background(), farmagent.MandiPricesTool, json.RawMessage(`{"state":"Karnataka","commodity":"tomato"}`)); err == nil || !strings.Contains(err.Error(), "no mandi prices") {
		t.Fatalf("Karnataka: err %v", err)
	}
}

func TestAssistantWithPricesHasTheToolsAndTheRules(t *testing.T) {
	t.Parallel()
	svc, _ := service(t)
	skills, err := farmagent.Skills()
	if err != nil {
		t.Fatal(err)
	}
	model := llmtest.NewFake(t,
		llmtest.ToolCalls(llm.ToolUse{ID: "c1", Name: "skill_load", Input: json.RawMessage(`{"name":"sell-or-hold"}`)}),
		llmtest.ToolCalls(llm.ToolUse{ID: "c2", Name: farmagent.MandiPricesTool, Input: json.RawMessage(`{"farm":"farm_1","commodity":"tomato"}`)}),
		llmtest.Text("Tomato is ₹1,400 at Gudimalkapur today."),
	)
	a, err := farmagent.NewAssistant(model, svc, farmagent.WithPrices(priceService(t)), farmagent.WithSkills(skills))
	if err != nil {
		t.Fatal(err)
	}
	r, err := a.Ask(context.Background(), "", "What is tomato selling for?")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.Tools, []string{"skill_load", farmagent.MandiPricesTool}) {
		t.Fatalf("tools %v", r.Tools)
	}
	if req := model.Requests()[0]; !strings.Contains(req.System, "never state a price") || !strings.Contains(req.System, "sell-or-hold") {
		t.Fatal("system prompt lacks the price rules or the skill")
	}
	sets, err := farmagent.Toolsets(svc, farmagent.WithPrices(priceService(t)))
	if err != nil || sets[farmagent.ToolsetMarket] == nil {
		t.Fatalf("toolsets %v, %v", sets, err)
	}
}
