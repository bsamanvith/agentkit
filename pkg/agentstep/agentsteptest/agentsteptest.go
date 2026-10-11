// Package agentsteptest provides a conformance suite for
// agentstep.TranscriptSaver implementations.
//
// Every TranscriptSaver (the in-memory one, the store of E05) runs the same
// suite, so they behave the same way:
//
//	func TestConformance(t *testing.T) {
//		agentsteptest.RunSaver(t, func(t *testing.T) agentstep.TranscriptSaver {
//			return newMySaver(t)
//		})
//	}
package agentsteptest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentstep"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
)

// RunSaver runs the conformance suite. newSaver must return an empty saver
// for each call.
func RunSaver(t *testing.T, newSaver func(t *testing.T) agentstep.TranscriptSaver) {
	t.Run("LoadOfNothingIsEmpty", func(t *testing.T) {
		t.Parallel()
		got, err := newSaver(t).Load(t.Context(), "run", "flow/agent")
		if err != nil || len(got) != 0 {
			t.Fatalf("Load = %v, %v; want empty, nil", got, err)
		}
	})

	t.Run("AppendsAccumulateExactly", func(t *testing.T) {
		t.Parallel()
		s := newSaver(t)
		ctx := t.Context()
		want := conversation()
		mustAppend(t, s, "run", "flow/agent", want[:1])
		mustAppend(t, s, "run", "flow/agent", want[1:3])
		mustAppend(t, s, "run", "flow/agent", want[3:])
		got, err := s.Load(ctx, "run", "flow/agent")
		if err != nil {
			t.Fatal(err)
		}
		sameJSON(t, got, want)
	})

	t.Run("KeysAreIndependent", func(t *testing.T) {
		t.Parallel()
		s := newSaver(t)
		c := conversation()
		keys := [][2]string{{"run-1", "flow/agent"}, {"run-1", "flow/par[0]/agent"}, {"run-2", "flow/agent"}}
		for i, k := range keys {
			mustAppend(t, s, k[0], k[1], c[:i+1])
		}
		for i, k := range keys {
			got, err := s.Load(t.Context(), k[0], k[1])
			if err != nil {
				t.Fatal(err)
			}
			sameJSON(t, got, c[:i+1])
		}
	})

	t.Run("StoresCopies", func(t *testing.T) {
		t.Parallel()
		s := newSaver(t)
		in := conversation()
		mustAppend(t, s, "run", "p", in)
		// Scribble over what was passed in, then over what Load returned.
		in[1].Content[0] = llm.Text{Text: "changed"}
		in[1].Content[1].(llm.ToolUse).Input[2] = 'X'
		got, err := s.Load(t.Context(), "run", "p")
		if err != nil {
			t.Fatal(err)
		}
		sameJSON(t, got, conversation())
		got[1].Content[0] = llm.Text{Text: "changed"}
		got[1].Content[1].(llm.ToolUse).Input[2] = 'X'
		again, err := s.Load(t.Context(), "run", "p")
		if err != nil {
			t.Fatal(err)
		}
		sameJSON(t, again, conversation())
	})

	t.Run("HonorsContext", func(t *testing.T) {
		t.Parallel()
		s := newSaver(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := s.Append(ctx, "run", "p", conversation()[:1]); !errors.Is(err, context.Canceled) {
			t.Errorf("Append with a cancelled context = %v, want context.Canceled", err)
		}
		if _, err := s.Load(ctx, "run", "p"); !errors.Is(err, context.Canceled) {
			t.Errorf("Load with a cancelled context = %v, want context.Canceled", err)
		}
		got, err := s.Load(t.Context(), "run", "p")
		if err != nil || len(got) != 0 {
			t.Errorf("after a cancelled Append, Load = %v, %v; want empty", got, err)
		}
	})

	t.Run("ConcurrentAppendsToDifferentSteps", func(t *testing.T) {
		t.Parallel()
		s := newSaver(t)
		c := conversation()
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Go(func() {
				for _, m := range c {
					if err := s.Append(t.Context(), "run", fmt.Sprintf("par[%d]/agent", i), []llm.Message{m}); err != nil {
						t.Error(err)
						return
					}
				}
			})
		}
		wg.Wait()
		for i := range 8 {
			got, err := s.Load(t.Context(), "run", fmt.Sprintf("par[%d]/agent", i))
			if err != nil {
				t.Fatal(err)
			}
			sameJSON(t, got, c)
		}
	})
}

// conversation is a whole agent conversation with every kind of block,
// including an opaque one whose bytes must survive exactly.
func conversation() harness.Transcript {
	return harness.Transcript{
		llm.User(llm.Text{Text: "What is 6 × 7?"}),
		llm.Assistant(
			llm.Text{Text: "Let me calculate."},
			llm.ToolUse{ID: "call-1", Name: "calculate", Input: json.RawMessage(`{"a": 6, "op": "*", "b": 7}`)},
		),
		llm.User(llm.ToolResult{ToolUseID: "call-1", Content: "42"}),
		llm.Assistant(
			llm.Opaque{Provider: "anthropic", Data: json.RawMessage(`{"type":"thinking", "thinking":"<&>é", "signature":"c2ln"}`)},
			llm.Text{Text: "6 × 7 = 42."},
		),
	}
}

func mustAppend(t *testing.T, s agentstep.TranscriptSaver, runID, path string, msgs []llm.Message) {
	t.Helper()
	if err := s.Append(t.Context(), runID, path, msgs); err != nil {
		t.Fatalf("Append(%s, %s): %v", runID, path, err)
	}
}

// sameJSON compares transcripts by their JSON, which is exact for opaque
// blocks.
func sameJSON(t *testing.T, got, want harness.Transcript) {
	t.Helper()
	g, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	w, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(g) != string(w) {
		t.Errorf("transcript = %s\nwant         %s", g, w)
	}
}
