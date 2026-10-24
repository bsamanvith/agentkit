package agentapi

import (
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/harness"
)

// toolChoice checks a run's choice of tools against its agent: every
// toolset named must be one of the agent's, and the mode one of the
// modes. It returns the harness's choice, or what is wrong with it.
func toolChoice(ag *agent, c wire.ToolChoice) (*harness.ToolChoice, string) {
	out := harness.ToolChoice{Search: harness.ToolSearchMode(c.Search)}
	if !out.Search.Valid() {
		return nil, "tools.search " + quote(c.Search) + " is not one of " + quote(wire.ToolSearchDirect) + ", " + quote(wire.ToolSearchSearch)
	}
	if c.Toolsets != nil {
		out.Toolsets = []string{}
		for _, name := range c.Toolsets {
			if !slices.Contains(ag.Toolsets, name) && !slices.Contains(ag.RuntimeToolsets, name) {
				return nil, "tools.toolsets: agent " + quote(ag.Name) + " has no toolset " + quote(name) + "; it has " + strings.Join(slices.Concat(ag.Toolsets, ag.RuntimeToolsets), ", ")
			}
			if !slices.Contains(out.Toolsets, name) {
				out.Toolsets = append(out.Toolsets, name)
			}
		}
	}
	if out.IsZero() {
		return nil, ""
	}
	return &out, ""
}
