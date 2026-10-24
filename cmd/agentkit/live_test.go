//go:build live

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
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

// TestLiveMixedToolResultsAndText checks the T022 concern: a user message
// that holds tool results and then text (what steering sends, T086). The
// harness's Validate accepts it since steering, and so must Claude.
func TestLiveMixedToolResultsAndText(t *testing.T) {
	t.Parallel()
	requireCredentials(t)
	m, err := anthropic.New(anthropic.WithMaxTokens(4000), anthropic.WithOptions(anthropic.Options{Effort: anthropic.EffortLow}))
	if err != nil {
		t.Fatal(err)
	}
	tools, err := cli{}.demoTools(t.Context())
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
	if verr != nil {
		t.Errorf("Validate = %v; text after the tool results (steering) must be accepted", verr)
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

// TestLiveServeAgent runs agentkit serve with agent and mcp-server against
// Claude: a run that needs a tool of one toolset, then a follow-up in the
// same conversation that needs a tool of the other.
func TestLiveServeAgent(t *testing.T) {
	t.Parallel()
	key, token := os.Getenv("ANTHROPIC_API_KEY"), os.Getenv("ANTHROPIC_AUTH_TOKEN")
	if key == "" && token == "" {
		t.Skip("set ANTHROPIC_API_KEY (the server runs with an empty HOME, so an `ant` profile is not seen)")
	}
	p := startServe(t, agentConfigJSON, 20*time.Second,
		envComponents+"=agent,mcp-server", envAPIToken+"="+e2eToken,
		"ANTHROPIC_API_KEY="+key, "ANTHROPIC_AUTH_TOKEN="+token)
	a := agentClient{t: t, url: p.url, token: e2eToken}

	first := a.stream("Use the demo-b__echo tool to echo the word pineapple, then tell me exactly what the tool returned.", "", nil)
	calls, results := toolEvents(first)
	end := first[len(first)-1]
	t.Logf("first run: %v; answer %q; usage %+v", eventTypes(first), end.Answer, end.Usage)
	if len(calls) == 0 || calls[0].Name != "demo-b__echo" || !strings.Contains(results[0].Content, "[b] pineapple") || end.Type != agentapi.TypeRunDone {
		t.Fatalf("first run: calls %+v, results %+v, end %+v", calls, results, end)
	}
	second := a.stream("Now use demo-a__calculate to multiply 48271 by 9733, and tell me the result.", first[0].ConversationID, nil)
	calls, results = toolEvents(second)
	end = second[len(second)-1]
	t.Logf("follow-up: %v; answer %q; usage %+v", eventTypes(second), end.Answer, end.Usage)
	if len(calls) == 0 || calls[0].Name != "demo-a__calculate" || results[0].Content != `{"result":469821643}` ||
		end.Type != agentapi.TypeRunDone || !strings.Contains(strings.ReplaceAll(end.Answer, ",", ""), "469821643") {
		t.Fatalf("follow-up: calls %+v, results %+v, end %+v", calls, results, end)
	}
	if end.Usage.CacheReadTokens == 0 {
		t.Logf("note: the follow-up read nothing from the prompt cache")
	}
}
