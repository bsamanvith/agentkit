package farmagent_test

import (
	"context"
	"encoding/json"
	"math"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farm/farmtest"
	"github.com/bsamanvith/agentkit/apps/farm/farmagent"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/nominatim"
	"github.com/bsamanvith/agentkit/apps/farm/osm/osmtest"
	"github.com/bsamanvith/agentkit/apps/farm/surveyor"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	mcpclient "github.com/bsamanvith/agentkit/pkg/mcp/client"
	mcpserver "github.com/bsamanvith/agentkit/pkg/mcp/server"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

type places struct{}

func (places) Search(_ context.Context, q string, _ *geo.Point, _ string, _ int) ([]nominatim.Place, error) {
	return []nominatim.Place{{Name: q, Kind: "town", Point: geo.Point{Lat: 16.716755, Lng: 81.896565}, Address: q + ", Andhra Pradesh, India"}}, nil
}

// kothapeta returns a service with the recorded Kothapeta plot as farm_k
// and a surveyor over the recorded map.
func kothapeta(t *testing.T) (*farm.Service, *surveyor.Surveyor) {
	t.Helper()
	store := &farmtest.Memory{}
	f := farmtest.Sample("farm_k", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	f.Name, f.Plan, f.PlanReport = "Paddy by the drain", nil, nil
	f.Boundary.Corners = nil
	for _, p := range osmtest.KothapetaPlot {
		f.Boundary.Corners = append(f.Boundary.Corners, farm.Corner{Point: p})
	}
	// Measured afresh, where it really is, with no papers to compare.
	f.Declared, f.Profile = nil, nil
	f.Report = farm.NewVerifier().Verify(context.Background(), farm.Request{Boundary: f.Boundary})
	f.Report.Location = &farm.Location{Village: "Chinna Gollapalem", SubDistrict: "Kothapeta", District: "Dr. B.R. Ambedkar Konaseema", State: "Andhra Pradesh", Country: "India", CountryCode: "IN"}
	if err := store.Create(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	svc, err := farm.NewService(store, farm.NewVerifier())
	if err != nil {
		t.Fatal(err)
	}
	return svc, surveyor.New(svc, surveyor.WithFeatures(osmtest.Source()), surveyor.WithSearch(places{}, "IN"))
}

func mapRegistry(t *testing.T, s *surveyor.Surveyor) tool.Registry {
	t.Helper()
	tools, err := farmagent.MapTools(s)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tool.NewRegistry(tools)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestMapToolKinds(t *testing.T) {
	t.Parallel()
	_, s := kothapeta(t)
	defs, err := mapRegistry(t, s).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range defs {
		names = append(names, d.Name)
		want := tool.KindRead
		if d.Name == farmagent.SaveDesignTool {
			want = tool.KindWrite
		}
		if d.Kind != want {
			t.Errorf("%s: kind %s, want %s", d.Name, d.Kind, want)
		}
	}
	want := []string{
		farmagent.CheckDesignTool, farmagent.FindPlaceTool, farmagent.LocatePointTool, farmagent.MeasureTool,
		farmagent.NearbyTool, farmagent.SaveDesignTool, farmagent.SuggestDesignTool, farmagent.SurveyLandTool,
	}
	if !slices.Equal(names, want) {
		t.Errorf("tools %v, want %v", names, want)
	}
}

func TestMapToolsFlow(t *testing.T) {
	t.Parallel()
	svc, s := kothapeta(t)
	reg := mapRegistry(t, s)

	type nearby struct {
		Label     string  `json:"label"`
		Kind      string  `json:"kind"`
		DistanceM float64 `json:"distance_m"`
		Direction string  `json:"direction"`
	}
	sv := invoke[struct {
		Shape struct {
			Kind     string  `json:"kind"`
			AreaM2   float64 `json:"area_m2"`
			LongSide string  `json:"long_side"`
		} `json:"shape"`
		Nearest []nearby `json:"nearest"`
		Notes   []struct {
			ID string `json:"id"`
		} `json:"notes"`
		Sources []string `json:"sources"`
	}](t, reg, farmagent.SurveyLandTool, `{"farm":"farm_k"}`)
	if sv.Shape.Kind != "square" || math.Abs(sv.Shape.AreaM2-7596) > 5 || len(sv.Sources) != 1 || len(sv.Notes) == 0 {
		t.Errorf("survey %+v", sv)
	}
	if i := slices.IndexFunc(sv.Nearest, func(n nearby) bool { return n.Kind == "drain" }); i < 0 || sv.Nearest[i].Direction != "E" {
		t.Errorf("nearest %+v", sv.Nearest)
	}
	if f, _ := svc.Get(context.Background(), "farm_k"); f.Survey == nil {
		t.Error("survey_land did not save the survey")
	}

	near := invoke[struct {
		Features []nearby `json:"features"`
	}](t, reg, farmagent.NearbyTool, `{"farm":"farm_k","kinds":["canal","drain"],"within_m":1000}`)
	var kinds []string
	for _, f := range near.Features {
		kinds = append(kinds, f.Kind)
	}
	if !slices.Equal(kinds, []string{"drain", "canal"}) {
		t.Errorf("water within 1 km: %+v", near.Features)
	}
	// Asked for a kind without a distance: the nearest canal, past 400 m.
	canal := invoke[struct {
		Features []nearby `json:"features"`
	}](t, reg, farmagent.NearbyTool, `{"farm":"farm_k","kinds":["canal"]}`)
	if len(canal.Features) != 1 || canal.Features[0].Kind != "canal" {
		t.Errorf("the nearest canal: %+v", canal.Features)
	}
	// Without kinds the default reach is 400 m: no canal.
	all := invoke[struct {
		Features []nearby `json:"features"`
	}](t, reg, farmagent.NearbyTool, `{"farm":"farm_k","limit":50}`)
	for _, f := range all.Features {
		if f.Kind == "canal" {
			t.Errorf("a canal past 400 m in the default list: %+v", f)
		}
	}
	none := invoke[struct {
		Features []nearby `json:"features"`
		Note     string   `json:"note"`
	}](t, reg, farmagent.NearbyTool, `{"farm":"farm_k","kinds":["rail"]}`)
	if len(none.Features) != 0 || none.Note == "" {
		t.Errorf("no railway: %+v", none)
	}

	c := geo.Centroid(osmtest.KothapetaPlot)
	loc := invoke[surveyor.Located](t, reg, farmagent.LocatePointTool, `{"lat":`+jsonNum(c.Lat)+`,"lng":`+jsonNum(c.Lng)+`}`)
	if len(loc.Plots) != 1 || !loc.Plots[0].Inside {
		t.Errorf("locate %+v", loc)
	}

	m := invoke[struct {
		TotalM float64 `json:"total_m"`
		AreaM2 float64 `json:"area_m2"`
		Legs   []struct {
			Direction string `json:"direction"`
		} `json:"legs"`
	}](t, reg, farmagent.MeasureTool, `{"points":[{"lat":16.70677,"lng":81.900394},{"lat":16.70677,"lng":81.901177},{"lat":16.705948,"lng":81.901177},{"lat":16.705948,"lng":81.900394}],"closed":true}`)
	if math.Abs(m.AreaM2-7596) > 5 || len(m.Legs) != 4 || m.Legs[0].Direction != "E" || math.Abs(m.TotalM-349) > 1 {
		t.Errorf("measure %+v", m)
	}
	if _, err := reg.Invoke(context.Background(), farmagent.MeasureTool, json.RawMessage(`{"points":[{"lat":1,"lng":1}]}`)); err == nil {
		t.Error("measure of one point")
	}

	found := invoke[map[string][]surveyor.Found](t, reg, farmagent.FindPlaceTool, `{"query":"Kothapeta","farm":"farm_k"}`)
	if p := found["places"]; len(p) != 1 || p[0].Direction != "N" || p[0].DistanceM < 1000 {
		t.Errorf("find %+v", found)
	}

	type design struct {
		Design []struct {
			Kind string `json:"kind"`
			Why  string `json:"why"`
		} `json:"design"`
		Report struct {
			Status   string  `json:"status"`
			FieldsM2 float64 `json:"fields_m2"`
		} `json:"report"`
		Saved bool            `json:"saved"`
		Raw   json.RawMessage `json:"raw"`
	}
	sug := invoke[design](t, reg, farmagent.SuggestDesignTool, `{"farm":"farm_k","pond":true,"shed":true,"border":true}`)
	if len(sug.Design) != 4 || sug.Report.Status != "sound" || sug.Saved {
		t.Fatalf("suggest %+v", sug)
	}
	chk := invoke[design](t, reg, farmagent.CheckDesignTool, `{"farm":"farm_k","design":`+string(sug.Raw)+`}`)
	if chk.Report.Status != "sound" || chk.Report.FieldsM2 != sug.Report.FieldsM2 {
		t.Errorf("check of the suggestion %+v", chk.Report)
	}
	if _, err := reg.Invoke(context.Background(), farmagent.CheckDesignTool, json.RawMessage(`{"farm":"farm_k"}`)); err == nil || !strings.Contains(err.Error(), "no saved design") {
		t.Errorf("check with nothing saved: %v", err)
	}
	saved := invoke[design](t, reg, farmagent.SaveDesignTool, `{"farm":"farm_k","design":`+string(sug.Raw)+`}`)
	if !saved.Saved {
		t.Errorf("save %+v", saved)
	}
	again := invoke[design](t, reg, farmagent.CheckDesignTool, `{"farm":"farm_k"}`)
	if !again.Saved || again.Report.Status != "sound" {
		t.Errorf("check saved %+v", again)
	}
	bad := invoke[design](t, reg, farmagent.SaveDesignTool, `{"farm":"farm_k","design":{"elements":[{"kind":"pond","ring":[{"lat":16.8,"lng":81.9},{"lat":16.8,"lng":81.91},{"lat":16.81,"lng":81.91}]}]}}`)
	if bad.Saved || bad.Report.Status != "invalid" {
		t.Errorf("a pond outside was saved: %+v", bad)
	}
}

func jsonNum(f float64) string { b, _ := json.Marshal(f); return string(b) }

// The tools reach an agent elsewhere over MCP, as an agentkit agent's
// "mcp" toolset: listed, and called.
func TestToolsetsOverMCP(t *testing.T) {
	t.Parallel()
	svc, s := kothapeta(t)
	sets, err := farmagent.Toolsets(svc, farmagent.WithSurveyor(s))
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 2 || sets[farmagent.ToolsetFarm] == nil || sets[farmagent.ToolsetMap] == nil {
		t.Fatalf("toolsets %v", sets)
	}
	srv, err := mcpserver.New(sets, mcpserver.WithBasePath("/mcp"))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(func() {
		ts.CloseClientConnections()
		ts.Close()
	})
	catalog, err := toolset.NewCatalog(mcpclient.Factory())
	if err != nil {
		t.Fatal(err)
	}
	regs, problems, err := toolset.Build(t.Context(), catalog, []toolset.Spec{
		{Name: "farm-map", Type: mcpclient.Type, Settings: json.RawMessage(`{"url": "` + ts.URL + `/mcp/farm-map"}`)},
	})
	if err != nil || len(problems) > 0 {
		t.Fatal(err, problems)
	}
	reg := regs["farm-map"]
	defs, err := reg.List(t.Context())
	if err != nil || len(defs) != 8 {
		t.Fatalf("%d tools over MCP: %v", len(defs), err)
	}
	out, err := reg.Invoke(t.Context(), farmagent.SurveyLandTool, json.RawMessage(`{"farm":"farm_k"}`))
	if err != nil || !strings.Contains(string(out), `"kind":"drain"`) {
		t.Errorf("survey over MCP: %v %.300s", err, out)
	}
	prompt, err := farmagent.SystemPrompt(svc, farmagent.WithSurveyor(s))
	if err != nil || !strings.Contains(prompt, "survey_land") {
		t.Errorf("system prompt: %v", err)
	}
}

// The assistant with the map tools loads shape-farm, surveys, suggests.
func TestAssistantShapesTheFarm(t *testing.T) {
	t.Parallel()
	svc, s := kothapeta(t)
	skills, err := farmagent.Skills()
	if err != nil {
		t.Fatal(err)
	}
	model := llmtest.NewFake(t,
		llmtest.ToolCalls(llm.ToolUse{ID: "c1", Name: "skill_load", Input: json.RawMessage(`{"name":"shape-farm"}`)}),
		llmtest.ToolCalls(llm.ToolUse{ID: "c2", Name: farmagent.SurveyLandTool, Input: json.RawMessage(`{"farm":"farm_k"}`)}),
		llmtest.ToolCalls(llm.ToolUse{ID: "c3", Name: farmagent.SuggestDesignTool, Input: json.RawMessage(`{"farm":"farm_k","pond":true}`)}),
		llmtest.Text("Dig the pond in the east corner, by the drain."),
	)
	a, err := farmagent.NewAssistant(model, svc, farmagent.WithSurveyor(s), farmagent.WithSkills(skills))
	if err != nil {
		t.Fatal(err)
	}
	r, err := a.Ask(context.Background(), "", "Where should I dig a farm pond?")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.Tools, []string{"skill_load", farmagent.SurveyLandTool, farmagent.SuggestDesignTool}) {
		t.Errorf("tools %v", r.Tools)
	}
	req := model.Requests()[0]
	if !strings.Contains(req.System, "survey_land") || !strings.Contains(req.System, "shape-farm") {
		t.Error("the system prompt lacks the map part or the skill")
	}
}
