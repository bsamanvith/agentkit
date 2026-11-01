package farmapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farm/farmtest"
	"github.com/bsamanvith/agentkit/apps/farm/farmapi"
	"github.com/bsamanvith/agentkit/apps/farm/land"
	"github.com/bsamanvith/agentkit/apps/farm/osm/osmtest"
	"github.com/bsamanvith/agentkit/apps/farm/research"
	"github.com/bsamanvith/agentkit/apps/farm/research/researchtest"
	"github.com/bsamanvith/agentkit/apps/farm/surveyor"
	credsqlite "github.com/bsamanvith/agentkit/pkg/credentials/sqlite"
)

func kothapetaBoundary() string {
	var cs []string
	for _, p := range osmtest.KothapetaPlot {
		cs = append(cs, fmt.Sprintf(`{"lat":%v,"lng":%v}`, p.Lat, p.Lng))
	}
	return `{"source":"map","corners":[` + strings.Join(cs, ",") + `]}`
}

func mapServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	svc, err := farm.NewService(&farmtest.Memory{}, farm.NewVerifier())
	if err != nil {
		t.Fatal(err)
	}
	s := surveyor.New(svc, surveyor.WithFeatures(osmtest.Source()))
	store, err := credsqlite.Open(context.Background(), filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	v, err := research.OpenVault(context.Background(), store, "", filepath.Join(t.TempDir(), "v.key"))
	if err != nil {
		t.Fatal(err)
	}
	fake := researchtest.New()
	t.Cleanup(fake.Close)
	r, err := research.New(context.Background(), v, research.WithBaseURLs(fake.URL, fake.URL))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(farmapi.New(svc, farmapi.WithSurveyor(s), farmapi.WithResearch(r)))
	t.Cleanup(srv.Close)
	var f farm.Farm
	if code := call(t, srv, "POST", "/api/farms", `{"name":"Paddy by the drain","boundary":`+kothapetaBoundary()+`}`, &f); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	return srv, f.ID
}

func TestMapRoutes(t *testing.T) {
	t.Parallel()
	srv, id := mapServer(t)
	var cfg map[string]any
	if call(t, srv, "GET", "/api/config", "", &cfg); cfg["surveyor"] != true || cfg["research"] != true {
		t.Errorf("config %v", cfg)
	}
	var sv land.Survey
	if code := call(t, srv, "GET", "/api/farms/"+id+"/survey", "", &sv); code != 200 || len(sv.Features) == 0 || len(sv.Nearest) == 0 {
		t.Fatalf("survey: %d", code)
	}
	if code := call(t, srv, "POST", "/api/farms/"+id+"/survey", "", &sv); code != 200 {
		t.Errorf("refresh: %d", code)
	}
	var f farm.Farm
	if call(t, srv, "GET", "/api/farms/"+id, "", &f); f.Survey == nil {
		t.Error("the survey is not on the farm")
	}
	var sug struct {
		Design land.Design       `json:"design"`
		Report land.DesignReport `json:"report"`
	}
	if code := call(t, srv, "POST", "/api/farms/"+id+"/design/suggest", `{"pond":true,"path":true,"border":true}`, &sug); code != 200 || sug.Report.Status != land.DesignSound {
		t.Fatalf("suggest: %d %+v", code, sug.Report)
	}
	d, _ := json.Marshal(sug.Design)
	var rep land.DesignReport
	if code := call(t, srv, "POST", "/api/farms/"+id+"/design/check", string(d), &rep); code != 200 || rep.Status != land.DesignSound {
		t.Errorf("check: %d %s", code, rep.Status)
	}
	if code := call(t, srv, "PUT", "/api/farms/"+id+"/design", string(d), &f); code != 200 || f.Design == nil {
		t.Errorf("save: %d", code)
	}
	var bad struct {
		Error        string             `json:"error"`
		DesignReport *land.DesignReport `json:"design_report"`
	}
	outside := `{"elements":[{"kind":"shed","ring":[{"lat":16.8,"lng":81.9},{"lat":16.8,"lng":81.91},{"lat":16.81,"lng":81.91}]}]}`
	if code := call(t, srv, "PUT", "/api/farms/"+id+"/design", outside, &bad); code != http.StatusUnprocessableEntity || bad.DesignReport == nil {
		t.Errorf("bad design: %d %+v", code, bad)
	}
	var cleared farm.Farm
	if code := call(t, srv, "DELETE", "/api/farms/"+id+"/design", "", &cleared); code != 200 || cleared.Design != nil {
		t.Errorf("clear: %d", code)
	}
	if code := call(t, srv, "POST", "/api/farms/"+id+"/design/suggest", `{}`, &bad); code != 400 {
		t.Errorf("nothing wanted: %d", code)
	}
	var loc surveyor.Located
	if code := call(t, srv, "GET", "/api/locate?lat=16.7064&lng=81.9008", "", &loc); code != 200 || len(loc.Plots) != 1 || !loc.Plots[0].Inside {
		t.Errorf("locate: %d %+v", code, loc)
	}
	if code := call(t, srv, "GET", "/api/locate?lat=x", "", &bad); code != 400 {
		t.Errorf("bad locate: %d", code)
	}
	if code := call(t, srv, "GET", "/api/places?q=Kothapeta", "", &bad); code != 503 {
		t.Errorf("places without a geocoder: %d", code)
	}
	if code := call(t, srv, "GET", "/api/farms/farm_nope/survey", "", &bad); code != 404 {
		t.Errorf("unknown farm: %d", code)
	}
}

func TestResearchRoutes(t *testing.T) {
	t.Parallel()
	srv, _ := mapServer(t)
	var st struct {
		Providers []research.Status `json:"providers"`
	}
	if code := call(t, srv, "GET", "/api/research", "", &st); code != 200 || len(st.Providers) != 2 || st.Providers[0].Connected {
		t.Fatalf("status: %d %+v", code, st)
	}
	if code := call(t, srv, "PUT", "/api/research/exa", `{"key":"exa-test-key-1"}`, &st); code != 200 || !st.Providers[0].Connected {
		t.Errorf("connect: %d %+v", code, st)
	}
	// The key never comes back.
	resp, err := http.Get(srv.URL + "/api/research")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(body), "exa-test-key-1") || !strings.Contains(string(body), `"connected":true`) {
		t.Errorf("status %s", body)
	}
	var bad struct{ Error string }
	if code := call(t, srv, "PUT", "/api/research/bing", `{"key":"abcdefghijk"}`, &bad); code != 404 {
		t.Errorf("unknown provider: %d", code)
	}
	if code := call(t, srv, "PUT", "/api/research/exa", `{"key":"x"}`, &bad); code != 400 {
		t.Errorf("short key: %d", code)
	}
	if code := call(t, srv, "DELETE", "/api/research/exa", "", &st); code != 200 || st.Providers[0].Connected {
		t.Errorf("disconnect: %d %+v", code, st)
	}
}

func TestMapRoutesNotConfigured(t *testing.T) {
	t.Parallel()
	svc, _ := farm.NewService(&farmtest.Memory{}, farm.NewVerifier())
	srv := httptest.NewServer(farmapi.New(svc))
	defer srv.Close()
	var bad struct{ Error string }
	for _, p := range []string{"/api/farms/x/survey", "/api/research", "/api/locate?lat=1&lng=1"} {
		if code := call(t, srv, "GET", p, "", &bad); code != 503 {
			t.Errorf("%s: %d", p, code)
		}
	}
}
