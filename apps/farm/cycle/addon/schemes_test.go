package addon_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/agro/agrotest"
	"github.com/bsamanvith/agentkit/apps/farm/calendar"
	"github.com/bsamanvith/agentkit/apps/farm/cycle"
	"github.com/bsamanvith/agentkit/apps/farm/cycle/addon"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farm/farmtest"
	"github.com/bsamanvith/agentkit/apps/farm/planner"
	"github.com/bsamanvith/agentkit/apps/farm/region"
	"github.com/bsamanvith/agentkit/apps/farm/schemes"
	"github.com/bsamanvith/agentkit/apps/farm/work"
	"github.com/bsamanvith/agentkit/apps/farm/work/worktest"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

// insured is a plot with a rice plan and the season roadmap with the
// schemes as a task source, on a clock the test moves.
type insured struct {
	clk     *clocktest.Fake
	plan    *planner.Planner
	work    *work.Service
	schemes *addon.Schemes
	plot    string
}

// newInsured: in "bihar" (the plot's ID) PMFBY is changed to cover maize
// only; anywhere else the base pack holds.
func newInsured(t *testing.T, id string, crop string) *insured {
	t.Helper()
	set, err := region.Load(fstest.MapFS{"bihar.json": {Data: []byte(`{
		"region": {"country": "IN", "state": "Bihar"},
		"sources": [{"id": "t", "name": "Test", "publisher": "us", "url": "https://example.org", "read": "2026-11-15"}],
		"schemes": {"pmfby": {"crops": ["maize"], "ref": {"source": "t", "where": "p. 1"}}}
	}`)}})
	if err != nil {
		t.Fatal(err)
	}
	clk := clocktest.NewFake(now)
	svc, err := farm.NewService(&farmtest.Memory{}, farm.NewVerifier(), farm.WithServiceClock(clk))
	if err != nil {
		t.Fatal(err)
	}
	lib := set.Base().Library()
	plan := planner.New(svc, lib, &agrotest.Source{Clim: agrotest.Hyderabad()}, planner.WithClock(clk))
	f, err := svc.Create(ctx, farm.Draft{Name: "Insured plot", Boundary: farm.Boundary{Corners: field, Source: farm.SourceTyped}})
	if err != nil {
		t.Fatal(err)
	}
	packs := packsByID{set: set, in: "nowhere"}
	if id == "bihar" {
		packs.in = f.ID
	}
	sc := addon.NewSchemes(set.Base(), svc, plan.Today, addon.WithPacks(packs))
	w := work.New(plan, worktest.NewMemory(), work.WithTasks(sc))
	w.SetNow(clk.Now)
	sc.UseRoadmap(w)
	if _, err := plan.SetProfile(ctx, f.ID, agro.Profile{Soil: "black", Water: []agro.Water{{Kind: agro.Borewell, Reliable: true}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetPlan(ctx, f.ID, &agro.Plan{Zones: []agro.Zone{{Crop: crop, Sow: "2026-06-20", Ring: f.Boundary.Ring()}}}, &agro.PlanReport{}); err != nil {
		t.Fatal(err)
	}
	return &insured{clk: clk, plan: plan, work: w, schemes: sc, plot: f.ID}
}

func (x *insured) roadmap(t *testing.T) work.Roadmap {
	t.Helper()
	r, err := x.work.Roadmap(ctx, x.plot)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func fromSchemes(r work.Roadmap) []work.Task {
	var out []work.Task
	for _, k := range r.Tasks {
		if k.Source == work.FromSchemes {
			out = append(out, k)
		}
	}
	return out
}

// One enrolment task per covering scheme, once, with a stable ID, and a
// standing loss-report entry; the tick survives a moved sowing; the
// standing entry cannot be closed.
func TestInsuranceOnTheRoadmap(t *testing.T) {
	t.Parallel()
	x := newInsured(t, "", "rice")
	r := x.roadmap(t)
	got := fromSchemes(r)
	enrol := addon.EnrolTaskID("pmfby", agro.Kharif, 2026)
	e, ok1 := r.Task(enrol)
	loss, ok2 := r.Task(addon.LossTaskID(agro.Kharif, 2026))
	if len(got) != 2 || !ok1 || !ok2 {
		t.Fatalf("insurance tasks %+v", got)
	}
	if e.Title != "Enrol for crop insurance by 15 July" || e.Date != "2026-07-15" || !e.Deadline || e.Overdue || e.Passed || e.State != work.Open {
		t.Errorf("enrol task %+v", e)
	}
	for _, want := range []string{"read on 15 November 2026", "Rythu Seva Kendra"} {
		if !strings.Contains(e.Detail, want) {
			t.Errorf("enrol detail %q lacks %q", e.Detail, want)
		}
	}
	if !loss.Standing || loss.Title != "If the crop is damaged: report within 72 hours" || loss.Overdue {
		t.Errorf("loss entry %+v", loss)
	}
	if !slices.Contains(r.Notes, schemes.Confirm) {
		t.Errorf("notes %v", r.Notes)
	}
	// The counts leave the standing entry out.
	plain, err := work.New(x.plan, worktest.NewMemory()).Roadmap(ctx, x.plot)
	if err != nil {
		t.Fatal(err)
	}
	if r.Counts.Open != plain.Counts.Open+1 {
		t.Errorf("open %d, want the calendar's %d and the enrolment", r.Counts.Open, plain.Counts.Open)
	}

	if _, _, err := x.work.Record(ctx, x.plot, []work.Event{{ID: "e1", Task: enrol, Kind: work.Done, Day: "2026-06-01"}}); err != nil {
		t.Fatal(err)
	}
	var sow string
	for _, k := range r.Tasks {
		if k.Kind == calendar.Sow && k.Source == work.FromCalendar {
			sow = k.ID
		}
	}
	x.clk.Advance(30 * 24 * time.Hour) // 1 July
	if _, _, err := x.work.Record(ctx, x.plot, []work.Event{{ID: "e2", Task: sow, Kind: work.Done, Day: "2026-06-28"}}); err != nil {
		t.Fatal(err)
	}
	r = x.roadmap(t)
	got = fromSchemes(r)
	if e, ok := r.Task(enrol); len(got) != 2 || !ok || e.State != work.Done {
		t.Errorf("after a moved sowing: %+v", got)
	}
	_, _, err = x.work.Record(ctx, x.plot, []work.Event{{ID: "e3", Task: addon.LossTaskID(agro.Kharif, 2026), Kind: work.Done, Day: "2026-06-30"}})
	if !errors.Is(err, work.ErrInvalid) {
		t.Errorf("closing the standing entry: %v, want ErrInvalid", err)
	}
}

// Past its cut-off, an open enrolment task is passed: not overdue, not
// due, not open; nor is the standing entry ever due.
func TestPassedCutoffIsNotOverdue(t *testing.T) {
	t.Parallel()
	x := newInsured(t, "", "rice")
	x.clk.Advance(90 * 24 * time.Hour) // 30 August, weeks past 15 July
	r := x.roadmap(t)
	e, ok := r.Task(addon.EnrolTaskID("pmfby", agro.Kharif, 2026))
	if !ok || !e.Passed || e.Overdue || e.State != work.Open {
		t.Fatalf("enrol task after its date %+v", e)
	}
	if r.Counts.Passed < 1 {
		t.Errorf("counts %+v", r.Counts)
	}
	due, err := x.work.Due(ctx, x.plot, now, now.AddDate(1, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range due {
		if k.Source == work.FromSchemes {
			t.Errorf("due: %+v", k)
		}
	}
}

// No scheme covers the crop: no insurance task. The schemes answer still
// says so, for the assistant and the app.
func TestNoTaskWhenNoSchemeCovers(t *testing.T) {
	t.Parallel()
	x := newInsured(t, "bihar", "rice")
	if got := fromSchemes(x.roadmap(t)); len(got) != 0 {
		t.Errorf("tasks %+v", got)
	}
	a, err := x.schemes.Farm(ctx, x.plot)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Items) != 1 || a.Items[0].Cover != schemes.NotNamed {
		t.Errorf("answer %+v", a)
	}
	// Maize there is covered.
	if got := fromSchemes(newInsured(t, "bihar", "maize").roadmap(t)); len(got) != 2 {
		t.Errorf("maize tasks %+v", got)
	}
}

// The addon gives each planned cycle its answer; Farm gives the current
// cycle's, with the real sowing.
func TestSchemesOnTheCycle(t *testing.T) {
	t.Parallel()
	x := newInsured(t, "", "rice")
	v, err := x.schemes.For(ctx, farm.Farm{ID: "x"}, cycle.Cycle{Season: agro.Rabi, Sow: "2026-11-20", Areas: []cycle.Area{{Crop: "chickpea"}}})
	if err != nil {
		t.Fatal(err)
	}
	a, ok := v.(schemes.Answer)
	if !ok || a.Season != agro.Rabi || len(a.Items) != 1 || a.Items[0].By != "2026-12-15" {
		t.Fatalf("rabi cycle %+v", v)
	}
	if v, _ := x.schemes.For(ctx, farm.Farm{ID: "x"}, cycle.Cycle{Sow: "2026-11-20"}); v != nil {
		t.Errorf("a cycle with no crops: %+v", v)
	}
	got, err := x.schemes.Farm(ctx, x.plot)
	if err != nil {
		t.Fatal(err)
	}
	if got.Season != agro.Kharif || got.Year != 2026 || len(got.Items) != 1 || got.Items[0].Status != schemes.Open {
		t.Errorf("current cycle %+v", got)
	}
}
