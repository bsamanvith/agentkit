package policy_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/policy"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

const (
	A = policy.Allow
	F = policy.Flag
	P = policy.Approve
	D = policy.Deny
)

// presetTable is the documented decision of each preset by kind and risk
// (low, medium, high), for a tool that does not require approval. Tags never
// change a preset's decision.
var presetTable = map[string]map[tool.Kind][3]policy.Action{
	policy.ReadOnly: {
		tool.KindRead: {A, A, A}, tool.KindWrite: {D, D, D}, tool.KindDelete: {D, D, D},
		tool.KindSend: {D, D, D}, tool.KindExecute: {D, D, D},
	},
	policy.LowRiskWrites: {
		tool.KindRead: {A, A, A}, tool.KindWrite: {A, P, D}, tool.KindDelete: {D, D, D},
		tool.KindSend: {D, D, D}, tool.KindExecute: {D, D, D},
	},
	policy.ApproveWrites: {
		tool.KindRead: {A, A, A}, tool.KindWrite: {P, P, P}, tool.KindDelete: {P, P, P},
		tool.KindSend: {P, P, P}, tool.KindExecute: {P, P, P},
	},
	policy.All: {
		tool.KindRead: {A, A, A}, tool.KindWrite: {A, A, A}, tool.KindDelete: {A, A, A},
		tool.KindSend: {A, A, A}, tool.KindExecute: {A, A, A},
	},
}

func def(name string, k tool.Kind, r tool.Risk, tags ...string) tool.Definition {
	d := tool.Definition{Name: name}
	d.Kind, d.Risk, d.Tags = k, r, tags
	return d
}

func TestPresets(t *testing.T) {
	t.Parallel()
	if got := policy.Presets(); !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(maps.Keys(presetTable))) {
		t.Fatalf("Presets() = %v, want those of the table", got)
	}
	tagSets := [][]string{nil, {tool.TagExternal}, {tool.TagIdempotent, "pii"}}
	for _, name := range policy.Presets() {
		p, err := policy.Preset(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Validate(); err != nil {
			t.Fatalf("preset %s: %v", name, err)
		}
		for _, k := range tool.Kinds() {
			for ri, r := range tool.Risks() {
				for _, tags := range tagSets {
					for _, approval := range []bool{false, true} {
						d := def("t", k, r, tags...)
						d.RequiresApproval = approval
						want := presetTable[name][k][ri]
						if approval {
							want = policy.Stricter(want, policy.Approve)
						}
						got := p.Decide(d, "set")
						if got.Action != want {
							t.Errorf("%s: %s/%s tags %v approval %v: %s, want %s (%+v)", name, k, r, tags, approval, got.Action, want, got)
						}
						if got.Kind != k || got.Risk != r || got.Policy != name || got.Rule == "" || got.Reason == "" {
							t.Errorf("%s: %s/%s: decision %+v does not say why", name, k, r, got)
						}
					}
				}
			}
		}
	}
}

func TestUnclassifiedIsWriteMedium(t *testing.T) {
	t.Parallel()
	for _, d := range []tool.Definition{
		{Name: "bare"},
		def("risk_only", "", tool.RiskLow),            // write/low
		def("unknown_kind", "teleport", tool.RiskLow), // write/low
		def("kind_only", tool.KindWrite, ""),
	} {
		k, r := d.Class()
		for name, row := range presetTable {
			got := policy.MustPreset(name).Decide(d, "")
			want := row[k][slices.Index(tool.Risks(), r)]
			if got.Action != want || got.Kind != k || got.Risk != r {
				t.Errorf("%s: %s: %+v, want %s as %s/%s", name, d.Name, got, want, k, r)
			}
		}
	}
	got := policy.MustPreset(policy.ReadOnly).Decide(tool.Definition{Name: "bare"}, "")
	if got.Action != policy.Deny || got.Kind != tool.KindWrite || got.Risk != tool.RiskMedium {
		t.Fatalf("read_only on an unclassified tool: %+v, want deny as write/medium", got)
	}
	if !strings.Contains(got.Reason, "write tools are disabled") {
		t.Errorf("reason %q does not tell the model the call is not allowed", got.Reason)
	}
}

// permissive are agent policies that try every way to allow a call.
func permissive() []policy.Policy {
	return []policy.Policy{
		policy.MustPreset(policy.All),
		policy.MustPreset(policy.LowRiskWrites),
		{Name: "named", Rules: []policy.Rule{{Match: policy.Match{Tools: []string{"t", "set__t"}}, Action: policy.Allow}}, Default: policy.Allow},
		{Name: "flags", Default: policy.Flag},
		{Name: "everything", Rules: []policy.Rule{{Action: policy.Allow}}, Default: policy.Deny},
	}
}

func TestNarrowByReadOnly(t *testing.T) {
	t.Parallel()
	ro := policy.MustPreset(policy.ReadOnly)
	for _, agent := range permissive() {
		p := policy.Narrow(agent, ro)
		for _, k := range tool.Kinds() {
			for _, r := range tool.Risks() {
				for _, approval := range []bool{false, true} {
					d := def("t", k, r, tool.TagExternal)
					d.RequiresApproval = approval
					got := p.Decide(d, "set")
					if k != tool.KindRead && got.Action != policy.Deny {
						t.Errorf("%s narrowed by read_only: %s/%s approval %v: %+v, want deny", agent.Name, k, r, approval, got)
					}
					// The stricter decision, and on a tie the agent's.
					want, narrower := agent.Decide(d, "set"), ro.Decide(d, "set")
					if policy.Stricter(want.Action, narrower.Action) != want.Action {
						want = narrower
					}
					if got != want {
						t.Errorf("%s narrowed by read_only: %s/%s approval %v: %+v, want %+v", agent.Name, k, r, approval, got, want)
					}
				}
			}
		}
		// Narrowing the other way round is as strict.
		for _, k := range tool.Kinds() {
			d := def("t", k, tool.RiskLow)
			if k != tool.KindRead && policy.Narrow(ro, agent).Decide(d, "set").Action != policy.Deny {
				t.Errorf("read_only narrowed by %s allows %s", agent.Name, k)
			}
		}
	}
}

func TestNarrowSharesNothing(t *testing.T) {
	t.Parallel()
	a := policy.Policy{Name: "a", Rules: []policy.Rule{{Match: policy.Match{Kinds: []tool.Kind{tool.KindWrite}}, Action: policy.Allow}}, Default: policy.Deny}
	b := policy.MustPreset(policy.ApproveWrites)
	n := policy.Narrow(a, b)
	n.Rules[0].Match.Kinds[0] = tool.KindRead
	n.NarrowedBy[0].Rules[0].Action = policy.Deny
	if a.Rules[0].Match.Kinds[0] != tool.KindWrite || b.Rules[0].Action != policy.Allow {
		t.Fatalf("Narrow shares memory with its arguments: %+v %+v", a, b)
	}
	if len(a.NarrowedBy) != 0 {
		t.Fatalf("Narrow changed its base: %+v", a)
	}
	c := n.Clone()
	c.NarrowedBy[0].Rules[0].Match.Kinds[0] = tool.KindDelete
	if n.NarrowedBy[0].Rules[0].Match.Kinds[0] != tool.KindRead {
		t.Fatal("Clone shares a narrowing policy's rules")
	}
}

func TestRules(t *testing.T) {
	t.Parallel()
	p := policy.Policy{
		Name: "agent",
		Rules: []policy.Rule{
			{Name: "no_github_deletes", Match: policy.Match{Toolsets: []string{"git*"}, Kinds: []tool.Kind{tool.KindDelete}}, Action: policy.Deny, Reason: "deletes on GitHub are off"},
			{Match: policy.Match{Tags: []string{"billing", "pii"}}, Action: policy.Flag},
			{Match: policy.Match{Tools: []string{"send_*"}}, Action: policy.Approve},
			{Name: "trusted", Match: policy.Match{Tools: []string{"deploy"}}, Action: policy.Allow},
			{Match: policy.Match{Risks: []tool.Risk{tool.RiskHigh}}, Action: policy.Deny},
		},
		Default: policy.Allow,
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	approval := def("deploy", tool.KindExecute, tool.RiskHigh)
	approval.RequiresApproval = true
	other := def("other", tool.KindWrite, tool.RiskLow)
	other.RequiresApproval = true
	for _, c := range []struct {
		name    string
		def     tool.Definition
		toolset string
		action  policy.Action
		rule    string
		reason  string
	}{
		{"toolset glob and kind", def("github__drop", tool.KindDelete, tool.RiskLow), "github", D, "no_github_deletes", "deletes on GitHub are off"},
		{"toolset not known", def("github__drop", tool.KindDelete, tool.RiskLow), "", A, "default", ""},
		{"any tag", def("charge", tool.KindWrite, tool.RiskLow, "x", "pii"), "", F, "rules[1]", ""},
		{"tool glob, prefixed name", def("mail__send_email", tool.KindSend, tool.RiskLow), "mail", P, "rules[2]", ""},
		{"tool glob, own name only within its toolset", def("mail__send_email", tool.KindSend, tool.RiskLow), "", A, "default", ""},
		{"named tool overrides its approval", approval, "", A, "trusted", ""},
		{"requires approval otherwise", other, "", P, "default", "requires a person's approval"},
		{"risk", def("x", tool.KindWrite, tool.RiskHigh), "", D, "rules[4]", ""},
	} {
		got := p.Decide(c.def, c.toolset)
		if got.Action != c.action || got.Rule != c.rule || got.Policy != "agent" || !strings.Contains(got.Reason, c.reason) {
			t.Errorf("%s: %+v, want %s by %s (reason with %q)", c.name, got, c.action, c.rule, c.reason)
		}
	}
}

// Only a rule that names the tool exactly overrides its RequiresApproval: a
// wildcard that happens to match it does not.
func TestRequiresApprovalNeedsAnExactName(t *testing.T) {
	t.Parallel()
	d := def("deploy", tool.KindExecute, tool.RiskLow)
	d.RequiresApproval = true
	for glob, want := range map[string]policy.Action{"deploy": A, "dep*": P, "*": P, "set__deploy": A} {
		p := policy.Policy{Rules: []policy.Rule{{Match: policy.Match{Tools: []string{glob}}, Action: policy.Allow}}, Default: policy.Deny}
		d.Name = "set__deploy"
		if got := p.Decide(d, "set").Action; got != want {
			t.Errorf("a rule allowing %q: %s, want %s", glob, got, want)
		}
	}
}

// Which rule of low_risk_writes decides each class, as its table says.
func TestLowRiskWritesRules(t *testing.T) {
	t.Parallel()
	p := policy.MustPreset(policy.LowRiskWrites)
	for _, k := range tool.Kinds() {
		for _, r := range tool.Risks() {
			want := map[tool.Kind]string{tool.KindRead: "reads", tool.KindDelete: "no_delete_send_execute", tool.KindSend: "no_delete_send_execute", tool.KindExecute: "no_delete_send_execute"}[k]
			if k == tool.KindWrite {
				want = map[tool.Risk]string{tool.RiskLow: "low_risk_writes", tool.RiskMedium: "medium_risk_writes", tool.RiskHigh: "no_high_risk"}[r]
			}
			if got := p.Decide(def("t", k, r), "").Rule; got != want {
				t.Errorf("%s/%s decided by %q, want %q", k, r, got, want)
			}
		}
	}
}

func TestInvalidFailsClosed(t *testing.T) {
	t.Parallel()
	for _, p := range []policy.Policy{
		{},
		{Default: "maybe"},
		{Rules: []policy.Rule{{Action: "yes"}}, Default: policy.Allow},
	} {
		if err := p.Validate(); !errors.Is(err, policy.ErrInvalid) {
			t.Errorf("%+v: Validate = %v, want ErrInvalid", p, err)
		}
		if got := p.Decide(def("r", tool.KindRead, tool.RiskLow), ""); got.Action != policy.Deny {
			t.Errorf("%+v decides %s, want deny", p, got.Action)
		}
	}
	if policy.Stricter("odd", policy.Approve) != "odd" || policy.Stricter(policy.Allow, "odd") != "odd" {
		t.Error("an unknown action is not the strictest")
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	ok := policy.Policy{Default: policy.Deny}
	deep := ok
	for range 9 {
		deep = policy.Narrow(ok, deep)
	}
	tooMany := ok
	tooMany.Rules = make([]policy.Rule, 257)
	for i := range tooMany.Rules {
		tooMany.Rules[i].Action = policy.Allow
	}
	rule := func(r policy.Rule) policy.Policy {
		if r.Action == "" {
			r.Action = policy.Allow
		}
		return policy.Policy{Rules: []policy.Rule{r}, Default: policy.Deny}
	}
	for name, p := range map[string]policy.Policy{
		"no default":     {},
		"bad name":       {Name: "Agent", Default: policy.Deny},
		"bad rule name":  rule(policy.Rule{Name: "-x"}),
		"bad kind":       rule(policy.Rule{Match: policy.Match{Kinds: []tool.Kind{"teleport"}}}),
		"bad risk":       rule(policy.Rule{Match: policy.Match{Risks: []tool.Risk{"extreme"}}}),
		"bad tag":        rule(policy.Rule{Match: policy.Match{Tags: []string{"PII"}}}),
		"bad tool glob":  rule(policy.Rule{Match: policy.Match{Tools: []string{"a["}}}),
		"bad set glob":   rule(policy.Rule{Match: policy.Match{Toolsets: []string{"[x"}}}),
		"empty glob":     rule(policy.Rule{Match: policy.Match{Tools: []string{""}}}),
		"long glob":      rule(policy.Rule{Match: policy.Match{Tools: []string{strings.Repeat("a", 257)}}}),
		"two lines":      rule(policy.Rule{Reason: "a\nb"}),
		"bidi reason":    rule(policy.Rule{Reason: "a\u202eb"}),
		"long reason":    rule(policy.Rule{Reason: strings.Repeat("a", 301)}),
		"too many rules": tooMany,
		"too deep":       deep,
		"bad narrowing":  policy.Narrow(ok, policy.Policy{}),
	} {
		if err := p.Validate(); !errors.Is(err, policy.ErrInvalid) {
			t.Errorf("%s: Validate = %v, want ErrInvalid", name, err)
		}
	}
	good := rule(policy.Rule{Name: "r.1", Reason: "fine", Match: policy.Match{Tools: []string{"a*"}, Toolsets: []string{"s?"}, Tags: []string{"pii"}}})
	if err := policy.Narrow(good, policy.MustPreset(policy.ReadOnly)).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestJSON(t *testing.T) {
	t.Parallel()
	var p policy.Policy
	if err := json.Unmarshal([]byte(` "read_only" `), &p); err != nil {
		t.Fatal(err)
	}
	if p.Name != policy.ReadOnly || p.Decide(def("w", tool.KindWrite, tool.RiskLow), "").Action != policy.Deny {
		t.Fatalf("the preset's name read as %+v", p)
	}
	n := policy.Narrow(policy.Policy{
		Name:    "agent",
		Rules:   []policy.Rule{{Name: "r", Match: policy.Match{Kinds: []tool.Kind{tool.KindSend}, Tags: []string{"pii"}}, Action: policy.Flag, Reason: "why"}},
		Default: policy.Allow,
	}, policy.MustPreset(policy.LowRiskWrites))
	b, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	var back policy.Policy
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	b2, _ := json.Marshal(back)
	if string(b) != string(b2) {
		t.Fatalf("round trip:\n%s\n%s", b, b2)
	}
	if !strings.Contains(string(b), `"narrowed_by":[{"name":"low_risk_writes"`) {
		t.Errorf("JSON %s", b)
	}
	for _, bad := range []string{
		`"none"`, `{}`, `{"default":"allow","rule":[]}`, `{"default":"allow","rules":[{"action":"allow","match":{"kind":["read"]}}]}`,
		`{"default":"allow","narrowed_by":[{"default":"sometimes"}]}`, `{"default":"allow","narrowed_by":["no_such"]}`, `[1]`,
	} {
		var p policy.Policy
		if err := json.Unmarshal([]byte(bad), &p); !errors.Is(err, policy.ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", bad, err)
		}
	}
	var nested policy.Policy
	if err := json.Unmarshal([]byte(`{"default":"allow","narrowed_by":["read_only"]}`), &nested); err != nil {
		t.Fatal(err)
	}
	if nested.Decide(def("w", tool.KindWrite, tool.RiskLow), "").Action != policy.Deny {
		t.Error("a preset's name in narrowed_by is not that preset")
	}
}

func TestPresetsAreCopies(t *testing.T) {
	t.Parallel()
	p := policy.MustPreset(policy.ReadOnly)
	p.Rules[0].Match.Kinds[0] = tool.KindWrite
	if policy.MustPreset(policy.ReadOnly).Decide(def("w", tool.KindWrite, tool.RiskLow), "").Action != policy.Deny {
		t.Fatal("changing a preset changed the next one")
	}
	a := policy.Actions()
	a[0] = policy.Deny
	if policy.Actions()[0] != policy.Allow {
		t.Fatal("Actions hands out its list")
	}
	if _, err := policy.Preset("nope"); !errors.Is(err, policy.ErrInvalid) {
		t.Fatal(err)
	}
	defer func() {
		if recover() == nil {
			t.Error("MustPreset of an unknown name did not panic")
		}
	}()
	policy.MustPreset("nope")
}

func ExamplePolicy_Decide() {
	agent := policy.Policy{
		Name: "agent",
		Rules: []policy.Rule{
			{Match: policy.Match{Tags: []string{"billing"}}, Action: policy.Flag},
		},
		Default: policy.Allow,
	}
	// The run may only read.
	p := policy.Narrow(agent, policy.MustPreset(policy.ReadOnly))

	search := tool.Definition{Name: "web__search"}
	search.Kind, search.Risk, search.Tags = tool.KindRead, tool.RiskLow, []string{"billing"}
	create := tool.Definition{Name: "github__create_issue"}
	create.Kind, create.Risk = tool.KindWrite, tool.RiskLow

	for _, d := range []tool.Definition{search, create} {
		dec := p.Decide(d, "")
		fmt.Printf("%s: %s by %s/%s: %s\n", d.Name, dec.Action, dec.Policy, dec.Rule, dec.Reason)
	}
	// Output:
	// web__search: flag by agent/rules[0]: low-risk read tools are allowed, and flagged for review (policy agent)
	// github__create_issue: deny by read_only/default: low-risk write tools are disabled (policy read_only)
}
