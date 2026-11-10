package watch_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/automation"
	"github.com/bsamanvith/agentkit/apps/farm/automation/sqlite"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farm/farmtest"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/planner"
	"github.com/bsamanvith/agentkit/apps/farm/watch"
	"github.com/bsamanvith/agentkit/apps/farm/weather/weathertest"
	"github.com/bsamanvith/agentkit/apps/farm/work"
	"github.com/bsamanvith/agentkit/apps/farm/work/worktest"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

// Monday 9 November 2026, 06:00 in India.
var now = time.Date(2026, 11, 9, 0, 30, 0, 0, time.UTC)

var field = []farm.Corner{
	{Point: geo.Point{Lat: 17.07, Lng: 78.20}},
	{Point: geo.Point{Lat: 17.07, Lng: 78.201883}},
	{Point: geo.Point{Lat: 17.070904, Lng: 78.201883}},
	{Point: geo.Point{Lat: 17.070904, Lng: 78.20}},
}

type world struct {
	svc     *farm.Service
	plan    *planner.Planner
	watch   *watch.Watch
	engine  *automation.Engine
	store   automation.Store
	weather *weathertest.Fake
	planted string // a plot with maize sown 25 days ago
	bare    string // a plot with no plan
}

func setup(t *testing.T) *world {
	t.Helper()
	ctx := context.Background()
	w := &world{weather: weathertest.New(weathertest.Week("2026-11-09")...)}
	clk := clocktest.NewFake(now)
	var events []farm.Event
	svc, err := farm.NewService(&farmtest.Memory{}, farm.NewVerifier(), farm.WithServiceClock(clk),
		farm.WithObserver(func(_ context.Context, e farm.Event) {
			events = append(events, e)
			if w.engine != nil {
				w.engine.Emit(e.Name, e.Farm)
			}
		}))
	if err != nil {
		t.Fatal(err)
	}
	lib, err := agro.Default()
	if err != nil {
		t.Fatal(err)
	}
	w.svc = svc
	w.plan = planner.New(svc, lib, nil, planner.WithClock(clk))
	w.watch = watch.New(w.plan, w.weather)
	f, err := svc.Create(ctx, farm.Draft{Name: "East field", Boundary: farm.Boundary{Corners: field, Source: farm.SourceTyped}})
	if err != nil {
		t.Fatal(err)
	}
	w.planted = f.ID
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
	if len(events) != 2 || events[0].Name != farm.EventCreated {
		t.Fatalf("events %+v, want two farm.created", events)
	}
	w.store, err = sqlite.Open(ctx, filepath.Join(t.TempDir(), "farm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.store.(*sqlite.Store).Close() })
	w.engine, err = automation.New(w.store, w.watch, append(w.watch.Rules(), automation.WithClock(clk), automation.WithWeather(w.weather))...)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.engine.Seed(ctx, watch.Builtins(true, false)); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *world) savePlan(t *testing.T, id string, z agro.Zone) {
	t.Helper()
	f, _ := w.svc.Get(context.Background(), id)
	z.Ring = f.Boundary.Ring()
	if _, err := w.svc.SetPlan(context.Background(), id, &agro.Plan{Zones: []agro.Zone{z}}, &agro.PlanReport{}); err != nil {
		t.Fatal(err)
	}
}

func notices(t *testing.T, w *world, farm string) []automation.Notice {
	t.Helper()
	ns, err := w.store.ListNotices(context.Background(), automation.NoticeFilter{Farm: farm})
	if err != nil {
		t.Fatal(err)
	}
	return ns
}

// TestWeeklyWeatherOnlyWhenActionNeeded: a calm week sends nothing; a
// week with heavy rain sends the planted plot its advice and the bare plot
// the warning for any land; a second run sends nothing new.
func TestWeeklyWeatherOnlyWhenActionNeeded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := setup(t)
	w.savePlan(t, w.planted, agro.Zone{Crop: "maize", Sow: "2026-10-15"})
	runs, err := w.engine.RunNow(ctx, watch.WeeklyWeather, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if r.Status != automation.RunQuiet {
			t.Errorf("a calm week: run %+v, want no action", r)
		}
	}
	if n := len(notices(t, w, "")); n != 0 {
		t.Fatalf("a calm week sent %d notices", n)
	}
	wet := weathertest.Week("2026-11-09")
	wet[3].RainMM = 85
	w.weather.Set(nil, wet...)
	if _, err := w.engine.RunNow(ctx, watch.WeeklyWeather, ""); err != nil {
		t.Fatal(err)
	}
	got := notices(t, w, w.planted)
	if len(got) == 0 || got[0].Severity != automation.Warning || !strings.Contains(got[0].Title, "Heavy rain Thursday 12 Nov · East field") ||
		!strings.Contains(got[0].Body, "Why: 85 mm") {
		t.Errorf("planted plot's notices %+v", got)
	}
	if b := notices(t, w, w.bare); len(b) != 1 || strings.Contains(b[0].Body, "Maize") {
		t.Errorf("bare plot's notices %+v: want the heavy rain warning, no crops", b)
	}
	before := len(notices(t, w, ""))
	// The rain watch sees the same rain: nothing new.
	if _, err := w.engine.RunNow(ctx, watch.RainWatch, ""); err != nil {
		t.Fatal(err)
	}
	if after := len(notices(t, w, "")); after != before {
		t.Errorf("the rain watch repeated the weekly check: %d notices, then %d", before, after)
	}
}

// TestNewPlanChecksTheWeather: saving a plan fires the weather check for
// that plot, in the background.
func TestNewPlanChecksTheWeather(t *testing.T) {
	t.Parallel()
	w := setup(t)
	wet := weathertest.Week("2026-11-09")
	wet[1].GustKmh = 70
	w.weather.Set(nil, wet...)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = w.engine.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	w.savePlan(t, w.planted, agro.Zone{Crop: "sugarcane", Sow: "2026-08-01"})
	deadline := time.Now().Add(5 * time.Second)
	for {
		rs, _ := w.store.ListRuns(context.Background(), automation.RunFilter{Automation: watch.NewPlan})
		if len(rs) == 1 {
			if rs[0].Farm != w.planted || rs[0].Status != automation.RunNotified {
				t.Errorf("run %+v", rs[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no run after the plan was saved")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if ns := notices(t, w, w.planted); len(ns) != 1 || !strings.Contains(ns[0].Body, "Sugarcane") {
		t.Errorf("notices %+v: want the wind warning for the sugarcane", ns)
	}
}

// TestWeeklyTasks: the week's work of the calendar, as one notice; none
// for a plot without a plan.
func TestWeeklyTasks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := setup(t)
	w.savePlan(t, w.planted, agro.Zone{Crop: "maize", Sow: "2026-10-15"}) // day 25 today: top-dressing on day 26
	runs, err := w.engine.RunNow(ctx, watch.WeeklyTasks, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Farm != w.planted {
		t.Fatalf("runs %+v: want one, for the planted plot", runs)
	}
	ns := notices(t, w, w.planted)
	if len(ns) != 1 || !strings.Contains(ns[0].Body, "Tue 10 Nov: First top-dressing of nitrogen (Maize)") {
		t.Errorf("notices %+v", ns)
	}
}

// TestWeeklyTasksFollowTheRoadmap: with the roadmap, the week's work
// leaves out what the farmer has done and includes their own tasks.
func TestWeeklyTasksFollowTheRoadmap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := setup(t)
	w.savePlan(t, w.planted, agro.Zone{Crop: "maize", Sow: "2026-10-15"})
	roads := work.New(w.plan, worktest.NewMemory())
	w.watch.UseRoadmap(roads)
	if _, _, err := roads.Record(ctx, w.planted, []work.Event{
		{ID: "n1", Task: work.TaskID(0, "maize", "2026-10-15", "nitrogen-1"), Kind: work.Done, Day: "2026-11-09"},
		{ID: "own", Kind: work.Added, Title: "Repair the borewell motor", Day: "2026-11-11"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.engine.RunNow(ctx, watch.WeeklyTasks, ""); err != nil {
		t.Fatal(err)
	}
	ns := notices(t, w, w.planted)
	if len(ns) != 1 || strings.Contains(ns[0].Body, "top-dressing") || !strings.Contains(ns[0].Body, "Wed 11 Nov: Repair the borewell motor") {
		t.Errorf("notices %+v; want the motor and not the done top-dressing", ns)
	}
}

// TestWeekAndCalendar: the API's view of a plot's week and calendar.
func TestWeekAndCalendar(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := setup(t)
	if _, err := w.watch.Calendar(ctx, w.bare); err == nil {
		t.Error("a calendar for a plot with no plan")
	}
	w.savePlan(t, w.planted, agro.Zone{Crop: "maize", Sow: "2026-10-15"})
	r, err := w.watch.Week(ctx, w.planted)
	if err != nil || len(r.Days) != 7 || len(r.Crops) != 1 || len(r.Tasks) == 0 || r.ActionNeeded() {
		t.Errorf("week %+v, %v", r, err)
	}
	cal, err := w.watch.Calendar(ctx, w.planted)
	if err != nil || len(cal.Zones) != 1 || cal.Zones[0].HarvestFrom != "2027-01-13" {
		t.Errorf("calendar %+v, %v", cal, err)
	}
	if _, err := watch.New(w.plan, nil).Week(ctx, w.planted); err == nil {
		t.Error("a week without a forecast source")
	}
}
