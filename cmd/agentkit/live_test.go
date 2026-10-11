//go:build live

package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/anthropic"
)

// requireCredentials skips the test unless the SDK can find credentials.
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

// TestLiveRunUsesTool runs `agentkit run` against Claude with a question that
// needs the calculate tool.
func TestLiveRunUsesTool(t *testing.T) {
	t.Parallel()
	requireCredentials(t)
	var stdout, stderr strings.Builder
	c := cli{stdout: &stdout, stderr: &stderr, newModel: newClaude}
	code := c.main(t.Context(), []string{
		"run", "-effort", "low", "-max-turns", "6",
		"Use the calculate tool to multiply 48271 by 9733, then tell me the result.",
	})
	t.Logf("stdout:\n%s\nstderr:\n%s", &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	out := stdout.String()
	for _, want := range []string{"→ calculate", `{"result":469821643}`, "· turn 1: tool_use", ": end_turn", "· done: "} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not contain %q", want)
		}
	}
}

// TestLiveMixedToolResultsAndText checks the T022 concern: harness Validate
// rejects a user message that holds both tool results and text. Does Claude
// accept one?
func TestLiveMixedToolResultsAndText(t *testing.T) {
	t.Parallel()
	requireCredentials(t)
	m, err := anthropic.New(anthropic.WithMaxTokens(4000), anthropic.WithOptions(anthropic.Options{Effort: anthropic.EffortLow}))
	if err != nil {
		t.Fatal(err)
	}
	tools, err := demoTools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defs, err := tools.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	req := llm.Request{
		System:   "You are a test harness. Follow the instructions exactly.",
		Tools:    defs,
		Messages: []llm.Message{llm.User(llm.Text{Text: `Call the echo tool once with the text "pineapple".`})},
	}
	first, err := llm.Collect(m.Generate(t.Context(), req))
	if err != nil {
		t.Fatal(err)
	}
	if first.Stop.Kind != llm.StopToolUse {
		t.Fatalf("first reply stop = %+v, want tool_use", first.Stop)
	}
	var results []llm.Block
	for _, b := range first.Message.Content {
		if u, ok := b.(llm.ToolUse); ok {
			results = append(results, llm.ToolResult{ToolUseID: u.ID, Content: `{"text":"pineapple"}`})
		}
	}
	mixed := llm.User(append(results, llm.Text{Text: "Now say what the tool returned, in French."})...)
	transcript := harness.Transcript{req.Messages[0], first.Message, mixed}

	verr := transcript.Validate()
	t.Logf("harness Validate: %v", verr)
	if !errors.Is(verr, harness.ErrInvalidInput) {
		t.Errorf("Validate = %v; the restriction recorded in T024 has changed, update the notes", verr)
	}

	req.Messages = transcript
	second, err := llm.Collect(m.Generate(t.Context(), req))
	if err != nil {
		t.Fatalf("Claude rejected the mixed message: %v", err)
	}
	t.Logf("Claude accepted it: stop %+v, text %q", second.Stop, second.Message.Text())
	if second.Stop.Kind != llm.StopEndTurn || second.Message.Text() == "" {
		t.Errorf("second reply = %+v, want a text answer", second)
	}
}
