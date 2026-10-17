package calendar_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/connectors/google/calendar"
)

// discovery is testdata/discovery.json: the methods the connector calls and
// the schemas they reach, cut from Google's discovery document for Calendar
// v3. Google publishes no OpenAPI, and the cassettes are written by hand
// (nobody can record them without an account), so this is what keeps them
// from only repeating the code's own assumptions.
type discovery struct {
	ServicePath string              `json:"servicePath"`
	Methods     map[string]dMethod  `json:"methods"`
	Schemas     map[string]*dSchema `json:"schemas"`
}

type dMethod struct {
	HTTPMethod string              `json:"httpMethod"`
	Path       string              `json:"path"`
	Parameters map[string]*dSchema `json:"parameters"`
	Request    *dSchema            `json:"request"`
	Response   *dSchema            `json:"response"`
	Scopes     []string            `json:"scopes"`
}

type dSchema struct {
	Ref                  string              `json:"$ref"`
	Type                 string              `json:"type"`
	Properties           map[string]*dSchema `json:"properties"`
	Items                *dSchema            `json:"items"`
	AdditionalProperties *dSchema            `json:"additionalProperties"`
	Enum                 []string            `json:"enum"`
	Location             string              `json:"location"`
}

func loadDiscovery(t *testing.T) *discovery {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "discovery.json"))
	if err != nil {
		t.Fatal(err)
	}
	var d discovery
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return &d
}

// check reports where v does not fit s: a field the schema does not declare
// or a value of another type.
func (d *discovery) check(s *dSchema, v any, at string) []string {
	if s.Ref != "" {
		return d.check(d.Schemas[s.Ref], v, at)
	}
	var bad []string
	wrong := func(want string) []string { return append(bad, fmt.Sprintf("%s: %T, want %s", at, v, want)) }
	switch s.Type {
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			return wrong("object")
		}
		for k, x := range m {
			switch {
			case s.Properties[k] != nil:
				bad = append(bad, d.check(s.Properties[k], x, at+"."+k)...)
			case s.AdditionalProperties != nil:
				bad = append(bad, d.check(s.AdditionalProperties, x, at+"."+k)...)
			default:
				bad = append(bad, fmt.Sprintf("%s.%s: not a field of the schema", at, k))
			}
		}
	case "array":
		a, ok := v.([]any)
		if !ok {
			return wrong("array")
		}
		for i, x := range a {
			bad = append(bad, d.check(s.Items, x, fmt.Sprintf("%s[%d]", at, i))...)
		}
	case "string":
		str, ok := v.(string)
		if !ok {
			return wrong("string")
		}
		if len(s.Enum) > 0 && !slices.Contains(s.Enum, str) {
			bad = append(bad, fmt.Sprintf("%s: %q is not one of %v", at, str, s.Enum))
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return wrong("boolean")
		}
	case "integer", "number":
		if _, ok := v.(float64); !ok {
			return wrong("number")
		}
	}
	return bad
}

// operation finds the method for a cassette's request.
func (d *discovery) operation(method, path string) (string, dMethod, bool) {
	for name, m := range d.Methods {
		if m.HTTPMethod != method {
			continue
		}
		want := strings.Split("/"+strings.TrimPrefix(m.Path, "/"), "/")
		got := strings.Split(path, "/")
		if len(want) != len(got) {
			continue
		}
		ok := true
		for i := range want {
			if !strings.HasPrefix(want[i], "{") && want[i] != got[i] {
				ok = false
			}
		}
		if ok {
			return name, m, true
		}
	}
	return "", dMethod{}, false
}

func TestCassettesFitTheDiscoveryDocument(t *testing.T) {
	t.Parallel()
	d := loadDiscovery(t)
	files, err := filepath.Glob(filepath.Join("testdata", "cassettes", "*.req.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("cassettes: %v, %d", err, len(files))
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".req.json")
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var req struct {
				Method, Path string
				Query        map[string][]string
				Body         any
			}
			b, _ := os.ReadFile(f)
			if err := json.Unmarshal(b, &req); err != nil {
				t.Fatal(err)
			}
			op, m, ok := d.operation(req.Method, req.Path)
			if !ok {
				t.Fatalf("%s %s is no method of the Calendar API", req.Method, req.Path)
			}
			for k, vs := range req.Query {
				p := m.Parameters[k]
				if p == nil || p.Location != "query" {
					t.Errorf("%s: %q is not a query parameter", op, k)
					continue
				}
				if len(p.Enum) > 0 && !slices.Contains(p.Enum, vs[0]) {
					t.Errorf("%s: %s=%q is not one of %v", op, k, vs[0], p.Enum)
				}
			}
			if req.Body != nil {
				if m.Request == nil {
					t.Errorf("%s takes no body", op)
				} else {
					for _, e := range d.check(m.Request, req.Body, "request") {
						t.Error(e)
					}
				}
			}
			res, err := os.ReadFile(strings.TrimSuffix(f, ".req.json") + ".res.http")
			if err != nil {
				t.Fatal(err)
			}
			_, body, _ := strings.Cut(string(res), "\n\n")
			if strings.TrimSpace(body) == "" {
				return
			}
			var v any
			if err := json.Unmarshal([]byte(body), &v); err != nil {
				t.Fatal(err)
			}
			if m.Response == nil {
				t.Fatalf("%s returns no body", op)
			}
			for _, e := range d.check(m.Response, v, "response") {
				t.Error(e)
			}
		})
	}
}

func TestEveryMethodTheConnectorCallsIsCoveredByItsScopes(t *testing.T) {
	t.Parallel()
	d := loadDiscovery(t)
	read := map[string]bool{"calendarList.list": true, "events.list": true, "events.get": true, "freebusy.query": true}
	for name, m := range d.Methods {
		scopes := calendar.Scopes(read[name])
		if !slices.ContainsFunc(scopes, func(s string) bool { return slices.Contains(m.Scopes, s) }) {
			t.Errorf("%s accepts %v, but Scopes(readOnly=%v) = %v", name, m.Scopes, read[name], scopes)
		}
	}
}
