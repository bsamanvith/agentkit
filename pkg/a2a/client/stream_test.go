package client_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/a2a"
	"github.com/bsamanvith/agentkit/pkg/a2a/client"
)

// TestMalformedResponses: a remote agent's answers that are not A2A are
// ErrProtocol, and its errors match the sentinels of package a2a.
func TestMalformedResponses(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		ct, body string
		stream   bool
		want     error
	}{
		"two fields in an event": {"text/event-stream", `data: {"jsonrpc":"2.0","id":1,"result":{"task":{"id":"t","status":{"state":"TASK_STATE_WORKING"}},"message":{"messageId":"m","role":"ROLE_AGENT","parts":[]}}}` + "\n\n", true, client.ErrProtocol},
		"empty event":            {"text/event-stream", "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n", true, client.ErrProtocol},
		"event not json":         {"text/event-stream", "data: hello\n\n", true, client.ErrProtocol},
		"huge event":             {"text/event-stream", "data: " + strings.Repeat("x", 2<<10) + "\n\n", true, client.ErrProtocol},
		"stream as html":         {"text/html", "<p>hi</p>", true, client.ErrProtocol},
		"stream error":           {"text/event-stream", `data: {"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":"gone"}}` + "\n\n", true, a2a.ErrTaskNotFound},
		"unary not json-rpc":     {"application/json", `{"task": {}}`, false, client.ErrProtocol},
		"unary neither":          {"application/json", `{"jsonrpc":"2.0","id":1}`, false, client.ErrProtocol},
		"unary error":            {"application/json", `{"jsonrpc":"2.0","id":1,"error":{"code":-32002,"message":"no"}}`, false, a2a.ErrTaskNotCancelable},
		"unary task without id":  {"application/json", `{"jsonrpc":"2.0","id":1,"result":{"status":{"state":"TASK_STATE_WORKING"}}}`, false, client.ErrProtocol},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var ts *httptest.Server
			ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					w.Header().Set("Content-Type", "application/json")
					c := cardAt(ts.URL + "/a2a")
					c.Capabilities.Streaming = true
					_ = json.NewEncoder(w).Encode(c)
					return
				}
				w.Header().Set("Content-Type", tc.ct)
				fmt.Fprint(w, tc.body)
			}))
			t.Cleanup(ts.Close)
			c, err := client.Connect(t.Context(), ts.URL+"/card", client.Config{HTTPClient: ts.Client(), MaxBodyBytes: 1 << 10})
			if err != nil {
				t.Fatal(err)
			}
			if tc.stream {
				var got error
				for _, err := range c.SubscribeToTask(t.Context(), "t") {
					if err != nil {
						got = err
						break
					}
				}
				if !errors.Is(got, tc.want) {
					t.Errorf("stream: %v, want %v", got, tc.want)
				}
				return
			}
			if _, err := c.GetTask(t.Context(), "t"); !errors.Is(err, tc.want) {
				t.Errorf("GetTask: %v, want %v", err, tc.want)
			}
		})
	}
}
