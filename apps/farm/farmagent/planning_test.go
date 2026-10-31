package farmagent_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/agro/agrotest"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farmagent"
	"github.com/bsamanvith/agentkit/apps/farm/handbook"
	"github.com/bsamanvith/agentkit/apps/farm/planner"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

func plannerFor(t *testing.T, svc *farm.Service) *planner.Planner {
	t.Helper()
	lib, err := agro.Default()
	if err != nil {
		t.Fatal(err)
	}
	return planner.New(svc, lib, &agrotest.Source{Clim: agrotest.Hyderabad()},
		planner.WithClock(clocktest.NewFake(time.Date(2026, 10, 31, 6, 0, 0, 0, time.UTC))))
}

func planningRegistry(t *testing.T) tool.Registry {
	t.Helper()
	svc, _ := service(t)
	tools, err := farmagent.PlanningTools(plannerFor(t, svc))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tool.NewRegistry(tools)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestPlanningToolKinds(t *testing.T) {
	t.Parallel()
	defs, err := planningRegistry(t).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	writes := []string{farmagent.SetProfileTool, farmagent.SavePlanTool}
	for _, d := range defs {
		want := tool.KindRead
		if slices.Contains(writes, d.Name) {
			want = tool.KindWrite
		}
		if d.Kind != want || d.Risk != tool.RiskLow {
			t.Errorf("%s: kind %s risk %s, want %s low", d.Name, d.Kind, d.Risk, want)
		}
	}
	if len(defs) != 9 {
		t.Errorf("%d planning tools, want 9", len(defs))
	}
}

func TestPlanningToolsFlow(t *testing.T) {
	t.Parallel()
	reg := planningRegistry(t)
	soils := invoke[struct{ Soils []struct{ ID, Signs string } }](t, reg, farmagent.ListSoilsTool, `{}`)
	if len(soils.Soils) == 0 || soils.Soils[0].Signs == "" {
		t.Fatalf("soils %+v", soils)
	}
	pulses := invoke[struct{ Crops []struct{ ID, Kind string } }](t, reg, farmagent.ListCropsTool, `{"kind":"pulse"}`)
	for _, c := range pulses.Crops {
		if c.Kind != "pulse" {
			t.Errorf("list_crops kind pulse gave %s", c.ID)
		}
	}
	if len(pulses.Crops) < 4 {
		t.Errorf("%d pulses", len(pulses.Crops))
	}
	prof := invoke[agro.Profile](t, reg, farmagent.SetProfileTool, `{"farm":"farm_1","soil":"red_loam","water":[{"kind":"borewell","reliable":true}]}`)
	if prof.Soil != "red_loam" {
		t.Errorf("profile %+v", prof)
	}
	rec := invoke[struct {
		Today   string
		Soil    string
		Options []agro.Option
	}](t, reg, farmagent.RecommendCropsTool, `{"farm":"farm_1","around":"2026-11-20"}`)
	if len(rec.Options) != 5 || rec.Soil == "" || rec.Today != "2026-10-31" || !rec.Options[0].InRange {
		t.Errorf("recommend: %d options, soil %q, today %s", len(rec.Options), rec.Soil, rec.Today)
	}
	parts := `"parts":[{"crop":"groundnut","with":"pigeon_pea","pattern":[4,1],"share":0.5,"sow":"2027-06-20"},{"crop":"finger_millet","share":0.5,"sow":"2027-06-25"}]`
	laid := invoke[struct {
		Plan   agro.Plan
		Report agro.PlanReport
	}](t, reg, farmagent.SuggestLayoutTool, `{"farm":"farm_1",`+parts+`}`)
	if len(laid.Plan.Zones) != 2 || laid.Report.Status == agro.PlanInvalid {
		t.Fatalf("layout %+v", laid.Report)
	}
	if _, err := reg.Invoke(context.Background(), farmagent.CheckPlanTool, json.RawMessage(`{"farm":"farm_missing"}`)); !errors.Is(err, tool.ErrToolFailed) {
		t.Errorf("check_plan of a missing farm: %v, want a tool failure", err)
	}
	saved := invoke[struct{ Saved bool }](t, reg, farmagent.SavePlanTool, `{"farm":"farm_1",`+parts+`}`)
	checked := invoke[struct {
		Saved  bool
		Report agro.PlanReport
	}](t, reg, farmagent.CheckPlanTool, `{"farm":"farm_1"}`)
	if !saved.Saved || !checked.Saved || len(checked.Report.Zones) != 2 {
		t.Errorf("saved %v, checked %+v", saved.Saved, checked)
	}
	// A plan with errors comes back unsaved, with its report.
	bad := invoke[struct {
		Saved  bool
		Report agro.PlanReport
	}](t, reg, farmagent.SavePlanTool, `{"farm":"farm_1","parts":[{"crop":"maize","with":"soybean","pattern":[0,1],"share":1,"sow":"2027-06-20"}]}`)
	if bad.Saved || bad.Report.Status != agro.PlanInvalid {
		t.Errorf("a bad plan: saved %v, status %s", bad.Saved, bad.Report.Status)
	}
	if _, err := reg.Invoke(context.Background(), farmagent.SetProfileTool, json.RawMessage(`{"farm":"farm_1","soil":"moon_dust"}`)); err == nil {
		t.Error("set_profile took an unknown soil")
	}
}

func TestAssistantWithPlanningSkillsAndHandbook(t *testing.T) {
	t.Parallel()
	svc, _ := service(t)
	book, err := handbook.New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	skills, err := farmagent.Skills()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(skills.Names(), []string{"check-plan", "plan-season"}) {
		t.Fatalf("skills %v", skills.Names())
	}
	model := llmtest.NewFake(t,
		llmtest.ToolCalls(llm.ToolUse{ID: "c1", Name: "skill_load", Input: json.RawMessage(`{"name":"plan-season"}`)}),
		llmtest.ToolCalls(llm.ToolUse{ID: "c2", Name: farmagent.RecommendCropsTool, Input: json.RawMessage(`{"farm":"farm_1"}`)}),
		llmtest.Text("Groundnut in June."),
	)
	a, err := farmagent.NewAssistant(model, svc, farmagent.WithPlanner(plannerFor(t, svc)),
		farmagent.WithKnowledge(book, handbook.Description), farmagent.WithSkills(skills))
	if err != nil {
		t.Fatal(err)
	}
	r, err := a.Ask(context.Background(), "", "What should I plant?")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.Tools, []string{"skill_load", farmagent.RecommendCropsTool}) {
		t.Errorf("tools %v", r.Tools)
	}
	req := model.Requests()[0]
	for _, want := range []string{"plan-season", "check-plan", "recommend_crops"} {
		if !strings.Contains(req.System, want) {
			t.Errorf("system prompt lacks %s", want)
		}
	}
	var names []string
	for _, d := range req.Tools {
		names = append(names, d.Name)
	}
	for _, want := range []string{"list_farms", "recommend_crops", "save_plan", "knowledge_outline", "skill_load"} {
		if !slices.Contains(names, want) {
			t.Errorf("tools offered %v lack %s", names, want)
		}
	}
	// The skill's instructions went back to the model.
	if !strings.Contains(string(mustJSON(t, model.Requests()[1].Messages)), "Never score a crop") {
		t.Error("skill_load did not return plan-season's instructions")
	}
}
