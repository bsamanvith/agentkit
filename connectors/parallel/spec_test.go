package parallel_test

// Offline checks against Parallel's own OpenAPI spec (the /v1/search and
// /v1/extract parts, vendored in testdata/openapi.json): every cassette, and
// every request the connector sends in any test, must fit it. This matters
// most while the cassettes are synthetic (hand-built from the spec).

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

// endpointSchemas are one endpoint's request schema and response schemas by
// status code.
type endpointSchemas struct {
	request   *jsonschema.Resolved
	responses map[string]*jsonschema.Resolved
}

var loadSpec = sync.OnceValues(func() (map[string]endpointSchemas, error) {
	raw, err := os.ReadFile(filepath.Join("testdata", "openapi.json"))
	if err != nil {
		return nil, err
	}
	// OpenAPI refers to components; JSON Schema 2020-12 to $defs.
	raw = bytes.ReplaceAll(raw, []byte("#/components/schemas/"), []byte("#/$defs/"))
	type media struct {
		Content map[string]struct {
			Schema struct {
				Ref string `json:"$ref"`
			} `json:"schema"`
		} `json:"content"`
	}
	var spec struct {
		Paths map[string]struct {
			Post struct {
				RequestBody media            `json:"requestBody"`
				Responses   map[string]media `json:"responses"`
			} `json:"post"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, err
	}
	// resolve builds a schema for one $ref, with every component as $defs.
	// Each gets its own copy: a resolved schema must not be shared.
	resolve := func(m media) (*jsonschema.Resolved, error) {
		var doc struct {
			Components struct {
				Schemas map[string]*jsonschema.Schema `json:"schemas"`
			} `json:"components"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, err
		}
		root := &jsonschema.Schema{Ref: m.Content["application/json"].Schema.Ref, Defs: doc.Components.Schemas}
		return root.Resolve(nil)
	}
	out := map[string]endpointSchemas{}
	for path, p := range spec.Paths {
		req, err := resolve(p.Post.RequestBody)
		if err != nil {
			return nil, err
		}
		es := endpointSchemas{request: req, responses: map[string]*jsonschema.Resolved{}}
		for code, m := range p.Post.Responses {
			if es.responses[code], err = resolve(m); err != nil {
				return nil, err
			}
		}
		out[path] = es
	}
	return out, nil
})

func spec(t *testing.T) map[string]endpointSchemas {
	t.Helper()
	s, err := loadSpec()
	if err != nil {
		t.Fatalf("load testdata/openapi.json: %v", err)
	}
	return s
}

// validate checks a JSON document against a resolved schema.
func validate(rs *jsonschema.Resolved, doc []byte) error {
	var v any
	if err := json.Unmarshal(doc, &v); err != nil {
		return err
	}
	return rs.Validate(v)
}

// TestCassettesMatchSpec: every cassette's request fits the endpoint's
// request schema and its response the response schema for its status.
func TestCassettesMatchSpec(t *testing.T) {
	t.Parallel()
	schemas := spec(t)
	dir := filepath.Join("testdata", "cassettes")
	reqs, err := filepath.Glob(filepath.Join(dir, "*.req.json"))
	must(t, err)
	if len(reqs) == 0 {
		t.Fatal("no cassettes")
	}
	var synthetic []string
	for _, reqPath := range reqs {
		name := strings.TrimSuffix(filepath.Base(reqPath), ".req.json")
		raw, err := os.ReadFile(reqPath)
		must(t, err)
		var c struct {
			Synthetic bool            `json:"synthetic"`
			Method    string          `json:"method"`
			Path      string          `json:"path"`
			Body      json.RawMessage `json:"body"`
		}
		must(t, json.Unmarshal(raw, &c))
		if c.Synthetic {
			synthetic = append(synthetic, name)
		}
		es, ok := schemas[c.Path]
		if !ok || c.Method != http.MethodPost {
			t.Errorf("%s: %s %s is not an endpoint in the spec", name, c.Method, c.Path)
			continue
		}
		if err := validate(es.request, c.Body); err != nil {
			t.Errorf("%s: request does not fit the spec: %v", name, err)
		}
		resRaw, err := os.ReadFile(filepath.Join(dir, name+".res.http"))
		must(t, err)
		resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(resRaw)), nil)
		must(t, err)
		body, err := io.ReadAll(resp.Body)
		must(t, err)
		rs, ok := es.responses[strconv.Itoa(resp.StatusCode)]
		if !ok {
			t.Errorf("%s: the spec has no %d response for %s", name, resp.StatusCode, c.Path)
			continue
		}
		if err := validate(rs, body); err != nil {
			t.Errorf("%s: response does not fit the spec: %v", name, err)
		}
	}
	slices.Sort(synthetic)
	if len(synthetic) > 0 {
		t.Logf("synthetic cassettes (not recorded from the real API; re-record them): %v", synthetic)
	}
}

// TestSpecCheckRejects: the spec check is not vacuous; documents that break
// the schemas fail it.
func TestSpecCheckRejects(t *testing.T) {
	t.Parallel()
	schemas := spec(t)
	for _, tc := range []struct {
		name, path, kind, doc string
	}{
		{"extract option top-level", "/v1/extract", "request", `{"urls": ["https://a.example/"], "full_content": true}`},
		{"max_results top-level", "/v1/search", "request", `{"search_queries": ["q"], "max_results": 5}`},
		{"no search_queries", "/v1/search", "request", `{"objective": "o"}`},
		{"bad mode", "/v1/search", "request", `{"search_queries": ["q"], "mode": "ultra"}`},
		{"result without excerpts", "/v1/search", "200", `{"search_id": "s", "session_id": "x", "results": [{"url": "u"}]}`},
		{"error without content", "/v1/extract", "200", `{"extract_id": "e", "session_id": "x", "results": [], "errors": [{"url": "u", "error_type": "t", "http_status_code": null}]}`},
	} {
		es := schemas[tc.path]
		rs := es.request
		if tc.kind != "request" {
			rs = es.responses[tc.kind]
		}
		if err := validate(rs, []byte(tc.doc)); err == nil {
			t.Errorf("%s: the spec check accepted %s", tc.name, tc.doc)
		}
	}
}
