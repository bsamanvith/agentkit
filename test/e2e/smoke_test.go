//go:build e2e

// Package e2e is the smoke suite (T007): a few black-box flows against a
// running deployment, one per user-visible feature. It proves the pieces are
// wired together (the API, the console, the runner, the toolsets, the store);
// the details are proven by unit and integration tests. See README.md.
package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// target is the deployment under test, from the environment.
type target struct {
	url, token string
	client     *http.Client
}

func newTarget(t *testing.T) target {
	t.Helper()
	url := strings.TrimRight(os.Getenv("AGENTKIT_URL"), "/")
	if url == "" {
		t.Fatal("set AGENTKIT_URL to the agent API (make smoke and make k8s-smoke do)")
	}
	token := os.Getenv("AGENTKIT_API_TOKEN")
	if token == "" {
		t.Fatal("set AGENTKIT_API_TOKEN to the deployment's API token")
	}
	return target{url: url, token: token, client: &http.Client{Timeout: 30 * time.Second}}
}

// skipOrFail skips t for why, or fails it under AGENTKIT_SMOKE_STRICT=1,
// which scripts/smoke.sh (make smoke, CI's image job) sets: there every
// check is meant to run, and one that skips is a broken harness.
func skipOrFail(t *testing.T, why string) {
	t.Helper()
	if os.Getenv("AGENTKIT_SMOKE_STRICT") == "1" {
		t.Fatalf("AGENTKIT_SMOKE_STRICT=1, so this does not skip: %s", why)
	}
	t.Skip(why)
}

// do sends a request, with the token when auth is true, and returns the
// response; the caller closes its body.
func (tg target) do(t *testing.T, method, path, body string, auth bool) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, tg.url+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+tg.token)
	}
	resp, err := tg.client.Do(req)
	if err != nil {
		t.Fatalf("api: %s %s: %v (is the deployment up?)", method, path, err)
	}
	return resp
}

// getJSON GETs path with the token and decodes a 200 answer into v. A
// failure names who, the component the answer comes from.
func (tg target) getJSON(t *testing.T, who, path string, v any) {
	t.Helper()
	resp := tg.do(t, http.MethodGet, path, "", true)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: GET %s: %s: %s", who, path, resp.Status, b)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: GET %s: %v in %s", who, path, err, b)
	}
}

func TestHealth(t *testing.T) {
	tg := newTarget(t)
	for _, p := range []string{"/healthz", "/version"} {
		resp := tg.do(t, http.MethodGet, p, "", false)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("api: GET %s: %s", p, resp.Status)
		}
	}
	// Ready means every component of the API process is: the agent API, the
	// console, and the runner and toolsets it calls.
	var ready struct {
		Status     string `json:"status"`
		Components []struct{ Name, Status, Reason string }
	}
	resp := tg.do(t, http.MethodGet, "/readyz", "", false)
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&ready); err != nil {
		t.Fatalf("api: GET /readyz: %s: %v", resp.Status, err)
	}
	if ready.Status != "ready" {
		t.Errorf("api: not ready: %+v", ready)
	}
	// A component the API calls (the runner, the toolsets) that does not
	// answer leaves the API ready but degraded, naming it.
	for _, c := range ready.Components {
		if c.Status != "ready" {
			t.Errorf("%s: %s: %s", c.Name, c.Status, c.Reason)
		}
	}
}

func TestAuth(t *testing.T) {
	tg := newTarget(t)
	resp := tg.do(t, http.MethodGet, "/v1/agents", "", false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("api: GET /v1/agents with no token: %s, want 401", resp.Status)
	}
	var agents struct{ Agents []struct{ Name string } }
	tg.getJSON(t, "api", "/v1/agents", &agents)
	if len(agents.Agents) == 0 {
		t.Error("api: GET /v1/agents: no agents")
	}
}

func TestConsole(t *testing.T) {
	tg := newTarget(t)
	resp := tg.do(t, http.MethodGet, "/", "", false)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), `<div id="root"`) {
		t.Errorf("web: GET /: %s, %d bytes, want the console's page", resp.Status, len(b))
	}
}

// TestToolsets checks the API sees the toolsets component's tools.
func TestToolsets(t *testing.T) {
	tg := newTarget(t)
	var list struct {
		Toolsets []struct{ Name, Status string }
	}
	tg.getJSON(t, "toolsets", "/v1/toolsets", &list)
	if len(list.Toolsets) == 0 {
		t.Fatal("toolsets: GET /v1/toolsets: none")
	}
	for _, ts := range list.Toolsets {
		if ts.Status != "ready" {
			t.Errorf("toolsets: %s is %q", ts.Name, ts.Status)
		}
	}
}

// TestChat runs one chat turn end to end: the API takes the message, the
// runner calls the model and a tool on the toolsets, the store keeps the
// conversation. The message asks for the demo echo tool, which the fake model
// (and a real one) calls. AGENTKIT_SMOKE_CHAT=0 skips it, as it calls the
// model.
func TestChat(t *testing.T) {
	if os.Getenv("AGENTKIT_SMOKE_CHAT") == "0" {
		skipOrFail(t, "AGENTKIT_SMOKE_CHAT=0")
	}
	tg := newTarget(t)
	conv, events := tg.run(t, `{"message":"Please echo the word pineapple with the echo tool, then say what it returned."}`)
	if !slices.ContainsFunc(events, func(ev runEvent) bool { return ev.Type == "tool_result" }) {
		t.Errorf("runner: no tool was called (events: %v)", types(events))
	}

	// The store kept both sides of the exchange.
	var c struct{ Messages []json.RawMessage }
	tg.getJSON(t, "store", "/v1/conversations/"+conv, &c)
	if len(c.Messages) < 2 {
		t.Errorf("store: conversation %s has %d messages, want the prompt and the answer", conv, len(c.Messages))
	}
}

// TestKnowledge asks the support agent a question its knowledge base (the
// handbook toolset, pkg/knowledge, on the toolsets component) answers: the
// model searches it, and the answer has the handbook's figure. The fake model
// searches when a message says "handbook"; a real one is told to by the
// agent's prompt. AGENTKIT_SMOKE_CHAT=0 skips it, as it calls the model.
func TestKnowledge(t *testing.T) {
	if os.Getenv("AGENTKIT_SMOKE_CHAT") == "0" {
		skipOrFail(t, "AGENTKIT_SMOKE_CHAT=0")
	}
	tg := newTarget(t)
	_, events := tg.run(t, `{"agent":"support","message":"Using the handbook: what is the restocking fee for an opened e-bike?"}`)
	searched := false
	var answer strings.Builder
	for _, ev := range events {
		switch ev.Type {
		case "tool_result":
			if strings.HasSuffix(ev.Name, "knowledge_search") || strings.Contains(ev.Content, "returns.md") {
				searched = true
			}
		case "tool_call":
			if strings.HasSuffix(ev.Name, "knowledge_search") {
				searched = true
			}
		case "text_delta":
			answer.WriteString(ev.Text)
		}
	}
	if !searched {
		t.Errorf("runner: the agent did not search the handbook (events: %v)", types(events))
	}
	if !strings.Contains(answer.String(), "12%") {
		t.Errorf("toolsets: the answer does not give the handbook's 12%% fee: %q", answer.String())
	}
}

// TestToolSearch runs a chat turn with the chat's Search mode (T114): the
// agent's tools are held back behind tool_search, so the model finds the
// echo tool first and then calls it. The fake model does so on "findtool";
// a real one is asked to. AGENTKIT_SMOKE_CHAT=0 skips it.
func TestToolSearch(t *testing.T) {
	if os.Getenv("AGENTKIT_SMOKE_CHAT") == "0" {
		skipOrFail(t, "AGENTKIT_SMOKE_CHAT=0")
	}
	tg := newTarget(t)
	_, events := tg.run(t, `{"message":"findtool: use tool_search to find the tool that echoes text, then echo the word mango with it.","tools":{"search":"search"}}`)
	searched, echoed := false, false
	var called string // the tool of the last call: a result has no name
	for _, ev := range events {
		switch ev.Type {
		case "tool_call":
			called = ev.Name
			searched = searched || called == "tool_search"
		case "tool_result":
			echoed = echoed || strings.HasSuffix(called, "echo") && !ev.IsError
		}
	}
	if !searched || !echoed {
		t.Errorf("runner: searched %v, echoed %v, want both (events: %v)", searched, echoed, types(events))
	}
}

// runEvent is what the suite reads of a run's event.
type runEvent struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Text    string `json:"text"`
	Content string `json:"content"`
	IsError bool   `json:"is_error"`
}

func types(events []runEvent) []string {
	out := make([]string, len(events))
	for i, ev := range events {
		out[i] = ev.Type
	}
	return out
}

// run starts a run with body (POST /v1/runs), reads its stream to the end
// and returns its conversation, deleted when t ends, and its events. A run
// that fails, or a tool call that does, fails t.
func (tg target) run(t *testing.T, body string) (string, []runEvent) {
	t.Helper()
	tg.client.Timeout = 2 * time.Minute
	resp := tg.do(t, http.MethodPost, "/v1/runs", body, true)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("api: POST /v1/runs: %s: %s", resp.Status, b)
	}
	conv := resp.Header.Get("Agentkit-Conversation-Id")
	t.Cleanup(func() {
		req, err := http.NewRequest(http.MethodDelete, tg.url+"/v1/conversations/"+conv, nil)
		if err != nil {
			return
		}
		req.Header.Set("Authorization", "Bearer "+tg.token)
		if resp, err := tg.client.Do(req); err == nil {
			resp.Body.Close()
		}
	})

	var events []runEvent
	var last string
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var ev runEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			t.Fatalf("api: run event: %v in %s", err, data)
		}
		events, last = append(events, ev), data
		if ev.Type == "tool_result" && ev.IsError {
			t.Errorf("toolsets: a tool failed: %s", data)
		}
		if ev.Type == "run_done" || ev.Type == "run_error" {
			break
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("api: reading the run's stream: %v (events: %v)", err, types(events))
	}
	switch {
	case len(events) == 0:
		t.Fatal("api: the run's stream had no events")
	case events[len(events)-1].Type == "run_error":
		t.Fatalf("runner: the run failed: %s", last)
	case events[len(events)-1].Type != "run_done":
		t.Fatalf("api: the stream ended before the run did (events: %v)", types(events))
	}
	return conv, events
}

func TestMain(m *testing.M) {
	code := m.Run()
	if code != 0 {
		fmt.Fprintln(os.Stderr, "smoke: failed; each failure starts with the component it points at (api, web, runner, toolsets, store)")
	}
	os.Exit(code)
}
