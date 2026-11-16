package ideas_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/agro/agrotest"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farm/farmtest"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/ideas"
	"github.com/bsamanvith/agentkit/apps/farm/planner"
	"github.com/bsamanvith/agentkit/apps/farm/prices"
	"github.com/bsamanvith/agentkit/apps/farm/region"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

var (
	now = time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	// A 200 m by 100 m plot (2 ha) at Kadapa.
	field = []farm.Corner{
		{Point: geo.Point{Lat: 14.4673, Lng: 78.8242}},
		{Point: geo.Point{Lat: 14.4673, Lng: 78.826058}},
		{Point: geo.Point{Lat: 14.468204, Lng: 78.826058}},
		{Point: geo.Point{Lat: 14.468204, Lng: 78.8242}},
	}
	kadapa = farm.Location{Village: "Kadapa", District: "YSR Kadapa", State: "Andhra Pradesh", Country: "India", CountryCode: "IN"}
)

// setup is an ideas service over one plot at Kadapa with the profile
// given, the embedded book and region packs, and a price history holding
// qs.
func setup(t *testing.T, prof agro.Profile, qs ...prices.Quote) (*ideas.Service, string) {
	t.Helper()
	ctx := context.Background()
	loc := &farmtest.Locator{Location: kadapa}
	svc, err := farm.NewService(&farmtest.Memory{}, farm.NewVerifier(farm.WithLocator(loc)))
	if err != nil {
		t.Fatal(err)
	}
	f, err := svc.Create(ctx, farm.Draft{Name: "Red field", Boundary: farm.Boundary{Corners: field, Source: farm.SourceTyped}})
	if err != nil {
		t.Fatal(err)
	}
	regions, err := region.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	clk := clocktest.NewFake(now)
	p := planner.New(svc, regions.Base().Library(), &agrotest.Source{Clim: agrotest.Hyderabad()},
		planner.WithClock(clk), planner.WithRegions(regions), planner.WithLocator(loc))
	if _, err := p.SetProfile(ctx, f.ID, prof); err != nil {
		t.Fatal(err)
	}
	h := prices.NewMemoryHistory()
	if _, err := h.Save(ctx, qs); err != nil {
		t.Fatal(err)
	}
	book, err := ideas.DefaultBook()
	if err != nil {
		t.Fatal(err)
	}
	return ideas.New(p, prices.New(h, nil, prices.WithClock(clk)), book), f.ID
}

var irrigated = agro.Profile{Soil: "red_sandy", Water: []agro.Water{{Kind: agro.Borewell, Reliable: true}}}

func find(t *testing.T, r ideas.Report, crop string) ideas.Idea {
	t.Helper()
	i := slices.IndexFunc(r.Ideas, func(i ideas.Idea) bool { return i.Crop == crop })
	if i < 0 {
		t.Fatalf("no idea for %s", crop)
	}
	return r.Ideas[i]
}

func ask(t *testing.T, s *ideas.Service, id string, a ideas.Ask) ideas.Report {
	t.Helper()
	r, err := s.For(context.Background(), id, a)
	if err != nil {
		t.Fatal(err)
	}
	if r.Farm != id || r.Place.District != "YSR Kadapa" || r.Today != "2026-11-01" {
		t.Fatalf("farm %q, place %+v, today %s", r.Farm, r.Place, r.Today)
	}
	for _, i := range r.Ideas {
		if i.Why == "" || i.Sell == nil || i.Name == "" || i.Open.Sow == "" {
			t.Errorf("idea %s: no reason, routes, name or sowing date: %+v", i.Crop, i)
		}
		if !slices.Contains([]string{ideas.Realistic, ideas.Stretch, ideas.Unrealistic}, i.Verdict) {
			t.Errorf("idea %s: verdict %q", i.Crop, i.Verdict)
		}
		// The verdict follows the scores: never realistic on a poor open
		// field, never a method stretch without a method that is good.
		if i.Verdict == ideas.Realistic && i.Open.Score < agro.Good {
			t.Errorf("idea %s: realistic at %d in the open", i.Crop, i.Open.Score)
		}
		if i.Band == ideas.BandMethod && (i.With == nil || i.With.Score < agro.Good || i.Verdict != ideas.Stretch) {
			t.Errorf("idea %s: in the methods band with %+v, verdict %s", i.Crop, i.With, i.Verdict)
		}
		if (i.Band == ideas.BandNotHere) != (i.Verdict == ideas.Unrealistic) {
			t.Errorf("idea %s: band %s, verdict %s", i.Crop, i.Band, i.Verdict)
		}
	}
	return r
}

func TestIdeasForAPlot(t *testing.T) {
	t.Parallel()
	s, id := setup(t, irrigated)
	r := ask(t, s, id, ideas.Ask{})
	n := map[string]int{}
	for _, i := range r.Ideas {
		n[i.Band]++
	}
	for _, band := range []string{ideas.BandCommon, ideas.BandNew, ideas.BandNotHere} {
		if n[band] == 0 || n[band] > ideas.DefaultLimit {
			t.Errorf("band %s has %d ideas, want 1 to %d", band, n[band], ideas.DefaultLimit)
		}
	}
	// The district's own crops lead the common band.
	if first := r.Ideas[0]; first.Band != ideas.BandCommon || first.Crop != "castor" || first.Verdict != ideas.Realistic {
		t.Errorf("first idea %s (%s, %s), want castor, a crop of Kadapa", first.Crop, first.Band, first.Verdict)
	}
	// Apple and saffron are what people ask about: they are answered, and
	// the answer is no, with the climate as the reason.
	for _, crop := range []string{"malus_domestica", "crocus_sativus"} {
		i := find(t, r, crop)
		if i.Verdict != ideas.Unrealistic || !i.Experimental || !strings.Contains(i.Why, "climate") && !strings.Contains(i.Why, "hot") {
			t.Errorf("%s: %s, %q", crop, i.Verdict, i.Why)
		}
	}
	// A crop new to India that fits is realistic, and said to be new.
	if i := find(t, r, "chia"); i.Verdict != ideas.Realistic || i.Band != ideas.BandNew || !strings.Contains(i.Why, "new to India") {
		t.Errorf("chia: %+v", i)
	}
	if !slices.ContainsFunc(r.Notes, func(n agro.Note) bool { return n.ID == "ideas.methods" }) || !slices.ContainsFunc(r.Notes, func(n agro.Note) bool { return n.ID == "ideas.money" }) {
		t.Errorf("the report does not say what is assumed and what is left out: %+v", r.Notes)
	}
	// A smaller limit holds for every band.
	for band, c := range func() map[string]int {
		m := map[string]int{}
		for _, i := range ask(t, s, id, ideas.Ask{Limit: 2}).Ideas {
			m[i.Band]++
		}
		return m
	}() {
		if c > 2 {
			t.Errorf("band %s has %d ideas at limit 2", band, c)
		}
	}
	if _, err := s.For(context.Background(), id, ideas.Ask{Limit: -1}); !errors.Is(err, agro.ErrInvalid) {
		t.Errorf("limit -1: %v", err)
	}
}

// A method that needs water is never suggested on a rainfed plot, and the
// report says why.
func TestRainfedPlotGetsNoStructure(t *testing.T) {
	t.Parallel()
	s, id := setup(t, agro.Profile{Soil: "red_sandy"})
	book := s.Book()
	r := ask(t, s, id, ideas.Ask{Limit: 50})
	for _, i := range r.Ideas {
		if i.With == nil {
			continue
		}
		m := book.Methods[slices.IndexFunc(book.Methods, func(m ideas.Method) bool { return m.ID == i.With.Method })]
		if m.NeedsWater {
			t.Errorf("%s: %s suggested on a rainfed plot", i.Crop, m.ID)
		}
	}
	if !slices.ContainsFunc(r.Notes, func(n agro.Note) bool { return n.ID == "ideas.rainfed" }) {
		t.Errorf("notes %+v", r.Notes)
	}
}

// On a soil a crop dislikes, growing it without soil is offered as an idea
// of its own, with the cost; a structure is offered only for the crops
// grown under one.
func TestMethodLiftsACropTheSoilHoldsBack(t *testing.T) {
	t.Parallel()
	s, id := setup(t, agro.Profile{Soil: "black", PH: 7.6, Water: []agro.Water{{Kind: agro.Borewell, Reliable: true}}})
	r := ask(t, s, id, ideas.Ask{Limit: 50})
	var tomato *ideas.Idea
	structures := []string{"shade_net", "polyhouse", "hydroponics"}
	for i, idea := range r.Ideas {
		if idea.Crop == "tomato" && idea.Band == ideas.BandMethod {
			tomato = &r.Ideas[i]
		}
		if idea.With != nil && slices.Contains(structures, idea.With.Method) && slices.Contains([]string{"sweet_potato", "pumpkin", "okra", "cluster_bean"}, idea.Crop) {
			t.Errorf("%s: %s suggested", idea.Crop, idea.With.Method)
		}
	}
	if tomato == nil {
		t.Fatal("no method idea for tomato on black soil")
	}
	if w := tomato.With; w == nil || w.Method != "hydroponics" || w.Score < tomato.Open.Score+ideas.MinLift || w.Gain != nil || w.Cost == nil || tomato.Verdict != ideas.Stretch || !strings.Contains(tomato.Why, "costly") {
		t.Errorf("tomato: %s, %q, with %+v", tomato.Verdict, tomato.Why, w)
	}
	// Asked about by name, it is one answer: realistic, with the method.
	if i := find(t, ask(t, s, id, ideas.Ask{Search: "tomato"}), "tomato"); i.Verdict != ideas.Realistic || i.With == nil || !strings.Contains(i.Why, "Hydroponics") {
		t.Errorf("tomato by name: %s, %q, with %+v", i.Verdict, i.Why, i.With)
	}
}

func TestSearchAnIdea(t *testing.T) {
	t.Parallel()
	s, id := setup(t, irrigated)
	r := ask(t, s, id, ideas.Ask{Search: "saffron"})
	if i := find(t, r, "crocus_sativus"); i.Verdict != ideas.Unrealistic || !strings.Contains(i.Why, "Too hot") || len(r.Ideas) > 3 {
		t.Errorf("saffron: %d ideas, %s, %q", len(r.Ideas), i.Verdict, i.Why)
	}
	// A library crop by its Telugu name.
	if r := ask(t, s, id, ideas.Ask{Search: "వేరుశనగ"}); len(r.Ideas) != 1 || r.Ideas[0].Crop != "groundnut" || r.Ideas[0].Verdict != ideas.Realistic {
		t.Errorf("groundnut in Telugu: %+v", r.Ideas)
	}
	// An invasive plant is found by name, and the answer is no.
	if r := ask(t, s, id, ideas.Ask{Search: "Prosopis juliflora"}); len(r.Ideas) != 1 || r.Ideas[0].Verdict != ideas.Unrealistic || !strings.Contains(r.Ideas[0].Why, "invasive") || r.Ideas[0].With != nil {
		t.Errorf("Prosopis juliflora: %+v", r.Ideas)
	}
	if r := ask(t, s, id, ideas.Ask{Search: "unobtainium"}); len(r.Ideas) != 0 || len(r.Notes) != 1 || r.Notes[0].ID != "ideas.none" {
		t.Errorf("unobtainium: %+v, %+v", r.Ideas, r.Notes)
	}
}

// Prices and the ways to sell: a support price and the yards' prices
// where the history has them, and no market yard for a plant no yard
// trades.
func TestPricesAndRoutes(t *testing.T) {
	t.Parallel()
	q := func(day int, market string, modal float64) prices.Quote {
		return prices.Quote{Date: prices.NewDay(2026, time.October, day), State: "Andhra Pradesh", District: "YSR Kadapa", Market: market, Commodity: "Groundnut", Min: modal - 300, Max: modal + 300, Modal: modal, Source: "test"}
	}
	var qs []prices.Quote
	for d := 1; d <= 28; d++ {
		qs = append(qs, q(d, "Kadapa", 6200), q(d, "Proddatur", 6400))
	}
	s, id := setup(t, irrigated, qs...)
	routes := func(i ideas.Idea) []string {
		var ids []string
		for _, r := range i.Sell {
			ids = append(ids, r.ID)
		}
		return ids
	}
	g := ask(t, s, id, ideas.Ask{Search: "groundnut"}).Ideas[0]
	if g.Price == nil || g.Price.MSP <= 0 || g.Price.Likely < 6000 || g.Price.Likely > 6600 || len(g.Price.Markets) != 2 || g.Price.Markets[0].Market != "Proddatur" {
		t.Errorf("groundnut price %+v", g.Price)
	}
	if got := routes(g); !slices.Equal(got, []string{"msp", "mandi", "enam", "fpo", "processor"}) {
		t.Errorf("groundnut routes %v", got)
	}
	// A vegetable in Andhra Pradesh: no support price, a Rythu Bazaar.
	if got := routes(ask(t, s, id, ideas.Ask{Search: "brinjal"}).Ideas[0]); !slices.Equal(got, []string{"mandi", "enam", "rythu_bazaar", "fpo", "processor"}) {
		t.Errorf("brinjal routes %v", got)
	}
	// An experimental plant: no yard, no price.
	o := ask(t, s, id, ideas.Ask{Search: "Olea europaea"}).Ideas[0]
	if got := routes(o); o.Price != nil || slices.Contains(got, "mandi") || !slices.Contains(got, "fpo") {
		t.Errorf("olive: price %+v, routes %v", o.Price, got)
	}
}

func TestBook(t *testing.T) {
	t.Parallel()
	b, err := ideas.DefaultBook()
	if err != nil {
		t.Fatal(err)
	}
	wide, err := agro.Wide()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range b.Asked {
		if _, err := wide.Crop(id); err != nil {
			t.Errorf("asked-about plant %s is not in the experimental tier", id)
		}
	}
	for name, doc := range map[string]string{
		"an unknown field":              `{"sources":[],"assumed":"x","methods":[],"asked":[],"routes":[],"extra":1}`,
		"nothing said to be assumed":    `{"sources":[],"assumed":"","methods":[],"asked":[],"routes":[]}`,
		"a method that changes nothing": `{"sources":[],"assumed":"x","methods":[{"id":"m","name":"M","what":"w","skill":"low"}],"asked":[],"routes":[]}`,
		"a cost without its source":     `{"sources":[],"assumed":"x","methods":[{"id":"m","name":"M","what":"w","skill":"low","drip":true,"cost_per_sqm":10}],"asked":[],"routes":[]}`,
		"a skill that is none":          `{"sources":[],"assumed":"x","methods":[{"id":"m","name":"M","what":"w","skill":"expert","drip":true}],"asked":[],"routes":[]}`,
		"a route that says nothing":     `{"sources":[],"assumed":"x","methods":[],"asked":[],"routes":[{"id":"r","name":"R"}]}`,
	} {
		if _, err := ideas.LoadBook([]byte(doc)); err == nil {
			t.Errorf("a book with %s loaded", name)
		}
	}
}

// What each method changes for the crop, and what it costs on a plot.
func TestMethods(t *testing.T) {
	t.Parallel()
	b, err := ideas.DefaultBook()
	if err != nil {
		t.Fatal(err)
	}
	method := func(id string) ideas.Method {
		return b.Methods[slices.IndexFunc(b.Methods, func(m ideas.Method) bool { return m.ID == id })]
	}
	lib, err := agro.Default()
	if err != nil {
		t.Fatal(err)
	}
	tomato, _ := lib.Crop("tomato")
	mango, _ := lib.Crop("mango")
	black, _ := lib.Soil("black")
	base := agro.Request{Climate: agrotest.Hyderabad(), Soil: &black, Profile: agro.Profile{Soil: "black"}}
	may := base.Climate.Months[4]

	c, r := method("shade_net").Apply(tomato, base)
	if m := r.Climate.Months[4]; m.TempMax != may.TempMax-3 || m.SunMJ != may.SunMJ*0.5 || m.RainMM != may.RainMM || !r.Profile.Drip || c.WaterMM[0] >= tomato.WaterMM[0] {
		t.Errorf("shade net: May %+v, drip %t, water %v", m, r.Profile.Drip, c.WaterMM)
	}
	if _, r := method("polyhouse").Apply(tomato, base); r.Climate.Months[6].RainMM != 0 || r.Climate.Months[0].TempMin != base.Climate.Months[0].TempMin+3 {
		t.Errorf("polyhouse: July rain %v, January low %v", r.Climate.Months[6].RainMM, r.Climate.Months[0].TempMin)
	}
	if _, r := method("raised_beds").Apply(tomato, base); r.Soil.DrainsPoorly() || black.Drainage == r.Soil.Drainage {
		t.Errorf("raised beds: drainage %s (the library's soil: %s)", r.Soil.Drainage, black.Drainage)
	}
	if _, r := method("hydroponics").Apply(tomato, base); r.Soil != nil || r.Profile.Soil != "" {
		t.Errorf("hydroponics: soil %+v", r.Soil)
	}
	if base.Climate.Months[4] != may || base.Profile.Drip || tomato.WaterMM != c.WaterMM && tomato.WaterMM[0] == c.WaterMM[0] {
		t.Error("a method changed the plot it was applied to")
	}
	// A structure is for a low one-season crop, not an orchard.
	if !method("shade_net").Fits(tomato) || method("shade_net").Fits(mango) || method("polyhouse").Fits(mango) {
		t.Error("shade net and polyhouse: want tomato, not mango")
	}
	// A method with a crop list is for those crops alone, and every crop
	// listed is one the app knows.
	wide, err := agro.Wide()
	if err != nil {
		t.Fatal(err)
	}
	okra, _ := lib.Crop("okra")
	if method("hydroponics").Fits(okra) || !method("hydroponics").Fits(tomato) {
		t.Error("hydroponics: want tomato, not okra")
	}
	for _, m := range b.Methods {
		for _, id := range m.Crops {
			if _, err := lib.Crop(id); err != nil {
				if _, err := wide.Crop(id); err != nil {
					t.Errorf("method %s: crop %s is in neither library", m.ID, id)
				}
			}
		}
	}
	// The scheme's norm: Rs 710 a square metre, half assisted, on at most
	// 2,500 square metres however large the plot.
	if c := method("shade_net").CostOn(20000); c == nil || c.SqM != 2500 || c.Total != 1775000 || c.Subsidy != 887500 || c.Farmer != 887500 || c.Ref == nil {
		t.Errorf("shade net on 2 ha: %+v", c)
	}
	if c := method("mulch_drip").CostOn(5000); c == nil || c.Total != 20000 || c.Farmer != 10000 {
		t.Errorf("mulch on half a hectare: %+v", c)
	}
	if c := method("raised_beds").CostOn(5000); c != nil {
		t.Errorf("raised beds have no cost norm, got %+v", c)
	}
}

func TestGain(t *testing.T) {
	t.Parallel()
	cal := func(scores ...int) []agro.Sowing {
		out := make([]agro.Sowing, len(scores))
		for i, s := range scores {
			out[i] = agro.Sowing{Sow: time.Date(2026, 11, 1+7*i, 0, 0, 0, 0, time.UTC).Format(agro.DateLayout), Score: s}
		}
		return out
	}
	// Weeks 2 to 4 are good only under the method; week 6 is one more,
	// alone; week 0 was good in the open already.
	g := ideas.GainOf(cal(80, 30, 30, 30, 30, 30, 30), cal(90, 20, 70, 65, 60, 10, 75))
	if g == nil || g.Weeks != 3 || g.From != "2026-11-15" || g.To != "2026-11-29" {
		t.Errorf("gain %+v", g)
	}
	if g := ideas.GainOf(cal(80, 80), cal(90, 90)); g != nil {
		t.Errorf("nothing gained, got %+v", g)
	}
}
