//go:build !noagui

package main

// End-to-end test of the agui component in agentkit serve (T069): the
// built binary serves the agents to AG-UI clients beside the agent API,
// with the API's credentials. The AG-UI behaviour itself is pkg/agui's
// tests.

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agui"
)

// aguiSays is what serve says of a bad agui setup: says, in a build with
// the component.
func aguiSays(says string) string { return says }

// TestAGUIName: serve's name of the component is the package's.
func TestAGUIName(t *testing.T) {
	if aguiName != agui.Name {
		t.Errorf("aguiName = %q, pkg/agui's Name %q", aguiName, agui.Name)
	}
}

func TestE2EServeAGUI(t *testing.T) {
	t.Parallel()
	p := startServe(t, `{"agent":{"description":"Answers questions."}}`, 5*time.Second,
		envComponents+"=agent,agui", envAPIToken+"="+e2eToken, "ANTHROPIC_API_KEY=unused-in-this-test")

	post := func(key, body string) (int, http.Header, string) {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, p.url+agui.PathPrefix+"default", strings.NewReader(body))
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header, string(b)
	}

	// A run whose last message is not the user's never reaches the model.
	const body = `{"threadId":"t1","runId":"r1","messages":[{"id":"a1","role":"assistant","content":"hi"}]}`
	if code, h, _ := post("", body); code != http.StatusUnauthorized || !strings.HasPrefix(h.Get("WWW-Authenticate"), "Bearer") {
		t.Errorf("POST with no credentials = %d %q, want 401 Bearer", code, h.Get("WWW-Authenticate"))
	}
	code, h, stream := post(e2eToken, body)
	if code != http.StatusOK || !strings.HasPrefix(h.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("POST = %d %q: %s", code, h.Get("Content-Type"), stream)
	}
	var evs []agui.Event
	for line := range strings.Lines(stream) {
		if data, ok := strings.CutPrefix(strings.TrimSpace(line), "data: "); ok {
			var e agui.Event
			if err := json.Unmarshal([]byte(data), &e); err != nil {
				t.Fatalf("event %s: %v", data, err)
			}
			evs = append(evs, e)
		}
	}
	if len(evs) != 2 || evs[0].Type != agui.EventRunStarted || evs[0].ThreadID != "t1" || evs[0].RunID != "r1" ||
		evs[1].Type != agui.EventRunError || evs[1].Code != "invalid_input" {
		t.Errorf("events %+v, want RUN_STARTED then RUN_ERROR invalid_input", evs)
	}
	// The agent API keeps its paths.
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, p.url+"/v1/agents", nil)
	req.Header.Set("Authorization", "Bearer "+e2eToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /v1/agents = %d", resp.StatusCode)
	}

	var ready struct {
		Status     string
		Components []struct{ Name, Status string }
	}
	code = p.getJSON(t, "/readyz", &ready)
	if !slices.ContainsFunc(ready.Components, func(c struct{ Name, Status string }) bool { return c.Name == agui.Name && c.Status == "ready" }) {
		t.Errorf("/readyz = %d %+v, want %s ready", code, ready, agui.Name)
	}
}
