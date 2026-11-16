package farmagent_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/cycle/addon"
	"github.com/bsamanvith/agentkit/apps/farm/farmagent"
	"github.com/bsamanvith/agentkit/apps/farm/region"
	"github.com/bsamanvith/agentkit/apps/farm/schemes"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// get_schemes only reads, gives exactly the computed answer (dates, the
// source's read date, the confirm line), and the prompt sends insurance
// questions to it.
func TestGetSchemes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, f := service(t)
	p := plannerFor(t, svc)
	if _, err := svc.SetPlan(ctx, f.ID, &agro.Plan{Zones: []agro.Zone{{Crop: "chickpea", Sow: "2026-11-10", Ring: f.Boundary.Ring()}}}, &agro.PlanReport{}); err != nil {
		t.Fatal(err)
	}
	set, err := region.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	src := addon.NewSchemes(set.Base(), svc, p.Today)
	tools, err := farmagent.SchemeTools(src)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tool.NewRegistry(tools)
	if err != nil {
		t.Fatal(err)
	}
	defs, _ := reg.List(ctx)
	if len(defs) != 1 || defs[0].Name != farmagent.GetSchemesTool || defs[0].Kind != tool.KindRead {
		t.Fatalf("tools %+v", defs)
	}
	res, err := reg.Invoke(ctx, farmagent.GetSchemesTool, json.RawMessage(`{"farm":"farm_1"}`))
	if err != nil {
		t.Fatal(err)
	}
	var got schemes.Answer
	if err := json.Unmarshal(res, &got); err != nil {
		t.Fatalf("%v: %s", err, res)
	}
	want, err := src.Farm(ctx, "farm_1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tool answer %+v, want %+v", got, want)
	}
	if len(got.Items) != 1 || got.Items[0].By != "2026-12-15" || got.Items[0].Source.Read == "" || got.Confirm != schemes.Confirm {
		t.Errorf("answer %+v", got)
	}
	prompt, err := farmagent.SystemPrompt(svc, farmagent.WithPlanner(p), farmagent.WithSchemes(src))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"get_schemes", "never from memory", "Rythu Seva Kendra"} {
		if !strings.Contains(prompt, w) {
			t.Errorf("system prompt lacks %q", w)
		}
	}
}
