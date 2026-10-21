//go:build !nomodels

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/bsamanvith/agentkit/pkg/skill"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// Skills are read by an agent's harness, which needs a model, so a build
// without models (-tags nomodels: the api and toolsets images) leaves
// pkg/skill out (skills_none.go).

// addSkills loads an agent's skill folders, then adds their catalog to its
// system prompt and their tools to its tools.
func addSkills(ctx context.Context, label, system string, tools tool.Registry, sources []skillSource) (string, tool.Registry, agentSkills, error) {
	cat, info, err := loadSkills(label, sources)
	if err != nil {
		return "", nil, agentSkills{}, err
	}
	system, tools, err = withSkills(ctx, label, system, tools, cat)
	if err != nil {
		return "", nil, agentSkills{}, err
	}
	return system, tools, info, nil
}

// loadSkills loads an agent's skill folders into one catalog, nil when it
// names none. Each folder is opened as an os.Root that stays open for the
// life of the process, so skill_read never leaves it.
func loadSkills(label string, sources []skillSource) (*skill.Catalog, agentSkills, error) {
	var (
		all  []skill.Skill
		info agentSkills
	)
	for i, src := range sources {
		skills, root, err := loadSkillFolder(src.Path)
		if err == nil {
			// Names must be unique across the agent's folders too.
			if _, err = skill.NewCatalog(slices.Concat(all, skills)...); err != nil {
				root.Close()
			}
		}
		if err != nil {
			if src.Optional {
				info.skipped = append(info.skipped, fmt.Sprintf("%s: %v", src.Path, err))
				continue
			}
			return nil, agentSkills{}, fmt.Errorf("%s.skills[%d] (%s): %w", label, i, src.Path, err)
		}
		all = append(all, skills...)
	}
	if len(sources) == 0 {
		return nil, info, nil
	}
	cat, err := skill.NewCatalog(all...)
	if err != nil {
		return nil, agentSkills{}, fmt.Errorf("%s.skills: %w", label, err)
	}
	info.names = cat.Names()
	return cat, info, nil
}

// loadSkillFolder loads the skills in path and returns the root they read
// from, open (closed on an error, or when there are none).
func loadSkillFolder(path string) ([]skill.Skill, io.Closer, error) {
	if path == "" {
		return nil, nil, fmt.Errorf("%w: the path is empty", skill.ErrInvalidSkill)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, nil, err
	}
	skills, err := skill.Load(root.FS(), ".")
	if err != nil || len(skills) == 0 {
		root.Close()
	}
	return skills, root, err
}

// withSkills adds the catalog to an agent's system prompt and its toolset to
// its tools. A tool of the agent's toolsets with a skill tool's name fails.
func withSkills(ctx context.Context, label, system string, tools tool.Registry, cat *skill.Catalog) (string, tool.Registry, error) {
	if cat == nil || len(cat.Names()) == 0 {
		return system, tools, nil
	}
	defs, err := tools.List(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("%s.skills: %w", label, err)
	}
	for _, d := range defs {
		if d.Name == skill.LoadTool || d.Name == skill.ReadTool {
			return "", nil, fmt.Errorf("%s.skills: a toolset of the agent has a tool named %s; set prefix_tool_names", label, d.Name)
		}
	}
	set, err := cat.Toolset()
	if err != nil {
		return "", nil, err
	}
	reg, err := tool.NewRegistry(set)
	if err != nil {
		return "", nil, err
	}
	if system != "" {
		system += "\n\n"
	}
	return system + cat.SystemText(), joinRegistries(tools, reg), nil
}
