package main

// End-to-end test of the a2a component in agentkit serve (T067): the built
// binary serves the agents' Agent Cards and the A2A methods beside the
// agent API, with the API's credentials. The A2A behaviour itself is
// pkg/a2a's tests.

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/a2a"
)

func TestE2EServeA2A(t *testing.T) {
	t.Parallel()
	p := startServe(t, `{"agent":{"description":"Answers questions."}}`, 5*time.Second,
		envComponents+"=agent,a2a", envAPIToken+"="+e2eToken, envA2AURL+"=https://agents.example.com",
		"ANTHROPIC_API_KEY=unused-in-this-test")

	do := func(method, path, key, body string) (int, http.Header, string) {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), method, p.url+path, strings.NewReader(body))
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header, string(b)
	}

	for _, path := range []string{a2a.WellKnownPath, a2a.PathPrefix + "default/.well-known/agent-card.json"} {
		if code, h, _ := do(http.MethodGet, path, "", ""); code != http.StatusUnauthorized || !strings.HasPrefix(h.Get("WWW-Authenticate"), "Bearer") {
			t.Errorf("GET %s with no credentials = %d %q, want 401 Bearer", path, code, h.Get("WWW-Authenticate"))
		}
	}
	code, _, body := do(http.MethodGet, a2a.WellKnownPath, e2eToken, "")
	var card a2a.AgentCard
	if err := json.Unmarshal([]byte(body), &card); code != http.StatusOK || err != nil {
		t.Fatalf("GET %s = %d %v: %s", a2a.WellKnownPath, code, err, body)
	}
	if card.Name != "default" || card.Description != "Answers questions." || card.Version != e2eVersion ||
		len(card.SupportedInterfaces) == 0 || card.SupportedInterfaces[0].URL != "https://agents.example.com/a2a/default" {
		t.Errorf("card %+v", card)
	}
	if s := card.SecuritySchemes["bearer"]; s.HTTPAuth == nil || s.HTTPAuth.Scheme != "Bearer" {
		t.Errorf("security schemes %+v", card.SecuritySchemes)
	}

	code, _, body = do(http.MethodPost, a2a.PathPrefix+"default", e2eToken, `{"jsonrpc":"2.0","id":1,"method":"GetTask","params":{"id":"run_nope"}}`)
	var resp struct{ Error struct{ Code int } }
	if err := json.Unmarshal([]byte(body), &resp); code != http.StatusOK || err != nil || resp.Error.Code != a2a.ErrTaskNotFound.Code {
		t.Errorf("GetTask of no task = %d %s, want TaskNotFoundError", code, body)
	}
	// The agent API keeps its paths.
	if code, _, _ := do(http.MethodGet, "/v1/agents", e2eToken, ""); code != http.StatusOK {
		t.Errorf("GET /v1/agents = %d", code)
	}

	var ready struct {
		Status     string
		Components []struct{ Name, Status string }
	}
	code = p.getJSON(t, "/readyz", &ready)
	for _, name := range []string{a2a.Name, a2aDirectoryName} {
		if !slices.ContainsFunc(ready.Components, func(c struct{ Name, Status string }) bool { return c.Name == name && c.Status == "ready" }) {
			t.Errorf("/readyz = %d %+v, want %s ready", code, ready, name)
		}
	}
}
