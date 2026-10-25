package gemini_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/gemini"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
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

// conformanceThought is the thought of testdata/conformance/opaque.http as
// the adapter returns it: one thought part, its texts joined, with its
// signature.
const conformanceThought = `{"text":"2 < 3 && 5 > 4: naïve ✓","thought":true,"thoughtSignature":"c2lnbmF0dXJl"}`

func TestConformance(t *testing.T) {
	t.Parallel()
	llmtest.RunModel(t, func(t *testing.T, s llmtest.Scenario) llm.Model {
		name := s.Name
		if alias, ok := conformanceAliases[name]; ok {
			name = alias
		}
		_, srv := newReplay(t, fixtures(t, "conformance", name)...)
		return newModel(t, srv, gemini.WithIdleTimeout(conformanceIdle))
	}, llmtest.WithOpaque(llm.Opaque{Provider: gemini.Provider, Data: json.RawMessage(conformanceThought)}),
		llmtest.WithStallTimeout(conformanceIdle), llmtest.WithoutCacheWrites())
}
