package farmagent_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/automation"
	"github.com/bsamanvith/agentkit/apps/farm/automation/sqlite"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farmagent"
	"github.com/bsamanvith/agentkit/apps/farm/watch"
	"github.com/bsamanvith/agentkit/apps/farm/weather/weathertest"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// planted is the sample farm with maize sown on 15 October; the planner's
// today is 31 October (day 16).
func planted(t *testing.T) (*farm.Service, *watch.Watch, automation.Store, *weathertest.Fake) {
	t.Helper()
	svc, f := service(t)
	p := plannerFor(t, svc)
	if _, err := svc.SetPlan(context.Background(), f.ID, &agro.Plan{Zones: []agro.Zone{{Crop: "maize", Sow: "2026-10-15", Ring: f.Boundary.Ring()}}}, &agro.PlanReport{}); err != nil {
		t.Fatal(err)
	}
	wx := weathertest.New(weathertest.Week("2026-10-31")...)
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "farm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return svc, watch.New(p, wx), store, wx
}

func call(t *testing.T, reg tool.Registry, name, in string) map[string]any {
	t.Helper()
	out, err := reg.Invoke(context.Background(), name, json.RawMessage(in))
	if err != nil {
		t.Fatalf("%s(%s): %v", name, in, err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestWatchTools(t *testing.T) {
	t.Parallel()
	svc, w, store, wx := planted(t)
	p := plannerFor(t, svc)
	tools, err := farmagent.WatchTools(w, p, store)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tool.NewRegistry(tools)
	if err != nil {
		t.Fatal(err)
	}
	cal := call(t, reg, farmagent.CropCalendarTool, `{"farm":"farm_1"}`)
	zones := cal["zones"].([]any)
	if cal["saved_plan"] != true || len(zones) != 1 || zones[0].(map[string]any)["harvest_from"] != "2027-01-13" {
		t.Errorf("saved plan's calendar %v", cal)
	}
	week := call(t, reg, farmagent.CropCalendarTool, `{"farm":"farm_1","parts":[{"crop":"green_gram","sow":"2026-11-02"}],"from":"2026-10-31","to":"2026-11-06"}`)
	tasks := week["zones"].([]any)[0].(map[string]any)["tasks"].([]any)
	if week["saved_plan"] != false || len(tasks) != 2 { // seed treatment, sowing
		t.Errorf("a proposed crop's first week: %v", tasks)
	}
	wet := weathertest.Week("2026-10-31")
	wet[2].RainMM = 90
	wx.Set(nil, wet...)
	got := call(t, reg, farmagent.GetWeekTool, `{"farm":"farm_1"}`)
	if adv := got["advice"].([]any); len(adv) == 0 || adv[0].(map[string]any)["rule"] != "heavy_rain" {
		t.Errorf("get_week advice %v", got["advice"])
	}
	if a := call(t, reg, farmagent.ListAlertsTool, `{"farm":"farm_1"}`); len(a["alerts"].([]any)) != 0 {
		t.Errorf("alerts %v", a)
	}
	if _, err := reg.Invoke(context.Background(), farmagent.CropCalendarTool, json.RawMessage(`{"farm":"farm_1","from":"soon"}`)); err == nil {
		t.Error("a bad date passed")
	}
}

// TestSkillRunner: an automation's skill action runs the agent on the
// skill for the plot; what it sends with notify_farmer becomes the
// outcome's notices; a notice for another plot is refused.
func TestSkillRunner(t *testing.T) {
	t.Parallel()
	svc, w, store, _ := planted(t)
	if _, err := store.AddNotice(context.Background(), automation.Notice{ID: "n1", Farm: "farm_1", Key: "k", Title: "Heavy rain Thursday", CreatedAt: time.Now()}, time.Now()); err != nil {
		t.Fatal(err)
	}
	skills, err := farmagent.Skills()
	if err != nil {
		t.Fatal(err)
	}
	model := llmtest.NewFake(t,
		llmtest.ToolCalls(llm.ToolUse{ID: "c1", Name: "skill_load", Input: json.RawMessage(`{"name":"weather-check"}`)}),
		llmtest.ToolCalls(llm.ToolUse{ID: "c2", Name: farmagent.GetWeekTool, Input: json.RawMessage(`{"farm":"farm_1"}`)}),
		llmtest.ToolCalls(
			llm.ToolUse{ID: "c3", Name: farmagent.NotifyTool, Input: json.RawMessage(`{"farm":"farm_2","severity":"urgent","title":"x","body":"y"}`)},
			llm.ToolUse{ID: "c4", Name: farmagent.NotifyTool, Input: json.RawMessage(`{"farm":"farm_1","severity":"warning","title":"Weed this week","body":"The maize is 16 days old: hoe before the weeds grow.","day":"2026-11-05"}`)}),
		llmtest.Text("one notice: weeding"),
	)
	r, err := farmagent.NewSkillRunner(model, svc, farmagent.WithPlanner(plannerFor(t, svc)), farmagent.WithWatch(w, store), farmagent.WithSkills(skills))
	if err != nil {
		t.Fatal(err)
	}
	job := automation.Job{
		Automation: automation.Automation{
			ID: "brief", Name: "Weekly field brief",
			Action: automation.Action{Kind: automation.DoSkill, Skill: "weather-check", Prompt: "Only what they must do."},
		},
		Target: automation.Target{ID: "farm_1", Name: "East"}, Reason: "schedule",
	}
	out, err := r.Handle(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Notices) != 1 || out.Notices[0].Severity != automation.Warning || out.Notices[0].Key != "skill:weed this week:2026-11-05" || out.Note != "one notice: weeding" {
		t.Errorf("outcome %+v", out)
	}
	reqs := model.Requests()
	first := reqs[0]
	prompt := first.Messages[0].Content[0].(llm.Text).Text
	if !strings.Contains(prompt, `"weather-check"`) || !strings.Contains(prompt, "farm_1") || !strings.Contains(prompt, "Only what they must do.") ||
		!strings.Contains(prompt, "not read yet (do not repeat these, even in other words):\n- Heavy rain Thursday") {
		t.Errorf("prompt %q", prompt)
	}
	if !strings.Contains(first.System, "background automation") {
		t.Error("the system prompt does not say it is a background run")
	}
	var names []string
	for _, d := range first.Tools {
		names = append(names, d.Name)
	}
	for _, want := range []string{farmagent.NotifyTool, farmagent.GetWeekTool, farmagent.CropCalendarTool, "skill_load"} {
		if !slices.Contains(names, want) {
			t.Errorf("tools %v lack %s", names, want)
		}
	}
	// The refused notice for farm_2 went back to the model as an error.
	last := reqs[3].Messages
	if res := last[len(last)-1].Content[0].(llm.ToolResult); !res.IsError {
		t.Errorf("the notice for another plot: %+v, want an error", res)
	}
}

// TestListAlertsStaysOnItsFarm: with sign-in, the assistant's and MCP's
// list_alerts sees only the plots of the request's farm (holding).
func TestListAlertsStaysOnItsFarm(t *testing.T) {
	t.Parallel()
	svc, w, store, _ := planted(t)
	ctx := context.Background()
	for _, n := range []automation.Notice{
		{ID: "n1", Farm: "farm_1", Automation: "a", Run: "r", Key: "k1", Severity: "warning", Title: "Ours", CreatedAt: time.Now()},
		{ID: "n2", Farm: "farm_other", Automation: "a", Run: "r", Key: "k2", Severity: "warning", Title: "Someone else's", CreatedAt: time.Now()},
	} {
		if _, err := store.AddNotice(ctx, n, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	tools, err := farmagent.WatchTools(w, plannerFor(t, svc), store)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tool.NewRegistry(tools)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Adopt(ctx, "hold_a"); err != nil {
		t.Fatal(err)
	}
	titles := func(ctx context.Context, in string) ([]string, error) {
		out, err := reg.Invoke(ctx, farmagent.ListAlertsTool, json.RawMessage(in))
		if err != nil {
			return nil, err
		}
		var m struct{ Alerts []automation.Notice }
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, n := range m.Alerts {
			got = append(got, n.Title)
		}
		return got, nil
	}
	ours := farm.WithHolding(ctx, "hold_a")
	if got, err := titles(ours, `{"farm":""}`); err != nil || !slices.Equal(got, []string{"Ours"}) {
		t.Errorf("farm A's alerts: %v, %v; want only its own", got, err)
	}
	if got, err := titles(ours, `{"farm":"farm_other"}`); err == nil {
		t.Errorf("farm A asks for another farm's plot: %v, want an error", got)
	}
	if got, err := titles(farm.WithHolding(ctx, "hold_b"), `{"farm":""}`); err != nil || len(got) != 0 {
		t.Errorf("farm B sees %v, %v; want none", got, err)
	}
	if got, err := titles(ctx, `{"farm":""}`); err != nil || len(got) != 2 {
		t.Errorf("without sign-in: %v, %v; want every alert", got, err)
	}
}
