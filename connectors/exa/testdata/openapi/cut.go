//go:build ignore

// cut reads Exa's OpenAPI spec (YAML) on stdin and writes, as JSON, the
// slice the connector uses: POST /search and POST /contents (request body
// and 200 response schemas) and every component schema they reference.
//
//	curl -sSL https://exa.ai/docs/exa-spec.yaml | go run ./connectors/exa/testdata/openapi/cut.go > connectors/exa/testdata/openapi/exa.json
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	yaml "go.yaml.in/yaml/v4"
)

func main() {
	in, err := io.ReadAll(os.Stdin)
	if err != nil {
		panic(err)
	}
	var spec map[string]any
	if err := yaml.Unmarshal(in, &spec); err != nil {
		panic(err)
	}
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	paths := spec["paths"].(map[string]any)
	out := map[string]any{"openapi": spec["openapi"], "info": spec["info"]}
	cutPaths := map[string]any{}
	keep := map[string]bool{}
	var walk func(v any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if ref, ok := v["$ref"].(string); ok {
				name, found := strings.CutPrefix(ref, "#/components/schemas/")
				if !found {
					panic("unexpected $ref " + ref)
				}
				if !keep[name] {
					keep[name] = true
					walk(schemas[name])
				}
			}
			for _, x := range v {
				walk(x)
			}
		case []any:
			for _, x := range v {
				walk(x)
			}
		}
	}
	for _, p := range []string{"/search", "/contents"} {
		post := paths[p].(map[string]any)["post"].(map[string]any)
		req := post["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"]
		res := post["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"]
		walk(req)
		walk(res)
		cutPaths[p] = map[string]any{"post": map[string]any{"request": req, "response200": res}}
	}
	cut := map[string]any{}
	names := make([]string, 0, len(keep))
	for n := range keep {
		names = append(names, n)
		cut[n] = schemas[n]
	}
	sort.Strings(names)
	fmt.Fprintln(os.Stderr, "schemas:", strings.Join(names, " "))
	out["paths"] = cutPaths
	out["components"] = map[string]any{"schemas": cut}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		panic(err)
	}
	os.Stdout.Write(append(b, '\n'))
}
