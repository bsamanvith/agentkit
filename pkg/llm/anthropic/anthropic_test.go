package anthropic_test

import (
	"context"
	"encoding/json"
	"iter"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/anthropic"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
)

// conformanceAliases are scenarios that reuse another scenario's fixture: the
// request of "cancelled_context" is never sent, and "stop_early" needs the
// same stalled stream as "cancel".
var conformanceAliases = map[string]string{
	"cancelled_context": "text",
	"stop_early":        "cancel",
	"stall":             "cancel",
}

// conformanceIdle is the idle timeout of the model of the "stall" scenario,
// so that it does not wait the default five minutes. The other scenarios
// keep the default: a short one would cut their streams on a busy machine.
const conformanceIdle = 100 * time.Millisecond

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
		if s.Name != "stall" {
			return newModel(t, srv)
		}
		clk := clocktest.NewFake(time.Time{})
		return stallClock{newModel(t, srv, anthropic.WithIdleTimeout(conformanceIdle), anthropic.WithClock(clk)), clk}
	}, llmtest.WithOpaque(llm.Opaque{Provider: "anthropic", Data: json.RawMessage(conformanceThinking)}),
		llmtest.WithStallTimeout(conformanceIdle))
}

// stallClock is the model of the "stall" scenario, on a fake idle clock that
// it moves by the timeout once the stream is silent after its first event:
// once the caller has the event and the stream waits again. That the first
// event arrives before the timeout then does not depend on how fast the
// machine is. It moves the clock no sooner than the timeout in real time
// too, which the suite checks the failure comes after.
type stallClock struct {
	llm.Model
	clk *clocktest.Fake
}

func (m stallClock) Generate(ctx context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	return func(yield func(llm.Event, error) bool) {
		start := time.Now()
		first := true
		for ev, err := range m.Model.Generate(ctx, req) {
			if first && err == nil {
				first = false
				// The idle timer is stopped while the caller has an event:
				// it runs again once the stream waits for the next.
				go func() {
					select {
					case <-time.After(conformanceIdle - time.Since(start)):
					case <-ctx.Done():
						return
					}
					if m.clk.WaitForTimers(ctx, 1) == nil {
						m.clk.Advance(conformanceIdle)
					}
				}()
			}
			if !yield(ev, err) {
				return
			}
		}
	}
}
