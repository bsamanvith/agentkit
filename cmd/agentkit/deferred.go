package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"path"
	"slices"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// deferSpec is an agent's "defer_loading": which of its toolsets' tools the
// model is not sent until it finds them with tool_search (or a skill that
// lists them loads them). true defers every toolset tool; a list names
// toolsets (all their tools) and tool-name globs ("github_*"), by the name
// the model calls a tool. Skill and memory tools are never deferred: the
// model needs them to find the rest.
type deferSpec struct {
	all     bool
	entries []string
}

func (d *deferSpec) UnmarshalJSON(b []byte) error {
	*d = deferSpec{}
	b = bytes.TrimSpace(b)
	switch {
	case string(b) == "null":
		return nil
	case len(b) > 0 && (b[0] == 't' || b[0] == 'f'):
		return json.Unmarshal(b, &d.all)
	}
	if err := json.Unmarshal(b, &d.entries); err != nil {
		return errors.New(`defer_loading: want true, false or a list of toolset names and tool-name globs`)
	}
	return nil
}

func (d deferSpec) MarshalJSON() ([]byte, error) {
	if d.entries != nil {
		return json.Marshal(d.entries)
	}
	return json.Marshal(d.all)
}

func (d deferSpec) set() bool { return d.all || len(d.entries) > 0 }

// Tool search modes of an agent's model.tool_search.
const (
	toolSearchNative   = "native"
	toolSearchEmulated = "emulated"
)

// toolSearch is the agent's tool search mode: native for Claude unless the
// config says emulated, emulated for every other provider.
func (m agentModelConfig) toolSearch() string {
	if m.ToolSearch != "" {
		return m.ToolSearch
	}
	if cmp.Or(m.Provider, defaultProvider) != defaultProvider {
		return toolSearchEmulated
	}
	return toolSearchNative
}

// deferOptions are the harness options of an agent's defer_loading: which
// tools are deferred (by the toolset each belongs to, toolsetOf, and the
// toolset tools known at startup, static), native or emulated search, and
// skill_load as a loader of the tools its skills list. It fails on an
// entry that names no toolset of the agent and matches none of its tools.
func (d agentDef) deferOptions(label string, static, all []string, toolsetOf func(string) string, skills agentSkills, log *slog.Logger) ([]harness.Option, error) {
	spec := d.DeferLoading
	if !spec.set() {
		if len(skills.lists) > 0 {
			log.Info("skills list tools, but the agent defers none, so they are loaded already", "agent", d.Name)
		}
		return nil, nil
	}
	isSet := func(name string) bool {
		return slices.Contains(d.Toolsets, name) || slices.Contains(d.RuntimeToolsets, name)
	}
	for _, e := range spec.entries {
		if _, err := path.Match(e, ""); err != nil {
			return nil, fmt.Errorf("%s.defer_loading: pattern %q: %w", label, e, err)
		}
		if isSet(e) || len(d.RuntimeToolsets) > 0 || slices.ContainsFunc(static, func(n string) bool { return globMatch(e, n) }) {
			continue
		}
		return nil, fmt.Errorf("%s.defer_loading: %q names no toolset of the agent and matches none of its tools", label, e)
	}
	deferred := func(name string) bool {
		set := toolsetOf(name)
		if set == "" {
			return false // a skill or memory tool: never deferred
		}
		if spec.all {
			return true
		}
		for _, e := range spec.entries {
			if e == set || globMatch(e, name) {
				return true
			}
		}
		return false
	}
	opts := []harness.Option{harness.WithDeferLoading(deferred)}
	if d.Model.toolSearch() == toolSearchNative {
		opts = append(opts, harness.WithNativeToolSearch())
	}
	if skills.loader != nil {
		opts = append(opts, harness.WithToolLoader(skills.loadTool, skills.loader))
	}
	for _, w := range skillToolWarnings(skills.lists, all, len(d.RuntimeToolsets) > 0) {
		log.Warn("a skill lists a tool the agent does not have; it is ignored", "agent", d.Name, "skill", w[0], "tool", w[1])
	}
	return opts, nil
}

// skillToolWarnings returns the [skill, entry] pairs of the skills' tools:
// lists that match none of the agent's toolset tools known at startup: a
// skill cannot give the agent a tool it does not have, so they load
// nothing. With runtime toolsets, whose tools are known only per run, a
// glob is not checked.
func skillToolWarnings(lists map[string][]string, static []string, runtime bool) [][2]string {
	var out [][2]string
	for _, s := range slices.Sorted(maps.Keys(lists)) {
		for _, e := range lists[s] {
			if runtime || slices.ContainsFunc(static, func(n string) bool { return globMatch(e, n) }) {
				continue
			}
			out = append(out, [2]string{s, e})
		}
	}
	return out
}

func globMatch(pattern, name string) bool {
	ok, _ := path.Match(pattern, name)
	return ok || pattern == name
}

// defNames are the names of defs.
func defNames(defs []tool.Definition) []string {
	out := make([]string, len(defs))
	for i, d := range defs {
		out[i] = d.Name
	}
	return out
}
