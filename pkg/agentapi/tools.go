package agentapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/bsamanvith/agentkit/pkg/tool"
)

// ToolPrefixSeparator joins a toolset's name and a tool's name when
// [MergeTools] prefixes: "<toolset>__<tool>".
const ToolPrefixSeparator = "__"

// ErrToolNameClash reports two toolsets with a tool of the same name, merged
// without prefixes.
var ErrToolNameClash = errors.New("tool name clash between toolsets")

// MergeTools merges named toolsets (for example the registries
// toolset.Build returns) into the one registry an agent uses, in process.
// Each toolset's tools are listed once, now. Without prefix, tools keep their
// names and a name in two toolsets fails with [ErrToolNameClash]; with prefix,
// every tool is named "<toolset>__<tool>", which must still be a valid tool
// name (64 characters at most), else it fails with [tool.ErrInvalidDefinition].
// Calls go to the toolset's registry under the tool's own name.
func MergeTools(ctx context.Context, sets map[string]tool.Registry, prefix bool) (tool.Registry, error) {
	names := make([]string, 0, len(sets))
	for name := range sets {
		names = append(names, name)
	}
	slices.Sort(names)
	var tools []tool.Tool
	owner := map[string]string{} // exposed tool name → toolset
	for _, set := range names {
		reg := sets[set]
		if reg == nil {
			return nil, fmt.Errorf("%w: toolset %q has no registry", tool.ErrInvalidDefinition, set)
		}
		defs, err := reg.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("toolset %q: list tools: %w", set, err)
		}
		for _, def := range defs {
			own := def.Name
			if prefix {
				def.Name = set + ToolPrefixSeparator + own
			}
			if other, dup := owner[def.Name]; dup {
				return nil, fmt.Errorf("%w: %q is in toolsets %q and %q; set a prefix for tool names", ErrToolNameClash, def.Name, other, set)
			}
			owner[def.Name] = set
			tools = append(tools, &routed{def: def, reg: reg, name: own})
		}
	}
	return tool.NewRegistry(tools)
}

// routed is a tool of a merged registry: it calls its toolset's registry under
// the tool's own name.
type routed struct {
	def  tool.Definition
	reg  tool.Registry
	name string
}

func (r *routed) Definition() tool.Definition { return r.def.Clone() }

func (r *routed) Invoke(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	return r.reg.Invoke(ctx, r.name, input)
}
