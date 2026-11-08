package automation_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/automation"
	"github.com/bsamanvith/agentkit/apps/farm/automation/sqlite"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/weather"
	"github.com/bsamanvith/agentkit/apps/farm/weather/weathertest"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

// Monday 9 Nov 2026, 06:00 in India.
var monday6 = time.Date(2026, 11, 9, 0, 30, 0, 0, time.UTC)

type plots []automation.Target

func (p plots) Targets(_ context.Context, s automation.Scope) ([]automation.Target, error) {
	var out []automation.Target
	for _, t := range p {
		if len(s.Farms) > 0 && !slices.Contains(s.Farms, t.ID) {
			continue
		}
		if s.WithPlan && !t.HasPlan {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

var three = plots{
	{ID: "f1", Name: "East", Point: geo.Point{Lat: 17.4, Lng: 78.5}, HasPlan: true},
	{ID: "f2", Name: "West", Point: geo.Point{Lat: 18.3, Lng: 82.9}, HasPlan: true},
	{ID: "f3", Name: "Fallow", Point: geo.Point{Lat: 17.4, Lng: 78.5}},
}

func open(t *testing.T) automation.Store {
	t.Helper()
	s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "farm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// recorder is a rule that says one notice per plot, keyed by plot, and
// counts its calls.
type recorder struct {
	mu    sync.Mutex
	jobs  []automation.Job
	fails int // fail this many calls first
	quiet bool
}

func (r *recorder) Handle(_ context.Context, j automation.Job) (automation.Outcome, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs = append(r.jobs, j)
	if r.fails > 0 {
		r.fails--
		return automation.Outcome{}, errors.New("service hiccup")
	}
	if r.quiet {
		return automation.Outcome{Note: "nothing to do"}, nil
	}
	return automation.Outcome{Notices: []automation.Notice{{Key: "rain:" + j.Target.ID + ":2026-11-12", Severity: automation.Warning, Title: "Heavy rain Thursday"}}}, nil
}

func (r *recorder) farms() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, j := range r.jobs {
		out = append(out, j.Target.ID)
	}
	slices.Sort(out)
	return out
}

func weekly(id string) automation.Automation {
	return automation.Automation{
		ID: id, Name: "Weekly weather check", Enabled: true,
		Trigger: automation.Trigger{Kind: automation.OnSchedule, Schedule: "CRON_TZ=Asia/Kolkata 0 6 * * 1"},
		Scope:   automation.Scope{WithPlan: true},
		Action:  automation.Action{Kind: automation.DoRule, Rule: "advice"},
	}
}

func engine(t *testing.T, s automation.Store, clk *clocktest.Fake, rule automation.Handler, opts ...automation.Option) *automation.Engine {
	t.Helper()
	e, err := automation.New(s, three, append([]automation.Option{automation.WithClock(clk), automation.WithRule("advice", "test rule", rule), automation.WithRetries(2, 0)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// run starts the engine's workers and waits for n runs to be recorded.
func waitRuns(t *testing.T, s automation.Store, n int) []automation.Run {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rs, err := s.ListRuns(context.Background(), automation.RunFilter{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		if len(rs) >= n {
			return rs
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d runs recorded, want %d", len(rs), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func startEngine(t *testing.T, e *automation.Engine) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

// TestScheduleFiresOncePerTime: a weekly automation fires at its time for
// the plots in its scope, once; after a long downtime it fires once for
// the latest time missed, not for each.
func TestScheduleFiresOncePerTime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := open(t)
	clk := clocktest.NewFake(monday6.Add(-time.Hour))
	rule := &recorder{}
	e := engine(t, s, clk, rule)
	if err := e.Seed(ctx, []automation.Automation{weekly("weekly")}); err != nil {
		t.Fatal(err)
	}
	startEngine(t, e)
	e.Scan(ctx)
	time.Sleep(20 * time.Millisecond)
	if got := rule.farms(); len(got) != 0 {
		t.Fatalf("fired before its time for %v", got)
	}
	clk.Advance(time.Hour)
	e.Scan(ctx)
	waitRuns(t, s, 2)
	e.Scan(ctx)
	time.Sleep(20 * time.Millisecond)
	if got := rule.farms(); !slices.Equal(got, []string{"f1", "f2"}) {
		t.Errorf("fired for %v, want f1 and f2 once (f3 has no plan)", got)
	}
	a, _ := s.GetAutomation(ctx, "weekly")
	if !a.LastDue.Equal(monday6) {
		t.Errorf("LastDue %v, want %v", a.LastDue, monday6)
	}
	// Down for three weeks: one firing, for the latest Monday.
	clk.Advance(3*7*24*time.Hour + time.Minute)
	e.Scan(ctx)
	waitRuns(t, s, 4)
	time.Sleep(20 * time.Millisecond)
	if got := len(rule.farms()); got != 4 {
		t.Errorf("%d jobs after three missed weeks, want 4 (one firing)", got)
	}
	if a, _ := s.GetAutomation(ctx, "weekly"); !a.LastDue.Equal(monday6.Add(21 * 24 * time.Hour)) {
		t.Errorf("LastDue %v, want three Mondays on", a.LastDue)
	}
}

// TestTwoProcessesFireOnce: two engines on one store fire a time once.
func TestTwoProcessesFireOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := open(t)
	clk := clocktest.NewFake(monday6.Add(-time.Minute))
	r1, r2 := &recorder{}, &recorder{}
	e1, e2 := engine(t, s, clk, r1), engine(t, s, clk, r2)
	if err := e1.Seed(ctx, []automation.Automation{weekly("weekly")}); err != nil {
		t.Fatal(err)
	}
	startEngine(t, e1)
	startEngine(t, e2)
	clk.Advance(time.Minute)
	var wg sync.WaitGroup
	wg.Go(func() { e1.Scan(ctx) })
	wg.Go(func() { e2.Scan(ctx) })
	wg.Wait()
	waitRuns(t, s, 2)
	time.Sleep(30 * time.Millisecond)
	if n := len(r1.farms()) + len(r2.farms()); n != 2 {
		t.Errorf("%d jobs between two processes, want 2", n)
	}
}

// TestWeatherTrigger: the action runs only for plots whose forecast meets
// the condition; the others are recorded as quiet, with why.
func TestWeatherTrigger(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := open(t)
	clk := clocktest.NewFake(monday6)
	wet := weathertest.Week("2026-11-09")
	wet[2].RainMM = 80
	src := &perPlace{days: map[float64][]weather.Day{17.4: wet, 18.3: weathertest.Week("2026-11-09")}}
	rule := &recorder{}
	e := engine(t, s, clk, rule, automation.WithWeather(src))
	a := automation.Automation{
		Name: "Heavy rain watch", Enabled: true,
		Trigger: automation.Trigger{
			Kind: automation.OnWeather, Schedule: "0 */6 * * *",
			Weather: &automation.Condition{Metric: automation.MetricRainDay, Op: ">=", Value: 50, Days: 3},
		},
		Action: automation.Action{Kind: automation.DoRule, Rule: "advice"},
	}
	a, err := e.Create(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := e.RunNow(ctx, a.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	// By hand the condition is not tested: every plot runs.
	if len(runs) != 3 || len(rule.farms()) != 3 {
		t.Fatalf("by hand: %d runs, %v jobs; want 3 and 3", len(runs), rule.farms())
	}
	rule.jobs = nil
	startEngine(t, e)
	clk.Advance(6 * time.Hour)
	e.Scan(ctx)
	rs := waitRuns(t, s, 6)
	time.Sleep(20 * time.Millisecond)
	if got := rule.farms(); !slices.Equal(got, []string{"f1", "f3"}) {
		t.Errorf("rain watch ran for %v, want f1 and f3 (the wet place)", got)
	}
	quiet := 0
	for _, r := range rs[:3] {
		if r.Farm == "f2" && r.Status == automation.RunQuiet && r.Note != "" {
			quiet++
		}
		if r.Farm == "f1" && r.Reason == "" {
			t.Error("f1's run has no reason")
		}
	}
	if quiet != 1 {
		t.Errorf("runs %+v: want f2 quiet with a note", rs[:3])
	}
}

type perPlace struct{ days map[float64][]weather.Day }

func (p *perPlace) Name() string { return "per place" }

func (p *perPlace) Forecast(_ context.Context, pt geo.Point, n int) (weather.Forecast, error) {
	d := p.days[pt.Lat]
	return weather.Forecast{Point: pt, Days: d[:min(n, len(d))]}, nil
}

// TestEventTrigger: an event about one plot runs its automations for that
// plot only, in the background.
func TestEventTrigger(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := open(t)
	clk := clocktest.NewFake(monday6)
	rule := &recorder{}
	e := engine(t, s, clk, rule)
	if _, err := e.Create(ctx, automation.Automation{
		Name: "New plan", Enabled: true,
		Trigger: automation.Trigger{Kind: automation.OnEvent, Event: "farm.plan_changed"},
		Action:  automation.Action{Kind: automation.DoRule, Rule: "advice"},
	}); err != nil {
		t.Fatal(err)
	}
	startEngine(t, e)
	e.Emit("farm.created", "f1") // no automation listens
	e.Emit("farm.plan_changed", "f2")
	rs := waitRuns(t, s, 1)
	time.Sleep(20 * time.Millisecond)
	if got := rule.farms(); !slices.Equal(got, []string{"f2"}) || rs[0].Reason != "event farm.plan_changed" {
		t.Errorf("jobs %v, run %+v; want one for f2 from the event", got, rs[0])
	}
	ns, _ := s.ListNotices(ctx, automation.NoticeFilter{Farm: "f2"})
	if len(ns) != 1 || ns[0].Automation == "" || ns[0].Run != rs[0].ID {
		t.Errorf("notices %+v: want one, linked to its automation and run", ns)
	}
}

type channel struct{ n atomic.Int32 }

func (c *channel) Name() string { return "test" }
func (c *channel) Deliver(context.Context, automation.Notice) error {
	c.n.Add(1)
	return nil
}

// TestNoticesAreNotRepeated: the same notice from a second run, or a
// second automation, within the window is not saved or delivered again.
func TestNoticesAreNotRepeated(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := open(t)
	clk := clocktest.NewFake(monday6)
	ch := &channel{}
	e := engine(t, s, clk, &recorder{}, automation.WithChannels(ch))
	a, err := e.Create(ctx, weekly(""))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := e.Create(ctx, weekly(""))
	r1, _ := e.RunNow(ctx, a.ID, "f1")
	clk.Advance(6 * time.Hour)
	r2, _ := e.RunNow(ctx, b.ID, "f1")
	if r1[0].Status != automation.RunNotified || r2[0].Status != automation.RunQuiet || !strings.Contains(r2[0].Note, "already told the farmer") {
		t.Errorf("runs %+v then %+v: want notified, then quiet", r1[0], r2[0])
	}
	if ch.n.Load() != 1 {
		t.Errorf("delivered %d times, want 1", ch.n.Load())
	}
	clk.Advance(7 * 24 * time.Hour)
	if r3, _ := e.RunNow(ctx, a.ID, "f1"); r3[0].Status != automation.RunNotified {
		t.Errorf("a week later: %+v, want notified again", r3[0])
	}
}

// TestRetries: a failing action is tried again; one that keeps failing is
// recorded as failed with its error.
func TestRetries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := open(t)
	clk := clocktest.NewFake(monday6)
	rule := &recorder{fails: 2}
	e := engine(t, s, clk, rule)
	a, _ := e.Create(ctx, weekly(""))
	rs, _ := e.RunNow(ctx, a.ID, "f1")
	if rs[0].Status != automation.RunNotified || rs[0].Attempts != 3 {
		t.Errorf("run %+v: want notified on the third attempt", rs[0])
	}
	rule.fails = 5
	rs, _ = e.RunNow(ctx, a.ID, "f2")
	if rs[0].Status != automation.RunFailed || rs[0].Error != "service hiccup" || rs[0].Attempts != 3 {
		t.Errorf("run %+v: want failed after three attempts", rs[0])
	}
}

// TestCheck: definitions are checked against what the engine has.
func TestCheck(t *testing.T) {
	t.Parallel()
	s := open(t)
	e := engine(t, s, clocktest.NewFake(monday6), &recorder{})
	good := weekly("")
	bad := map[string]func(a *automation.Automation){
		"no name":          func(a *automation.Automation) { a.Name = " " },
		"bad schedule":     func(a *automation.Automation) { a.Trigger.Schedule = "every monday" },
		"unknown rule":     func(a *automation.Automation) { a.Action.Rule = "nope" },
		"unknown trigger":  func(a *automation.Automation) { a.Trigger.Kind = "sms" },
		"event + schedule": func(a *automation.Automation) { a.Trigger.Kind, a.Trigger.Event = automation.OnEvent, "farm.created" },
		"skill, no runner": func(a *automation.Automation) {
			a.Action = automation.Action{Kind: automation.DoSkill, Skill: "weather-check"}
		},
		"weather, no source": func(a *automation.Automation) {
			a.Trigger.Kind = automation.OnWeather
			a.Trigger.Weather = &automation.Condition{Metric: automation.MetricRainDay, Op: ">=", Value: 50}
		},
		"bad metric": func(a *automation.Automation) {
			a.Trigger.Kind = automation.OnWeather
			a.Trigger.Weather = &automation.Condition{Metric: "snow", Op: ">=", Value: 1}
		},
	}
	if err := e.Check(good); err != nil {
		t.Fatalf("Check(good) = %v", err)
	}
	for name, change := range bad {
		a := good
		change(&a)
		if err := e.Check(a); !errors.Is(err, automation.ErrInvalid) {
			t.Errorf("%s: Check = %v, want ErrInvalid", name, err)
		}
	}
}

// TestBuiltins: a built-in automation is turned on and off, not changed or
// deleted; seeding again keeps the farmer's on or off.
func TestBuiltins(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := open(t)
	e := engine(t, s, clocktest.NewFake(monday6), &recorder{})
	if err := e.Seed(ctx, []automation.Automation{weekly("weekly")}); err != nil {
		t.Fatal(err)
	}
	if err := e.Delete(ctx, "weekly"); !errors.Is(err, automation.ErrBuiltin) {
		t.Errorf("Delete = %v, want ErrBuiltin", err)
	}
	changed := weekly("weekly")
	changed.Name, changed.Enabled = "Renamed", false
	got, err := e.Update(ctx, changed)
	if err != nil || got.Name != "Weekly weather check" || got.Enabled {
		t.Errorf("Update = %+v, %v; want only turned off", got, err)
	}
	newer := weekly("weekly")
	newer.Description = "now with a description"
	if err := e.Seed(ctx, []automation.Automation{newer}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetAutomation(ctx, "weekly")
	if got.Enabled || got.Description == "" || !got.Builtin {
		t.Errorf("after re-seeding: %+v; want off, the new description, built in", got)
	}
}
