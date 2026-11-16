package watch_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/automation"
	"github.com/bsamanvith/agentkit/apps/farm/cycle/addon"
	"github.com/bsamanvith/agentkit/apps/farm/region"
	"github.com/bsamanvith/agentkit/apps/farm/watch"
	"github.com/bsamanvith/agentkit/apps/farm/work"
	"github.com/bsamanvith/agentkit/apps/farm/work/worktest"
)

func insuranceNotices(t *testing.T, w *world, farm string) []automation.Notice {
	t.Helper()
	var out []automation.Notice
	for _, n := range notices(t, w, farm) {
		if n.Automation == watch.WeeklyInsurance {
			out = append(out, n)
		}
	}
	return out
}

// The PMFBY rabi cut-off (15 December) warns once, when it is two weeks
// away or less and the enrolment is not ticked: not five weeks ahead, not
// again a week later, never for a plot that ticked it.
func TestInsuranceCutoffWarnsOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := setup(t)
	set, err := region.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	roads := work.New(w.plan, worktest.NewMemory(), work.WithTasks(addon.NewSchemes(set.Base(), w.svc, w.plan.Today)))
	w.watch.UseRoadmap(roads)
	w.savePlan(t, w.planted, agro.Zone{Crop: "chickpea", Sow: "2026-11-20"})
	w.savePlan(t, w.bare, agro.Zone{Crop: "chickpea", Sow: "2026-11-20"})
	enrol := addon.EnrolTaskID("pmfby", agro.Rabi, 2026)
	if _, _, err := roads.Record(ctx, w.bare, []work.Event{{ID: "e1", Task: enrol, Kind: work.Done, Day: "2026-11-09"}}); err != nil {
		t.Fatal(err)
	}
	run := func() {
		t.Helper()
		if _, err := w.engine.RunNow(ctx, watch.WeeklyInsurance, ""); err != nil {
			t.Fatal(err)
		}
	}
	run() // 9 November: 36 days ahead
	if ns := insuranceNotices(t, w, ""); len(ns) != 0 {
		t.Fatalf("five weeks ahead: %+v", ns)
	}
	w.clk.Advance(21 * 24 * time.Hour) // Monday 30 November: 15 days
	run()
	if ns := insuranceNotices(t, w, ""); len(ns) != 0 {
		t.Fatalf("15 days ahead: %+v", ns)
	}
	w.clk.Advance(2 * 24 * time.Hour) // 2 December: 13 days
	run()
	run()
	ns := insuranceNotices(t, w, "")
	if len(ns) != 1 || ns[0].Farm != w.planted || ns[0].Severity != automation.Warning ||
		!strings.Contains(ns[0].Title, "Enrol for crop insurance by 15 December · East field") || !strings.Contains(ns[0].Body, "13 days left") ||
		!strings.Contains(ns[0].Body, "Rythu Seva Kendra") {
		t.Fatalf("notices %+v: want one, for the plot that did not tick", ns)
	}
	w.clk.Advance(7 * 24 * time.Hour) // 9 December: 6 days
	run()
	if ns := insuranceNotices(t, w, ""); len(ns) != 1 {
		t.Errorf("a week later: %d notices, want the one", len(ns))
	}
}
