package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/skill"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// skillSource is an entry of an agent's "skills": a folder of skill
// folders. A plain string is the path of a required one.
type skillSource struct {
	Path string `json:"path"`
	// Optional skips the folder, and shows why in /readyz, when it is
	// missing or a skill in it is invalid; otherwise startup fails.
	Optional bool `json:"optional"`
}

func (s *skillSource) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		*s = skillSource{}
		return json.Unmarshal(b, &s.Path)
	}
	type plain skillSource
	var p plain
	if err := decodeStrict(b, &p); err != nil {
		return err
	}
	*s = skillSource(p)
	return nil
}

// resolveSkillPaths makes the agents' relative skill paths relative to the
// config file's folder, so a config ships with its skills next to it.
func resolveSkillPaths(agents []agentDef, configPath string) {
	dir := filepath.Dir(configPath)
	for i := range agents {
		for j, s := range agents[i].Skills {
			if s.Path != "" && !filepath.IsAbs(s.Path) {
				agents[i].Skills[j].Path = filepath.Join(dir, s.Path)
			}
		}
	}
}

// agentSkills is what an agent's skill folders gave: the skills loaded and
// the optional folders skipped, with the reason.
type agentSkills struct {
	names   []string
	skipped []string
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
		skills, err := loadSkillFolder(src.Path)
		if err == nil {
			// Names must be unique across the agent's folders too.
			_, err = skill.NewCatalog(slices.Concat(all, skills)...)
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

func loadSkillFolder(path string) ([]skill.Skill, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: the path is empty", skill.ErrInvalidSkill)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	skills, err := skill.Load(root.FS(), ".")
	if err != nil || len(skills) == 0 {
		root.Close()
	}
	return skills, err
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

// skillReport is what the agents of a component loaded, for /readyz.
type skillReport struct {
	// loaded lists, per agent that names skill folders, its skills.
	loaded map[string][]string
	// skipped are the optional folders skipped: "<agent>: <path>: <why>".
	skipped []string
}

// add records what an agent's skill folders gave.
func (r *skillReport) add(a agentDef, s agentSkills) {
	if len(a.Skills) == 0 {
		return
	}
	if r.loaded == nil {
		r.loaded = map[string][]string{}
	}
	r.loaded[a.Name] = append([]string{}, s.names...)
	for _, why := range s.skipped {
		r.skipped = append(r.skipped, a.Name+": "+why)
	}
}

// Report lists the skills each agent loaded under "skills" in /readyz.
func (r skillReport) Report() map[string]any {
	if r.loaded == nil {
		return nil
	}
	return map[string]any{"skills": r.loaded}
}

// reason says which optional skill folders were skipped, "" when none.
func (r skillReport) reason() string {
	if len(r.skipped) == 0 {
		return ""
	}
	return "skill folders skipped: " + strings.Join(r.skipped, "; ")
}
