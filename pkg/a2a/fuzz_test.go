package a2a_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/a2a"
)

// FuzzServe throws arbitrary methods, paths and bodies at the A2A handler,
// seeded with the spec's examples: whatever a client sends, it answers with
// a valid HTTP status, never panics, and a JSON-RPC answer is a JSON-RPC
// response.
func FuzzServe(f *testing.F) {
	for _, ex := range loadExamples(f) {
		method, path := http.MethodPost, a2a.PathPrefix+"default"+ex.Path
		switch ex.Binding {
		case "HTTP+JSON":
			method = ex.Method
		case "card":
			method, path = http.MethodGet, ex.Path
		}
		f.Add(method, path, string(ex.Request))
	}
	for _, s := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"GetTask","params":{"id":"run_1"}}`,
		`{"jsonrpc":"2.0","id":null,"method":"SendMessage","params":{}}`,
		`[{"jsonrpc":"2.0","id":1,"method":"GetTask"}]`,
		`{"jsonrpc":"2.0","id":{"x":1},"method":"ListTasks","params":{"pageToken":"!!"}}`,
		`{"message":{"role":"ROLE_USER","parts":[{"raw":"aGk="}]}}`,
		`{`, ``,
	} {
		f.Add(http.MethodPost, a2a.PathPrefix+"default", s)
	}
	f.Add(http.MethodGet, a2a.PathPrefix+"default/tasks?pageSize=-1&pageToken=%ff", "")
	f.Add(http.MethodPost, a2a.PathPrefix+"%2e%2e/tasks/x:cancel", "{}")

	r := newRigOn(f, shout{}) // the fake model reports to f, which a fuzz target may not use
	f.Fuzz(func(t *testing.T, method, path, body string) {
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		req, err := http.NewRequestWithContext(t.Context(), method, "http://localhost"+path, strings.NewReader(body))
		if err != nil {
			t.Skip()
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+keyA)
		rec := httptest.NewRecorder()
		r.srv.Handler().ServeHTTP(rec, req)
		if rec.Code < 200 || rec.Code > 599 {
			t.Fatalf("status %d for %s %q", rec.Code, method, path)
		}
		if method == http.MethodPost && path == a2a.PathPrefix+"default" && rec.Code == http.StatusOK &&
			!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
			var resp map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || string(resp["jsonrpc"]) != `"2.0"` {
				t.Fatalf("not a JSON-RPC response to %q: %s", body, rec.Body)
			}
			if _, ok := resp["result"]; ok == (resp["error"] != nil) {
				t.Fatalf("a response needs exactly one of result and error: %s", rec.Body)
			}
		}
	})
}
