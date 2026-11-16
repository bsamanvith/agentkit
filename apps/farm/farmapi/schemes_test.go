package farmapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/cycle/addon"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farm/farmtest"
	"github.com/bsamanvith/agentkit/apps/farm/farmapi"
	"github.com/bsamanvith/agentkit/apps/farm/planner"
	"github.com/bsamanvith/agentkit/apps/farm/region"
	"github.com/bsamanvith/agentkit/apps/farm/schemes"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

// GET /api/farms/{id}/schemes: the current cycle's schemes with their
// source; none for a plot with no plan; 404 for no such plot; 503 without
// the service.
func TestSchemesRoute(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clk := clocktest.NewFake(time.Date(2026, 7, 1, 1, 0, 0, 0, time.UTC))
	svc, err := farm.NewService(&farmtest.Memory{}, farm.NewVerifier())
	if err != nil {
		t.Fatal(err)
	}
	set, err := region.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	p := planner.New(svc, set.Base().Library(), nil, planner.WithClock(clk))
	srv := httptest.NewServer(farmapi.New(svc, farmapi.WithPlanner(p), farmapi.WithSchemes(addon.NewSchemes(set.Base(), svc, p.Today))))
	t.Cleanup(srv.Close)
	bare := httptest.NewServer(farmapi.New(svc))
	t.Cleanup(bare.Close)

	var f farm.Farm
	if code := call(t, srv, "POST", "/api/farms", `{"name":"Anantapur plot","boundary":`+boundary+`}`, &f); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	var a schemes.Answer
	if code := call(t, srv, "GET", "/api/farms/"+f.ID+"/schemes", "", &a); code != http.StatusOK || len(a.Items) != 0 || a.Confirm == "" {
		t.Errorf("no plan: %d %+v", code, a)
	}
	got, _ := svc.Get(ctx, f.ID)
	if _, err := svc.SetPlan(ctx, f.ID, &agro.Plan{Zones: []agro.Zone{{Crop: "groundnut", Sow: "2026-06-25", Ring: got.Boundary.Ring()}}}, &agro.PlanReport{}); err != nil {
		t.Fatal(err)
	}
	if code := call(t, srv, "GET", "/api/farms/"+f.ID+"/schemes", "", &a); code != http.StatusOK || len(a.Items) != 1 {
		t.Fatalf("schemes: %d %+v", code, a)
	}
	it := a.Items[0]
	if it.Scheme != "pmfby" || it.Status != schemes.Soon || it.By != "2026-07-15" || *it.DaysLeft != 14 || it.Source.Read != "2026-11-15" ||
		!strings.Contains(it.Says, "read on 15 November 2026") {
		t.Errorf("item %+v", it)
	}
	if code := call(t, srv, "GET", "/api/farms/farm_nope/schemes", "", nil); code != http.StatusNotFound {
		t.Errorf("no such plot: %d", code)
	}
	if code := call(t, bare, "GET", "/api/farms/"+f.ID+"/schemes", "", nil); code != http.StatusServiceUnavailable {
		t.Errorf("not configured: %d", code)
	}
}
