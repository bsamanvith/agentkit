package harness

import (
	"cmp"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// SearchTool is the name of the tool a run with deferred tools
// ([WithDeferLoading]) offers the model to find them.
const SearchTool = "tool_search"

// Limits of a [SearchTool] call: the results it gives by default and at most.
const (
	defaultSearchResults = 5
	maxSearchResults     = 20
)

// ToolLoaderFunc gives the tools a call of a loader tool ([WithToolLoader])
// loads, as names or [path.Match] globs, from the call's input. It must be a
// pure function of the input (it is called again for nothing, and the
// result is kept in the transcript), safe for concurrent use.
type ToolLoaderFunc func(input json.RawMessage) []string

// searchDefinition is the [SearchTool]'s definition.
func searchDefinition() tool.Definition {
	return tool.Definition{
		Name: SearchTool,
		Description: "Find more tools. Some of your tools are not listed until you look for them: " +
			"describe what you need in a few keywords (or give a tool's name), and the tools that match " +
			"become available to call from your next step on. Search again with other words if none fit.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"query":{"type":"string","description":"keywords for what the tool should do, or its name"},` +
			`"max_results":{"type":"integer","minimum":1,"maximum":20,"description":"how many tools to load at most (default 5)"}` +
			`},"required":["query"],"additionalProperties":false}`),
		Metadata: tool.Metadata{
			Kind: tool.KindRead, Risk: tool.RiskLow, Tags: []string{tool.TagIdempotent},
			Display: tool.Display{Title: "Find tools", Icon: "search", Present: "Finding tools", Past: "Found tools"},
		},
	}
}

// deferral is a run's deferred tools: the ones the model is not sent until
// a [SearchTool] call or a loader tool loads them. It is set up before the
// first turn; loaded grows only between turns ([run.commit]), while no tool
// call runs, so the calls read it without a lock.
type deferral struct {
	native bool
	tools  map[string]tool.Definition // the deferred tools, by name
	names  []string                   // their names, sorted
	loaded map[string]bool
	order  []string // the loaded tools, in the order they loaded
}

// load records the tools of names that are deferred and not loaded yet, and
// returns them.
func (d *deferral) load(names []string) []string {
	var added []string
	for _, n := range names {
		if _, ok := d.tools[n]; ok && !d.loaded[n] {
			d.loaded[n] = true
			d.order = append(d.order, n)
			added = append(added, n)
		}
	}
	return added
}

// waiting reports whether name is a deferred tool not loaded yet: one the
// model has not been given.
func (d *deferral) waiting(name string) bool {
	if d == nil {
		return false
	}
	_, ok := d.tools[name]
	return ok && !d.loaded[name]
}

// match returns the deferred tools not loaded yet that the patterns name, in
// name order.
func (d *deferral) match(patterns []string) []string {
	var out []string
	for _, n := range d.names {
		if d.loaded[n] {
			continue
		}
		for _, p := range patterns {
			if ok, _ := path.Match(p, n); ok || p == n {
				out = append(out, n)
				break
			}
		}
	}
	return out
}

// loadsOf returns the tools a message of results loads: the Loads of each
// result that is not an error.
func loadsOf(m llm.Message) []string {
	var out []string
	for _, b := range m.Content {
		if r, ok := b.(llm.ToolResult); ok && !r.IsError {
			out = append(out, r.Loads...)
		}
	}
	return out
}

// setupDeferral splits the offered tools, by match, into those sent from the first turn
// and the deferred ones, and loads what the transcript loaded already. It
// returns the tools sent from the first turn, the search tool among them
// when there is anything to defer.
func (r *run) setupDeferral(offered []tool.Definition, match func(string) bool) ([]tool.Definition, error) {
	if match == nil {
		return offered, nil
	}
	d := &deferral{native: r.h.cfg.nativeSearch, tools: map[string]tool.Definition{}, loaded: map[string]bool{}}
	var base []tool.Definition
	clash := false
	for _, def := range offered {
		clash = clash || def.Name == SearchTool
		if def.Name != SearchTool && match(def.Name) {
			d.tools[def.Name] = def
			d.names = append(d.names, def.Name)
			continue
		}
		base = append(base, def)
	}
	if len(d.tools) == 0 {
		return offered, nil
	}
	if clash {
		return nil, fmt.Errorf("%w: a tool is named %s, the name of the harness's tool search: rename it to defer tools", ErrInvalidConfig, SearchTool)
	}
	slices.Sort(d.names)
	search := searchDefinition()
	r.defs[SearchTool] = search
	base = append(base, search)
	r.deferral = d
	for _, m := range r.transcript {
		if m.Role == llm.RoleUser {
			d.load(loadsOf(m))
		}
	}
	return base, nil
}

// requestTools returns the tools and the deferred names of the next request:
// the base tools and the loaded ones (in load order) when the harness
// emulates deferral, or every tool and the deferred names when the model
// defers them natively.
func (r *run) requestTools(base []tool.Definition) ([]tool.Definition, []string) {
	d := r.deferral
	if d == nil {
		return base, nil
	}
	tools := slices.Clip(base)
	if d.native {
		for _, n := range d.names {
			tools = append(tools, d.tools[n])
		}
		return tools, slices.Clone(d.names)
	}
	for _, n := range d.order {
		tools = append(tools, d.tools[n])
	}
	return tools, nil
}

// errNotLoaded is the result of a call to a deferred tool that is not
// loaded: the model has not been given it.
func errNotLoaded(name string) error {
	return fmt.Errorf("tool %q is not loaded: call %s to find it first", name, SearchTool)
}

type searchIn struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

type searchHit struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type searchOut struct {
	Tools []searchHit `json:"tools"`
	Note  string      `json:"note"`
}

// search runs a [SearchTool] call: it finds the deferred tools not loaded
// yet that match the query best, and returns them as the result's Loads.
// The model sees their names and descriptions; a native search sends only
// the references, which the provider expands into the definitions.
func (d *deferral) search(id string, input json.RawMessage) llm.ToolResult {
	var in searchIn
	if err := json.Unmarshal(input, &in); err != nil || strings.TrimSpace(in.Query) == "" {
		return errorResult(id, fmt.Errorf("%w: %s needs a query: {\"query\": \"keywords\"}", tool.ErrInvalidInput, SearchTool))
	}
	limit := in.MaxResults
	if limit <= 0 {
		limit = defaultSearchResults
	}
	limit = min(limit, maxSearchResults)
	hits := d.find(in.Query, limit)
	if len(hits) == 0 {
		out, _ := json.Marshal(searchOut{Tools: []searchHit{}, Note: "No tool matched. Try other words."})
		return llm.ToolResult{ToolUseID: id, Content: string(out)}
	}
	res := llm.ToolResult{ToolUseID: id}
	out := searchOut{Note: "These tools are now available."}
	for _, h := range hits {
		res.Loads = append(res.Loads, h.Name)
		out.Tools = append(out.Tools, searchHit{Name: h.Name, Description: h.Description})
	}
	if !d.native {
		b, _ := json.Marshal(out)
		res.Content = string(b)
	}
	return res
}

// find scores each deferred tool not loaded yet against the query's words:
// a word that is a part of its name counts 3 (so a query that is a tool's
// name finds it first), one inside its name 2, and one in its description
// or tags 1. Ties go by name.
func (d *deferral) find(query string, limit int) []tool.Definition {
	q := strings.ToLower(strings.TrimSpace(query))
	terms := words(q)
	type scored struct {
		def   tool.Definition
		score int
	}
	var all []scored
	for _, n := range d.names {
		if d.loaded[n] {
			continue
		}
		def := d.tools[n]
		name := strings.ToLower(n)
		parts := words(name)
		text := strings.ToLower(def.Description + " " + strings.Join(def.Tags, " "))
		score := 0
		for _, t := range terms {
			switch {
			case slices.Contains(parts, t):
				score += 3
			case strings.Contains(name, t):
				score += 2
			case strings.Contains(text, t):
				score++
			}
		}
		if score > 0 {
			all = append(all, scored{def, score})
		}
	}
	slices.SortStableFunc(all, func(a, b scored) int { return cmp.Compare(b.score, a.score) })
	out := make([]tool.Definition, 0, min(limit, len(all)))
	for _, s := range all[:min(limit, len(all))] {
		out = append(out, s.def)
	}
	return out
}

// words splits s at everything but letters and digits.
func words(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}
