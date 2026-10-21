package harness_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/policy"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// ran records which tools ran.
type ran struct {
	mu    sync.Mutex
	names []string
}

func (r *ran) add(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.names = append(r.names, name)
}

func (r *ran) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Sorted(slices.Values(r.names))
}

// classified builds a tool with no input and the given metadata, which
// records its calls in r.
func classified(t testing.TB, r *ran, name string, m tool.Metadata) tool.Tool {
	t.Helper()
	tl, err := tool.Func(name, "test tool "+name, func(context.Context, struct{}) (string, error) {
		r.add(name)
		return name + " done", nil
	}, tool.WithMetadata(m))
	if err != nil {
		t.Fatal(err)
	}
	return tl
}

// policyTools are a read, a low-risk write, a medium-risk write, a delete, an
// unclassified tool and a billing tool from toolset "pay".
func policyTools(t testing.TB, r *ran) tool.Registry {
	return newRegistry(t,
		classified(t, r, "lookup", tool.Metadata{Kind: tool.KindRead, Risk: tool.RiskLow}),
		classified(t, r, "note", tool.Metadata{Kind: tool.KindWrite, Risk: tool.RiskLow}),
		classified(t, r, "update", tool.Metadata{Kind: tool.KindWrite, Risk: tool.RiskMedium}),
		classified(t, r, "drop", tool.Metadata{Kind: tool.KindDelete, Risk: tool.RiskLow}),
		classified(t, r, "legacy", tool.Metadata{}),
		classified(t, r, "pay__charge", tool.Metadata{Kind: tool.KindWrite, Risk: tool.RiskLow, Tags: []string{"billing"}}),
	)
}

// decisions are the policy events of a run, by tool name.
func decisions(events []harness.Event) map[string][]harness.Event {
	out := map[string][]harness.Event{}
	for _, ev := range events {
		if ev.Kind == harness.EventToolPolicy || ev.Kind == harness.EventToolFlagged {
			out[ev.ToolUse.Name] = append(out[ev.ToolUse.Name], ev)
		}
	}
	return out
}

func toolsetOf(name string) string {
	set, _, ok := strings.Cut(name, "__")
	if !ok {
		return ""
	}
	return set
}

// A denied call reaches the model as an error result, does not run, and the
// run goes on to the model's next reply.
func TestToolPolicyDeniedCallIsAToolError(t *testing.T) {
	leakcheck(t)
	r := &ran{}
	fake := llmtest.NewFake(t,
		llmtest.ToolCalls(use("c1", "note", `{}`), use("c2", "lookup", `{}`), use("c3", "legacy", `{}`)),
		llmtest.Text("I could only look it up."),
	)
	h := newHarness(t, fake, policyTools(t, r), harness.WithToolPolicy(policy.MustPreset(policy.ReadOnly), nil))
	events, res, err := run(t, h, prompt("go"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Final.Text() != "I could only look it up." {
		t.Fatalf("the run did not go on: %+v", res.Final)
	}
	diff(t, "tools that ran", []string{"lookup"}, r.list())
	got := results(t, fake.Requests()[1].Messages[2])
	want := []llm.ToolResult{
		{ToolUseID: "c1", Content: "not allowed in this run: low-risk write tools are disabled (policy read_only)", IsError: true},
		{ToolUseID: "c2", Content: `"lookup done"`},
		// Unclassified: write/medium, so read_only blocks it.
		{ToolUseID: "c3", Content: "not allowed in this run: medium-risk write tools are disabled (policy read_only)", IsError: true},
	}
	diff(t, "results the model received", want, got)
	ds := decisions(events)
	if len(ds["lookup"]) != 0 {
		t.Errorf("a plain allow has events: %+v", ds["lookup"])
	}
	for _, name := range []string{"note", "legacy"} {
		if len(ds[name]) != 1 || ds[name][0].Decision.Action != policy.Deny || ds[name][0].Decision.Policy != policy.ReadOnly {
			t.Errorf("%s: policy events %+v, want one deny by read_only", name, ds[name])
		}
	}
}

// A flagged call runs, with a tool_policy and a tool_flagged event between
// its start and its result.
func TestToolPolicyFlaggedCallRuns(t *testing.T) {
	leakcheck(t)
	r := &ran{}
	fake := llmtest.NewFake(t, llmtest.ToolCalls(use("c1", "pay__charge", `{}`)), llmtest.Text("charged"))
	p := policy.Policy{Name: "agent", Rules: []policy.Rule{
		{Name: "watch_billing", Match: policy.Match{Tags: []string{"billing"}}, Action: policy.Flag},
	}, Default: policy.Allow}
	h := newHarness(t, fake, policyTools(t, r), harness.WithToolPolicy(p, toolsetOf))
	events, _, err := run(t, h, prompt("go"))
	if err != nil {
		t.Fatal(err)
	}
	diff(t, "tools that ran", []string{"pay__charge"}, r.list())
	var kinds []harness.EventKind
	for _, ev := range events {
		if ev.Turn == 1 && ev.Kind != harness.EventTextDelta && ev.Kind != harness.EventTurnDone {
			kinds = append(kinds, ev.Kind)
		}
	}
	want := []harness.EventKind{harness.EventToolStart, harness.EventToolPolicy, harness.EventToolFlagged, harness.EventToolDone}
	if !slices.Equal(kinds, want) {
		t.Fatalf("events %v, want %v", kinds, want)
	}
	d := decisions(events)["pay__charge"][1]
	if d.Decision.Action != policy.Flag || d.Decision.Rule != "watch_billing" || d.ToolUse.ID != "c1" || d.Call != 0 {
		t.Errorf("tool_flagged %+v", d)
	}
	if got := results(t, fake.Requests()[1].Messages[2]); got[0].IsError || got[0].Content != `"pay__charge done"` {
		t.Errorf("result %+v", got)
	}
}

// Every decision but a plain allow is in the events, with its rule and
// reason; approve is refused, saying so, in an autonomous run.
func TestToolPolicyEventsShowEveryDecision(t *testing.T) {
	leakcheck(t)
	r := &ran{}
	fake := llmtest.NewFake(t,
		llmtest.ToolCalls(use("c1", "lookup", `{}`), use("c2", "note", `{}`), use("c3", "update", `{}`),
			use("c4", "drop", `{}`), use("c5", "pay__charge", `{}`)),
		llmtest.Text("done"),
	)
	agent := policy.Policy{Name: "agent", Rules: []policy.Rule{
		{Name: "no_pay", Match: policy.Match{Toolsets: []string{"pay"}}, Action: policy.Deny, Reason: "payments are off"},
		{Name: "watch_notes", Match: policy.Match{Tools: []string{"note"}}, Action: policy.Flag},
	}, Default: policy.Allow}
	h := newHarness(t, fake, policyTools(t, r), harness.WithToolPolicy(agent, toolsetOf))
	in := prompt("go")
	env := policy.MustPreset(policy.LowRiskWrites)
	in.ToolPolicy = &env
	events, _, err := run(t, h, in)
	if err != nil {
		t.Fatal(err)
	}
	diff(t, "tools that ran", []string{"lookup", "note"}, r.list())
	type seen struct {
		Kind                 harness.EventKind
		Action               policy.Action
		Policy, Rule, Reason string
	}
	got := map[string][]seen{}
	for name, evs := range decisions(events) {
		for _, ev := range evs {
			got[name] = append(got[name], seen{ev.Kind, ev.Decision.Action, ev.Decision.Policy, ev.Decision.Rule, ev.Decision.Reason})
		}
	}
	want := map[string][]seen{
		"note": {
			{harness.EventToolPolicy, policy.Flag, "agent", "watch_notes", "low-risk write tools are allowed, and flagged for review (policy agent)"},
			{harness.EventToolFlagged, policy.Flag, "agent", "watch_notes", "low-risk write tools are allowed, and flagged for review (policy agent)"},
		},
		"update": {{harness.EventToolPolicy, policy.Approve, "low_risk_writes", "medium_risk_writes", "medium-risk write tools need a person's approval (policy low_risk_writes)"}},
		"drop":   {{harness.EventToolPolicy, policy.Deny, "low_risk_writes", "no_delete_send_execute", "low-risk delete tools are disabled (policy low_risk_writes)"}},
		// Both deny: the agent's, as the base, decides.
		"pay__charge": {{harness.EventToolPolicy, policy.Deny, "agent", "no_pay", "payments are off"}},
	}
	diff(t, "policy events", want, got)
	res := results(t, fake.Requests()[1].Messages[2])
	if !res[2].IsError || !strings.Contains(res[2].Content, "approval") || !strings.HasPrefix(res[2].Content, "not allowed in this run: ") {
		t.Errorf("an approve reached the model as %+v", res[2])
	}
	if res[4].Content != "not allowed in this run: payments are off" {
		t.Errorf("a rule's reason reached the model as %q", res[4].Content)
	}
}

// The input's policy narrows the harness's for that run only; with no
// harness policy it is the run's; an invalid one fails the run.
func TestToolPolicyInputNarrows(t *testing.T) {
	leakcheck(t)
	for _, c := range []struct {
		name    string
		opts    []harness.Option
		input   *policy.Policy
		allowed bool
	}{
		{"harness all, input read_only", []harness.Option{harness.WithToolPolicy(policy.MustPreset(policy.All), nil)}, ptr(policy.MustPreset(policy.ReadOnly)), false},
		{"no harness policy, input read_only", nil, ptr(policy.MustPreset(policy.ReadOnly)), false},
		{"harness read_only, input all", []harness.Option{harness.WithToolPolicy(policy.MustPreset(policy.ReadOnly), nil)}, ptr(policy.MustPreset(policy.All)), false},
		{"harness all, no input policy", []harness.Option{harness.WithToolPolicy(policy.MustPreset(policy.All), nil)}, nil, true},
		{"neither", nil, nil, true},
	} {
		r := &ran{}
		fake := llmtest.NewFake(t, llmtest.ToolCalls(use("c1", "note", `{}`)), llmtest.Text("ok"))
		h := newHarness(t, fake, policyTools(t, r), c.opts...)
		in := prompt("go")
		in.ToolPolicy = c.input
		if _, _, err := run(t, h, in); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := len(r.list()) == 1; got != c.allowed {
			t.Errorf("%s: the write ran: %v, want %v", c.name, got, c.allowed)
		}
	}

	h := newHarness(t, llmtest.NewFake(t), policyTools(t, &ran{}))
	in := prompt("go")
	in.ToolPolicy = &policy.Policy{Default: "sometimes"}
	if _, _, err := run(t, h, in); !errors.Is(err, harness.ErrInvalidInput) {
		t.Fatalf("an invalid input policy: %v, want ErrInvalidInput", err)
	}
	if _, err := harness.New(llmtest.NewFake(t), policyTools(t, &ran{}), harness.WithToolPolicy(policy.Policy{}, nil)); !errors.Is(err, harness.ErrInvalidConfig) {
		t.Fatalf("an invalid harness policy: %v, want ErrInvalidConfig", err)
	}
}

// The harness keeps its own copy of the policy it is given.
func TestToolPolicyIsCopied(t *testing.T) {
	leakcheck(t)
	r := &ran{}
	p := policy.MustPreset(policy.ReadOnly)
	fake := llmtest.NewFake(t, llmtest.ToolCalls(use("c1", "note", `{}`)), llmtest.Text("ok"))
	h := newHarness(t, fake, policyTools(t, r), harness.WithToolPolicy(p, nil))
	p.Rules[0].Match.Kinds[0] = tool.KindWrite
	if _, _, err := run(t, h, prompt("go")); err != nil {
		t.Fatal(err)
	}
	if len(r.list()) != 0 {
		t.Fatal("changing the policy after New changed the harness's")
	}
}

func ptr[T any](v T) *T { return &v }
