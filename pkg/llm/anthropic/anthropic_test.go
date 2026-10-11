package anthropic_test

import (
	"encoding/json"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
)

// conformanceAliases are scenarios that reuse another scenario's fixture: the
// request of "cancelled_context" is never sent, and "stop_early" needs the
// same stalled stream as "cancel".
var conformanceAliases = map[string]string{
	"cancelled_context": "text",
	"stop_early":        "cancel",
}

// conformanceThinking is the thinking block of testdata/conformance/opaque.http
// as the adapter returns it: the block's wire JSON with its deltas applied.
const conformanceThinking = `{"type":"thinking","thinking":"2 < 3 && 5 > 4: naïve ✓","signature":"EqQBCkYIARgCKkD+/Zg=="}`

func TestConformance(t *testing.T) {
	t.Parallel()
	llmtest.RunModel(t, func(t *testing.T, s llmtest.Scenario) llm.Model {
		name := s.Name
		if alias, ok := conformanceAliases[name]; ok {
			name = alias
		}
		_, srv := newReplay(t, fixtures(t, "conformance", name)...)
		return newModel(t, srv)
	}, llmtest.WithOpaque(llm.Opaque{Provider: "anthropic", Data: json.RawMessage(conformanceThinking)}))
}
