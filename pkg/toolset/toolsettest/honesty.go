package toolsettest

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

// schemaHonesty runs the two checks on the outputs of the happy calls:
// undeclared, no output carries a field its schema does not declare (a
// schema check passes one with an open schema); dead_fields (opt-in), every
// field each output schema declares appears at least once with a non-null
// value (a schema check passes a field the connector never fills, misnamed
// or dropped by the provider).
func (s *suite) schemaHonesty(t *testing.T) {
	s.run(t, "schema_honesty.undeclared", func(t *testing.T) {
		t.Parallel()
		for _, h := range s.honesty(t) {
			if len(h.undeclared) > 0 {
				t.Errorf("[honesty] %s: outputs carry %s, which the output schema does not declare", h.tool, strings.Join(slices.Sorted(maps.Keys(h.undeclared)), ", "))
			}
		}
	})
	s.run(t, "schema_honesty.dead_fields", func(t *testing.T) {
		t.Parallel()
		for _, h := range s.honesty(t) {
			var dead []string
			for _, p := range h.declared {
				if !h.seen[p] {
					dead = append(dead, p)
				}
			}
			if len(dead) > 0 {
				t.Errorf("[honesty] %s: no cassette output has a non-null %s: the connector never fills it, or the calls miss the case that does (add a call, or drop the field)", h.tool, strings.Join(dead, ", "))
			}
		}
	})
}

// honesty invokes every happy call on one rig and walks each tool's outputs
// against its output schema, in tool name order.
func (s *suite) honesty(t *testing.T) []*honesty {
	r := s.newRig(t, modeKeyed)
	byTool := map[string][]any{}
	schemas := map[string]json.RawMessage{}
	for _, call := range s.c.Calls {
		tools := r.toolsFor(call)
		if _, ok := schemas[call.Tool]; !ok {
			if tl := find(tools, call.Tool); tl != nil {
				schemas[call.Tool] = tl.Definition().OutputSchema
			}
		}
		out, err := r.invoke(t.Context(), tools, call.Tool, call.Input)
		if err != nil {
			t.Fatalf("%s: %v", call.Name, err)
		}
		var v any
		if err := json.Unmarshal(out, &v); err != nil {
			t.Fatalf("%s: output is not JSON: %v", call.Name, err)
		}
		byTool[call.Tool] = append(byTool[call.Tool], v)
	}
	var all []*honesty
	for _, name := range slices.Sorted(maps.Keys(byTool)) {
		var sc jsonschema.Schema
		if err := json.Unmarshal(schemas[name], &sc); err != nil {
			t.Errorf("[honesty] %s: output schema: %v", name, err)
			continue
		}
		h := &honesty{tool: name, seen: map[string]bool{}, undeclared: map[string]bool{}}
		h.declare(&sc, "")
		for _, v := range byTool[name] {
			h.walk(&sc, v, "")
		}
		all = append(all, h)
	}
	return all
}

// honesty collects the fields a schema declares and those values fill.
// Fields are paths: "results[].title". A schema without properties (or with
// anyOf, a $ref, ...) is opaque: nothing under it is checked.
type honesty struct {
	tool       string
	declared   []string
	seen       map[string]bool
	undeclared map[string]bool
}

func (h *honesty) declare(s *jsonschema.Schema, path string) {
	if s == nil {
		return
	}
	for _, name := range slices.Sorted(maps.Keys(s.Properties)) {
		p := join(path, name)
		h.declared = append(h.declared, p)
		h.declare(s.Properties[name], p)
	}
	h.declare(s.Items, path+"[]")
}

func (h *honesty) walk(s *jsonschema.Schema, v any, path string) {
	if s == nil {
		return
	}
	switch v := v.(type) {
	case map[string]any:
		if len(s.Properties) == 0 {
			return
		}
		closed := s.AdditionalProperties == nil || isFalse(s.AdditionalProperties)
		for k, fv := range v {
			ps, ok := s.Properties[k]
			switch {
			case ok:
				if fv != nil {
					h.seen[join(path, k)] = true
				}
				h.walk(ps, fv, join(path, k))
			case closed:
				h.undeclared[join(path, k)] = true
			}
		}
	case []any:
		for _, e := range v {
			if e != nil {
				h.seen[path+"[]"] = true
			}
			h.walk(s.Items, e, path+"[]")
		}
	}
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// isFalse reports whether s is the schema false, as jsonschema decodes it.
func isFalse(s *jsonschema.Schema) bool {
	return s.Not != nil && reflect.ValueOf(*s.Not).IsZero()
}

// reportSynthetic lists the synthetic cassettes the calls use: fixtures not
// recorded from the real API, which repeat the connector's assumptions. A
// connector with any stays in review until they are recorded.
func (s *suite) reportSynthetic(tb testing.TB) {
	tb.Helper()
	if s.fake {
		return
	}
	cs, err := loadCassettes(s.c.Cassettes)
	if err != nil {
		tb.Fatalf("toolsettest: %v", err)
	}
	var names []string
	for _, c := range cs {
		if c.req.Synthetic {
			names = append(names, c.name)
		}
	}
	if len(names) > 0 {
		tb.Logf("toolsettest: synthetic cassettes (not recorded from the real API; record them before the connector leaves review): %s", strings.Join(names, ", "))
	}
}
