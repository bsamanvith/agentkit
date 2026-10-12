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

// schemaHonesty: across the outputs of the happy calls, every field each
// output schema declares appears at least once with a non-null value, and no
// output carries a field its schema does not declare. A schema check alone
// passes a field the connector never fills (misnamed, or dropped by the
// provider) and, with an open schema, a field it never declared.
func (s *suite) schemaHonesty(t *testing.T) {
	t.Parallel()
	r := s.newRig(t, modeKeyed)
	tools := r.mustBuild()
	byTool := map[string][]any{}
	for _, call := range s.c.Calls {
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
	for _, name := range slices.Sorted(maps.Keys(byTool)) {
		var sc jsonschema.Schema
		if err := json.Unmarshal(find(tools, name).Definition().OutputSchema, &sc); err != nil {
			t.Errorf("[honesty] %s: output schema: %v", name, err)
			continue
		}
		h := honesty{seen: map[string]bool{}, undeclared: map[string]bool{}}
		h.declare(&sc, "")
		for _, v := range byTool[name] {
			h.walk(&sc, v, "")
		}
		var dead []string
		for _, p := range h.declared {
			if !h.seen[p] {
				dead = append(dead, p)
			}
		}
		if len(dead) > 0 {
			t.Errorf("[honesty] %s: no cassette output has a non-null %s: the connector never fills it, or the calls miss the case that does (add a call, or drop the field)", name, strings.Join(dead, ", "))
		}
		if len(h.undeclared) > 0 {
			t.Errorf("[honesty] %s: outputs carry %s, which the output schema does not declare", name, strings.Join(slices.Sorted(maps.Keys(h.undeclared)), ", "))
		}
	}
}

// honesty collects the fields a schema declares and those values fill.
// Fields are paths: "results[].title". A schema without properties (or with
// anyOf, a $ref, ...) is opaque: nothing under it is checked.
type honesty struct {
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
