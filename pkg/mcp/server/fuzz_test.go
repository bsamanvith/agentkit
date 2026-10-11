package server_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/mcp/server"
)

// FuzzServeHTTP throws arbitrary paths and bodies at the handler: whatever a
// client sends, it must answer with a valid HTTP status and never panic.
func FuzzServeHTTP(f *testing.F) {
	f.Add("/mcp/a", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	f.Add("/mcp/a", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"}}}`)
	f.Add("/mcp/a", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":[1]}}`)
	f.Add("/mcp/b", `[{"jsonrpc":"2.0","id":1,"method":"initialize"}]`)
	f.Add("/mcp/", ``)
	f.Add("/mcp/%2e%2e/a", `null`)
	f.Add("/mcp/a/", `{`)

	srv, err := server.New(twoToolsets(f))
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, path, body string) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://localhost"+path, strings.NewReader(body))
		if err != nil {
			t.Skip()
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code < 200 || rec.Code > 599 {
			t.Fatalf("status %d for %q", rec.Code, path)
		}
	})
}
