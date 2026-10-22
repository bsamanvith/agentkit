package a2a_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/a2a"
)

// TestDotIDs: an agent name or a task ID that is "." or ".." names
// nothing. It is not looked up, where the agent API's router would clean
// it into another path (GET /v1/agents/. is the catalog, GET /v1/runs/.
// the list of runs).
func TestDotIDs(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var paths []string
	api := scriptedAPI(t, nil)
	srv, err := a2a.New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		api.ServeHTTP(w, r)
	}), a2a.WithKeepAlive(0))
	if err != nil {
		t.Fatal(err)
	}
	// The handler is called directly: a mux in front of it would clean the
	// path itself, and some routers do not.
	serve := func(method, p, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/", strings.NewReader(body))
		req.URL.Path = p
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}
	for _, id := range []string{".", ".."} {
		if rec := serve(http.MethodGet, a2a.PathPrefix+id+"/.well-known/agent-card.json", ""); rec.Code != http.StatusNotFound {
			t.Errorf("card of agent %q: %d %s, want 404", id, rec.Code, rec.Body)
		}
		for _, method := range []string{"GetTask", "CancelTask", "SubscribeToTask"} {
			rec := serve(http.MethodPost, a2a.PathPrefix+"x", `{"jsonrpc":"2.0","id":1,"method":"`+method+`","params":{"id":"`+id+`"}}`)
			var resp rpcResp
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || errCode(resp) != a2a.ErrTaskNotFound.Code {
				t.Errorf("%s of task %q: %s, want TaskNotFoundError", method, id, rec.Body)
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, p := range paths {
		if path.Clean(p) != p {
			t.Errorf("the agent API was asked for %s", p)
		}
	}
}
