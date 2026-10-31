package farmapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farm/farmtest"
	"github.com/bsamanvith/agentkit/apps/farm/farmagent"
	"github.com/bsamanvith/agentkit/apps/farm/farmapi"
)

// boundary is about one acre near Hyderabad.
const boundary = `{"source":"map","corners":[{"lat":17.385,"lng":78.4867},{"lat":17.385,"lng":78.48730},{"lat":17.38557,"lng":78.48730},{"lat":17.38557,"lng":78.4867}]}`

func server(t *testing.T, opts ...farmapi.Option) *httptest.Server {
	t.Helper()
	loc := &farmtest.Locator{Location: farm.Location{Village: "Nampally", State: "Telangana", Country: "India", CountryCode: "IN"}}
	svc, err := farm.NewService(&farmtest.Memory{}, farm.NewVerifier(farm.WithLocator(loc), farm.WithCountry("IN")))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(farmapi.New(svc, opts...))
	t.Cleanup(srv.Close)
	return srv
}

func call(t *testing.T, srv *httptest.Server, method, path, body string, out any) int {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: %v in %s", method, path, err, b)
		}
	}
	return resp.StatusCode
}

func TestFarmLifecycle(t *testing.T) {
	t.Parallel()
	srv := server(t)
	var created farm.Farm
	if code := call(t, srv, "POST", "/api/farms", `{"name":"East field","declared":{"value":1,"unit":"acre"},"boundary":`+boundary+`}`, &created); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	if created.ID == "" || created.Report.Status != farm.StatusVerified || created.Report.Location == nil {
		t.Errorf("created %+v", created)
	}
	var list struct{ Farms []farm.Farm }
	if code := call(t, srv, "GET", "/api/farms", "", &list); code != http.StatusOK || len(list.Farms) != 1 {
		t.Errorf("list: %d %+v", code, list)
	}
	var got farm.Farm
	if code := call(t, srv, "GET", "/api/farms/"+created.ID, "", &got); code != http.StatusOK || got.Name != "East field" {
		t.Errorf("get: %d %+v", code, got)
	}
	var updated farm.Farm
	if code := call(t, srv, "PUT", "/api/farms/"+created.ID, `{"name":"East field (north)","boundary":`+boundary+`}`, &updated); code != http.StatusOK || updated.Name != "East field (north)" {
		t.Errorf("update: %d %+v", code, updated)
	}
	var gj struct {
		Type     string
		Geometry struct {
			Type        string
			Coordinates [][][2]float64
		}
	}
	if code := call(t, srv, "GET", "/api/farms/"+created.ID+"/geojson", "", &gj); code != http.StatusOK ||
		gj.Type != "Feature" || gj.Geometry.Type != "Polygon" || len(gj.Geometry.Coordinates[0]) != 5 ||
		gj.Geometry.Coordinates[0][0] != [2]float64{78.4867, 17.385} {
		t.Errorf("geojson: %d %+v, want a closed ring of lng, lat", code, gj)
	}
	if code := call(t, srv, "DELETE", "/api/farms/"+created.ID, "", nil); code != http.StatusNoContent {
		t.Errorf("delete: %d", code)
	}
	if code := call(t, srv, "GET", "/api/farms/"+created.ID, "", nil); code != http.StatusNotFound {
		t.Errorf("get after delete: %d, want 404", code)
	}
}

func TestVerifySavesNothing(t *testing.T) {
	t.Parallel()
	srv := server(t)
	var rep farm.Report
	if code := call(t, srv, "POST", "/api/verify", `{"declared":{"value":1,"unit":"ha"},"boundary":`+boundary+`}`, &rep); code != http.StatusOK {
		t.Fatalf("verify: %d", code)
	}
	if rep.Status != farm.StatusNeedsReview || rep.Measures == nil {
		t.Errorf("verify of an acre declared as a hectare: %+v, want needs_review", rep)
	}
	var list struct{ Farms []farm.Farm }
	if call(t, srv, "GET", "/api/farms", "", &list); len(list.Farms) != 0 {
		t.Errorf("verify saved a farm: %+v", list)
	}
}

func TestErrors(t *testing.T) {
	t.Parallel()
	srv := server(t)
	tests := []struct {
		name, method, path, body string
		want                     int
	}{
		{"invalid boundary", "POST", "/api/farms", `{"name":"x","boundary":{"corners":[{"lat":17,"lng":78}]}}`, http.StatusUnprocessableEntity},
		{"no name", "POST", "/api/farms", `{"boundary":` + boundary + `}`, http.StatusBadRequest},
		{"unknown field", "POST", "/api/farms", `{"name":"x","boundary":` + boundary + `,"owner":"me"}`, http.StatusBadRequest},
		{"not JSON", "POST", "/api/verify", `corners please`, http.StatusBadRequest},
		{"two values", "POST", "/api/verify", `{"boundary":` + boundary + `} {}`, http.StatusBadRequest},
		{"too large", "POST", "/api/verify", `{"name":"` + strings.Repeat("a", farmapi.MaxBody) + `"}`, http.StatusBadRequest},
		{"missing farm", "GET", "/api/farms/farm_nope", ``, http.StatusNotFound},
		{"update missing farm", "PUT", "/api/farms/farm_nope", `{"name":"x","boundary":` + boundary + `}`, http.StatusNotFound},
		{"unknown route", "GET", "/api/nope", ``, http.StatusNotFound},
		{"no assistant", "POST", "/api/assistant", `{"message":"hi"}`, http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var body struct {
				Error  string
				Report *farm.Report
			}
			if code := call(t, srv, tt.method, tt.path, tt.body, &body); code != tt.want || body.Error == "" {
				t.Errorf("%d %+v, want %d with an error", code, body, tt.want)
			}
			if tt.want == http.StatusUnprocessableEntity && (body.Report == nil || body.Report.Status != farm.StatusInvalid) {
				t.Errorf("422 without the invalid report: %+v", body.Report)
			}
		})
	}
}

type fakeAssistant struct{}

func (fakeAssistant) Ask(_ context.Context, conv, msg string) (farmagent.Reply, error) {
	if conv == "conv_gone" {
		return farmagent.Reply{}, farmagent.ErrUnknownConversation
	}
	return farmagent.Reply{Conversation: "conv_1", Text: "you said " + msg, Tools: []string{}}, nil
}

func TestAssistant(t *testing.T) {
	t.Parallel()
	srv := server(t, farmapi.WithAssistant(fakeAssistant{}), farmapi.WithCountry("IN"))
	var cfg struct {
		Assistant bool
		Country   string
	}
	if call(t, srv, "GET", "/api/config", "", &cfg); !cfg.Assistant || cfg.Country != "IN" {
		t.Errorf("config = %+v", cfg)
	}
	var r farmagent.Reply
	if code := call(t, srv, "POST", "/api/assistant", `{"message":"hello"}`, &r); code != http.StatusOK || r.Text != "you said hello" {
		t.Errorf("ask: %d %+v", code, r)
	}
	if code := call(t, srv, "POST", "/api/assistant", `{"conversation":"conv_gone","message":"hello"}`, nil); code != http.StatusNotFound {
		t.Errorf("ask in a dropped conversation: %d, want 404", code)
	}
}
