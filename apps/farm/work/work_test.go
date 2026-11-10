package work_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/calendar"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farm/farmtest"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/planner"
	"github.com/bsamanvith/agentkit/apps/farm/work"
	"github.com/bsamanvith/agentkit/apps/farm/work/worktest"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

// 20 July 2026, 06:00 in India: maize planned for 20 June.
var now = time.Date(2026, 7, 20, 0, 30, 0, 0, time.UTC)

const plannedSow = "2026-06-20"

var field = []farm.Corner{
	{Point: geo.Point{Lat: 14.47, Lng: 78.82}},
	{Point: geo.Point{Lat: 14.47, Lng: 78.821883}},
	{Point: geo.Point{Lat: 14.470904, Lng: 78.821883}},
	{Point: geo.Point{Lat: 14.470904, Lng: 78.82}},
}

type world struct {
	svc   *farm.Service
	plan  *planner.Planner
	store *worktest.Memory
	work  *work.Service
	plot  string
	bare  string
	clock *clocktest.Fake
}

func setup(t *testing.T) *world {
	t.Helper()
	ctx := context.Background()
	clk := clocktest.NewFake(now)
	svc, err := farm.NewService(&farmtest.Memory{}, farm.NewVerifier(), farm.WithServiceClock(clk))
	if err != nil {
		t.Fatal(err)
	}
	lib, err := agro.Default()
	if err != nil {
		t.Fatal(err)
	}
	w := &world{svc: svc, plan: planner.New(svc, lib, nil, planner.WithClock(clk)), store: worktest.NewMemory(), clock: clk}
	w.work = work.New(w.plan, w.store)
	w.work.SetNow(clk.Now)
	f, err := svc.Create(ctx, farm.Draft{Name: "Kadapa plot", Boundary: farm.Boundary{Corners: field, Source: farm.SourceTyped}})
	if err != nil {
		t.Fatal(err)
	}
	w.plot = f.ID
	w.savePlan(t, plannedSow)
	shifted := make([]farm.Corner, len(field))
	for i, c := range field {
		shifted[i] = c
		shifted[i].Lat += 0.01
	}
	g, err := svc.Create(ctx, farm.Draft{Name: "Fallow", Boundary: farm.Boundary{Corners: shifted, Source: farm.SourceTyped}})
	if err != nil {
		t.Fatal(err)
	}
	w.bare = g.ID
	return w
}

func (w *world) savePlan(t *testing.T, sow string) {
	t.Helper()
	f, _ := w.svc.Get(context.Background(), w.plot)
	z := agro.Zone{Crop: "maize", Sow: sow, Ring: f.Boundary.Ring()}
	if _, err := w.svc.SetPlan(context.Background(), w.plot, &agro.Plan{Zones: []agro.Zone{z}}, &agro.PlanReport{}); err != nil {
		t.Fatal(err)
	}
}

func (w *world) roadmap(t *testing.T) work.Roadmap {
	t.Helper()
	r, err := w.work.Roadmap(context.Background(), w.plot)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (w *world) record(t *testing.T, evs ...work.Event) (int, work.Roadmap) {
	t.Helper()
	n, r, err := w.work.Record(context.Background(), w.plot, evs)
	if err != nil {
		t.Fatal(err)
	}
	return n, r
}

func id(key string) string { return work.TaskID(0, "maize", plannedSow, key) }

func task(t *testing.T, r work.Roadmap, id string) work.Task {
	t.Helper()
	k, ok := r.Task(id)
	if !ok {
		t.Fatalf("task %s not on the roadmap", id)
	}
	return k
}

func addDays(day string, n int) string {
	d, _ := time.Parse(agro.DateLayout, day)
	return d.AddDate(0, 0, n).Format(agro.DateLayout)
}

// The roadmap has every task of the plan's calendar, with IDs from the
// calendar's keys, open, in date order, each in its phase.
func TestRoadmapFromPlan(t *testing.T) {
	t.Parallel()
	w := setup(t)
	r := w.roadmap(t)
	f, _ := w.svc.Get(context.Background(), w.plot)
	cal, err := calendar.ForPlan(w.plan.Library(), *f.Plan, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Tasks) != len(cal.Zones[0].Tasks) || len(r.Zones) != 1 {
		t.Fatalf("%d tasks in %d zones; want the calendar's %d in 1", len(r.Tasks), len(r.Zones), len(cal.Zones[0].Tasks))
	}
	for _, k := range cal.Zones[0].Tasks {
		got := task(t, r, id(k.Key))
		if got.Date != k.Date || got.State != work.Open || got.Source != work.FromCalendar || got.Phase != work.PhaseOf(k.Kind) {
			t.Errorf("%s = %+v; want date %s, open, from the calendar", k.Key, got, k.Date)
		}
	}
	for i := 1; i < len(r.Tasks); i++ {
		if r.Tasks[i].Date < r.Tasks[i-1].Date {
			t.Fatalf("tasks out of date order at %d", i)
		}
	}
	if task(t, r, id("sow")).Phase != work.PhaseSow || task(t, r, id("plough")).Phase != work.PhasePrepare || task(t, r, id("harvest-by")).Phase != work.PhaseHarvest {
		t.Error("phases wrong")
	}
}

// Marking the sowing done ten days late moves every later task ten days,
// keeps their IDs, and records the real sowing date.
func TestSowingDateMovesLaterTasks(t *testing.T) {
	t.Parallel()
	w := setup(t)
	before := w.roadmap(t)
	sown := addDays(plannedSow, 10)
	_, after := w.record(t, work.Event{ID: "e-sow", Task: id("sow"), Kind: work.Done, Day: sown, By: "Ramesh"})
	if z := after.Zones[0]; z.Sown != sown || z.PlannedSow != plannedSow || z.HarvestFrom != addDays(before.Zones[0].HarvestFrom, 10) {
		t.Errorf("zone = %+v; want sown %s and the harvest ten days later", z, sown)
	}
	for _, key := range []string{"weed-1", "nitrogen-1", "harvest-by"} {
		b, a := task(t, before, id(key)), task(t, after, id(key))
		if a.Date != addDays(b.Date, 10) {
			t.Errorf("%s: %s, was %s; want ten days later", key, a.Date, b.Date)
		}
	}
	if s := task(t, after, id("sow")); s.State != work.Done || s.Day != sown || s.By != "Ramesh" || !s.Late {
		t.Errorf("sow task = %+v; want done on %s by Ramesh, late", s, sown)
	}
}

// Sending the same events again (a phone resending its queue) records
// nothing new and leaves the roadmap as it was.
func TestResendChangesNothing(t *testing.T) {
	t.Parallel()
	w := setup(t)
	evs := []work.Event{
		{ID: "q1", Task: id("plough"), Kind: work.Done, Day: "2026-05-30", Actuals: &work.Actuals{CostINR: 2400}},
		{ID: "q2", Kind: work.Added, Title: "Repair the motor", Day: "2026-07-22"},
	}
	n, first := w.record(t, evs...)
	if n != 2 {
		t.Fatalf("first send recorded %d; want 2", n)
	}
	evs[0].Kind = work.Skipped // a resend never rewrites what was recorded
	n, again := w.record(t, evs...)
	if n != 0 || again.Counts != first.Counts || task(t, again, id("plough")).State != work.Done {
		t.Errorf("resend recorded %d, counts %+v (were %+v); want nothing new", n, again.Counts, first.Counts)
	}
	if got, _ := w.store.Events(context.Background(), w.plot); len(got) != 2 {
		t.Errorf("%d events stored; want 2", len(got))
	}
}

// When two people close the same task the first wins; reopening clears it
// so it can be closed again.
func TestFirstCloseWinsUntilReopened(t *testing.T) {
	t.Parallel()
	w := setup(t)
	weed := id("weed-1")
	w.clock.Advance(time.Minute)
	w.record(t, work.Event{ID: "a", Task: weed, Kind: work.Done, Day: "2026-07-10", By: "Lakshmi"})
	w.clock.Advance(time.Minute)
	_, r := w.record(t, work.Event{ID: "b", Task: weed, Kind: work.Skipped, Day: "2026-07-11", By: "Ravi"})
	if k := task(t, r, weed); k.State != work.Done || k.By != "Lakshmi" {
		t.Errorf("after a second close: %+v; want Lakshmi's done", k)
	}
	w.clock.Advance(time.Minute)
	_, r = w.record(t, work.Event{ID: "c", Task: weed, Kind: work.Reopened})
	if k := task(t, r, weed); k.State != work.Open || k.By != "" {
		t.Errorf("after reopening: %+v; want open", k)
	}
	w.clock.Advance(time.Minute)
	_, r = w.record(t, work.Event{ID: "d", Task: weed, Kind: work.Skipped, Day: "2026-07-12", By: "Ravi", Note: "rain"})
	if k := task(t, r, weed); k.State != work.Skipped || k.Note != "rain" {
		t.Errorf("closed again: %+v; want skipped", k)
	}
}

// The app never closes a task itself: past its date and the grace period
// it stays open and is overdue; done that late, it is marked late.
func TestOverdueStaysOpen(t *testing.T) {
	t.Parallel()
	w := setup(t)
	r := w.roadmap(t)
	plough := task(t, r, id("plough")) // 21 days before sowing: long past
	if plough.State != work.Open || !plough.Overdue {
		t.Errorf("plough = %+v; want open and overdue", plough)
	}
	if h := task(t, r, id("harvest-by")); h.Overdue {
		t.Errorf("a task months ahead is overdue: %+v", h)
	}
	if r.Counts.Overdue == 0 || r.Counts.Open != len(r.Tasks) {
		t.Errorf("counts %+v", r.Counts)
	}
	_, r = w.record(t, work.Event{ID: "p", Task: id("plough"), Kind: work.Done, Day: "2026-07-15"})
	if p := task(t, r, id("plough")); p.Overdue || !p.Late {
		t.Errorf("plough done late = %+v; want late, not overdue", p)
	}
	_, r = w.record(t, work.Event{ID: "s", Task: id("treat-seed"), Kind: work.Done, Day: addDays(plannedSow, -1)})
	if s := task(t, r, id("treat-seed")); s.Late {
		t.Errorf("seed treated on time is late: %+v", s)
	}
}

// A farmer's own task sits beside the calendar's; it can be done and
// removed, while calendar tasks can only be marked not needed.
func TestFarmerTasks(t *testing.T) {
	t.Parallel()
	w := setup(t)
	zone := 0
	_, r := w.record(t, work.Event{ID: "own1", Kind: work.Added, Title: "Buy 2 bags urea", TaskKind: calendar.Fertilise, Day: "2026-07-21", Zone: &zone, By: "Ramesh"})
	k := task(t, r, "own1")
	if k.Source != work.FromFarmer || k.Kind != calendar.Fertilise || k.Zone != 0 || k.Date != "2026-07-21" || k.State != work.Open {
		t.Fatalf("own task = %+v", k)
	}
	_, r = w.record(t, work.Event{ID: "own1-done", Task: "own1", Kind: work.Done, Day: "2026-07-20", Actuals: &work.Actuals{Quantity: 2, Unit: "bags", CostINR: 534}})
	if k := task(t, r, "own1"); k.State != work.Done || k.Actuals == nil || k.Actuals.CostINR != 534 {
		t.Errorf("own task done = %+v", k)
	}
	_, r = w.record(t, work.Event{ID: "own1-rm", Task: "own1", Kind: work.Removed})
	if _, ok := r.Task("own1"); ok {
		t.Error("removed task still on the roadmap")
	}
	_, _, err := w.work.Record(context.Background(), w.plot, []work.Event{{ID: "rm-cal", Task: id("weed-2"), Kind: work.Removed}})
	if !errors.Is(err, work.ErrInvalid) {
		t.Errorf("removing a calendar task: %v; want ErrInvalid", err)
	}
	// Added and done in one batch, as a phone sends after being offline.
	n, r := w.record(t,
		work.Event{ID: "own2", Kind: work.Added, Title: "Clean the drain", Day: "2026-07-19"},
		work.Event{ID: "own2-done", Task: "own2", Kind: work.Done, Day: "2026-07-19"})
	if n != 2 || task(t, r, "own2").State != work.Done {
		t.Errorf("batch: %d recorded, task %+v", n, task(t, r, "own2"))
	}
}

// A batch with any invalid event records nothing.
func TestInvalidRecordsNothing(t *testing.T) {
	t.Parallel()
	ok := work.Event{ID: "good", Task: id("plough"), Kind: work.Done, Day: "2026-06-01"}
	for name, bad := range map[string]work.Event{
		"unknown task":      {ID: "x", Task: "z0-cotton-2026-06-20-sow", Kind: work.Done, Day: "2026-07-01"},
		"future day":        {ID: "x", Task: id("weed-1"), Kind: work.Done, Day: "2026-08-01"},
		"no day":            {ID: "x", Task: id("weed-1"), Kind: work.Done},
		"bad id":            {ID: "has space", Task: id("weed-1"), Kind: work.Done, Day: "2026-07-01"},
		"unknown kind":      {ID: "x", Task: id("weed-1"), Kind: "finished", Day: "2026-07-01"},
		"sown far off plan": {ID: "x", Task: id("sow"), Kind: work.Done, Day: "2026-01-02"},
		"added no title":    {ID: "x", Kind: work.Added, Day: "2026-07-25"},
		"added bad zone":    {ID: "x", Kind: work.Added, Title: "t", Day: "2026-07-25", Zone: new(5)},
		"negative cost":     {ID: "x", Task: id("weed-1"), Kind: work.Done, Day: "2026-07-01", Actuals: &work.Actuals{CostINR: -1}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := setup(t)
			_, _, err := w.work.Record(context.Background(), w.plot, []work.Event{ok, bad})
			if !errors.Is(err, work.ErrInvalid) {
				t.Fatalf("err = %v; want ErrInvalid", err)
			}
			if got, _ := w.store.Events(context.Background(), w.plot); len(got) != 0 {
				t.Errorf("%d events recorded; want none", len(got))
			}
		})
	}
}

// A new plan (another sowing date) is a new set of tasks: what was done on
// the old plan does not carry over.
func TestNewPlanStartsClean(t *testing.T) {
	t.Parallel()
	w := setup(t)
	w.record(t, work.Event{ID: "w", Task: id("weed-1"), Kind: work.Done, Day: "2026-07-10"})
	w.savePlan(t, "2026-07-05")
	r := w.roadmap(t)
	if r.Counts.Done != 0 {
		t.Errorf("counts %+v; want nothing done on the new plan", r.Counts)
	}
	if _, ok := r.Task(id("weed-1")); ok {
		t.Error("old plan's task still on the roadmap")
	}
}

// Due lists the open tasks in a stretch; a plot with no plan has none.
func TestDue(t *testing.T) {
	t.Parallel()
	w := setup(t)
	ctx := context.Background()
	from, to := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC), time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	due, err := w.work.Due(ctx, w.plot, from, to)
	if err != nil || len(due) != 2 { // treat seed, sow
		t.Fatalf("due = %+v, %v; want seed treatment and sowing", due, err)
	}
	w.record(t, work.Event{ID: "s", Task: id("treat-seed"), Kind: work.Done, Day: "2026-06-19"})
	if due, _ = w.work.Due(ctx, w.plot, from, to); len(due) != 1 || due[0].ID != id("sow") {
		t.Errorf("after the seed treatment: %+v; want only sowing", due)
	}
	if none, err := w.work.Due(ctx, w.bare, from, to); err != nil || len(none) != 0 {
		t.Errorf("bare plot: %v, %v; want none", none, err)
	}
	if _, err := w.work.Roadmap(ctx, w.bare); !errors.Is(err, work.ErrInvalid) {
		t.Errorf("roadmap of a bare plot: %v; want ErrInvalid", err)
	}
}

// Overdue and late start the day after the grace period, not on it.
func TestGraceBoundary(t *testing.T) {
	t.Parallel()
	w := setup(t) // today 2026-07-20
	whole := work.WholePlot
	_, r := w.record(t,
		work.Event{ID: "seven", Kind: work.Added, Title: "Due seven days ago", Day: "2026-07-13", Zone: &whole},
		work.Event{ID: "eight", Kind: work.Added, Title: "Due eight days ago", Day: "2026-07-12"},
		work.Event{ID: "x7", Task: "seven", Kind: work.Done, Day: "2026-07-20"},
		work.Event{ID: "x8", Task: "eight", Kind: work.Done, Day: "2026-07-20"},
	)
	if s, e := task(t, r, "seven"), task(t, r, "eight"); s.Late || !e.Late || s.Zone != work.WholePlot {
		t.Errorf("done on day 7: late=%v; on day 8: late=%v; want false, true", s.Late, e.Late)
	}
	_, r = w.record(t,
		work.Event{ID: "o7", Kind: work.Added, Title: "Open, seven days", Day: "2026-07-13"},
		work.Event{ID: "o8", Kind: work.Added, Title: "Open, eight days", Day: "2026-07-12"},
	)
	if task(t, r, "o7").Overdue || !task(t, r, "o8").Overdue {
		t.Errorf("open seven days past: overdue=%v; eight: %v; want false, true", task(t, r, "o7").Overdue, task(t, r, "o8").Overdue)
	}
}

// A sowing up to 120 days from the plan is taken; 121 is refused.
func TestSowingWindow(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		day string
		ok  bool
	}{{addDays(plannedSow, -120), true}, {addDays(plannedSow, -121), false}} {
		w := setup(t)
		_, _, err := w.work.Record(context.Background(), w.plot, []work.Event{{ID: "s", Task: id("sow"), Kind: work.Done, Day: c.day}})
		if (err == nil) != c.ok {
			t.Errorf("sown %s: %v; want ok=%v", c.day, err, c.ok)
		}
	}
	// 120 days after the plan is in the future here, so check it on a
	// plan sown long ago.
	w := setup(t)
	w.savePlan(t, "2026-01-01")
	for _, c := range []struct {
		day string
		ok  bool
	}{{"2026-05-01", true}, {"2026-05-02", false}} {
		_, _, err := w.work.Record(context.Background(), w.plot, []work.Event{{ID: "s" + c.day, Task: work.TaskID(0, "maize", "2026-01-01", "sow"), Kind: work.Done, Day: c.day}})
		if (err == nil) != c.ok {
			t.Errorf("sown %s on a plan for 1 Jan: %v; want ok=%v", c.day, err, c.ok)
		}
	}
}

// A phone whose clock runs ahead cannot reorder events: an event stamped
// in the future is stamped now, so the one recorded first still wins.
func TestFutureStampIsNow(t *testing.T) {
	t.Parallel()
	w := setup(t)
	weed := id("weed-1")
	w.record(t, work.Event{ID: "first", Task: weed, Kind: work.Done, Day: "2026-07-19", By: "Lakshmi", At: now.Add(48 * time.Hour)})
	w.clock.Advance(time.Hour)
	_, r := w.record(t, work.Event{ID: "second", Task: weed, Kind: work.Skipped, Day: "2026-07-20", By: "Ravi"})
	if k := task(t, r, weed); k.State != work.Done || k.By != "Lakshmi" {
		t.Errorf("weeding = %+v; want Lakshmi's done, recorded first", k)
	}
}

// The refused event is named, so a phone can drop only that change; an
// added task cannot take a calendar task's ID; a removed task stays gone
// even if reopened.
func TestRefusalsAndRemoval(t *testing.T) {
	t.Parallel()
	w := setup(t)
	_, _, err := w.work.Record(context.Background(), w.plot, []work.Event{
		{ID: "fine", Kind: work.Added, Title: "t", Day: "2026-07-25"},
		{ID: id("weed-1"), Kind: work.Added, Title: "clash", Day: "2026-07-25"},
	})
	var bad *work.EventError
	if !errors.As(err, &bad) || bad.Index != 1 || bad.ID != id("weed-1") || !errors.Is(err, work.ErrInvalid) {
		t.Fatalf("err = %v; want an EventError for event 1", err)
	}
	w.record(t, work.Event{ID: "own", Kind: work.Added, Title: "Mend the fence", Day: "2026-07-25"})
	w.record(t, work.Event{ID: "rm", Task: "own", Kind: work.Removed})
	if _, _, err := w.work.Record(context.Background(), w.plot, []work.Event{{ID: "back", Task: "own", Kind: work.Reopened}}); !errors.Is(err, work.ErrInvalid) {
		t.Errorf("reopening a removed task: %v; want ErrInvalid", err)
	}
	// Even if such an event got in, the task stays removed.
	f, _ := w.svc.Get(context.Background(), w.plot)
	at := now
	evs := []work.Event{
		{ID: "own", Farm: w.plot, Kind: work.Added, Title: "Mend the fence", Day: "2026-07-25", At: at},
		{ID: "rm", Farm: w.plot, Task: "own", Kind: work.Removed, At: at.Add(time.Minute)},
		{ID: "back", Farm: w.plot, Task: "own", Kind: work.Reopened, At: at.Add(2 * time.Minute)},
	}
	r, err := work.Build(w.plan.Library(), w.plot, *f.Plan, false, evs, w.plan.Today())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Task("own"); ok {
		t.Error("a removed task came back when reopened")
	}
}

// A perennial's tasks keep their keys and events when the planting moves
// across a year end.
func TestPerennialPlantingAcrossYearEnd(t *testing.T) {
	t.Parallel()
	w := setup(t)
	f, _ := w.svc.Get(context.Background(), w.plot)
	if _, err := w.svc.SetPlan(context.Background(), w.plot, &agro.Plan{Zones: []agro.Zone{{Crop: "mango", Sow: "2026-12-20", Ring: f.Boundary.Ring()}}}, &agro.PlanReport{}); err != nil {
		t.Skipf("no mango in the library: %v", err)
	}
	w.clock.Advance(200 * 24 * time.Hour) // into 2027
	before := w.roadmap(t)
	var later work.Task
	for _, k := range before.Tasks {
		if k.Date > "2027-06-01" && k.Kind != calendar.Sow {
			later = k
			break
		}
	}
	w.record(t, work.Event{ID: "plant", Task: work.TaskID(0, "mango", "2026-12-20", "sow"), Kind: work.Done, Day: "2027-01-10"})
	_, r := w.record(t, work.Event{ID: "n", Task: later.ID, Kind: work.Done, Day: "2027-02-01"})
	if got := task(t, r, later.ID); got.State != work.Done || got.Date != later.Date {
		t.Errorf("%s after planting moved = %+v; want done, same date", later.ID, got)
	}
	if r.Zones[0].Sown != "2027-01-10" || len(r.Tasks) != len(before.Tasks) {
		t.Errorf("zone %+v, %d tasks (were %d)", r.Zones[0], len(r.Tasks), len(before.Tasks))
	}
}

func TestAReadOnlyMemberRecordsNothing(t *testing.T) {
	w := setup(t)
	w.savePlan(t, plannedSow)
	ctx := farm.WithReadOnly(context.Background())
	_, _, err := w.work.Record(ctx, w.plot, []work.Event{{ID: "w1", Task: id("weed-1"), Kind: work.Done, Day: plannedSow}})
	if !errors.Is(err, farm.ErrReadOnly) {
		t.Fatalf("a worker records work: %v, want farm.ErrReadOnly", err)
	}
	if r := w.roadmap(t); task(t, r, id("weed-1")).State != work.Open {
		t.Error("the refused event was recorded")
	}
}
