package toolsettest_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
)

// exchange is one cassette: its .req.json and .res.http.
type exchange struct{ req, res string }

// toyCassettes copies the toy's cassettes to a new directory, adding or
// replacing the exchanges given.
func toyCassettes(t *testing.T, extra map[string]exchange) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join("testdata", "cassettes")
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, e.Name()), string(b))
	}
	for name, ex := range extra {
		writeFile(t, filepath.Join(dir, name+".req.json"), ex.req)
		writeFile(t, filepath.Join(dir, name+".res.http"), ex.res)
	}
	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func ok200(body string) string {
	return "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n" + body
}

const (
	searchReq = `{"method": "POST", "path": "/v1/search", "body": {"q": "go", "limit": 2}}`
	goodHits  = `{"hits": [{"title": "Go", "link": "https://toy.example/items/go1", "snippet": "Go."}], "took_ms": 3}`
)

// validate runs ValidateCassettes on a spy and returns what it reported.
func validate(t *testing.T, dir, spec string) []string {
	t.Helper()
	spy := toolsettest.NewSpy(t)
	spy.Run(func(tb testing.TB) { toolsettest.ValidateCassettes(tb, dir, spec) })
	return spy.Errs()
}

// wantErr fails t unless errs is one error containing every part, or, with
// no parts, no error.
func wantErr(t *testing.T, errs []string, parts ...string) {
	t.Helper()
	if len(parts) == 0 {
		if len(errs) > 0 {
			t.Errorf("errors %q, want none", errs)
		}
		return
	}
	for _, p := range parts {
		if len(errs) != 1 || !strings.Contains(errs[0], p) {
			t.Errorf("errors %q, want one containing %q", errs, parts)
			return
		}
	}
}

// TestValidateCassettes: against the toy's OpenAPI spec (3.1: $ref, allOf,
// additionalProperties, a path template, a default response), conforming
// cassettes pass, and each violation fails with the cassette's name, the JSON
// path and the rule. A cassette the spec has no operation or status for fails;
// it is not skipped.
func TestValidateCassettes(t *testing.T) {
	t.Parallel()
	spec := filepath.Join("testdata", "openapi.json")
	for _, tc := range []struct {
		name  string
		extra map[string]exchange
		want  []string // parts of the one error expected; nil: none
	}{
		{name: "conforming"},
		{
			name: "conforming 400 and default",
			extra: map[string]exchange{
				"bad_q":   {`{"method": "POST", "path": "/v1/search", "body": {"q": "x"}}`, "HTTP/1.1 400 Bad Request\r\n\r\n" + `{"error": {"message": "q is too short"}}`},
				"no_item": {`{"method": "GET", "path": "/v1/items/nope"}`, "HTTP/1.1 404 Not Found\r\n\r\n" + `{"error": {"message": "no such item"}}`},
			},
		},
		{
			name:  "wrong type behind $ref",
			extra: map[string]exchange{"x": {searchReq, ok200(`{"hits": [{"title": "Go", "link": 7, "snippet": "Go."}], "took_ms": 3}`)}},
			want:  []string{"cassette x:", "response 200 at $.hits[*].link:", "type: 7", `want "string"`},
		},
		{
			name:  "allOf part not met",
			extra: map[string]exchange{"x": {searchReq, ok200(`{"hits": [{"title": "Go", "link": "https://toy.example/"}], "took_ms": 3}`)}},
			want:  []string{"cassette x:", "response 200 at $.hits[*]:", "required", "snippet"},
		},
		{
			name:  "undeclared field",
			extra: map[string]exchange{"x": {searchReq, ok200(`{"hits": [], "took_ms": 3, "cursor": "abc"}`)}},
			want:  []string{"cassette x:", "response 200 at $:", "additional properties", "cursor"},
		},
		{
			name:  "missing field",
			extra: map[string]exchange{"x": {searchReq, ok200(`{"hits": []}`)}},
			want:  []string{"cassette x:", "response 200 at $:", "required", "took_ms"},
		},
		{
			name:  "status picks the schema",
			extra: map[string]exchange{"x": {searchReq, "HTTP/1.1 400 Bad Request\r\n\r\n" + goodHits}},
			want:  []string{"cassette x:", "response 400 at $:", "required", "error"},
		},
		{
			name:  "default picks the schema",
			extra: map[string]exchange{"x": {`{"method": "GET", "path": "/v1/items/go9"}`, "HTTP/1.1 404 Not Found\r\n\r\n" + `{"id": "go9", "name": "n", "text": "t"}`}},
			want:  []string{"cassette x:", "response 404 at $:", "required", "error"},
		},
		{
			name:  "status not in the spec",
			extra: map[string]exchange{"x": {searchReq, "HTTP/1.1 503 Service Unavailable\r\n\r\n" + `{"error": {"message": "down"}}`}},
			want:  []string{"cassette x:", "the spec has no 503 response", "POST /v1/search"},
		},
		{
			name:  "unknown path",
			extra: map[string]exchange{"x": {`{"method": "POST", "path": "/v1/answer", "body": {"q": "go"}}`, ok200(goodHits)}},
			want:  []string{"cassette x:", "POST /v1/answer is not an operation in"},
		},
		{
			name:  "unknown method",
			extra: map[string]exchange{"x": {`{"method": "DELETE", "path": "/v1/items/go1"}`, ok200(`{}`)}},
			want:  []string{"cassette x:", "DELETE /v1/items/go1 is not an operation in"},
		},
		{
			name:  "request body",
			extra: map[string]exchange{"x": {`{"method": "POST", "path": "/v1/search", "body": {"q": "go", "limit": 50}}`, ok200(goodHits)}},
			want:  []string{"cassette x:", "request body at $.limit:", "maximum"},
		},
		{
			name:  "request body missing",
			extra: map[string]exchange{"x": {`{"method": "POST", "path": "/v1/search"}`, ok200(goodHits)}},
			want:  []string{"cassette x:", "request: the spec requires a body"},
		},
		{
			name:  "response not JSON",
			extra: map[string]exchange{"x": {searchReq, ok200(`<html>`)}},
			want:  []string{"cassette x:", "response 200: not JSON"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wantErr(t, validate(t, toyCassettes(t, tc.extra), spec), tc.want...)
		})
	}
}

// openapi30 is an OpenAPI 3.0 spec: nullable, and a boolean exclusiveMinimum.
const openapi30 = `{
  "openapi": "3.0.3",
  "paths": {"/v1/search": {"post": {"responses": {"200": {"content": {"application/json": {"schema": {
    "type": "object",
    "required": ["hits", "took_ms"],
    "properties": {
      "hits": {"type": "array", "items": {"type": "string"}},
      "summary": {"type": "string", "nullable": true},
      "kind": {"type": "string", "enum": ["web", "news"], "nullable": true},
      "took_ms": {"type": "integer", "minimum": 0, "exclusiveMinimum": true},
      "cursor": {"oneOf": [{"type": "string"}, {"type": "number"}, {"type": "integer"}]}
    }
  }}}}}}}}
}`

// TestValidateCassettesOpenAPI30: a nullable field may be null or absent, and
// nowhere else is null allowed; a boolean exclusiveMinimum excludes the
// minimum; a oneOf takes exactly one alternative, and says why none fits.
func TestValidateCassettesOpenAPI30(t *testing.T) {
	t.Parallel()
	spec := filepath.Join(t.TempDir(), "openapi.json")
	writeFile(t, spec, openapi30)
	req := `{"method": "POST", "path": "/v1/search"}`
	for _, tc := range []struct {
		name, body string
		want       []string
	}{
		{name: "nullable is null", body: `{"hits": [], "summary": null, "kind": null, "took_ms": 1}`},
		{name: "nullable is absent", body: `{"hits": [], "took_ms": 1}`},
		{name: "nullable has a value", body: `{"hits": ["a"], "summary": "s", "kind": "news", "took_ms": 1}`},
		{name: "nullable enum keeps its values", body: `{"hits": [], "kind": "blog", "took_ms": 1}`, want: []string{"at $.kind:", "enum"}},
		{name: "not nullable", body: `{"hits": [null], "took_ms": 1}`, want: []string{"at $.hits[*]:", "type: null"}},
		{name: "exclusive minimum", body: `{"hits": [], "took_ms": 0}`, want: []string{"at $.took_ms:", "exclusiveMinimum"}},
		{name: "oneOf fits one", body: `{"hits": [], "took_ms": 1, "cursor": "c"}`},
		{name: "oneOf fits none", body: `{"hits": [], "took_ms": 1, "cursor": true}`, want: []string{"at $.cursor:", `want "string"`, `want "number"`, `want "integer"`}},
		{name: "oneOf fits two", body: `{"hits": [], "took_ms": 1, "cursor": 2}`, want: []string{"at $.cursor:", "oneOf: validated against both"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, "x.req.json"), req)
			writeFile(t, filepath.Join(dir, "x.res.http"), ok200(tc.body))
			wantErr(t, validate(t, dir, spec), tc.want...)
		})
	}
}

// TestValidateCassettesRefusesSpecs: a spec the check would misread fails
// the test instead of passing every cassette.
func TestValidateCassettesRefusesSpecs(t *testing.T) {
	t.Parallel()
	op := func(schema string) string {
		return `"paths": {"/v1/search": {"post": {"responses": {"200": {"content": {"application/json": {"schema": ` + schema + `}}}}}}}`
	}
	for _, tc := range []struct{ name, spec, want string }{
		{"unknown keyword", `{"openapi": "3.1.0", ` + op(`{"type": "object", "minProprties": 1}`) + `}`, `schema keyword "minProprties" is not supported`},
		{"nullable in 3.1", `{"openapi": "3.1.0", ` + op(`{"type": "string", "nullable": true}`) + `}`, `schema keyword "nullable" is not supported`},
		{"nullable without type", `{"openapi": "3.0.3", ` + op(`{"allOf": [{"type": "string"}], "nullable": true}`) + `}`, "nullable without a type"},
		{"external $ref", `{"openapi": "3.1.0", ` + op(`{"$ref": "other.json#/Hit"}`) + `}`, "only #/components/schemas/ refs"},
		{"dangling $ref", `{"openapi": "3.1.0", ` + op(`{"$ref": "#/components/schemas/Nope"}`) + `}`, "Nope"},
		{"version 2", `{"swagger": "2.0", ` + op(`{}`) + `}`, "want 3.0.x or 3.1.x"},
		{"YAML", "openapi: 3.1.0\n", "not JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec := filepath.Join(t.TempDir(), "openapi.json")
			writeFile(t, spec, tc.spec)
			wantErr(t, validate(t, toyCassettes(t, nil), spec), tc.want)
		})
	}
}

// TestCheckRequest: a request body is checked against its operation's
// request schema, path template included.
func TestCheckRequest(t *testing.T) {
	t.Parallel()
	spec, err := toolsettest.LoadOpenAPI(filepath.Join("testdata", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path, body string
		want               string // "" for no error
	}{
		{"POST", "/v1/search", `{"q": "go", "limit": 2}`, ""},
		{"GET", "/v1/items/go1", ``, ""},
		{"POST", "/v1/search", `{"q": "go", "lmit": 2}`, `request body at $: unexpected additional properties ["lmit"]`},
		{"POST", "/v1/search", ``, "the spec requires a body"},
		{"GET", "/v1/items/go1", `{"id": "go1"}`, "takes no JSON body"},
		{"POST", "/v1/items/go1", `{}`, "POST /v1/items/go1 is not an operation"},
	} {
		err := spec.CheckRequest(tc.method, tc.path, []byte(tc.body))
		if got := fmt.Sprint(err); (tc.want == "") != (err == nil) || !strings.Contains(got, tc.want) {
			t.Errorf("%s %s %s: error %v, want %q", tc.method, tc.path, tc.body, err, tc.want)
		}
	}
}
