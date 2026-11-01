package farmapi_test

import (
	"net/http"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/planting"
)

func TestGroupRoutes(t *testing.T) {
	t.Parallel()
	srv, id := planningServer(t, nil)
	var g farm.Group
	if code := call(t, srv, "POST", "/api/groups", `{"name":"Home farm","plots":["`+id+`"]}`, &g); code != http.StatusCreated || g.ID == "" {
		t.Fatalf("create group: %d %+v", code, g)
	}
	if code := call(t, srv, "POST", "/api/groups", `{"name":"Twice","plots":["`+id+`"]}`, nil); code != http.StatusBadRequest {
		t.Errorf("a plot in two groups: %d, want 400", code)
	}
	var list struct{ Groups []farm.Group }
	if code := call(t, srv, "GET", "/api/groups", "", &list); code != 200 || len(list.Groups) != 1 {
		t.Errorf("list: %d %+v", code, list)
	}
	if code := call(t, srv, "GET", "/api/groups/"+g.ID+"/distribution", "", nil); code != http.StatusBadRequest {
		t.Errorf("no distribution yet: %d, want 400", code)
	}
	body := `{"plants":[{"id":"trees","name":"Mango border","crops":["mango"],"place":"border"},{"id":"veg","name":"Vegetables","crops":["tomato","onion"],"share":2},{"id":"pulse","name":"Pulses","crops":["green_gram"]}],"rows":"north_south"}`
	var res planting.Result
	if code := call(t, srv, "POST", "/api/groups/"+g.ID+"/distribution/preview", body, &res); code != 200 || len(res.Plots) != 1 || len(res.Plants) != 3 {
		t.Fatalf("preview: %d %+v", code, res)
	}
	if code := call(t, srv, "GET", "/api/groups/"+g.ID, "", &g); code != 200 || g.Distribution != nil {
		t.Errorf("a preview saved: %+v", g.Distribution)
	}
	var saved struct {
		Group  farm.Group
		Result planting.Result
	}
	if code := call(t, srv, "PUT", "/api/groups/"+g.ID+"/distribution", body, &saved); code != 200 || saved.Group.Distribution == nil || saved.Result.AreaM2 == 0 {
		t.Fatalf("save: %d %+v", code, saved)
	}
	if code := call(t, srv, "GET", "/api/groups/"+g.ID+"/distribution", "", &res); code != 200 || res.AreaM2 != saved.Result.AreaM2 {
		t.Errorf("saved distribution: %d %+v", code, res)
	}
	if code := call(t, srv, "PUT", "/api/groups/"+g.ID+"/distribution", `{"plants":[{"id":"x","crops":["nope"]}]}`, nil); code != http.StatusBadRequest {
		t.Errorf("unknown crop: %d, want 400", code)
	}
	if code := call(t, srv, "PUT", "/api/groups/"+g.ID, `{"name":"Home","plots":[]}`, &g); code != 200 || g.Name != "Home" || len(g.Plots) != 0 {
		t.Errorf("update: %d %+v", code, g)
	}
	var cleared farm.Group
	if code := call(t, srv, "DELETE", "/api/groups/"+g.ID+"/distribution", "", &cleared); code != 200 || cleared.Distribution != nil || cleared.ID != g.ID {
		t.Errorf("clear: %d %+v", code, cleared)
	}
	if code := call(t, srv, "DELETE", "/api/groups/"+g.ID, "", nil); code != http.StatusNoContent {
		t.Errorf("delete: %d", code)
	}
	if code := call(t, srv, "GET", "/api/groups/"+g.ID, "", nil); code != http.StatusNotFound {
		t.Errorf("deleted: %d, want 404", code)
	}
}
