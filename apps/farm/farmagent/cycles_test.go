package farmagent_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/cycle"
	"github.com/bsamanvith/agentkit/apps/farm/cycle/addon"
	"github.com/bsamanvith/agentkit/apps/farm/cycle/cycletest"
	"github.com/bsamanvith/agentkit/apps/farm/farmagent"
	"github.com/bsamanvith/agentkit/apps/farm/inputs"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// The cycle tools: get_cycles reads the line; set_cycle accepts, sets the
// farmer's own crop and date, chooses the method, records a sowing, and
// refuses what it cannot do as invalid input.
func TestCycleTools(t *testing.T) {
	t.Parallel()
	svc, _ := service(t)
	p := plannerFor(t, svc)
	book, err := inputs.Default(p.Library())
	if err != nil {
		t.Fatal(err)
	}
	tools, err := farmagent.CycleTools(cycle.New(p, cycletest.NewMemory(), cycle.WithAddons(addon.NewInputs(book, p.Library()))))
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
		if d.Name == farmagent.SetCycleTool {
			want = tool.KindWrite
		}
		if d.Kind != want {
			t.Errorf("%s: kind %s, want %s", d.Name, d.Kind, want)
		}
	}
	line := func(m map[string]any) cycle.Line {
		var l cycle.Line
		b, _ := json.Marshal(m)
		if err := json.Unmarshal(b, &l); err != nil {
			t.Fatal(err)
		}
		return l
	}
	l := line(call(t, reg, farmagent.GetCyclesTool, `{"farm":"farm_1"}`))
	if len(l.Cycles) != cycle.MaxCycles || l.Cycles[0].Suggested == nil {
		t.Fatalf("get_cycles = %+v", l)
	}
	// The shopping list and schedule are get_inputs': get_cycles points there.
	if _, ok := l.Cycles[0].Addons["inputs"]; ok || l.Cycles[0].Addons["inputs_by"] != farmagent.GetInputsTool {
		t.Errorf("get_cycles addons = %v; want a pointer to get_inputs", l.Cycles[0].Addons)
	}
	// get_inputs: the plan's numbers, as the book makes them, without the
	// per-value references.
	in := call(t, reg, farmagent.GetInputsTool, `{"farm":"farm_1"}`)
	plan, _ := in["inputs"].(map[string]any)
	buy, _ := plan["buy_before"].([]any)
	if in["crops"] == "" || len(buy) == 0 || plan["notes"] == nil {
		t.Fatalf("get_inputs = %v", in)
	}
	if first, _ := buy[0].(map[string]any); first["refs"] != nil || first["qty"] == nil || first["unit"] == nil {
		t.Errorf("get_inputs item = %v; want quantity and unit, no refs", first)
	}
	if _, err := reg.Invoke(context.Background(), farmagent.GetInputsTool, json.RawMessage(`{"farm":"farm_1","position":3}`)); !errors.Is(err, tool.ErrInvalidInput) {
		t.Errorf("get_inputs position 3: %v; want invalid input", err)
	}
	l = line(call(t, reg, farmagent.SetCycleTool, `{"farm":"farm_1","position":0,"accept":true}`))
	if l.Cycles[0].Own == nil {
		t.Errorf("accept: %+v", l.Cycles[0])
	}
	l = line(call(t, reg, farmagent.SetCycleTool, `{"farm":"farm_1","position":1,"crop":"chickpea","sow":"2027-02-01","method":"organic"}`))
	if o := l.Cycles[1].Own; o == nil || o.MainCrop() != "chickpea" || o.Sow != "2027-02-01" || o.Method != cycle.Organic {
		t.Errorf("own cycle 2: %+v", o)
	}
	l = line(call(t, reg, farmagent.SetCycleTool, `{"farm":"farm_1","position":1,"area":0,"crop":"green_gram","sow":"2027-02-10"}`))
	if o := l.Cycles[1].Own; o == nil || o.MainCrop() != "green_gram" {
		t.Errorf("area 0 changed: %+v", o)
	}
	l = line(call(t, reg, farmagent.SetCycleTool, `{"farm":"farm_1","position":0,"sown":"2026-10-30"}`))
	if c := l.Cycles[0].Planned; c.Sow != "2026-10-30" || c.SowKind != cycle.Actual {
		t.Errorf("sown: %+v", c)
	}
	// "I already sowed sesame on 28 October": the crop and the day in one
	// call.
	l = line(call(t, reg, farmagent.SetCycleTool, `{"farm":"farm_1","position":0,"sown":"2026-10-28","crop":"sesame"}`))
	if c := l.Cycles[0].Planned; c.MainCrop() != "sesame" || c.Sow != "2026-10-28" || c.SowKind != cycle.Actual {
		t.Errorf("sowed sesame: %+v", c)
	}
	// A crop for the current cycle with a sowing day already gone by is
	// a sowing that happened, however the call is worded.
	l = line(call(t, reg, farmagent.SetCycleTool, `{"farm":"farm_1","position":0,"crop":"chickpea","sow":"2026-10-20"}`))
	if c := l.Cycles[0].Planned; c.MainCrop() != "chickpea" || c.Sow != "2026-10-20" || c.SowKind != cycle.Actual {
		t.Errorf("a past sowing day: %+v; want it recorded as sown", c)
	}
	for _, bad := range []string{
		`{"farm":"farm_1","position":1,"sown":"2026-10-30"}`,
		// A sowing is recorded alone: nothing else in the call is dropped
		// without a word.
		`{"farm":"farm_1","position":0,"sown":"2026-10-30","method":"organic"}`,
		`{"farm":"farm_1","position":0,"sown":"2026-10-30","crop":"chickpea","sow":"2026-10-30"}`,
		`{"farm":"farm_1","position":0,"sown":"2026-10-30","accept":true}`,
		`{"farm":"farm_1","position":1,"crop":"chickpea"}`,
		`{"farm":"farm_1","position":5,"accept":true}`,
		`{"farm":"farm_1","position":1,"area":4,"crop":"chickpea","sow":"2027-02-01"}`,
		`{"farm":"farm_1","position":1,"crop":"kale","sow":"2027-02-01"}`,
	} {
		_, err := reg.Invoke(context.Background(), farmagent.SetCycleTool, json.RawMessage(bad))
		if !errors.Is(err, tool.ErrInvalidInput) {
			t.Errorf("%s: %v; want invalid input", bad, err)
		}
	}
}
