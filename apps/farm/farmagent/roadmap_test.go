package farmagent_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/farmagent"
	"github.com/bsamanvith/agentkit/apps/farm/work"
	"github.com/bsamanvith/agentkit/apps/farm/work/worktest"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// The roadmap tools: get_roadmap shows what is overdue and next;
// record_work records what the farmer said and refuses what it cannot.
func TestRoadmapTools(t *testing.T) {
	t.Parallel()
	svc, f := service(t)
	p := plannerFor(t, svc)
	if _, err := svc.SetPlan(context.Background(), f.ID, &agro.Plan{Zones: []agro.Zone{{Crop: "maize", Sow: "2026-10-15", Ring: f.Boundary.Ring()}}}, &agro.PlanReport{}); err != nil {
		t.Fatal(err)
	}
	tools, err := farmagent.RoadmapTools(work.New(p, worktest.NewMemory()))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tool.NewRegistry(tools)
	if err != nil {
		t.Fatal(err)
	}
	defs, _ := reg.List(context.Background())
	for _, d := range defs {
		want := tool.KindRead
		if d.Name == farmagent.RecordWorkTool {
			want = tool.KindWrite
		}
		if d.Kind != want {
			t.Errorf("%s: kind %s, want %s", d.Name, d.Kind, want)
		}
	}
	r := call(t, reg, farmagent.GetRoadmapTool, `{"farm":"farm_1"}`)
	all := call(t, reg, farmagent.GetRoadmapTool, `{"farm":"farm_1","all":true}`)
	if len(r["tasks"].([]any)) == 0 || len(r["tasks"].([]any)) >= len(all["tasks"].([]any)) || r["tasks_left_out"] == nil {
		t.Errorf("short roadmap has %d tasks, the whole %d", len(r["tasks"].([]any)), len(all["tasks"].([]any)))
	}
	weed := work.TaskID(0, "maize", "2026-10-15", "weed-1")
	got := call(t, reg, farmagent.RecordWorkTool, `{"farm":"farm_1","task":"`+weed+`","kind":"done","day":"2026-10-28","by":"Lakshmi","actuals":{"workers":4,"cost_inr":1400}}`)
	task := got["task"].(map[string]any)
	if got["recorded"] != true || task["state"] != "done" || task["actuals"].(map[string]any)["cost_inr"] != 1400.0 {
		t.Errorf("record_work = %v", got)
	}
	added := call(t, reg, farmagent.RecordWorkTool, `{"farm":"farm_1","kind":"added","title":"Repair the motor","day":"2026-11-02"}`)
	if added["task"].(map[string]any)["title"] != "Repair the motor" {
		t.Errorf("added = %v", added)
	}
	again := call(t, reg, farmagent.RecordWorkTool, `{"farm":"farm_1","kind":"added","title":"Repair the motor","day":"2026-11-02"}`)
	if again["recorded"] != false || again["task"].(map[string]any)["id"] != added["task"].(map[string]any)["id"] {
		t.Errorf("a retried add = %v; want the same task, not recorded again", again)
	}
	_, err = reg.Invoke(context.Background(), farmagent.RecordWorkTool, json.RawMessage(`{"farm":"farm_1","task":"nope","kind":"done","day":"2026-10-28"}`))
	if !errors.Is(err, tool.ErrToolFailed) {
		t.Errorf("unknown task: %v; want a tool failure", err)
	}
}
