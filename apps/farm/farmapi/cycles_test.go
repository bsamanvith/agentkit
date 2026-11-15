package farmapi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/agro/agrotest"
	"github.com/bsamanvith/agentkit/apps/farm/cycle"
	"github.com/bsamanvith/agentkit/apps/farm/cycle/cycletest"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farm/farmtest"
	"github.com/bsamanvith/agentkit/apps/farm/farmapi"
	"github.com/bsamanvith/agentkit/apps/farm/planner"
	"github.com/bsamanvith/agentkit/apps/farm/work"
	"github.com/bsamanvith/agentkit/apps/farm/work/worktest"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

// The cycle routes: read the line, accept a suggestion (saved as the
// plot's plan), set an own later cycle and clear it, type a sowing date,
// turn later cycles off, move on; bad requests 400, a plan with errors
// 422, an unknown plot 404, 503 without the service.
func TestCycleRoutes(t *testing.T) {
	t.Parallel()
	clk := clocktest.NewFake(time.Date(2026, 6, 1, 1, 0, 0, 0, time.UTC))
	svc, err := farm.NewService(&farmtest.Memory{}, farm.NewVerifier())
	if err != nil {
		t.Fatal(err)
	}
	lib, _ := agro.Default()
	p := planner.New(svc, lib, &agrotest.Source{Clim: agrotest.Hyderabad()}, planner.WithClock(clk))
	ws := work.New(p, worktest.NewMemory())
	cs := cycle.New(p, cycletest.NewMemory(), cycle.WithRoadmap(ws))
	srv := httptest.NewServer(farmapi.New(svc, farmapi.WithPlanner(p), farmapi.WithRoadmap(ws), farmapi.WithCycles(cs)))
	t.Cleanup(srv.Close)

	var cfg map[string]any
	if call(t, srv, "GET", "/api/config", "", &cfg); cfg["cycles"] != true {
		t.Errorf("config cycles = %v", cfg["cycles"])
	}
	var f farm.Farm
	if code := call(t, srv, "POST", "/api/farms", `{"name":"Plot","boundary":`+boundary+`}`, &f); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	base := "/api/farms/" + f.ID + "/cycles"
	var l cycle.Line
	if code := call(t, srv, "GET", base, "", &l); code != http.StatusOK || len(l.Cycles) != 3 || l.Cycles[0].Suggested == nil {
		t.Fatalf("line: %d, %+v", code, l)
	}
	if code := call(t, srv, "PUT", base+"/0", `{"accept":true}`, &l); code != http.StatusOK || l.Cycles[0].Own == nil {
		t.Fatalf("accept: %d, %+v", code, l.Cycles[0])
	}
	if code := call(t, srv, "GET", "/api/farms/"+f.ID+"/roadmap", "", nil); code != http.StatusOK {
		t.Errorf("roadmap after accepting: %d", code)
	}
	if code := call(t, srv, "PUT", base+"/1", `{"plan":{"zones":[{"crop":"chickpea","sow":"2026-11-01"}]},"method":"organic"}`, &l); code != http.StatusOK ||
		l.Cycles[1].Own == nil || l.Cycles[1].Own.Method != cycle.Organic {
		t.Fatalf("own cycle 2: %d, %+v", code, l.Cycles[1])
	}
	l = cycle.Line{} // decoding over the old line would keep its own cycle
	if code := call(t, srv, "DELETE", base+"/1", "", &l); code != http.StatusOK || l.Cycles[1].Own != nil {
		t.Errorf("clear: %d, %+v", code, l.Cycles[1])
	}
	if code := call(t, srv, "POST", base+"/sown", `{"day":"2026-06-01"}`, &l); code != http.StatusOK || l.Cycles[0].Planned.SowKind != cycle.Actual {
		t.Errorf("sown: %d, %+v", code, l.Cycles[0].Planned)
	}
	if code := call(t, srv, "PUT", base+"/settings", `{"future":false}`, &l); code != http.StatusOK || len(l.Cycles) != 1 {
		t.Errorf("future off: %d, %d cycles", code, len(l.Cycles))
	}
	if code := call(t, srv, "PUT", base+"/settings", `{"future":true}`, &l); code != http.StatusOK || len(l.Cycles) != 3 {
		t.Errorf("future on: %d, %d cycles", code, len(l.Cycles))
	}
	if code := call(t, srv, "POST", base+"/advance", "", &l); code != http.StatusOK || l.Cycles[0].Own == nil {
		t.Errorf("advance: %d, %+v", code, l.Cycles[0])
	}
	for name, c := range map[string]struct {
		method, path, body string
		want               int
	}{
		"position 3":     {"PUT", base + "/3", `{"accept":true}`, http.StatusBadRequest},
		"not a position": {"PUT", base + "/next", `{"accept":true}`, http.StatusBadRequest},
		"unknown field":  {"PUT", base + "/1", `{"colour":"red"}`, http.StatusBadRequest},
		"bad method":     {"PUT", base + "/1", `{"method":"natural"}`, http.StatusBadRequest},
		"future day":     {"POST", base + "/sown", `{"day":"2027-01-01"}`, http.StatusBadRequest},
		"plan errors":    {"PUT", base + "/1", `{"plan":{"zones":[{"crop":"maize","sow":"soon"}]}}`, http.StatusUnprocessableEntity},
		"unknown plot":   {"GET", "/api/farms/nope/cycles", "", http.StatusNotFound},
		"clear current":  {"DELETE", base + "/0", "", http.StatusBadRequest},
		"unknown crop":   {"PUT", base + "/2", `{"plan":{"zones":[{"crop":"kale","sow":"2027-08-01"}]}}`, http.StatusUnprocessableEntity},
	} {
		if code := call(t, srv, c.method, c.path, c.body, nil); code != c.want {
			t.Errorf("%s: %d; want %d", name, code, c.want)
		}
	}
	bare := server(t)
	if code := call(t, bare, "GET", "/api/farms/x/cycles", "", nil); code != http.StatusServiceUnavailable {
		t.Errorf("without the service: %d; want 503", code)
	}
}
