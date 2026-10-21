//go:build live

package memory_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/anthropic"
	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/inmem"
	"github.com/bsamanvith/agentkit/pkg/memory/memorytest"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

func requireCredentials(t *testing.T) {
	t.Helper()
	if os.Getenv("ANTHROPIC_API_KEY") != "" || os.Getenv("ANTHROPIC_AUTH_TOKEN") != "" {
		return
	}
	if ant, err := exec.LookPath("ant"); err == nil && exec.Command(ant, "auth", "status").Run() == nil {
		return
	}
	t.Skip("no Anthropic credentials: set ANTHROPIC_API_KEY or run `ant auth login`")
}

// TestLiveRecallAcrossConversations: real Claude, told a preference in one
// conversation, saves it with the memory tool, and in a new conversation
// (no transcript) reads it back and uses it. It runs with the tool as a
// plain definition and as the native memory tool type.
func TestLiveRecallAcrossConversations(t *testing.T) {
	t.Parallel()
	requireCredentials(t)
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "definition", true: "native"}[native], func(t *testing.T) {
			t.Parallel()
			store, err := inmem.New(memory.Config{})
			if err != nil {
				t.Fatal(err)
			}
			mt, err := memory.NewTool(store)
			if err != nil {
				t.Fatal(err)
			}
			tools, err := tool.NewRegistry([]tool.Tool{mt})
			if err != nil {
				t.Fatal(err)
			}
			model, err := anthropic.New(anthropic.WithMaxTokens(8000), anthropic.WithOptions(anthropic.Options{
				Effort: anthropic.EffortLow, NativeMemory: native,
			}))
			if err != nil {
				t.Fatal(err)
			}
			h, err := harness.New(model, tools, harness.WithMaxTurns(10),
				harness.WithSystem("You are a concise assistant with a memory directory. Check your memory before you answer, and save anything the user asks you to remember."))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(memorytest.Ctx(t, "live", ""), 5*time.Minute)
			defer cancel()

			if _, err := harness.Collect(h.Run(ctx, harness.Input{Message: harness.UserText(
				"Please remember this for our future conversations: my favourite fruit is the mangosteen.")})); err != nil {
				t.Fatalf("first conversation: %v", err)
			}
			files, err := memorytest.Snapshot(ctx, store)
			if err != nil {
				t.Fatal(err)
			}
			saved := false
			for p, text := range files {
				t.Logf("saved %s: %q", p, text)
				saved = saved || strings.Contains(strings.ToLower(text), "mangosteen")
			}
			if !saved {
				t.Fatalf("nothing about the preference was saved: %q", files)
			}

			res, err := harness.Collect(h.Run(ctx, harness.Input{Message: harness.UserText(
				"What is my favourite fruit? Answer with its name only.")}))
			if err != nil {
				t.Fatalf("second conversation: %v", err)
			}
			answer := ""
			for _, b := range res.Final.Content {
				if tb, ok := b.(llm.Text); ok {
					answer += tb.Text
				}
			}
			if !strings.Contains(strings.ToLower(answer), "mangosteen") {
				t.Errorf("second conversation answered %q, want the mangosteen", answer)
			}
			t.Logf("answer %q in %d turns", answer, res.Turns)
		})
	}
}
