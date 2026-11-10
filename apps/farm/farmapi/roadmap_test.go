package farmapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farm/farmtest"
	"github.com/bsamanvith/agentkit/apps/farm/farmapi"
	"github.com/bsamanvith/agentkit/apps/farm/planner"
	"github.com/bsamanvith/agentkit/apps/farm/work"
	"github.com/bsamanvith/agentkit/apps/farm/work/worktest"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

// The roadmap routes: read a plot's roadmap, record events (a resend adds
// nothing), refuse bad events with 400, and 503 without the service.
func TestRoadmapRoutes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clk := clocktest.NewFake(time.Date(2026, 7, 20, 1, 0, 0, 0, time.UTC))
	svc, err := farm.NewService(&farmtest.Memory{}, farm.NewVerifier())
	if err != nil {
		t.Fatal(err)
	}
	lib, _ := agro.Default()
	p := planner.New(svc, lib, nil, planner.WithClock(clk))
	ws := work.New(p, worktest.NewMemory())
	srv := httptest.NewServer(farmapi.New(svc, farmapi.WithPlanner(p), farmapi.WithRoadmap(ws)))
	t.Cleanup(srv.Close)

	var f farm.Farm
	if code := call(t, srv, "POST", "/api/farms", `{"name":"Anantapur plot","boundary":`+boundary+`}`, &f); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	if code := call(t, srv, "GET", "/api/farms/"+f.ID+"/roadmap", "", nil); code != http.StatusBadRequest {
		t.Errorf("roadmap with no plan: %d; want 400", code)
	}
	got, _ := svc.Get(ctx, f.ID)
	if _, err := svc.SetPlan(ctx, f.ID, &agro.Plan{Zones: []agro.Zone{{Crop: "groundnut", Sow: "2026-07-01", Ring: got.Boundary.Ring()}}}, &agro.PlanReport{}); err != nil {
		t.Fatal(err)
	}
	var rm work.Roadmap
	if code := call(t, srv, "GET", "/api/farms/"+f.ID+"/roadmap", "", &rm); code != http.StatusOK || len(rm.Tasks) == 0 || rm.Counts.Open != len(rm.Tasks) {
		t.Fatalf("roadmap: %d, %+v", code, rm.Counts)
	}
	sow := work.TaskID(0, "groundnut", "2026-07-01", "sow")
	body := `{"events":[{"id":"phone-1","task":"` + sow + `","kind":"done","day":"2026-07-03","by":"Ramesh"},{"id":"phone-2","kind":"added","title":"Hire 4 workers for weeding","day":"2026-07-25"}]}`
	var out struct {
		Added   int          `json:"added"`
		Roadmap work.Roadmap `json:"roadmap"`
	}
	if code := call(t, srv, "POST", "/api/farms/"+f.ID+"/roadmap/events", body, &out); code != http.StatusOK || out.Added != 2 || out.Roadmap.Zones[0].Sown != "2026-07-03" {
		t.Fatalf("record: %d, added %d, zone %+v", code, out.Added, out.Roadmap.Zones)
	}
	if code := call(t, srv, "POST", "/api/farms/"+f.ID+"/roadmap/events", body, &out); code != http.StatusOK || out.Added != 0 {
		t.Errorf("resend: %d, added %d; want 200 and 0", code, out.Added)
	}
	var refused struct {
		EventID string `json:"event_id"`
		Index   int    `json:"index"`
	}
	twoBad := `{"events":[{"id":"ok-1","kind":"added","title":"t","day":"2026-07-25"},{"id":"bad-1","task":"nope","kind":"done","day":"2026-07-03"}]}`
	if code := call(t, srv, "POST", "/api/farms/"+f.ID+"/roadmap/events", twoBad, &refused); code != http.StatusBadRequest || refused.EventID != "bad-1" || refused.Index != 1 {
		t.Errorf("refused event: %d, %+v; want 400 naming bad-1 at 1", code, refused)
	}
	for name, bad := range map[string]string{
		"unknown task":  `{"events":[{"id":"x","task":"nope","kind":"done","day":"2026-07-03"}]}`,
		"no events":     `{"events":[]}`,
		"unknown field": `{"events":[{"id":"x","task":"` + sow + `","kind":"done","day":"2026-07-03","colour":"red"}]}`,
	} {
		if code := call(t, srv, "POST", "/api/farms/"+f.ID+"/roadmap/events", bad, nil); code != http.StatusBadRequest {
			t.Errorf("%s: %d; want 400", name, code)
		}
	}
	if code := call(t, srv, "GET", "/api/farms/nope/roadmap", "", nil); code != http.StatusNotFound {
		t.Errorf("unknown plot: %d; want 404", code)
	}
	bare := server(t)
	if code := call(t, bare, "GET", "/api/farms/x/roadmap", "", nil); code != http.StatusServiceUnavailable {
		t.Errorf("without the service: %d; want 503", code)
	}
	var cfg map[string]any
	if call(t, srv, "GET", "/api/config", "", &cfg); cfg["roadmap"] != true {
		t.Errorf("config %v; want roadmap true", cfg)
	}
}
