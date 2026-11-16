package farmagent_test

import (
	"context"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/farmagent"
	"github.com/bsamanvith/agentkit/apps/farm/ideas"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// check_ideas reads only, and answers a plot's spread and one plant by
// name with a verdict and its reason.
func TestCheckIdeasTool(t *testing.T) {
	t.Parallel()
	svc, _ := service(t)
	book, err := ideas.DefaultBook()
	if err != nil {
		t.Fatal(err)
	}
	tools, err := farmagent.IdeaTools(ideas.New(plannerFor(t, svc), nil, book))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tool.NewRegistry(tools)
	if err != nil {
		t.Fatal(err)
	}
	defs, err := reg.List(context.Background())
	if err != nil || len(defs) != 1 || defs[0].Name != farmagent.CheckIdeasTool || defs[0].Kind != tool.KindRead {
		t.Fatalf("tools %+v, %v", defs, err)
	}
	spread := invoke[ideas.Report](t, reg, farmagent.CheckIdeasTool, `{"farm":"farm_1","limit":2}`)
	n := map[string]int{}
	for _, i := range spread.Ideas {
		n[i.Band]++
		if i.Why == "" || i.Verdict == "" {
			t.Errorf("idea %s has no verdict or reason", i.Crop)
		}
	}
	if n[ideas.BandCommon] != 2 || n[ideas.BandNotHere] != 2 {
		t.Errorf("bands %v at limit 2", n)
	}
	one := invoke[ideas.Report](t, reg, farmagent.CheckIdeasTool, `{"farm":"farm_1","search":"Malus domestica"}`)
	if len(one.Ideas) != 1 || one.Ideas[0].Verdict != ideas.Unrealistic {
		t.Errorf("apple: %+v", one.Ideas)
	}
}
