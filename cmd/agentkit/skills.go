package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
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
