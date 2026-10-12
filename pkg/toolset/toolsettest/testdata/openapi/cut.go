//go:build ignore

// cut vendors the part of a provider's OpenAPI spec a connector uses, for
// toolsettest.ValidateCassettes: the operations named by -op (request body and
// responses, JSON content only, $refs to request bodies and responses inlined)
// and every component schema they reference. Schemas are copied as they are.
// It reads the spec from files: YAML, JSON, or Markdown pages holding
// ```yaml blocks (Tavily's docs), merged. make openapi-specs runs it:
//
//	go run ./pkg/toolset/toolsettest/testdata/openapi/cut.go -o connectors/exa/testdata/openapi.json \
//		-source https://exa.ai/docs/exa-spec.yaml -op 'POST /search' -op 'POST /contents' exa-spec.yaml
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	yaml "go.yaml.in/yaml/v4"
)

type list []string

func (l *list) String() string     { return strings.Join(*l, ", ") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

func main() {
	var ops, sources list
	out := flag.String("o", "", "output file")
	flag.Var(&ops, "op", "an operation to keep, as 'METHOD /path' (repeat)")
	flag.Var(&sources, "source", "where the spec was fetched from, for the comment (repeat)")
	flag.Parse()
	if *out == "" || len(ops) == 0 || flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: cut -o out.json -source URL -op 'POST /path' spec-file...")
		os.Exit(2)
	}
	if err := run(*out, ops, sources, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "cut:", err)
		os.Exit(1)
	}
}

func run(out string, ops, sources, files []string) error {
	spec := map[string]any{}
	for _, f := range files {
		docs, err := read(f)
		if err != nil {
			return err
		}
		for _, d := range docs {
			if err := merge(spec, d, ""); err != nil {
				return fmt.Errorf("%s: %w", f, err)
			}
		}
	}
	components, _ := spec["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	keep := map[string]bool{}
	var walk func(v any) error
	walk = func(v any) error {
		switch v := v.(type) {
		case map[string]any:
			if ref, ok := v["$ref"].(string); ok {
				name, found := strings.CutPrefix(ref, "#/components/schemas/")
				if !found {
					return fmt.Errorf("unexpected $ref %s", ref)
				}
				if s, ok := schemas[name]; !ok {
					return fmt.Errorf("$ref %s: no such schema", ref)
				} else if !keep[name] {
					keep[name] = true
					if err := walk(s); err != nil {
						return err
					}
				}
			}
			for _, k := range slices.Sorted(maps.Keys(v)) {
				if err := walk(v[k]); err != nil {
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
	paths, _ := spec["paths"].(map[string]any)
	cutPaths := map[string]any{}
	for _, op := range ops {
		method, path, ok := strings.Cut(op, " ")
		item, _ := paths[path].(map[string]any)
		o, _ := item[strings.ToLower(method)].(map[string]any)
		if !ok || o == nil {
			return fmt.Errorf("operation %q is not in the spec", op)
		}
		c, err := cutOperation(o, components)
		if err != nil {
			return fmt.Errorf("%s: %w", op, err)
		}
		if err := walk(c); err != nil {
			return fmt.Errorf("%s: %w", op, err)
		}
		if cutPaths[path] == nil {
			cutPaths[path] = map[string]any{}
		}
		cutPaths[path].(map[string]any)[strings.ToLower(method)] = c
	}
	cutSchemas := map[string]any{}
	for name := range keep {
		cutSchemas[name] = schemas[name]
	}
	info, _ := spec["info"].(map[string]any)
	doc := map[string]any{
		"_comment": fmt.Sprintf("Cut by make openapi-specs on %s from %s: the operations this connector uses and the schemas they reference, as published. Do not edit by hand: re-run it and review the diff.",
			time.Now().Format(time.DateOnly), strings.Join(sources, " and ")),
		"openapi":    spec["openapi"],
		"info":       map[string]any{"title": info["title"], "version": info["version"]},
		"paths":      cutPaths,
		"components": map[string]any{"schemas": cutSchemas},
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%s: %d operations, schemas: %s\n", out, len(ops), strings.Join(slices.Sorted(maps.Keys(keep)), " "))
	return os.WriteFile(out, append(b, '\n'), 0o644)
}

// cutOperation keeps an operation's request body and responses, with their
// JSON content's schema only.
func cutOperation(o, components map[string]any) (map[string]any, error) {
	c := map[string]any{}
	if id, ok := o["operationId"]; ok {
		c["operationId"] = id
	}
	if rb, ok := o["requestBody"].(map[string]any); ok {
		rb, err := inline(rb, components, "requestBodies")
		if err != nil {
			return nil, err
		}
		body := map[string]any{"content": jsonContent(rb)}
		if req, ok := rb["required"]; ok {
			body["required"] = req
		}
		c["requestBody"] = body
	}
	responses, _ := o["responses"].(map[string]any)
	if len(responses) == 0 {
		return nil, fmt.Errorf("no responses")
	}
	cut := map[string]any{}
	for code, r := range responses {
		rm, _ := r.(map[string]any)
		rm, err := inline(rm, components, "responses")
		if err != nil {
			return nil, err
		}
		resp := map[string]any{"description": rm["description"]}
		if content := jsonContent(rm); len(content) > 0 {
			resp["content"] = content
		}
		cut[code] = resp
	}
	c["responses"] = cut
	return c, nil
}

// inline replaces a $ref to components/<kind> with what it refers to.
func inline(v map[string]any, components map[string]any, kind string) (map[string]any, error) {
	ref, ok := v["$ref"].(string)
	if !ok {
		return v, nil
	}
	name, found := strings.CutPrefix(ref, "#/components/"+kind+"/")
	all, _ := components[kind].(map[string]any)
	target, _ := all[name].(map[string]any)
	if !found || target == nil {
		return nil, fmt.Errorf("unresolved $ref %s", ref)
	}
	return target, nil
}

func jsonContent(v map[string]any) map[string]any {
	content, _ := v["content"].(map[string]any)
	out := map[string]any{}
	for mt, m := range content {
		base, _, _ := strings.Cut(mt, ";")
		mm, _ := m.(map[string]any)
		if s, ok := mm["schema"]; ok && (base == "application/json" || strings.HasSuffix(base, "+json")) {
			out[mt] = map[string]any{"schema": s}
		}
	}
	return out
}

var yamlBlock = regexp.MustCompile("(?ms)^(`{3,})yaml[^\n]*\n(.*?)\n^`{3,}\\s*$")

// read returns the spec documents in file.
func read(file string) ([]map[string]any, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	texts := []string{string(raw)}
	if strings.HasSuffix(file, ".md") {
		texts = nil
		for _, m := range yamlBlock.FindAllStringSubmatch(string(raw), -1) {
			texts = append(texts, m[2])
		}
		if len(texts) == 0 {
			return nil, fmt.Errorf("%s: no ```yaml block", file)
		}
	}
	var docs []map[string]any
	for _, t := range texts {
		var v any
		if err := yaml.Unmarshal([]byte(t), &v); err != nil { // JSON is YAML
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		d, ok := normalize(v).(map[string]any)
		if !ok || d["openapi"] == nil {
			return nil, fmt.Errorf("%s: not an OpenAPI document", file)
		}
		docs = append(docs, d)
	}
	return docs, nil
}

// normalize gives every map string keys (YAML allows 200: as an int key).
func normalize(v any) any {
	switch v := v.(type) {
	case map[string]any:
		for k, x := range v {
			v[k] = normalize(x)
		}
		return v
	case map[any]any:
		m := map[string]any{}
		for k, x := range v {
			m[fmt.Sprint(k)] = normalize(x)
		}
		return m
	case []any:
		for i, x := range v {
			v[i] = normalize(x)
		}
		return v
	case time.Time:
		panic(fmt.Sprintf("a YAML timestamp (%v) would not survive the cut: quote it upstream or extend cut.go", v))
	}
	return v
}

// merge merges document d into spec; the same key with different values is
// an error.
func merge(spec, d map[string]any, at string) error {
	for k, v := range d {
		old, ok := spec[k]
		switch {
		case !ok:
			spec[k] = v
		case reflect.DeepEqual(old, v):
		case at == "" && k != "openapi" && k != "paths" && k != "components":
			// info, servers, tags, ...: the first document's.
		default:
			om, ok1 := old.(map[string]any)
			nm, ok2 := v.(map[string]any)
			if !ok1 || !ok2 {
				return fmt.Errorf("%s/%s differs between documents", at, k)
			}
			if err := merge(om, nm, at+"/"+k); err != nil {
				return err
			}
		}
	}
	return nil
}
