package harness

import (
	"fmt"
	"slices"

	"github.com/bsamanvith/agentkit/pkg/tool"
)

// ToolSearchMode is how a run offers its toolsets' tools ([ToolChoice]).
type ToolSearchMode string

// The modes of [ToolChoice].Search.
const (
	// SearchAgent keeps the harness's own deferral ([WithDeferLoading]).
	SearchAgent ToolSearchMode = ""
	// SearchDirect sends every tool of the chosen toolsets and defers none,
	// whatever the harness defers.
	SearchDirect ToolSearchMode = "direct"
	// SearchDeferred holds every tool of the chosen toolsets back behind
	// [SearchTool], which loads them as the model finds them.
	SearchDeferred ToolSearchMode = "search"
)

// Valid reports whether m is one of the modes.
func (m ToolSearchMode) Valid() bool {
	return m == SearchAgent || m == SearchDirect || m == SearchDeferred
}

// ToolChoice narrows the tools a run offers ([Input.Tools]): which of the
// harness's toolsets the model gets, and whether their tools are sent
// directly or found with [SearchTool]. Tools that belong to no toolset (as
// [WithToolsets] says), such as skills, memory and ask_user, are always
// kept and never deferred: the model needs them to find the rest.
type ToolChoice struct {
	// Toolsets are the toolsets to keep, by name. Nil keeps every one; an
	// empty list keeps none.
	Toolsets []string `json:"toolsets"`
	// Search says whether the kept toolsets' tools are deferred.
	Search ToolSearchMode `json:"search,omitempty"`
}

// Clone returns a deep copy of c.
func (c *ToolChoice) Clone() *ToolChoice {
	if c == nil {
		return nil
	}
	out := *c
	if c.Toolsets != nil {
		out.Toolsets = slices.Clone(c.Toolsets)
	}
	return &out
}

// IsZero reports whether c narrows nothing.
func (c *ToolChoice) IsZero() bool {
	return c == nil || c.Toolsets == nil && c.Search == SearchAgent
}

// WithToolsets names the toolset of each tool, by the name the model calls
// it ("" for a tool of no toolset), so that a run can choose toolsets
// ([Input.Tools]). It is the same function [WithToolPolicy] takes; either
// option sets it. It must be safe for concurrent use.
func WithToolsets(toolsetOf func(name string) string) Option {
	return func(c *config) { c.toolsetOf = toolsetOf }
}

// errNotChosen is the result of a call to a tool the run's [ToolChoice]
// left out: the model was not given it.
func errNotChosen(name string) error {
	return fmt.Errorf("%w: tool %q is not available in this chat", tool.ErrInvalidInput, name)
}

// checkChoice validates a run's choice against the harness's setup.
func (r *run) checkChoice(c *ToolChoice) error {
	if c.IsZero() {
		return nil
	}
	if !c.Search.Valid() {
		return invalidInput("tools.search %q: want %q or %q", c.Search, SearchDirect, SearchDeferred)
	}
	if r.h.cfg.toolsetOf == nil {
		return invalidInput("tools: this harness does not know its tools' toolsets (WithToolsets)")
	}
	return nil
}

// choose applies the run's [ToolChoice] to the offered tools: it drops the
// tools of the toolsets not chosen (recording them in r.dropped, so their
// calls are refused) and returns what is left and which of it to defer
// (nil: none).
func (r *run) choose(offered []tool.Definition) ([]tool.Definition, func(string) bool) {
	deferred := r.h.cfg.deferred
	c := r.choice
	if c.IsZero() {
		return offered, deferred
	}
	toolsetOf := r.h.cfg.toolsetOf
	if c.Toolsets != nil {
		kept := offered[:0:0]
		for _, d := range offered {
			if ts := toolsetOf(d.Name); ts != "" && !slices.Contains(c.Toolsets, ts) {
				if r.dropped == nil {
					r.dropped = map[string]bool{}
				}
				r.dropped[d.Name] = true
				continue
			}
			kept = append(kept, d)
		}
		offered = kept
	}
	switch c.Search {
	case SearchDirect:
		deferred = nil
	case SearchDeferred:
		deferred = func(name string) bool { return toolsetOf(name) != "" }
	}
	return offered, deferred
}
