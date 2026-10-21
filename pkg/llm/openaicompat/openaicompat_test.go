package openaicompat_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/llm/openaicompat"
)

// conformanceAliases are scenarios that reuse another scenario's fixture: the
// request of "cancelled_context" is never sent, and "stop_early" and "stall"
// need the same stalled stream as "cancel".
var conformanceAliases = map[string]string{
	"cancelled_context": "text",
	"stop_early":        "cancel",
	"stall":             "cancel",
}

// conformanceIdle is the idle timeout of the models the suite runs, so the
// "stall" scenario does not wait the default ten minutes. It covers the
// connection too, so it is long enough for a loaded machine running -race.
const conformanceIdle = time.Second

// conformanceReasoning is the reasoning of testdata/conformance/opaque.http as
// the adapter returns it: the field it came in, with its deltas joined.
const conformanceReasoning = `{"reasoning_content":"2 < 3 && 5 > 4: naïve ✓"}`

func TestConformance(t *testing.T) {
	t.Parallel()
	llmtest.RunModel(t, func(t *testing.T, s llmtest.Scenario) llm.Model {
		name := s.Name
		if alias, ok := conformanceAliases[name]; ok {
			name = alias
		}
		_, srv := newReplay(t, fixtures(t, "conformance", name)...)
		return newModel(t, srv, openaicompat.WithIdleTimeout(conformanceIdle))
	}, llmtest.WithOpaque(llm.Opaque{Provider: openaicompat.Provider, Data: json.RawMessage(conformanceReasoning)}),
		llmtest.WithStallTimeout(conformanceIdle))
}
