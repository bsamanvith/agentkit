package a2a_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/a2a"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
)

// scriptedAPI is an agent API with one agent, "x", and one running run,
// "run_1", whose event stream is events.
func scriptedAPI(t *testing.T, events []wire.Event) http.Handler {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /v1/agents/x", func(w http.ResponseWriter, _ *http.Request) {
		reply(w, wire.Agent{V: 1, Name: "x"})
	})
	mux.HandleFunc("GET /v1/runs/run_1", func(w http.ResponseWriter, _ *http.Request) {
		reply(w, wire.Run{V: 1, ID: "run_1", ConversationID: "conv_1", Agent: "x", Status: wire.StatusRunning, CreatedAt: at})
	})
	mux.HandleFunc("GET /v1/runs/run_1/events", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range events {
			e.V, e.RunID, e.At = 1, "run_1", at
			b, err := json.Marshal(e)
			if err != nil {
				t.Error(err)
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
	})
	return mux
}

// TestFollowRetriedTurn: the text of a turn the run drops and does again
// (retrying, rewind) replaces what was streamed of it, rather than being
// appended to it.
func TestFollowRetriedTurn(t *testing.T) {
	t.Parallel()
	for _, drop := range []wire.Event{{Type: wire.TypeRetrying, Attempt: 2}, {Type: wire.TypeRewind, Turns: 0}} {
		t.Run(drop.Type, func(t *testing.T) {
			t.Parallel()
			srv, err := a2a.New(scriptedAPI(t, []wire.Event{
				{Type: wire.TypeRunStart, ConversationID: "conv_1"},
				{Type: wire.TypeTextDelta, Turn: 0, Text: "Hel"},
				drop,
				{Type: wire.TypeTextDelta, Turn: 0, Text: "Hello"},
				{Type: wire.TypeTextDelta, Turn: 0, Text: " there"},
				{Type: wire.TypeRunDone, Answer: "Hello there"},
			}), a2a.WithKeepAlive(0))
			if err != nil {
				t.Fatal(err)
			}
			ts := httptest.NewServer(srv.Handler())
			defer ts.Close()
			resp, err := ts.Client().Get(ts.URL + "/a2a/x/tasks/run_1:subscribe")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var text string
			var appends []bool
			sc := bufio.NewScanner(resp.Body)
			for sc.Scan() {
				d, ok := strings.CutPrefix(sc.Text(), "data: ")
				if !ok {
					continue
				}
				var sr a2a.StreamResponse
				if err := json.Unmarshal([]byte(d), &sr); err != nil {
					t.Fatalf("%v: %s", err, d)
				}
				if up := sr.ArtifactUpdate; up != nil && !up.LastChunk {
					if !up.Append {
						text = ""
					}
					text += *up.Artifact.Parts[0].Text
					appends = append(appends, up.Append)
				}
			}
			if text != "Hello there" || fmt.Sprint(appends) != "[false false true]" {
				t.Errorf("streamed answer %q (append %v), want \"Hello there\" (append [false false true])", text, appends)
			}
			if err := srv.Shutdown(t.Context()); err != nil {
				t.Error(err)
			}
		})
	}
}
