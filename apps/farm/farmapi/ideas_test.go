package farmapi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro/agrotest"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farm/farmtest"
	"github.com/bsamanvith/agentkit/apps/farm/farmapi"
	"github.com/bsamanvith/agentkit/apps/farm/ideas"
	"github.com/bsamanvith/agentkit/apps/farm/planner"
	"github.com/bsamanvith/agentkit/apps/farm/region"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

// GET /api/farms/{id}/ideas: a spread of ideas with verdicts; one plant
// by ?search=; 400 for a limit that is no number; 404 for no such plot;
// 503 without the service.
func TestIdeasRoute(t *testing.T) {
	t.Parallel()
	clk := clocktest.NewFake(time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC))
	svc, err := farm.NewService(&farmtest.Memory{}, farm.NewVerifier())
	if err != nil {
		t.Fatal(err)
	}
	set, err := region.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	p := planner.New(svc, set.Base().Library(), &agrotest.Source{Clim: agrotest.Hyderabad()}, planner.WithClock(clk))
	book, err := ideas.DefaultBook()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(farmapi.New(svc, farmapi.WithPlanner(p), farmapi.WithIdeas(ideas.New(p, nil, book))))
	t.Cleanup(srv.Close)
	bare := httptest.NewServer(farmapi.New(svc))
	t.Cleanup(bare.Close)

	var f farm.Farm
	if code := call(t, srv, "POST", "/api/farms", `{"name":"Idea plot","boundary":`+boundary+`}`, &f); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	var rep ideas.Report
	if code := call(t, srv, "GET", "/api/farms/"+f.ID+"/ideas", "", &rep); code != http.StatusOK || rep.Farm != f.ID || len(rep.Ideas) < 6 || len(rep.Notes) == 0 {
		t.Fatalf("ideas: %d, %d ideas, notes %+v", code, len(rep.Ideas), rep.Notes)
	}
	bands := map[string]bool{}
	for _, i := range rep.Ideas {
		bands[i.Band] = true
	}
	if !bands[ideas.BandCommon] || !bands[ideas.BandNotHere] {
		t.Errorf("bands %v: want the usual crops and the ones that do not work", bands)
	}
	if code := call(t, srv, "GET", "/api/farms/"+f.ID+"/ideas?search=apple&limit=3", "", &rep); code != http.StatusOK || len(rep.Ideas) == 0 || rep.Ideas[0].Verdict == "" {
		t.Errorf("search apple: %d %+v", code, rep.Ideas)
	}
	if code := call(t, srv, "GET", "/api/farms/"+f.ID+"/ideas?limit=many", "", nil); code != http.StatusBadRequest {
		t.Errorf("limit=many: %d", code)
	}
	if code := call(t, srv, "GET", "/api/farms/farm_nope/ideas", "", nil); code != http.StatusNotFound {
		t.Errorf("no such plot: %d", code)
	}
	if code := call(t, bare, "GET", "/api/farms/"+f.ID+"/ideas", "", nil); code != http.StatusServiceUnavailable {
		t.Errorf("not configured: %d", code)
	}
}
