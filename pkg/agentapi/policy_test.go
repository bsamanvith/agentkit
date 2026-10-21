package agentapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/policy"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// policyRegistry has a low-risk write (note) and a read (look).
func policyRegistry(t *testing.T) tool.Registry {
	t.Helper()
	mk := func(name string, m tool.Metadata) tool.Tool {
		tl, err := tool.Func(name, "Test tool "+name+".", func(context.Context, struct{}) (string, error) { return name + " ran", nil }, tool.WithMetadata(m))
		if err != nil {
			t.Fatal(err)
		}
		return tl
	}
	reg, err := tool.NewRegistry([]tool.Tool{
		mk("note", tool.Metadata{Kind: tool.KindWrite, Risk: tool.RiskLow}),
		mk("look", tool.Metadata{Kind: tool.KindRead, Risk: tool.RiskLow}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// A run request's tool policy narrows the agent's: its decisions are in the
// run's events, streamed and stored, and the model is told why a call did
// not run.
func TestRunRequestToolPolicy(t *testing.T) {
	t.Parallel()
	m := script(t,
		llmtest.ToolCalls(llm.ToolUse{ID: "c1", Name: "note", Input: json.RawMessage(`{}`)}, llm.ToolUse{ID: "c2", Name: "look", Input: json.RawMessage(`{}`)}),
		llmtest.Text("looked only"),
	)
	agent := policy.Policy{Name: "agent", Rules: []policy.Rule{
		{Name: "watch_looks", Match: policy.Match{Tools: []string{"look"}}, Action: policy.Flag},
	}, Default: policy.Allow}
	r := newRig(t, m, rigConfig{tools: policyRegistry(t), harness: []harness.Option{harness.WithToolPolicy(agent, nil)}})

	s := r.open(r.post(t.Context(), `{"message":"go","tool_policy":"read_only"}`, ""))
	evs := s.all()
	if last := evs[len(evs)-1]; last.Type != agentapi.TypeRunDone || last.Answer != "looked only" {
		t.Fatalf("the run did not go on: %+v", last)
	}
	type seen struct{ Type, Name, Action, Policy, Rule, Kind string }
	var got []seen
	for _, e := range evs {
		if e.Type == agentapi.TypeToolPolicy || e.Type == agentapi.TypeToolFlagged {
			got = append(got, seen{e.Type, e.Name, e.Action, e.Policy, e.Rule, e.ToolKind})
		}
	}
	// The calls run in parallel: their events interleave in any order.
	slices.SortStableFunc(got, func(a, b seen) int { return strings.Compare(a.Name, b.Name) })
	want := []seen{
		{agentapi.TypeToolPolicy, "look", "flag", "agent", "watch_looks", "read"},
		{agentapi.TypeToolFlagged, "look", "flag", "agent", "watch_looks", "read"},
		{agentapi.TypeToolPolicy, "note", "deny", "read_only", "default", "write"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("policy events\n got %+v\nwant %+v", got, want)
	}
	results := map[string]agentapi.Event{}
	for _, e := range evs {
		if e.Type == agentapi.TypeToolResult {
			results[e.ToolUseID] = e
		}
	}
	if c1 := results["c1"]; !c1.IsError || c1.Content != "not allowed in this run: low-risk write tools are disabled (policy read_only)" {
		t.Errorf("the denied call's result: %+v", c1)
	}
	if c2 := results["c2"]; c2.IsError {
		t.Errorf("the flagged call's result: %+v", c2)
	}
	// Stored like any event: a client that reads them later sees them.
	if stored := r.events(s.runID(), ""); !sameRun(stored, evs) {
		t.Errorf("stored events differ from the stream:\n%v\n%v", types(stored), types(evs))
	}
	raw, _ := json.Marshal(evs[slices.IndexFunc(evs, func(e agentapi.Event) bool { return e.Type == agentapi.TypeToolPolicy && e.ToolUseID == "c1" })])
	for _, field := range []string{`"type":"tool_policy"`, `"action":"deny"`, `"policy":"read_only"`, `"rule":"default"`, `"tool_kind":"write"`, `"tool_risk":"low"`, `"tool_use_id":"c1"`, `"name":"note"`, `"call":0`} {
		if !strings.Contains(string(raw), field) {
			t.Errorf("tool_policy on the wire has no %s: %s", field, raw)
		}
	}
}

// A run request's own rules work too, named "run" in decisions.
func TestRunRequestToolPolicyRules(t *testing.T) {
	t.Parallel()
	m := script(t, llmtest.ToolCalls(llm.ToolUse{ID: "c1", Name: "look", Input: json.RawMessage(`{}`)}), llmtest.Text("ok"))
	r := newRig(t, m, rigConfig{tools: policyRegistry(t)})
	s := r.open(r.post(t.Context(), `{"message":"go","tool_policy":{"rules":[{"match":{"tools":["lo*"]},"action":"deny","reason":"no looking"}],"default":"allow"}}`, ""))
	var deny agentapi.Event
	for _, e := range s.all() {
		if e.Type == agentapi.TypeToolPolicy {
			deny = e
		}
	}
	if deny.Action != "deny" || deny.Policy != "run" || deny.Rule != "rules[0]" || deny.Reason != "no looking" {
		t.Fatalf("decision %+v", deny)
	}
}

// A resume cannot set a policy, even of a conversation it would otherwise
// report busy: the run keeps the policy it started with.
func TestRunRequestToolPolicyNotOnResume(t *testing.T) {
	t.Parallel()
	m := echo(t)
	m.gate = make(chan struct{})
	r := newRig(t, m, rigConfig{})
	busy := r.start(t.Context(), "busy", "")
	conv := busy.until(agentapi.TypeRunStart, 0).ConversationID
	resp := r.post(t.Context(), `{"message":"","conversation_id":"`+conv+`","tool_policy":"read_only"}`, "")
	code, e := errorOf(t, resp)
	if code != http.StatusBadRequest || e.Code != agentapi.CodeInvalidRequest {
		t.Errorf("a resume with a policy: %d %+v, want 400 invalid_request", code, e)
	}
	close(m.gate)
	busy.all()
}

func TestRunRequestToolPolicyRefused(t *testing.T) {
	t.Parallel()
	r := newRig(t, echo(t), rigConfig{tools: policyRegistry(t)})
	first := r.run("hi", "")
	for name, body := range map[string]string{
		"unknown preset":    `{"message":"go","tool_policy":"lenient"}`,
		"no default":        `{"message":"go","tool_policy":{"rules":[]}}`,
		"unknown field":     `{"message":"go","tool_policy":{"default":"deny","rule":[]}}`,
		"bad action":        `{"message":"go","tool_policy":{"default":"perhaps"}}`,
		"not a policy":      `{"message":"go","tool_policy":7}`,
		"resume with one":   `{"message":"","conversation_id":"` + first.conversationID() + `","tool_policy":"read_only"}`,
		"bad glob in rules": `{"message":"go","tool_policy":{"rules":[{"match":{"tools":["["]},"action":"deny"}],"default":"allow"}}`,
	} {
		resp := r.post(t.Context(), body, "")
		var e agentapi.ErrorResponse
		_ = json.NewDecoder(resp.Body).Decode(&e)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || e.Error == nil || e.Error.Code != agentapi.CodeInvalidRequest {
			t.Errorf("%s: %d %+v, want 400 invalid_request", name, resp.StatusCode, e.Error)
		}
	}
}
