package agui_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agui"
)

// FuzzServe posts arbitrary bodies to the server, over the fake agent API
// with a run that calls a tool: every answer is an HTTP error with the agent
// API's error body, or an event stream that keeps the invariants (one
// RUN_STARTED first, one end last, each event of a type with its fields).
func FuzzServe(f *testing.F) {
	for _, s := range []string{
		`{"threadId":"t1","runId":"r1","messages":[{"id":"u1","role":"user","content":"hi"}]}`,
		`{"threadId":"t1","runId":"r1","messages":[{"id":"u1","role":"user","content":[{"type":"text","text":"a"},{"type":"binary","mimeType":"image/png","data":"aGk="}]}]}`,
		`{"threadId":"t1","runId":"r1","messages":[{"id":"run_9/t1","role":"assistant","content":"x"},{"id":"u2","role":"user","content":"more"}]}`,
		`{"threadId":"t1","runId":"r1","messages":[{"id":"t","role":"tool","toolCallId":"c","content":"x"}]}`,
		`{"threadId":"t1","runId":"r1","messages":[],"resume":[{"interruptId":"run_9/i/call_1","status":"resolved","payload":{"approved":true}}]}`,
		`{"threadId":"t1","runId":"r1","messages":[],"resume":[{"interruptId":"nope","status":"cancelled"}]}`,
		`{"threadId":"t1","runId":"r1","messages":[],"forwardedProps":{"a2uiAction":{"userAction":{"name":"go","surfaceId":"s","sourceComponentId":"b","context":{"x":1}}}}}`,
		`{"threadId":"t1","runId":"r1","messages":[],"forwardedProps":{"a2uiAction":{"userAction":{}}}}`,
		`{"threadId":"","runId":"","messages":null}`,
		`{"threadId":1}`, `[]`, `{`, ``,
	} {
		f.Add(s)
	}
	api := &fakeAPI{events: []wire.Event{
		{Type: wire.TypeRunStart},
		delta(1, "Let me check."),
		{Type: wire.TypeToolCall, Turn: 1, ToolUseID: "call_1", Name: "echo", Input: json.RawMessage(`{"text":"hi"}`)},
		{Type: wire.TypeToolResult, Turn: 1, ToolUseID: "call_1", Content: "hi"},
		turnDone(1, "Let me check."),
		runDone("done"),
	}}
	srv, err := agui.New(api, agui.WithKeepAlive(0))
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, body string) {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, agui.PathPrefix+"agent", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		done := make(chan struct{})
		go func() {
			defer close(done)
			srv.Handler().ServeHTTP(rec, req)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			buf := make([]byte, 1<<20)
			t.Fatalf("no answer in 10s to %q\n%s", body, buf[:runtime.Stack(buf, true)])
		}
		if rec.Code != http.StatusOK {
			var e wire.ErrorResponse
			if rec.Code < 400 || rec.Code > 599 || json.Unmarshal(rec.Body.Bytes(), &e) != nil || e.Error == nil || e.Error.Code == "" {
				t.Fatalf("status %d, body %s, for %q", rec.Code, rec.Body, body)
			}
			return
		}
		if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
			t.Fatalf("a 200 that is not an event stream: %s", rec.Body)
		}
		// The input as the server read it (encoding/json matches field
		// names without regard to case).
		var parsed agui.RunAgentInput
		if err := json.Unmarshal([]byte(body), &parsed); err != nil {
			t.Fatalf("a stream for a body that is not a RunAgentInput: %q", body)
		}
		var in map[string]any
		b, _ := json.Marshal(parsed)
		_ = json.Unmarshal(b, &in)
		raw := readSSE(t, rec.Body)
		checkStream(t, "stream", recorded{in: in, events: decode(t, raw), raw: raw})
	})
}
