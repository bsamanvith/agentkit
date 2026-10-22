package tavily_test

import (
	"encoding/json"
	"testing"

	"github.com/bsamanvith/agentkit/connectors/tavily"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// TestDefinitions pins the tool definitions models and MCP clients see: the
// names, descriptions, and input and output schemas, with the limits the
// code enforces. With model_chooses_depth, search also takes search_depth.
func TestDefinitions(t *testing.T) {
	t.Parallel()
	for name, settings := range map[string]string{
		"definitions":                     `{}`,
		"definitions-model-chooses-depth": `{"search_defaults": {"model_chooses_depth": true}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tools, err := tavily.Factory().Build(t.Context(), toolset.BuildInput{Settings: json.RawMessage(settings)})
			if err != nil {
				t.Fatal(err)
			}
			var defs []any
			for _, tl := range tools {
				defs = append(defs, tl.Definition())
			}
			b, err := json.Marshal(defs)
			if err != nil {
				t.Fatal(err)
			}
			golden(t, name+".json", b)
			// The rig validates outputs against these schemas; they must
			// reject a wrong shape.
			schemas := outputSchemas(t, tools)
			for tl, bad := range map[string]string{
				"tavily_search":  `{"results": [{"title": "t", "url": 1, "content": "c"}]}`,
				"tavily_extract": `{"results": []}`,
			} {
				if validOutput(schemas, tl, []byte(bad)) == nil {
					t.Errorf("%s output schema accepts %s", tl, bad)
				}
			}
		})
	}
}
