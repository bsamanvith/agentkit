package main

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// maxToolDescription is the longest description an override may give.
const maxToolDescription = 4 << 10

// describeTools is tools with the descriptions an agent overrides (T126):
// its tool_descriptions, and those of its model's variant over them
// (onModel merged both into descs). Each name must be one of the agent's
// tools, by the name the model calls it, or start with a runtime toolset's
// prefix, whose tools are known only at run time. It also checks the
// overrides of every variant, so a bad one is refused when the agent is
// built, not when a run first picks its model.
func describeTools(ctx context.Context, label string, tools tool.Registry, a agentDef) (tool.Registry, error) {
	defs, err := tools.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s.toolsets: %w", label, err)
	}
	names := defNames(defs)
	if err := checkDescriptions(label+".tool_descriptions", a.ToolDescriptions, names, a.RuntimeToolsets); err != nil {
		return nil, err
	}
	for i, v := range a.Models.Variants {
		if err := checkDescriptions(fmt.Sprintf("%s.models.variants[%d].tool_descriptions", label, i), v.ToolDescriptions, names, a.RuntimeToolsets); err != nil {
			return nil, err
		}
	}
	if len(a.ToolDescriptions) == 0 {
		return tools, nil
	}
	return described{tools, maps.Clone(a.ToolDescriptions)}, nil
}

// checkDescriptions checks the overrides descs, reported at field: the
// first bad one by name.
func checkDescriptions(field string, descs map[string]string, names, runtime []string) error {
	for _, name := range slices.Sorted(maps.Keys(descs)) {
		d := descs[name]
		switch {
		case !slices.Contains(names, name) && !slices.ContainsFunc(runtime, func(rt string) bool {
			return strings.HasPrefix(name, rt+agentapi.ToolPrefixSeparator)
		}):
			return fmt.Errorf("%s.%s: not a tool of the agent (name it as the model calls it, with its toolset's prefix if it has one)", field, name)
		case strings.TrimSpace(d) == "":
			return fmt.Errorf("%s.%s: an empty description; leave the tool out to keep its own", field, name)
		case len(d) > maxToolDescription:
			return fmt.Errorf("%s.%s: longer than %d bytes", field, name, maxToolDescription)
		}
	}
	return nil
}

// described lists its tools with the descriptions in descs instead of
// their own; calls go through unchanged.
type described struct {
	tool.Registry
	descs map[string]string
}

func (d described) List(ctx context.Context) ([]tool.Definition, error) {
	defs, err := d.Registry.List(ctx)
	if err != nil {
		return nil, err
	}
	out := slices.Clone(defs)
	for i, def := range out {
		if desc, ok := d.descs[def.Name]; ok {
			out[i].Description = desc
		}
	}
	return out, nil
}
