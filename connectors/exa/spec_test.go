package exa_test

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/connectors/exa"
)

// testdata/openapi/exa.json is the slice of Exa's OpenAPI spec this
// connector uses (POST /search and /contents, and the schemas they
// reference), cut by testdata/openapi/cut.go from
// https://exa.ai/docs/exa-spec.yaml (fetched 2026-10-12).
//
// specChecker is a JSON Schema checker for the keywords that slice uses,
// and nothing more: a keyword it does not know fails the check rather than
// being ignored, so a re-vendored spec can never pass by being misread. It
// belongs in toolsettest once T049 lands (Parallel's spec needs it too).
type specChecker struct {
	schemas map[string]any
	paths   map[string]struct {
		Post struct {
			Request  any `json:"request"`
			Response any `json:"response200"`
		} `json:"post"`
	}
}

func loadSpec(t testing.TB) *specChecker {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "openapi", "exa.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths      json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]any `json:"schemas"`
		} `json:"components"`
	}
	sc := &specChecker{}
	err = json.Unmarshal(b, &doc)
	if err == nil {
		sc.schemas = doc.Components.Schemas
		err = json.Unmarshal(doc.Paths, &sc.paths)
	}
	if err != nil || len(sc.schemas) == 0 || len(sc.paths) == 0 {
		t.Fatalf("testdata/openapi/exa.json: %v", err)
	}
	return sc
}

// schemaKeywords are the keywords specChecker enforces; annotations are
// the ones it may ignore. Anything else (bar x- extensions) is a failure.
var (
	schemaKeywords = []string{
		"$ref", "type", "enum", "const", "anyOf", "oneOf",
		"properties", "required", "additionalProperties", "propertyNames",
		"items", "minItems", "maxItems",
		"minLength", "maxLength", "pattern", "format",
		"minimum", "maximum", "exclusiveMinimum",
	}
	schemaAnnotations = []string{"description", "title", "default", "deprecated", "example", "discriminator"}
)

// check returns where instance x breaks schema s, if anywhere.
func (sc *specChecker) check(s, x any, at string) []string {
	switch s := s.(type) {
	case bool:
		if !s {
			return []string{at + ": no value is allowed here"}
		}
		return nil
	case map[string]any:
		return sc.checkObject(s, x, at)
	default:
		return []string{fmt.Sprintf("%s: schema %v is not an object or a boolean", at, s)}
	}
}

func (sc *specChecker) checkObject(s map[string]any, x any, at string) []string {
	var errs []string
	fail := func(format string, args ...any) { errs = append(errs, at+": "+fmt.Sprintf(format, args...)) }
	for k := range s {
		if !slices.Contains(schemaKeywords, k) && !slices.Contains(schemaAnnotations, k) && !strings.HasPrefix(k, "x-") {
			fail("schema keyword %q is not supported by the checker", k)
		}
	}
	if ref, ok := s["$ref"].(string); ok {
		sub, found := sc.schemas[strings.TrimPrefix(ref, "#/components/schemas/")]
		if !found {
			fail("unresolved $ref %s", ref)
		} else {
			errs = append(errs, sc.check(sub, x, at)...)
		}
	}
	if t, ok := s["type"]; ok && !typeMatches(t, x) {
		fail("%s is not of type %v", short(x), t)
		return errs
	}
	if e, ok := s["enum"].([]any); ok && !slices.ContainsFunc(e, func(c any) bool { return reflect.DeepEqual(c, x) }) {
		fail("%s is not one of %v", short(x), e)
	}
	if c, ok := s["const"]; ok && !reflect.DeepEqual(c, x) {
		fail("%s is not %v", short(x), c)
	}
	if alts, ok := s["anyOf"].([]any); ok {
		if n, closest := sc.matching(alts, x, at); n == 0 {
			fail("%s matches none of anyOf; closest: %s", short(x), strings.Join(closest, "; "))
		}
	}
	if alts, ok := s["oneOf"].([]any); ok {
		if n, closest := sc.matching(alts, x, at); n != 1 {
			fail("%s matches %d of oneOf, want 1; closest: %s", short(x), n, strings.Join(closest, "; "))
		}
	}
	switch x := x.(type) {
	case map[string]any:
		errs = append(errs, sc.checkMembers(s, x, at)...)
	case []any:
		if n, ok := s["minItems"].(float64); ok && float64(len(x)) < n {
			fail("%d items, fewer than %v", len(x), n)
		}
		if n, ok := s["maxItems"].(float64); ok && float64(len(x)) > n {
			fail("%d items, more than %v", len(x), n)
		}
		if it, ok := s["items"]; ok {
			for i, e := range x {
				errs = append(errs, sc.check(it, e, fmt.Sprintf("%s[%d]", at, i))...)
			}
		}
	case string:
		n := float64(utf8.RuneCountInString(x))
		if m, ok := s["minLength"].(float64); ok && n < m {
			fail("%q is shorter than %v", x, m)
		}
		if m, ok := s["maxLength"].(float64); ok && n > m {
			fail("%d characters, more than %v", int(n), m)
		}
		if p, ok := s["pattern"].(string); ok {
			if re, err := regexp.Compile(p); err != nil || !re.MatchString(x) {
				fail("%q does not match pattern %s (%v)", x, p, err)
			}
		}
		if f, ok := s["format"].(string); ok {
			if err := checkFormat(f, x); err != nil {
				fail("%v", err)
			}
		}
	case float64:
		if m, ok := s["minimum"].(float64); ok && x < m {
			fail("%v is below the minimum %v", x, m)
		}
		if m, ok := s["maximum"].(float64); ok && x > m {
			fail("%v is above the maximum %v", x, m)
		}
		if m, ok := s["exclusiveMinimum"].(float64); ok && x <= m {
			fail("%v is not above %v", x, m)
		}
	}
	return errs
}

func (sc *specChecker) checkMembers(s, x map[string]any, at string) []string {
	var errs []string
	if req, ok := s["required"].([]any); ok {
		for _, r := range req {
			if _, present := x[r.(string)]; !present {
				errs = append(errs, fmt.Sprintf("%s: required %q is missing", at, r))
			}
		}
	}
	props, _ := s["properties"].(map[string]any)
	for k, v := range x {
		if ps, ok := props[k]; ok {
			errs = append(errs, sc.check(ps, v, at+"."+k)...)
		} else if ap, ok := s["additionalProperties"]; ok {
			errs = append(errs, sc.check(ap, v, at+"."+k)...)
		}
		if pn, ok := s["propertyNames"]; ok {
			errs = append(errs, sc.check(pn, k, at+" key "+k)...)
		}
	}
	return errs
}

// matching counts the alternatives x is valid for, and returns the problems
// with the closest one that it is not valid for.
func (sc *specChecker) matching(alts []any, x any, at string) (int, []string) {
	n := 0
	var closest []string
	for _, a := range alts {
		errs := sc.check(a, x, at)
		switch {
		case len(errs) == 0:
			n++
		case closest == nil || len(errs) < len(closest):
			closest = errs
		}
	}
	return n, closest
}

func typeMatches(t, x any) bool {
	if ts, ok := t.([]any); ok {
		return slices.ContainsFunc(ts, func(t any) bool { return typeMatches(t, x) })
	}
	switch t {
	case "null":
		return x == nil
	case "boolean":
		_, ok := x.(bool)
		return ok
	case "string":
		_, ok := x.(string)
		return ok
	case "number":
		_, ok := x.(float64)
		return ok
	case "integer":
		f, ok := x.(float64)
		return ok && f == math.Trunc(f)
	case "array":
		_, ok := x.([]any)
		return ok
	case "object":
		_, ok := x.(map[string]any)
		return ok
	}
	return false
}

func checkFormat(format, s string) error {
	var err error
	switch format {
	case "date-time":
		_, err = time.Parse(time.RFC3339Nano, s)
	case "date":
		_, err = time.Parse("2006-01-02", s)
	case "uri":
		var u *url.URL
		if u, err = url.Parse(s); err == nil && u.Scheme == "" {
			err = fmt.Errorf("%q has no scheme", s)
		}
	case "float":
		// An annotation on a number.
	default:
		err = fmt.Errorf("format %q is not supported by the checker", format)
	}
	if err != nil {
		return fmt.Errorf("%q is not a %s: %w", s, format, err)
	}
	return nil
}

func short(x any) string {
	b, _ := json.Marshal(x)
	if len(b) > 80 {
		return string(b[:80]) + "…"
	}
	return string(b)
}

func (sc *specChecker) checkJSON(t *testing.T, what string, schema any, body []byte) {
	t.Helper()
	var x any
	if err := json.Unmarshal(body, &x); err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	for _, e := range sc.check(schema, x, what) {
		t.Error(e)
	}
}

// Every cassette's request body is valid for Exa's request schema, and its
// response body for the 200 response schema (T049 §6: spec validation).
// Synthetic cassettes are listed: the task stays in review until they are
// re-recorded from the real API.
func TestCassettesMatchSpec(t *testing.T) {
	t.Parallel()
	sc := loadSpec(t)
	files, err := filepath.Glob(cassettePath("*", ".req.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no cassettes: %v", err)
	}
	var synthetic []string
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".req.json")
		req, resp := loadCassette(t, name)
		if req.Synthetic {
			synthetic = append(synthetic, name)
		}
		p, ok := sc.paths[req.Path]
		if !ok || req.Method != "POST" {
			t.Errorf("%s: %s %s is not in the vendored spec", name, req.Method, req.Path)
			continue
		}
		sc.checkJSON(t, name+" request", p.Post.Request, req.Body)
		if resp.status != 200 {
			t.Errorf("%s: status %d; cassettes hold 200s (failures stay synthetic in the error tests)", name, resp.status)
			continue
		}
		sc.checkJSON(t, name+" response", p.Post.Response, resp.body)
	}
	if len(synthetic) > 0 {
		t.Logf("synthetic cassettes (re-record with -tags live and AGENTKIT_RECORD_CASSETTES=1): %s", strings.Join(synthetic, ", "))
	}
}

// Every search type and category the connector accepts is one Exa's spec
// accepts, so a value the model or an operator picks is never refused by
// Exa for being unknown.
func TestEnumsMatchSpec(t *testing.T) {
	t.Parallel()
	sc := loadSpec(t)
	schema := sc.paths["/search"].Post.Request
	for field, values := range map[string][]string{"type": exa.SearchTypes, "category": exa.Categories} {
		for _, v := range values {
			for _, e := range sc.check(schema, map[string]any{"query": "q", field: v}, field+"="+v) {
				t.Error(e)
			}
		}
	}
}

// The checker is not vacuous: it rejects bodies that break the spec in the
// ways a connector or a spec change would.
func TestSpecCheckerRejects(t *testing.T) {
	t.Parallel()
	sc := loadSpec(t)
	cases := map[string]struct{ path, kind, body string }{
		"results not an array":       {"/search", "response", `{"results": {"url": "https://a.example/"}}`},
		"undeclared response field":  {"/search", "response", `{"results": [], "surprise": 1}`},
		"result url not a string":    {"/search", "response", `{"results": [{"id": "x", "url": 7}]}`},
		"status not success / error": {"/contents", "response", `{"results": [], "statuses": [{"id": "https://a.example/", "status": "pending"}]}`},
		"status without id":          {"/contents", "response", `{"results": [], "statuses": [{"status": "error"}]}`},
		"unknown search type":        {"/search", "request", `{"query": "q", "type": "neural"}`},
		"numResults not an integer":  {"/search", "request", `{"query": "q", "numResults": 2.5}`},
		"date not a date-time":       {"/search", "request", `{"query": "q", "startPublishedDate": "2026-10-12"}`},
		"no query":                   {"/search", "request", `{"type": "auto"}`},
		"no urls":                    {"/contents", "request", `{"text": true}`},
	}
	for name, tc := range cases {
		p := sc.paths[tc.path].Post
		schema := p.Request
		if tc.kind == "response" {
			schema = p.Response
		}
		var x any
		if err := json.Unmarshal([]byte(tc.body), &x); err != nil {
			t.Fatal(err)
		}
		if errs := sc.check(schema, x, name); len(errs) == 0 {
			t.Errorf("%s: %s passes the spec check", name, tc.body)
		}
	}
	if errs := sc.check(map[string]any{"type": "string", "if": true}, "x", "unknown keyword"); len(errs) == 0 {
		t.Error("a schema with an unsupported keyword passes")
	}
}
