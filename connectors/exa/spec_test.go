package exa_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/bsamanvith/agentkit/connectors/exa"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
)

// Every search type and category the connector accepts is one Exa's spec
// (testdata/openapi.json) accepts, so a value the model or an operator picks
// is never refused by Exa for being unknown. The suite's cassettes.openapi
// check covers the cassettes.
func TestEnumsMatchSpec(t *testing.T) {
	t.Parallel()
	spec, err := toolsettest.LoadOpenAPI(filepath.Join("testdata", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	for field, values := range map[string][]string{"type": exa.SearchTypes, "category": exa.Categories} {
		for _, v := range values {
			body, _ := json.Marshal(map[string]string{"query": "q", field: v})
			if err := spec.CheckRequest("POST", "/search", body); err != nil {
				t.Errorf("%s %q: %v", field, v, err)
			}
		}
	}
}
