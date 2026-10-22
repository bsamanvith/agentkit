package toolset

import (
	"fmt"
	"path"
	"slices"

	"github.com/bsamanvith/agentkit/pkg/tool"
)

// checkGlobs reports malformed patterns before anything runs.
func checkGlobs(lists ...[]string) error {
	for _, list := range lists {
		for _, pat := range list {
			if _, err := path.Match(pat, ""); err != nil {
				return fmt.Errorf("%w: pattern %q: %w", ErrInvalidFilter, pat, err)
			}
		}
	}
	return nil
}

// filterTools applies include then exclude. Every pattern must match at least
// one of the factory's tools (a pattern matching nothing is almost always a
// typo, and for exclude would silently leave a tool exposed), and at least one
// tool must remain.
func filterTools(tools []tool.Tool, include, exclude []string) ([]tool.Tool, error) {
	if len(include) == 0 && len(exclude) == 0 {
		return tools, nil
	}
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Definition().Name
	}
	for _, list := range [][]string{include, exclude} {
		for _, pat := range list {
			if !matchesAny(pat, names) {
				return nil, fmt.Errorf("%w: pattern %q matches no tool (tools: %v)", ErrInvalidFilter, pat, names)
			}
		}
	}

	var kept []tool.Tool
	for i, t := range tools {
		if len(include) > 0 && !globsMatch(include, names[i]) {
			continue
		}
		if globsMatch(exclude, names[i]) {
			continue
		}
		kept = append(kept, t)
	}
	if len(kept) == 0 {
		return nil, fmt.Errorf("%w: include/exclude leave no tools (tools: %v)", ErrInvalidFilter, names)
	}
	return kept, nil
}

func globsMatch(patterns []string, name string) bool {
	for _, pat := range patterns {
		if ok, _ := path.Match(pat, name); ok {
			return true
		}
	}
	return false
}

func matchesAny(pattern string, names []string) bool {
	for _, n := range names {
		if ok, _ := path.Match(pattern, n); ok {
			return true
		}
	}
	return false
}

// describeTools replaces the descriptions of the tools descs names. Every
// name must be one of tools, and every description non-empty.
func describeTools(tools []tool.Tool, descs map[string]string) ([]tool.Tool, error) {
	if len(descs) == 0 {
		return tools, nil
	}
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Definition().Name
	}
	for name, desc := range descs {
		if !slices.Contains(names, name) {
			return nil, fmt.Errorf("%w: descriptions names %q, which is not a tool of the toolset (tools: %v)", ErrInvalidFilter, name, names)
		}
		if desc == "" {
			return nil, fmt.Errorf("%w: descriptions gives %q an empty description", ErrInvalidFilter, name)
		}
	}
	out := make([]tool.Tool, len(tools))
	for i, t := range tools {
		out[i] = t
		if desc, ok := descs[names[i]]; ok {
			out[i] = described{Tool: t, description: desc}
		}
	}
	return out, nil
}

// described is a tool with another description.
type described struct {
	tool.Tool
	description string
}

func (d described) Definition() tool.Definition {
	def := d.Tool.Definition()
	def.Description = d.description
	return def
}
