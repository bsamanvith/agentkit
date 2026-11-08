package farmapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/advise"
	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/automation"
	"github.com/bsamanvith/agentkit/apps/farm/automation/sqlite"
	"github.com/bsamanvith/agentkit/apps/farm/calendar"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farm/farmtest"
	"github.com/bsamanvith/agentkit/apps/farm/farmapi"
	"github.com/bsamanvith/agentkit/apps/farm/planner"
	"github.com/bsamanvith/agentkit/apps/farm/watch"
	"github.com/bsamanvith/agentkit/apps/farm/weather"
	"github.com/bsamanvith/agentkit/apps/farm/weather/weathertest"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

func TestWatchAndAutomationRoutes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clk := clocktest.NewFake(time.Date(2026, 11, 9, 1, 0, 0, 0, time.UTC))
	svc, err := farm.NewService(&farmtest.Memory{}, farm.NewVerifier())
	if err != nil {
		t.Fatal(err)
	}
	lib, _ := agro.Default()
	p := planner.New(svc, lib, nil, planner.WithClock(clk))
	wet := weathertest.Week("2026-11-09")
	wet[2].RainMM = 95
	wx := weathertest.New(wet...)
	w := watch.New(p, wx)
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "farm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	e, err := automation.New(store, w, append(w.Rules(), automation.WithClock(clk), automation.WithWeather(wx))...)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Seed(ctx, watch.Builtins(true, false)); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(farmapi.New(svc, farmapi.WithPlanner(p), farmapi.WithWatch(w), farmapi.WithAutomations(e)))
	t.Cleanup(srv.Close)
	var f farm.Farm
	if code := call(t, srv, "POST", "/api/farms", `{"name":"Black field","boundary":`+boundary+`}`, &f); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	var cfg map[string]any
	if call(t, srv, "GET", "/api/config", "", &cfg); cfg["weather"] != true || cfg["automations"] != true {
		t.Errorf("config %v", cfg)
	}
	if code := call(t, srv, "GET", "/api/farms/"+f.ID+"/calendar", "", nil); code != http.StatusBadRequest {
		t.Errorf("calendar with no plan: %d, want 400", code)
	}
	if _, err := svc.SetPlan(ctx, f.ID, &agro.Plan{Zones: []agro.Zone{{Crop: "maize", Sow: "2026-10-01", Ring: f.Boundary.Ring()}}}, &agro.PlanReport{}); err != nil {
		t.Fatal(err)
	}
	var week advise.Report
	if code := call(t, srv, "GET", "/api/farms/"+f.ID+"/week", "", &week); code != 200 || len(week.Advice) == 0 || week.Advice[0].Rule != "heavy_rain" {
		t.Errorf("week: %d %+v", code, week.Advice)
	}
	var cal calendar.Calendar
	if code := call(t, srv, "GET", "/api/farms/"+f.ID+"/calendar", "", &cal); code != 200 || len(cal.Zones) != 1 {
		t.Errorf("calendar: %d %+v", code, cal)
	}
	var list struct {
		Automations []automation.Automation
		Rules       map[string]string
	}
	if code := call(t, srv, "GET", "/api/automations", "", &list); code != 200 || len(list.Automations) != 4 || len(list.Rules) != 2 {
		t.Fatalf("automations: %d %+v", code, list)
	}
	var runs struct{ Runs []automation.Run }
	if code := call(t, srv, "POST", "/api/automations/"+watch.WeeklyWeather+"/run", `{"farm":"`+f.ID+`"}`, &runs); code != 200 || len(runs.Runs) != 1 || runs.Runs[0].Status != automation.RunNotified {
		t.Fatalf("run: %d %+v", code, runs)
	}
	var inbox struct {
		Notices []automation.Notice
		Unread  int
	}
	if call(t, srv, "GET", "/api/notices?farm="+f.ID, "", &inbox); len(inbox.Notices) == 0 || inbox.Unread != len(inbox.Notices) {
		t.Fatalf("inbox %+v", inbox)
	}
	var marked map[string]int
	if call(t, srv, "POST", "/api/notices/read", `{"ids":["`+inbox.Notices[0].ID+`"]}`, &marked); marked["marked"] != 1 {
		t.Errorf("marked %v", marked)
	}
	total := len(inbox.Notices)
	if call(t, srv, "GET", "/api/notices?unread=1", "", &inbox); inbox.Unread != total-1 || len(inbox.Notices) != total-1 {
		t.Errorf("unread %+v, want %d", inbox, total-1)
	}
	if code := call(t, srv, "GET", "/api/automations/"+watch.WeeklyWeather+"/runs", "", &runs); code != 200 || len(runs.Runs) != 1 {
		t.Errorf("runs: %d %+v", code, runs)
	}
	// A farmer's own automation: a heat watch.
	var made automation.Automation
	body := `{"name":"Heat watch","enabled":true,"trigger":{"kind":"weather","schedule":"0 1 * * *","weather":{"metric":"tmax_c","op":">=","value":40,"days":5}},"action":{"kind":"rule","rule":"weather_advice"}}`
	if code := call(t, srv, "POST", "/api/automations", body, &made); code != http.StatusCreated || made.ID == "" || made.Builtin {
		t.Fatalf("create: %d %+v", code, made)
	}
	if code := call(t, srv, "POST", "/api/automations", `{"name":"x","trigger":{"kind":"schedule","schedule":"daily"},"action":{"kind":"rule","rule":"weather_advice"}}`, nil); code != http.StatusBadRequest {
		t.Errorf("a bad schedule: %d, want 400", code)
	}
	if code := call(t, srv, "PUT", "/api/automations/"+watch.WeeklyWeather, `{"name":"Mine now","enabled":false,"trigger":{"kind":"event","event":"x"},"action":{"kind":"rule","rule":"crop_tasks"}}`, &made); code != 200 || made.Enabled || made.Name != "Weekly weather check" {
		t.Errorf("turning a built-in off: %d %+v", code, made)
	}
	if code := call(t, srv, "DELETE", "/api/automations/"+watch.WeeklyWeather, "", nil); code != http.StatusConflict {
		t.Errorf("deleting a built-in: %d, want 409", code)
	}
	if code := call(t, srv, "DELETE", "/api/automations/nope", "", nil); code != http.StatusNotFound {
		t.Errorf("deleting none: %d, want 404", code)
	}
	wx.Set(weather.ErrUnavailable)
	if code := call(t, srv, "GET", "/api/farms/"+f.ID+"/week", "", nil); code != http.StatusServiceUnavailable {
		t.Errorf("forecast down: %d, want 503", code)
	}
}

func TestWatchRoutesNotConfigured(t *testing.T) {
	t.Parallel()
	srv := server(t)
	for _, path := range []string{"/api/farms/x/week", "/api/notices", "/api/automations"} {
		if code := call(t, srv, "GET", path, "", nil); code != http.StatusServiceUnavailable {
			t.Errorf("%s: %d, want 503", path, code)
		}
	}
}
