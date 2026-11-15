package farmagent_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/cycle"
	"github.com/bsamanvith/agentkit/apps/farm/cycle/cycletest"
	"github.com/bsamanvith/agentkit/apps/farm/farmagent"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// The cycle tools: get_cycles reads the line; set_cycle accepts, sets the
// farmer's own crop and date, chooses the method, records a sowing, and
// refuses what it cannot do as invalid input.
func TestCycleTools(t *testing.T) {
	t.Parallel()
	svc, _ := service(t)
	p := plannerFor(t, svc)
	tools, err := farmagent.CycleTools(cycle.New(p, cycletest.NewMemory()))
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
	for _, bad := range []string{
		`{"farm":"farm_1","position":1,"sown":"2026-10-30"}`,
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
