package toolsettest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

// ValidateCassettes checks every cassette in dir against the OpenAPI spec at
// specPath: a vendored copy of the provider's published spec, in JSON
// (OpenAPI 3.0 or 3.1), cut down to the operations the connector uses. For
// each cassette it finds the operation by method and path, then checks the
// request body against the operation's request body schema and the response
// body against its schema for the response's status (the exact code, else
// 4XX-style, else default).
//
// A violation fails tb, naming the cassette, the JSON path in the body ([*]
// for any array element) and the rule that failed; validation stops at a
// body's first violation. A cassette whose operation or status the spec does
// not have fails too: it is never skipped.
//
// The schemas are JSON Schema 2020-12 as OpenAPI 3.1 uses it, plus 3.0's
// nullable and boolean exclusiveMinimum/exclusiveMaximum. A schema keyword the
// check does not know fails the spec rather than being ignored, so a
// re-vendored spec cannot pass by being misread. format is an annotation: it
// is not checked. A oneOf no alternative fits is reported as an anyOf, with
// each alternative's failure.
//
// [RunConnector] runs it as check cassettes.openapi when [Connector.OpenAPI]
// is set.
func ValidateCassettes(tb testing.TB, dir, specPath string) {
	tb.Helper()
	spec, err := LoadOpenAPI(specPath)
	if err != nil {
		tb.Fatalf("toolsettest: %v", err)
	}
	cs, err := loadCassettes(dir)
	if err != nil {
		tb.Fatalf("toolsettest: %v", err)
	}
	if len(cs) == 0 {
		tb.Fatalf("toolsettest: no cassettes in %s", dir)
	}
	for _, c := range cs {
		for _, e := range spec.check(c) {
			tb.Errorf("toolsettest: cassette %s: %s", c.name, e)
		}
	}
}

// OpenAPI is a vendored OpenAPI spec, loaded by [LoadOpenAPI], to check
// cassettes ([ValidateCassettes]) and requests ([OpenAPI.CheckRequest])
// against. It is safe for concurrent use.
type OpenAPI struct {
	file string
	ops  map[string]*openapiOp // by "METHOD /path/{param}"
}

type openapiOp struct {
	method, path string
	// request is the JSON request body's schema, nil if the spec declares
	// none; requestRequired is requestBody.required.
	request         *jsonschema.Resolved
	requestRequired bool
	// responses are by status ("200", "4XX", "default"); a nil schema means
	// the spec declares no JSON body for it.
	responses map[string]*jsonschema.Resolved
}

var httpMethods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

// LoadOpenAPI loads the OpenAPI spec in file (JSON, OpenAPI 3.0 or 3.1) and
// resolves its operations' schemas, under the rules of [ValidateCassettes].
// A connector's test loads it once (sync.OnceValues) to check every request
// its rig sends.
func LoadOpenAPI(file string) (*OpenAPI, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("OpenAPI spec: %w", err)
	}
	var doc struct {
		OpenAPI    string                               `json:"openapi"`
		Paths      map[string]map[string]map[string]any `json:"paths"`
		Components struct {
			Schemas map[string]any `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("OpenAPI spec %s: not JSON: %w (a YAML spec is cut to JSON: make openapi-specs)", file, err)
	}
	var tr translator
	switch {
	case strings.HasPrefix(doc.OpenAPI, "3.0."):
		tr.v30 = true
	case strings.HasPrefix(doc.OpenAPI, "3.1."):
	default:
		return nil, fmt.Errorf("OpenAPI spec %s: version %q: want 3.0.x or 3.1.x", file, doc.OpenAPI)
	}
	defs := map[string]any{}
	for name, s := range doc.Components.Schemas {
		if defs[name], err = tr.schema(s, "#/components/schemas/"+name); err != nil {
			return nil, fmt.Errorf("OpenAPI spec %s: %w", file, err)
		}
	}
	spec := &OpenAPI{file: file, ops: map[string]*openapiOp{}}
	for path, item := range doc.Paths {
		for method, op := range item {
			if !slices.Contains(httpMethods, method) {
				continue // parameters, summary, ...
			}
			o, err := tr.operation(strings.ToUpper(method), path, op, defs)
			if err != nil {
				return nil, fmt.Errorf("OpenAPI spec %s: %w", file, err)
			}
			spec.ops[o.method+" "+path] = o
		}
	}
	if len(spec.ops) == 0 {
		return nil, fmt.Errorf("OpenAPI spec %s has no operations", file)
	}
	return spec, nil
}

func (tr translator) operation(method, path string, op map[string]any, defs map[string]any) (*openapiOp, error) {
	o := &openapiOp{method: method, path: path, responses: map[string]*jsonschema.Resolved{}}
	at := method + " " + path
	if rb, ok := op["requestBody"].(map[string]any); ok {
		o.requestRequired, _ = rb["required"].(bool)
		s, err := tr.content(rb, at+" requestBody", defs)
		if err != nil {
			return nil, err
		}
		o.request = s
	}
	responses, _ := op["responses"].(map[string]any)
	for status, r := range responses {
		rm, ok := r.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s response %s is not an object", at, status)
		}
		s, err := tr.content(rm, at+" response "+status, defs)
		if err != nil {
			return nil, err
		}
		o.responses[strings.ToUpper(status)] = s
	}
	return o, nil
}

// content resolves the JSON schema of a request body or response object, nil
// if it has no JSON content.
func (tr translator) content(obj map[string]any, at string, defs map[string]any) (*jsonschema.Resolved, error) {
	if _, ok := obj["$ref"]; ok {
		return nil, fmt.Errorf("%s: $ref to a request body or response is not supported (cut the spec with it inlined)", at)
	}
	content, _ := obj["content"].(map[string]any)
	for _, mt := range slices.Sorted(maps.Keys(content)) {
		base, _, _ := strings.Cut(mt, ";")
		if base != "application/json" && !strings.HasSuffix(base, "+json") {
			continue
		}
		m, _ := content[mt].(map[string]any)
		s, ok := m["schema"]
		if !ok {
			return nil, nil
		}
		root, err := tr.schema(s, at)
		if err != nil {
			return nil, err
		}
		// A schema of its own, with the components it uses as $defs: a
		// resolved schema must not share nodes with another, and resolving
		// every component for every status is slow on a large spec.
		used, err := reachable(root, defs)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", at, err)
		}
		b, err := json.Marshal(map[string]any{"allOf": []any{root}, "$defs": used})
		if err != nil {
			return nil, err
		}
		var js jsonschema.Schema
		if err := json.Unmarshal(b, &js); err != nil {
			return nil, fmt.Errorf("%s: %w", at, err)
		}
		rs, err := js.Resolve(nil)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", at, err)
		}
		return rs, nil
	}
	return nil, nil
}

// reachable returns the components schema s refers to, directly or not.
func reachable(s any, defs map[string]any) (map[string]any, error) {
	used := map[string]any{}
	var walk func(v any) error
	walk = func(v any) error {
		switch v := v.(type) {
		case map[string]any:
			if ref, ok := v["$ref"].(string); ok {
				name := strings.TrimPrefix(ref, "#/$defs/")
				d, ok := defs[name]
				if !ok {
					return fmt.Errorf("$ref %q: no such component schema", "#/components/schemas/"+name)
				}
				if _, seen := used[name]; !seen {
					used[name] = d
					if err := walk(d); err != nil {
						return err
					}
				}
			}
			for _, x := range v {
				if err := walk(x); err != nil {
					return err
				}
			}
		case []any:
			for _, x := range v {
				if err := walk(x); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return used, walk(s)
}

// translator turns an OpenAPI schema into JSON Schema 2020-12, refusing what
// it does not know.
type translator struct{ v30 bool }

// Schema keywords by what the translator does with them. Subschema keywords
// take one schema, a list or a map of them.
var (
	subschemaKeywords = map[string]string{
		"items": "one", "additionalProperties": "one", "not": "one", "propertyNames": "one",
		"contains": "one", "if": "one", "then": "one", "else": "one",
		"unevaluatedItems": "one", "unevaluatedProperties": "one",
		"allOf": "list", "anyOf": "list", "oneOf": "list", "prefixItems": "list",
		"properties": "map", "patternProperties": "map", "dependentSchemas": "map",
	}
	assertionKeywords = []string{
		"type", "enum", "const", "multipleOf", "maximum", "exclusiveMaximum", "minimum", "exclusiveMinimum",
		"maxLength", "minLength", "pattern", "maxItems", "minItems", "uniqueItems", "maxContains", "minContains",
		"maxProperties", "minProperties", "required", "dependentRequired",
	}
	// Annotations JSON Schema keeps; openapiAnnotations (and x- extensions)
	// are OpenAPI's, dropped because jsonschema-go would not know them.
	annotationKeywords = []string{"title", "description", "default", "deprecated", "readOnly", "writeOnly", "examples", "format", "$comment", "contentEncoding", "contentMediaType"}
	openapiAnnotations = []string{"example", "discriminator", "xml", "externalDocs"}
)

func (tr translator) schema(s any, at string) (any, error) {
	switch s := s.(type) {
	case bool:
		if tr.v30 {
			return nil, fmt.Errorf("%s: boolean schema in an OpenAPI 3.0 spec", at)
		}
		return s, nil
	case map[string]any:
		return tr.object(s, at)
	default:
		return nil, fmt.Errorf("%s: schema is not an object", at)
	}
}

func (tr translator) object(s map[string]any, at string) (map[string]any, error) {
	out := map[string]any{}
	for _, k := range slices.Sorted(maps.Keys(s)) {
		v := s[k]
		switch {
		case k == "$ref":
			ref, _ := v.(string)
			name, ok := strings.CutPrefix(ref, "#/components/schemas/")
			if !ok {
				return nil, fmt.Errorf("%s: $ref %q: only #/components/schemas/ refs are supported", at, ref)
			}
			out[k] = "#/$defs/" + name
		case subschemaKeywords[k] != "":
			sub, err := tr.subschemas(k, v, at)
			if err != nil {
				return nil, err
			}
			out[k] = sub
		case slices.Contains(assertionKeywords, k), slices.Contains(annotationKeywords, k):
			out[k] = v
		case k == "nullable" && tr.v30:
			// Handled below, once type and enum are in.
		case slices.Contains(openapiAnnotations, k), strings.HasPrefix(k, "x-"):
		default:
			return nil, fmt.Errorf("%s: schema keyword %q is not supported", at, k)
		}
	}
	if tr.v30 {
		if err := from30(out, s, at); err != nil {
			return nil, err
		}
	}
	// jsonschema-go says why no alternative of an anyOf fits, but not of a
	// oneOf. allOf [anyOf X, oneOf X] means oneOf X and reports the anyOf's
	// reasons when none fits.
	if alts, ok := out["oneOf"]; ok {
		delete(out, "oneOf")
		all, _ := out["allOf"].([]any)
		out["allOf"] = append(all, map[string]any{"anyOf": alts}, map[string]any{"oneOf": alts})
	}
	return out, nil
}

func (tr translator) subschemas(k string, v any, at string) (any, error) {
	at += "/" + k
	switch subschemaKeywords[k] {
	case "list":
		l, ok := v.([]any)
		if !ok {
			return nil, fmt.Errorf("%s: not a list", at)
		}
		out := make([]any, len(l))
		for i, s := range l {
			var err error
			if out[i], err = tr.schema(s, at+"/"+strconv.Itoa(i)); err != nil {
				return nil, err
			}
		}
		return out, nil
	case "map":
		m, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: not an object", at)
		}
		out := map[string]any{}
		for name, s := range m {
			var err error
			if out[name], err = tr.schema(s, at+"/"+name); err != nil {
				return nil, err
			}
		}
		return out, nil
	default:
		if b, ok := v.(bool); ok && k == "additionalProperties" {
			return b, nil // a boolean here is fine in 3.0 too
		}
		return tr.schema(v, at)
	}
}

// from30 applies OpenAPI 3.0's own keywords to out, the translated schema:
// nullable: true adds null to the type (and to the enum); a boolean
// exclusiveMinimum or exclusiveMaximum makes minimum or maximum exclusive.
func from30(out, s map[string]any, at string) error {
	if n, ok := s["nullable"]; ok {
		nullable, isBool := n.(bool)
		switch {
		case !isBool:
			return fmt.Errorf("%s: nullable is not a boolean", at)
		case !nullable:
		case out["type"] == nil:
			return fmt.Errorf("%s: nullable without a type is not supported", at)
		default:
			t, ok := out["type"].(string)
			if !ok {
				return fmt.Errorf("%s: type %v is not a string", at, out["type"])
			}
			out["type"] = []any{t, "null"}
			if e, ok := out["enum"].([]any); ok && !slices.Contains(e, nil) {
				out["enum"] = append(slices.Clone(e), nil)
			}
		}
	}
	for ex, bound := range map[string]string{"exclusiveMinimum": "minimum", "exclusiveMaximum": "maximum"} {
		b, ok := out[ex].(bool)
		if !ok {
			continue
		}
		delete(out, ex)
		if b {
			if out[bound] == nil {
				return fmt.Errorf("%s: %s without %s", at, ex, bound)
			}
			out[ex] = out[bound]
			delete(out, bound)
		}
	}
	return nil
}

// operation returns the spec's operation for method and path; a path
// template segment ({id}) matches any one segment.
func (s *OpenAPI) operation(method, path string) *openapiOp {
	if o, ok := s.ops[method+" "+path]; ok {
		return o
	}
	segs := strings.Split(path, "/")
	for _, key := range slices.Sorted(maps.Keys(s.ops)) {
		o := s.ops[key]
		tsegs := strings.Split(o.path, "/")
		if o.method != method || len(tsegs) != len(segs) {
			continue
		}
		match := true
		for i, t := range tsegs {
			isParam := strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}")
			if (!isParam && t != segs[i]) || (isParam && segs[i] == "") {
				match = false
				break
			}
		}
		if match {
			return o
		}
	}
	return nil
}

// check returns what is wrong with cassette c under the spec.
func (s *OpenAPI) check(c cassette) []string {
	o := s.operation(c.req.Method, c.req.Path)
	if o == nil {
		return []string{fmt.Sprintf("%s %s is not an operation in %s", c.req.Method, c.req.Path, s.file)}
	}
	errs := o.checkRequest(c.req.Body)
	status, body, err := parseResponse(c.res)
	if err != nil {
		return append(errs, err.Error())
	}
	key := strconv.Itoa(status)
	rs, ok := o.responses[key]
	for _, k := range []string{key[:1] + "XX", "DEFAULT"} {
		if !ok {
			key = k
			rs, ok = o.responses[k]
		}
	}
	what := fmt.Sprintf("response %d", status)
	switch {
	case !ok:
		errs = append(errs, fmt.Sprintf("%s: the spec has no %d response (nor default) for %s %s", what, status, o.method, o.path))
	case rs == nil && len(bytes.TrimSpace(body)) > 0:
		errs = append(errs, fmt.Sprintf("%s: the spec declares no JSON body for %s %s %s", what, o.method, o.path, strings.ToLower(key)))
	case rs != nil:
		errs = append(errs, validateBody(what, rs, body)...)
	}
	return errs
}

// CheckRequest checks a request's JSON body (nil for none) against the
// spec's operation for method and path. The error names the JSON path and
// the rule, as [ValidateCassettes] does.
func (s *OpenAPI) CheckRequest(method, path string, body []byte) error {
	o := s.operation(method, path)
	if o == nil {
		return fmt.Errorf("%s %s is not an operation in %s", method, path, s.file)
	}
	if errs := o.checkRequest(body); len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (o *openapiOp) checkRequest(body []byte) []string {
	hasBody := len(bytes.TrimSpace(body)) > 0
	switch {
	case o.request == nil && hasBody:
		return []string{fmt.Sprintf("request: %s %s takes no JSON body in the spec", o.method, o.path)}
	case o.request != nil && !hasBody && o.requestRequired:
		return []string{"request: the spec requires a body"}
	case o.request != nil && hasBody:
		return validateBody("request body", o.request, body)
	}
	return nil
}

func parseResponse(raw []byte) (int, []byte, error) {
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(raw)), nil)
	if err != nil {
		return 0, nil, fmt.Errorf("response: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, fmt.Errorf("response: %w", err)
	}
	return resp.StatusCode, body, nil
}

func validateBody(what string, rs *jsonschema.Resolved, body []byte) []string {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return []string{fmt.Sprintf("%s: not JSON: %v", what, err)}
	}
	if err := rs.Validate(v); err != nil {
		path, rule := describeSchemaError(err)
		return []string{fmt.Sprintf("%s at %s: %s", what, path, rule)}
	}
	return nil
}

// describeSchemaError splits a jsonschema-go error into the JSON path of the
// failing value and the rule. Its errors read "validating <schema location>:
// ... : <rule>"; the locations nest as the validator descends, and each step
// into properties/<name>, items or additionalProperties is a step into the
// value. A $ref jumps to a location that does not extend the last one.
func describeSchemaError(err error) (path, rule string) {
	msg := err.Error()
	path, prev := "$", ""
	for {
		rest, ok := strings.CutPrefix(msg, "validating ")
		if !ok {
			break
		}
		loc, after, ok := strings.Cut(rest, ": ")
		if !ok {
			break
		}
		msg = after
		if loc == "root" {
			prev = ""
			continue
		}
		if sub, ok := strings.CutPrefix(loc, prev+"/"); ok {
			path += instanceSteps(strings.Split(sub, "/"))
		}
		prev = loc
	}
	// jsonschema-go prints a JSON null as the zero reflect.Value.
	return path, strings.ReplaceAll(msg, "<invalid reflect.Value>", "null")
}

// instanceSteps turns the schema location tokens of one descent into JSON
// path steps.
func instanceSteps(toks []string) string {
	var b strings.Builder
	for i := 0; i < len(toks); i++ {
		switch toks[i] {
		case "properties":
			if i+1 < len(toks) {
				i++
				b.WriteString("." + strings.NewReplacer("~1", "/", "~0", "~").Replace(toks[i]))
			}
		case "patternProperties":
			i++
			b.WriteString(".*")
		case "allOf", "anyOf", "oneOf", "dependentSchemas":
			i++ // which alternative or dependency: the same value
		case "additionalProperties", "unevaluatedProperties":
			b.WriteString(".*")
		case "items", "unevaluatedItems", "contains":
			b.WriteString("[*]")
		case "prefixItems":
			if i+1 < len(toks) {
				i++
				b.WriteString("[" + toks[i] + "]")
			}
		}
	}
	return b.String()
}

// openapiCheck is check cassettes.openapi.
func (s *suite) openapiCheck(tb testing.TB) {
	tb.Helper()
	if s.c.OpenAPI == "" {
		tb.Fatal("toolsettest: check cassettes.openapi needs Connector.OpenAPI, the provider's vendored OpenAPI spec (testdata/openapi.json); a connector whose provider publishes none skips it with that reason")
	}
	ValidateCassettes(tb, s.c.Cassettes, s.c.OpenAPI)
}
