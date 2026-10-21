package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/pkg/tool"
)

// ErrInvalid reports a policy that breaks the rules of [Policy.Validate], or
// JSON that is not a policy.
var ErrInvalid = errors.New("invalid tool policy")

// Action is what a policy does with a tool call.
type Action string

// The actions, from the least strict to the strictest.
const (
	// Allow lets the call run.
	Allow Action = "allow"
	// Flag lets the call run, and the run records that it was flagged.
	Flag Action = "flag"
	// Approve holds the call until a person approves it. Until approvals
	// exist (T084), the harness refuses such a call, saying why.
	Approve Action = "approve"
	// Deny refuses the call: the model gets an error saying why.
	Deny Action = "deny"
)

var actions = []Action{Allow, Flag, Approve, Deny}

// Actions returns every action, from the least strict to the strictest.
func Actions() []Action { return slices.Clone(actions) }

// rank orders actions by strictness. An action that is not one of the four
// ranks as Deny, so a policy that skipped validation fails closed.
func (a Action) rank() int {
	if i := slices.Index(actions, a); i >= 0 {
		return i
	}
	return len(actions) - 1
}

// Stricter returns the stricter of a and b: Deny over Approve over Flag over
// Allow. An action that is not one of them counts as Deny.
func Stricter(a, b Action) Action {
	if b.rank() > a.rank() {
		return b
	}
	return a
}

// Match says which tool calls a rule applies to. Each field that is set must
// match; an empty field matches every call, so the zero Match matches all.
type Match struct {
	// Kinds matches a tool whose kind ([tool.Metadata.Class]) is one of
	// them.
	Kinds []tool.Kind `json:"kinds,omitempty"`
	// Risks matches a tool whose risk ([tool.Metadata.Class]) is one of
	// them.
	Risks []tool.Risk `json:"risks,omitempty"`
	// Tags matches a tool that has at least one of them.
	Tags []string `json:"tags,omitempty"`
	// Tools are globs ([path.Match]) over the tool's name, as the model
	// sees it ("github__create_issue") or within its toolset
	// ("create_issue").
	Tools []string `json:"tools,omitempty"`
	// Toolsets are globs ([path.Match]) over the name of the tool's
	// toolset. A tool whose toolset is not known matches none.
	Toolsets []string `json:"toolsets,omitempty"`
}

// Rule is one entry of a policy: the calls it matches and what to do with
// them.
type Rule struct {
	// Name names the rule in decisions; empty, the rule is "rules[i]" by its
	// position.
	Name   string `json:"name,omitempty"`
	Match  Match  `json:"match"`
	Action Action `json:"action"`
	// Reason is what the model and the run's events are told when the rule
	// decides; empty, a reason is made from the action and the tool's
	// class.
	Reason string `json:"reason,omitempty"`
}

// Policy decides tool calls. Its zero value is not valid: a policy has a
// Default. Build one from rules, or with [Preset] and [Narrow].
type Policy struct {
	// Name names the policy in decisions, such as a preset's name, "agent"
	// or "run".
	Name string `json:"name,omitempty"`
	// Rules are tried in order; the first that matches decides.
	Rules []Rule `json:"rules,omitempty"`
	// Default decides a call no rule matches.
	Default Action `json:"default"`
	// NarrowedBy are policies that narrow this one: a call gets the
	// strictest decision of this policy and each of them. [Narrow] sets it.
	NarrowedBy []Policy `json:"narrowed_by,omitempty"`
}

// Decision is what a policy decided about a call, and why.
type Decision struct {
	Action Action `json:"action"`
	// Policy is the name of the policy that decided, and Rule the rule:
	// its name, "rules[i]", or "default".
	Policy string `json:"policy,omitempty"`
	Rule   string `json:"rule"`
	// Reason says why, for the run's events and, for a call that does not
	// run, the model ("not allowed in this run: " and the reason).
	Reason string `json:"reason"`
	// Kind and Risk are the tool's class, as the policy saw it.
	Kind tool.Kind `json:"kind"`
	Risk tool.Risk `json:"risk"`
}

// Limits on a policy, so that one from a run request stays cheap to check.
const (
	maxRules  = 256
	maxDepth  = 8 // of NarrowedBy
	maxGlob   = 256
	maxReason = 300
)

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

// Validate checks p: the default and every rule's action are actions; the
// kinds, risks and tags are ones a tool can declare; the globs are valid
// [path.Match] patterns of at most 256 bytes; names match
// ^[a-z0-9][a-z0-9_.-]{0,63}$; a reason is one line of at most 300
// characters; there are at most 256 rules; and the policies that narrow it,
// at most 8 deep, are valid too. Errors wrap [ErrInvalid].
func (p Policy) Validate() error {
	if err := p.validate(0); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return nil
}

func (p Policy) validate(depth int) error {
	if depth > maxDepth {
		return fmt.Errorf("narrowed more than %d deep", maxDepth)
	}
	if p.Name != "" && !namePattern.MatchString(p.Name) {
		return fmt.Errorf("name %q must match %s", p.Name, namePattern)
	}
	if !slices.Contains(actions, p.Default) {
		return fmt.Errorf("default %q must be one of %v", p.Default, actions)
	}
	if len(p.Rules) > maxRules {
		return fmt.Errorf("%d rules, more than %d", len(p.Rules), maxRules)
	}
	for i, r := range p.Rules {
		if err := r.validate(); err != nil {
			return fmt.Errorf("rules[%d]: %w", i, err)
		}
	}
	for i, n := range p.NarrowedBy {
		if err := n.validate(depth + 1); err != nil {
			return fmt.Errorf("narrowed_by[%d]: %w", i, err)
		}
	}
	return nil
}

func (r Rule) validate() error {
	if r.Name != "" && !namePattern.MatchString(r.Name) {
		return fmt.Errorf("name %q must match %s", r.Name, namePattern)
	}
	if !slices.Contains(actions, r.Action) {
		return fmt.Errorf("action %q must be one of %v", r.Action, actions)
	}
	if err := reasonText(r.Reason); err != nil {
		return err
	}
	m := r.Match
	for _, k := range m.Kinds {
		if !slices.Contains(tool.Kinds(), k) {
			return fmt.Errorf("kind %q must be one of %v", k, tool.Kinds())
		}
	}
	for _, k := range m.Risks {
		if !slices.Contains(tool.Risks(), k) {
			return fmt.Errorf("risk %q must be one of %v", k, tool.Risks())
		}
	}
	for _, t := range m.Tags {
		if !namePattern.MatchString(t) {
			return fmt.Errorf("tag %q must match %s", t, namePattern)
		}
	}
	for _, g := range slices.Concat(m.Tools, m.Toolsets) {
		if err := glob(g); err != nil {
			return err
		}
	}
	return nil
}

func glob(g string) error {
	if g == "" || len(g) > maxGlob {
		return fmt.Errorf("glob %q must have 1 to %d bytes", g, maxGlob)
	}
	if _, err := path.Match(g, ""); err != nil {
		return fmt.Errorf("glob %q: %w", g, err)
	}
	return nil
}

func reasonText(s string) error {
	switch {
	case !utf8.ValidString(s):
		return fmt.Errorf("reason %q is not valid UTF-8", s)
	case strings.IndexFunc(s, func(r rune) bool {
		return unicode.IsControl(r) || unicode.In(r, unicode.Zl, unicode.Zp, unicode.Bidi_Control)
	}) >= 0:
		return fmt.Errorf("reason %q has a control character", s)
	case utf8.RuneCountInString(s) > maxReason:
		return fmt.Errorf("reason is longer than %d characters", maxReason)
	}
	return nil
}

// Narrow returns base narrowed by by: for every call, the stricter of their
// two decisions (on a tie, base's). It is never more permissive than either.
// Neither argument is changed, and the result shares no memory with them.
func Narrow(base, by Policy) Policy {
	out := base.Clone()
	out.NarrowedBy = append(out.NarrowedBy, by.Clone())
	return out
}

// Clone returns a deep copy of p.
func (p Policy) Clone() Policy {
	out := p
	out.Rules = make([]Rule, len(p.Rules))
	for i, r := range p.Rules {
		r.Match = Match{
			Kinds: slices.Clone(r.Match.Kinds), Risks: slices.Clone(r.Match.Risks),
			Tags: slices.Clone(r.Match.Tags), Tools: slices.Clone(r.Match.Tools),
			Toolsets: slices.Clone(r.Match.Toolsets),
		}
		out.Rules[i] = r
	}
	if p.Rules == nil {
		out.Rules = nil
	}
	out.NarrowedBy = nil
	for _, n := range p.NarrowedBy {
		out.NarrowedBy = append(out.NarrowedBy, n.Clone())
	}
	return out
}

// Decide decides a call of the tool def, from the toolset named toolset
// ("" when not known). The tool's class is [tool.Metadata.Class], so an
// unclassified tool is write/medium. A policy that is not valid still
// decides, failing closed: an unknown action counts as Deny.
func (p Policy) Decide(def tool.Definition, toolset string) Decision {
	d := p.own(def, toolset)
	for _, n := range p.NarrowedBy {
		if nd := n.Decide(def, toolset); nd.Action.rank() > d.Action.rank() {
			d = nd
		}
	}
	return d
}

// own is the decision of p's own rules.
func (p Policy) own(def tool.Definition, toolset string) Decision {
	kind, risk := def.Class()
	d := Decision{Action: p.Default, Policy: p.Name, Rule: "default", Kind: kind, Risk: risk}
	named := false
	for i, r := range p.Rules {
		if r.Match.matches(def, toolset, kind, risk) {
			d.Action, d.Rule, d.Reason = r.Action, r.Name, r.Reason
			if d.Rule == "" {
				d.Rule = "rules[" + strconv.Itoa(i) + "]"
			}
			named = r.Match.names(def.Name, toolset)
			break
		}
	}
	if !slices.Contains(actions, d.Action) {
		d.Action, d.Reason = Deny, ""
	}
	if def.RequiresApproval && d.Action.rank() < Approve.rank() && !named {
		d.Action = Approve
		d.Reason = "the tool " + strconv.Quote(def.Name) + " requires a person's approval for each call"
	}
	if d.Reason == "" {
		d.Reason = reason(d)
	}
	return d
}

// reason is the reason of a decision whose rule gives none.
func reason(d Decision) string {
	of := ""
	if d.Policy != "" {
		of = " (policy " + d.Policy + ")"
	}
	class := string(d.Risk) + "-risk " + string(d.Kind) + " tools"
	switch d.Action {
	case Allow:
		return class + " are allowed" + of
	case Flag:
		return class + " are allowed, and flagged for review" + of
	case Approve:
		return class + " need a person's approval" + of
	}
	return class + " are disabled" + of
}

func (m Match) matches(def tool.Definition, toolset string, kind tool.Kind, risk tool.Risk) bool {
	if len(m.Kinds) > 0 && !slices.Contains(m.Kinds, kind) {
		return false
	}
	if len(m.Risks) > 0 && !slices.Contains(m.Risks, risk) {
		return false
	}
	if len(m.Tags) > 0 && !slices.ContainsFunc(m.Tags, def.HasTag) {
		return false
	}
	if len(m.Tools) > 0 && !anyGlob(m.Tools, toolNames(def.Name, toolset)...) {
		return false
	}
	if len(m.Toolsets) > 0 && (toolset == "" || !anyGlob(m.Toolsets, toolset)) {
		return false
	}
	return true
}

// names reports whether m.Tools names the tool exactly, without a wildcard:
// such a rule overrides the tool's own RequiresApproval.
func (m Match) names(name, toolset string) bool {
	for _, n := range toolNames(name, toolset) {
		if slices.Contains(m.Tools, n) {
			return true
		}
	}
	return false
}

// toolNames are the names a tool goes by: as the model sees it and, when it
// carries its toolset's prefix, within its toolset.
func toolNames(name, toolset string) []string {
	if own, ok := strings.CutPrefix(name, toolset+toolSeparator); ok && toolset != "" {
		return []string{name, own}
	}
	return []string{name}
}

// toolSeparator joins a toolset's name and a tool's in the name the model
// sees, as agentapi.ToolPrefixSeparator does.
const toolSeparator = "__"

func anyGlob(globs []string, names ...string) bool {
	for _, g := range globs {
		for _, n := range names {
			if ok, err := path.Match(g, n); ok && err == nil {
				return true
			}
		}
	}
	return false
}

// UnmarshalJSON reads a policy: an object with the fields of Policy, or the
// name of a preset as a string. Unknown fields are errors, and the policy
// read must be valid; errors wrap [ErrInvalid].
func (p *Policy) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '"' {
		var name string
		if err := json.Unmarshal(data, &name); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		pr, err := Preset(name)
		if err != nil {
			return err
		}
		*p = pr
		return nil
	}
	type plain Policy // without methods, so decoding it does not recurse
	var v plain
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if err := Policy(v).Validate(); err != nil {
		return err
	}
	*p = Policy(v)
	return nil
}
