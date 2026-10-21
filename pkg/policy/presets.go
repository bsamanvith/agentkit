package policy

import (
	"fmt"
	"slices"

	"github.com/bsamanvith/agentkit/pkg/tool"
)

// The presets' names.
const (
	// ReadOnly allows read tools and denies every other call.
	ReadOnly = "read_only"
	// LowRiskWrites allows read tools and low-risk writes, needs approval
	// for medium-risk writes, and denies high-risk tools and every delete,
	// send and execute.
	LowRiskWrites = "low_risk_writes"
	// ApproveWrites allows read tools and needs approval for every other
	// call.
	ApproveWrites = "approve_writes"
	// All allows every call (a tool that requires approval still needs it).
	All = "all"
)

var presetNames = []string{ReadOnly, LowRiskWrites, ApproveWrites, All}

// Presets returns the names of the presets, from the strictest.
func Presets() []string { return slices.Clone(presetNames) }

// reads is the rule every preset but All starts with: read tools run,
// whatever their risk.
func reads() Rule {
	return Rule{Name: "reads", Match: Match{Kinds: []tool.Kind{tool.KindRead}}, Action: Allow}
}

// Preset returns the named preset, a new copy each time. An unknown name is
// [ErrInvalid].
func Preset(name string) (Policy, error) {
	switch name {
	case ReadOnly:
		return Policy{Name: name, Rules: []Rule{reads()}, Default: Deny}, nil
	case LowRiskWrites:
		return Policy{Name: name, Rules: []Rule{
			reads(),
			{Name: "no_delete_send_execute", Match: Match{Kinds: []tool.Kind{tool.KindDelete, tool.KindSend, tool.KindExecute}}, Action: Deny},
			{Name: "no_high_risk", Match: Match{Risks: []tool.Risk{tool.RiskHigh}}, Action: Deny},
			{Name: "low_risk_writes", Match: Match{Kinds: []tool.Kind{tool.KindWrite}, Risks: []tool.Risk{tool.RiskLow}}, Action: Allow},
			{Name: "medium_risk_writes", Match: Match{Kinds: []tool.Kind{tool.KindWrite}, Risks: []tool.Risk{tool.RiskMedium}}, Action: Approve},
		}, Default: Deny}, nil
	case ApproveWrites:
		return Policy{Name: name, Rules: []Rule{reads()}, Default: Approve}, nil
	case All:
		return Policy{Name: name, Default: Allow}, nil
	}
	return Policy{}, fmt.Errorf("%w: no preset %q; the presets are %v", ErrInvalid, name, presetNames)
}

// MustPreset is [Preset] for a name known to be one; it panics otherwise.
func MustPreset(name string) Policy {
	p, err := Preset(name)
	if err != nil {
		panic(err)
	}
	return p
}
