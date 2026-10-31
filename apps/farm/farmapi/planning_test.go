package farmapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/agro/agrotest"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farm/farmtest"
	"github.com/bsamanvith/agentkit/apps/farm/farmapi"
	"github.com/bsamanvith/agentkit/apps/farm/planner"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

func planningServer(t *testing.T, climate agro.ClimateSource) (*httptest.Server, string) {
	t.Helper()
	svc, err := farm.NewService(&farmtest.Memory{}, farm.NewVerifier())
	if err != nil {
		t.Fatal(err)
	}
	lib, err := agro.Default()
	if err != nil {
		t.Fatal(err)
	}
	p := planner.New(svc, lib, climate, planner.WithClock(clocktest.NewFake(time.Date(2026, 10, 31, 6, 0, 0, 0, time.UTC))))
	srv := httptest.NewServer(farmapi.New(svc, farmapi.WithPlanner(p)))
	t.Cleanup(srv.Close)
	var f farm.Farm
	if code := call(t, srv, "POST", "/api/farms", `{"name":"Black field","boundary":`+boundary+`}`, &f); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	return srv, f.ID
}

func TestPlanningRoutes(t *testing.T) {
	t.Parallel()
	srv, id := planningServer(t, &agrotest.Source{Clim: agrotest.Hyderabad()})
	var soils struct{ Soils []agro.Soil }
	var crops struct{ Crops []agro.Crop }
	if call(t, srv, "GET", "/api/soils", "", &soils) != 200 || call(t, srv, "GET", "/api/crops", "", &crops) != 200 || len(soils.Soils) == 0 || len(crops.Crops) == 0 {
		t.Fatalf("libraries: %d soils, %d crops", len(soils.Soils), len(crops.Crops))
	}
	var cl agro.Climate
	if code := call(t, srv, "GET", "/api/farms/"+id+"/climate", "", &cl); code != 200 || cl.RainMM() == 0 {
		t.Errorf("climate: %d, %v mm", code, cl.RainMM())
	}
	var f farm.Farm
	if code := call(t, srv, "PUT", "/api/farms/"+id+"/profile", `{"soil":"black","water":[{"kind":"borewell","reliable":true}]}`, &f); code != 200 || f.Profile == nil {
		t.Fatalf("profile: %d %+v", code, f.Profile)
	}
	var rec planner.Recommendation
	if code := call(t, srv, "POST", "/api/farms/"+id+"/recommend", `{"around":"2026-11-15","limit":3}`, &rec); code != 200 || len(rec.Options) != 3 || rec.Soil == nil {
		t.Fatalf("recommend: %d, %d options", code, len(rec.Options))
	}
	var laid struct {
		Plan   agro.Plan
		Report agro.PlanReport
	}
	if code := call(t, srv, "POST", "/api/farms/"+id+"/layout", `{"parts":[{"crop":"chickpea","share":0.5,"sow":"2026-11-10"},{"crop":"sunflower","share":0.5,"sow":"2026-11-10"}]}`, &laid); code != 200 || len(laid.Plan.Zones) != 2 {
		t.Fatalf("layout: %d, %+v", code, laid)
	}
	plan := mustJSON(t, laid.Plan)
	var rep agro.PlanReport
	if code := call(t, srv, "POST", "/api/farms/"+id+"/plan/check", plan, &rep); code != 200 || rep.Status == agro.PlanInvalid {
		t.Fatalf("check: %d %s", code, rep.Status)
	}
	if code := call(t, srv, "PUT", "/api/farms/"+id+"/plan", plan, &f); code != 200 || f.Plan == nil || f.PlanReport == nil {
		t.Fatalf("save: %d", code)
	}
	var bad struct {
		Error      string
		PlanReport *agro.PlanReport `json:"plan_report"`
	}
	if code := call(t, srv, "PUT", "/api/farms/"+id+"/plan", `{"zones":[]}`, &bad); code != http.StatusUnprocessableEntity || bad.PlanReport == nil || bad.PlanReport.Status != agro.PlanInvalid {
		t.Errorf("an empty plan: %d %+v", code, bad)
	}
	var cleared farm.Farm
	if code := call(t, srv, "DELETE", "/api/farms/"+id+"/plan", "", &cleared); code != 200 || cleared.Plan != nil || cleared.ID != id {
		t.Errorf("clear: %d", code)
	}
}

func TestPlanningErrors(t *testing.T) {
	t.Parallel()
	srv, id := planningServer(t, &agrotest.Source{Err: agro.ErrNoClimate})
	for _, tc := range []struct {
		method, path, body string
		want               int
	}{
		{"POST", "/api/farms/" + id + "/recommend", `{}`, http.StatusServiceUnavailable},
		{"GET", "/api/farms/" + id + "/climate", ``, http.StatusServiceUnavailable},
		{"POST", "/api/farms/" + id + "/recommend", `{"around":"soon"}`, http.StatusBadRequest},
		{"PUT", "/api/farms/" + id + "/profile", `{"soil":"moon_dust"}`, http.StatusBadRequest},
		{"PUT", "/api/farms/" + id + "/profile", `{"colour":"red"}`, http.StatusBadRequest},
		{"POST", "/api/farms/" + id + "/layout", `{"parts":[{"crop":"unobtainium","share":1}]}`, http.StatusBadRequest},
		{"POST", "/api/farms/farm_missing/plan/check", `{"zones":[]}`, http.StatusNotFound},
	} {
		if code := call(t, srv, tc.method, tc.path, tc.body, nil); code != tc.want {
			t.Errorf("%s %s %s: %d, want %d", tc.method, tc.path, tc.body, code, tc.want)
		}
	}
	// Without a planner, planning is not there.
	plain := server(t)
	if code := call(t, plain, "GET", "/api/crops", "", nil); code != http.StatusServiceUnavailable {
		t.Errorf("no planner: %d, want 503", code)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
