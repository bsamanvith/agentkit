package addon_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/agro/agrotest"
	"github.com/bsamanvith/agentkit/apps/farm/calendar"
	"github.com/bsamanvith/agentkit/apps/farm/cycle"
	"github.com/bsamanvith/agentkit/apps/farm/cycle/addon"
	"github.com/bsamanvith/agentkit/apps/farm/cycle/cycletest"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farm/farmtest"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/inputs"
	"github.com/bsamanvith/agentkit/apps/farm/planner"
	"github.com/bsamanvith/agentkit/apps/farm/prices"
	"github.com/bsamanvith/agentkit/apps/farm/prices/synthetic"
	"github.com/bsamanvith/agentkit/apps/farm/work"
	"github.com/bsamanvith/agentkit/apps/farm/work/worktest"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

var ctx = context.Background()

// 1 June 2026: the monsoon is coming to a plot near Hyderabad.
var now = time.Date(2026, 6, 1, 0, 30, 0, 0, time.UTC)

// About 2 hectares.
var field = []farm.Corner{
	{Point: geo.Point{Lat: 17.07, Lng: 78.20}},
	{Point: geo.Point{Lat: 17.07, Lng: 78.201883}},
	{Point: geo.Point{Lat: 17.070904, Lng: 78.201883}},
	{Point: geo.Point{Lat: 17.070904, Lng: 78.20}},
}

type world struct {
	svc    *farm.Service
	lib    *agro.Library
	book   *inputs.Book
	store  *cycletest.Memory
	work   *work.Service
	cycles *cycle.Service
	plot   string
}

func setup(t *testing.T, irrigated bool) *world {
	t.Helper()
	clk := clocktest.NewFake(now)
	svc, err := farm.NewService(&farmtest.Memory{}, farm.NewVerifier(), farm.WithServiceClock(clk))
	if err != nil {
		t.Fatal(err)
	}
	lib, err := agro.Default()
	if err != nil {
		t.Fatal(err)
	}
	book, err := inputs.Default(lib)
	if err != nil {
		t.Fatal(err)
	}
	w := &world{svc: svc, lib: lib, book: book, store: cycletest.NewMemory()}
	plan := planner.New(svc, lib, &agrotest.Source{Clim: agrotest.Hyderabad()}, planner.WithClock(clk))
	w.work = work.New(plan, worktest.NewMemory(), work.WithTasks(addon.NewRoadmap(book, lib, w.store)))
	w.work.SetNow(clk.Now)
	w.cycles = cycle.New(plan, w.store, cycle.WithRoadmap(w.work), cycle.WithAddons(addon.NewInputs(book, lib)))
	w.cycles.SetNow(clk.Now)
	f, err := svc.Create(ctx, farm.Draft{Name: "Farooqnagar plot", Boundary: farm.Boundary{Corners: field, Source: farm.SourceTyped}})
	if err != nil {
		t.Fatal(err)
	}
	w.plot = f.ID
	prof := agro.Profile{Soil: "black", PreviousCrop: "maize"}
	if irrigated {
		prof.Water = []agro.Water{{Kind: agro.Borewell, Reliable: true}}
	}
	if _, err := plan.SetProfile(ctx, f.ID, prof); err != nil {
		t.Fatal(err)
	}
	return w
}

func inputsOf(t *testing.T, p cycle.Position) addon.InputsPlan {
	t.Helper()
	v, ok := p.Addons[addon.InputsName].(addon.InputsPlan)
	if !ok {
		t.Fatalf("cycle %d: no inputs plan in %v", p.Position, p.Addons)
	}
	return v
}

// Each planned cycle carries the inputs plan of its areas, method and the
// plot's water: exactly the book's plan for that request (no number of
// its own), for the plot's area, with the starter-values and label notes
// first. Organic swaps every synthetic product out; switching back brings
// them back.
func TestInputsPlanEachCycle(t *testing.T) {
	t.Parallel()
	w := setup(t, true)
	l, err := w.cycles.Line(ctx, w.plot)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Cycles) < 2 {
		t.Fatalf("line %+v", l)
	}
	for _, p := range l.Cycles {
		if p.Planned == nil {
			continue
		}
		got := inputsOf(t, p)
		var req inputs.Request
		ha := 0.0
		for _, a := range p.Planned.Areas {
			req.Fields = append(req.Fields, inputs.Field{Crop: a.Crop, Hectares: a.AreaHa, Sow: a.Sow})
			ha += a.AreaHa
		}
		req.Method, req.Water = inputs.Conventional, inputs.Irrigated
		want, err := w.book.Plan(req)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.EqualFunc(got.BuyBefore, want.BuyBefore, sameItem) || !slices.EqualFunc(got.BuyLater, want.BuyLater, sameItem) || len(got.Schedule) != len(want.Schedule) || len(got.IfSeen) != len(want.IfSeen) {
			t.Errorf("cycle %d: plan differs from the book's for %+v", p.Position, req)
		}
		if got.Water != inputs.Irrigated || got.Method != inputs.Conventional || len(got.Notes) < 2 || got.Notes[0] != inputs.StarterNote || got.Notes[1] != inputs.LabelNote {
			t.Errorf("cycle %d: water %s, method %s, notes %v", p.Position, got.Water, got.Method, got.Notes)
		}
		if acres := ha * inputs.AcresPerHectare; got.Acres < acres-0.01 || got.Acres > acres+0.01 || len(got.Fields) != len(p.Planned.Areas) || got.Fields[0].Crop != p.Planned.Areas[0].Crop || got.Fields[0].CropName == "" {
			t.Errorf("cycle %d: acres %v (want %v), fields %+v", p.Position, got.Acres, acres, got.Fields)
		}
	}
	synthetic := func(p addon.InputsPlan) bool {
		for _, it := range slices.Concat(p.BuyBefore, p.BuyLater) {
			if it.Synthetic {
				return true
			}
		}
		for _, s := range p.Schedule {
			for _, d := range s.Doses {
				if d.Synthetic {
					return true
				}
			}
		}
		return false
	}
	if !synthetic(inputsOf(t, l.Cycles[0])) {
		t.Fatalf("a conventional plan of %s with no synthetic product: the organic test below proves nothing", l.Cycles[0].Planned.MainCrop())
	}
	l, err = w.cycles.Set(ctx, w.plot, 0, cycle.Change{Method: cycle.Organic})
	if err != nil {
		t.Fatal(err)
	}
	if p := inputsOf(t, l.Cycles[0]); p.Method != inputs.Organic || synthetic(p) {
		t.Errorf("organic: method %s, synthetic products listed", p.Method)
	}
	if l, err = w.cycles.Set(ctx, w.plot, 0, cycle.Change{Method: cycle.Conventional}); err != nil || !synthetic(inputsOf(t, l.Cycles[0])) {
		t.Errorf("back to conventional: %v", err)
	}
}

func sameItem(a, b inputs.Item) bool {
	return a.Product == b.Product && a.Qty == b.Qty && a.Unit == b.Unit && a.Packs == b.Packs
}

// A rainfed plot plans rainfed; an intercropped area is two fields, split
// by its rows; an area of no size has nothing to plan.
func TestInputsWaterAndIntercrop(t *testing.T) {
	t.Parallel()
	w := setup(t, false)
	f, err := w.svc.Get(ctx, w.plot)
	if err != nil {
		t.Fatal(err)
	}
	a := addon.NewInputs(w.book, w.lib)
	c := cycle.Cycle{
		Method: cycle.Conventional,
		Plan:   agro.Plan{Zones: []agro.Zone{{Crop: "sorghum", With: "pigeon_pea", Pattern: [2]int{4, 1}, Sow: "2026-06-20"}}},
		Areas:  []cycle.Area{{Zone: 0, Crop: "sorghum", With: "pigeon_pea", AreaHa: 1, Sow: "2026-06-20"}},
	}
	v, err := a.For(ctx, f, c)
	if err != nil {
		t.Fatal(err)
	}
	p := v.(addon.InputsPlan)
	if p.Water != inputs.Rainfed || len(p.Fields) != 2 || p.Fields[0].Hectares != 0.8 || p.Fields[1].Hectares != 0.2 || p.Fields[1].Crop != "pigeon_pea" || p.Fields[1].Area != 0 {
		t.Errorf("intercrop: water %s, fields %+v", p.Water, p.Fields)
	}
	c.Areas[0].AreaHa = 0
	if v, err := a.For(ctx, f, c); err != nil || v != nil {
		t.Errorf("no area: %v, %v; want nothing", v, err)
	}
}

// The season roadmap carries the current cycle's schedule: an entry on
// or near a calendar task enriches that task (no twin), the rest are
// tasks of their own; the notes say these are starter values and the label wins. Ticking
// one is recorded like any task and survives a later sowing, which moves
// it with the crop. Organic changes the details, not the tasks' IDs.
func TestRoadmapCarriesTheSchedule(t *testing.T) {
	t.Parallel()
	w := setup(t, true)
	// Groundnut has its own feeds (gypsum at 45 days) and pests.
	if _, err := w.cycles.Set(ctx, w.plot, 0, cycle.Change{Plan: &agro.Plan{Zones: []agro.Zone{{Crop: "groundnut", Sow: "2026-06-20"}}}}); err != nil {
		t.Fatal(err)
	}
	r, err := w.work.Roadmap(ctx, w.plot)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	var own []work.Task
	enriched := 0
	for _, task := range r.Tasks {
		if ids[task.ID] {
			t.Errorf("task %s twice", task.ID)
		}
		ids[task.ID] = true
		if task.Source == work.FromInputs {
			own = append(own, task)
		}
		if task.Source == work.FromCalendar && strings.Contains(task.Detail, "an acre") {
			enriched++
			own = append(own, task)
		}
	}
	if enriched == 0 {
		t.Fatalf("roadmap: no calendar fertilising task enriched (notes %v)", r.Notes)
	}
	if !slices.Contains(r.Notes, inputs.StarterNote) || !slices.Contains(r.Notes, inputs.LabelNote) {
		t.Errorf("notes %v; want the starter-values and label notes", r.Notes)
	}
	// A task of the plan, ticked done today.
	task := own[0]
	for _, k := range own {
		if k.DAS != nil && *k.DAS > 0 {
			task = k
			break
		}
	}
	if _, _, err := w.work.Record(ctx, w.plot, []work.Event{{ID: "e1", Task: task.ID, Kind: work.Done, Day: "2026-06-01"}}); err != nil {
		t.Fatalf("record on an inputs task: %v", err)
	}
	// Sown twelve days early (today): the task keeps its ID and its tick.
	if _, err := w.cycles.SetSown(ctx, w.plot, "2026-06-01"); err != nil {
		t.Fatal(err)
	}
	r, err = w.work.Roadmap(ctx, w.plot)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := r.Task(task.ID)
	if !ok || got.State != work.Done || got.Date >= task.Date {
		t.Fatalf("after the sowing: %+v, %v; want the same task, done, moved earlier than %s", got, ok, task.Date)
	}
	// Organic: the same tasks' IDs, no urea anywhere.
	if _, err := w.cycles.Set(ctx, w.plot, 0, cycle.Change{Method: cycle.Organic}); err != nil {
		t.Fatal(err)
	}
	r, err = w.work.Roadmap(ctx, w.plot)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range r.Tasks {
		if k.Source == work.FromInputs || k.Kind == calendar.Fertilise {
			if strings.Contains(k.Detail, "Urea") || strings.Contains(k.Detail, "DAP") {
				t.Errorf("organic task %s: %s", k.ID, k.Detail)
			}
		}
	}
	if got, ok := r.Task(task.ID); ok && got.State != work.Done {
		t.Errorf("organic: %s lost its tick", task.ID)
	}
}

type failing struct{}

func (failing) Get(context.Context, string) (cycle.Record, error) {
	return cycle.Record{}, errors.New("store down")
}

// When the method cannot be read the inputs tasks are left out with a
// note; the calendar's tasks all stand.
func TestRoadmapWithoutTheSchedule(t *testing.T) {
	t.Parallel()
	w := setup(t, true)
	if _, err := w.cycles.Set(ctx, w.plot, 0, cycle.Change{Accept: true}); err != nil {
		t.Fatal(err)
	}
	f, _ := w.svc.Get(ctx, w.plot)
	src := addon.NewRoadmap(w.book, w.lib, failing{})
	if _, _, err := src.Tasks(ctx, f, []work.Zone{{Zone: 0, Crop: f.Plan.Zones[0].Crop, PlannedSow: f.Plan.Zones[0].Sow}}); err == nil {
		t.Fatal("tasks with the store down: want an error")
	}
	clk := clocktest.NewFake(now)
	plan := planner.New(w.svc, w.lib, &agrotest.Source{Clim: agrotest.Hyderabad()}, planner.WithClock(clk))
	r, err := work.New(plan, worktest.NewMemory(), work.WithTasks(src)).Roadmap(ctx, w.plot)
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := work.New(plan, worktest.NewMemory()).Roadmap(ctx, w.plot)
	if len(r.Tasks) != len(plain.Tasks) || !slices.Contains(r.Notes, work.NoteMissing) {
		t.Errorf("%d tasks (plain %d), notes %v", len(r.Tasks), len(plain.Tasks), r.Notes)
	}
}

// Amounts are said as they are bought.
func TestAmount(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		v    float64
		unit string
		want string
	}{
		{771604.94, "g", "772 kg"},
		{2500, "g", "2.5 kg"},
		{384, "g", "384 g"},
		{1200, "ml", "1.2 litres"},
		{9.45, "kg", "9.5 kg"},
		{21.17, "kg", "21 kg"},
		{1, "t", "1 tonne"},
		{0.8, "t", "0.8 tonnes"},
		{3, "piece", "3 pieces"},
	} {
		if got := addon.Amount(c.v, c.unit); got != c.want {
			t.Errorf("Amount(%v, %s) = %q; want %q", c.v, c.unit, got, c.want)
		}
	}
}

type windows struct{ w *agro.Window }

func (x windows) Window(context.Context, farm.Farm, string, string) (*agro.Window, error) {
	return x.w, nil
}

// Sell timing: advice only where the backtest on the area's past prices
// passed; with too little history, no advice and the reason; nothing at
// all for a plot with no known state.
func TestTiming(t *testing.T) {
	t.Parallel()
	lib, err := agro.Default()
	if err != nil {
		t.Fatal(err)
	}
	h := prices.NewMemoryHistory()
	// 15 years of a post-harvest dip in Kurnool, 2010 to 2024: synthetic.
	s := synthetic.Series{
		Commodity: "Groundnut", State: "Andhra Pradesh", District: "Kurnool", Markets: 2,
		From: synthetic.Start, Years: 15, Base: 5000, Season: synthetic.PostHarvestDip,
		TrendPct: 4, Noise: 0.15, MarketNoise: 0.02, Seed: 2,
	}
	if _, err := h.Save(ctx, s.Quotes()); err != nil {
		t.Fatal(err)
	}
	today := func() time.Time { return time.Date(2024, 11, 20, 0, 0, 0, 0, time.UTC) }
	a := addon.NewTiming(lib, h, windows{}, today)
	kurnool := farm.Farm{ID: "p1", Report: farm.Report{Location: &farm.Location{State: "Andhra Pradesh", District: "Kurnool"}}}
	sown := cycle.Cycle{Areas: []cycle.Area{{Crop: "groundnut", AreaHa: 1, Sow: "2024-07-01", SowKind: cycle.Actual}}, Sow: "2024-07-01", SowKind: cycle.Actual}
	v, err := a.For(ctx, kurnool, sown)
	if err != nil {
		t.Fatal(err)
	}
	adv, ok := v.(addon.TimingAdvice)
	if !ok || adv.Selling == nil || adv.Crop != "groundnut" || adv.CropName == "" {
		t.Fatalf("Kurnool groundnut, sown: %+v", v)
	}
	// Again: the same answer (kept, not asked again).
	if v2, _ := a.For(ctx, kurnool, sown); v2.(addon.TimingAdvice).Selling == nil {
		t.Error("second read lost the advice")
	}
	// Chilli has no prices here: no advice, and why.
	chilli := sown
	chilli.Areas = []cycle.Area{{Crop: "chilli", AreaHa: 1, Sow: "2024-07-01", SowKind: cycle.Actual}}
	v, err = a.For(ctx, kurnool, chilli)
	if err != nil {
		t.Fatal(err)
	}
	if adv, ok := v.(addon.TimingAdvice); !ok || adv.Selling != nil || adv.Sowing != nil || len(adv.Reasons) == 0 {
		t.Errorf("chilli with no prices: %+v", v)
	}
	// No place: nothing at all.
	if v, err := a.For(ctx, farm.Farm{ID: "p2"}, sown); v != nil || err != nil {
		t.Errorf("no location: %v, %v", v, err)
	}
	// Not sown yet, with an open window: the sowing rule is weighed too.
	before := addon.NewTiming(lib, h, windows{&agro.Window{From: "2024-12-01", To: "2025-01-15", Best: "2024-12-20"}}, today)
	planned := cycle.Cycle{Areas: []cycle.Area{{Crop: "groundnut", AreaHa: 1, Sow: "2024-12-20"}}, Sow: "2024-12-20", SowKind: cycle.Planned}
	v, err = before.For(ctx, kurnool, planned)
	if err != nil {
		t.Fatal(err)
	}
	if adv, ok := v.(addon.TimingAdvice); !ok || adv.SowingTest == nil {
		t.Errorf("before sowing: %+v; want the sowing rule tested", v)
	}
}
