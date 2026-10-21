package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/policy"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
)

// TestChatAnswersApprovals: a call the policy holds shows what it waits for;
// the next line answers it, and chat follows the run to its answer. y
// approves; n and a reason denies.
func TestChatAnswersApprovals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		line, result string
	}{
		{"y", `← {"text":"x"}`},
		{"n not today", "✗ denied by a person: not today"},
	} {
		t.Run(tc.line, func(t *testing.T) {
			t.Parallel()
			tools, err := cli{}.demoTools(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			model := llmtest.NewFake(t,
				llmtest.ToolCalls(llm.ToolUse{ID: "toolu_1", Name: "echo", Input: json.RawMessage(`{"text":"x"}`)}),
				llmtest.Text("Done echoing."),
			)
			h, err := harness.New(model, tools,
				harness.WithInteraction(harness.Interactive),
				harness.WithToolPolicy(policy.Policy{Rules: []policy.Rule{
					{Name: "hold", Match: policy.Match{Tools: []string{"echo"}}, Action: policy.Approve, Reason: "echo needs a yes"},
				}, Default: policy.Allow}, nil))
			if err != nil {
				t.Fatal(err)
			}
			srv, err := agentapi.New(h, agentapi.WithStore(scoped.Memory(nil)), agentapi.WithToken(testToken))
			if err != nil {
				t.Fatal(err)
			}
			ts := httptest.NewServer(srv.Handler())
			t.Cleanup(ts.Close)
			out := newSyncBuffer("")
			c, stderr := clientCLI(ts.URL, out, strings.NewReader("Echo x.\n"+tc.line+"\n"), make(chan os.Signal))
			if code := c.main(t.Context(), []string{"chat"}); code != exitDone {
				t.Fatalf("exit %d\n%s%s", code, out, stderr)
			}
			got := out.String()
			t.Logf("output:\n%s", got)
			checkClientOutput(t, got+stderr.String())
			mustContain(t, got, `? echo {"text":"x"} needs your approval (echo needs a yes): y, or n and why`, "waiting for input", tc.result, "Done echoing.", "2 turns")
			if strings.Contains(got, "run failed") {
				t.Error("a suspended run is shown as failed")
			}
		})
	}
}

func TestAnswerOf(t *testing.T) {
	t.Parallel()
	approval := agentapi.Interrupt{ID: "a", Kind: "approval"}
	question := agentapi.Interrupt{ID: "q", Kind: "question"}
	for _, tc := range []struct {
		in   agentapi.Interrupt
		line string
		want agentapi.Answer
	}{
		{approval, "y", agentapi.Answer{ID: "a", Approved: true}},
		{approval, " YES ", agentapi.Answer{ID: "a", Approved: true}},
		{approval, "n", agentapi.Answer{ID: "a"}},
		{approval, "no  too risky", agentapi.Answer{ID: "a", Reason: "too risky"}},
		{approval, "not now", agentapi.Answer{ID: "a", Reason: "not now"}},
		{question, " blue ", agentapi.Answer{ID: "q", Text: "blue"}},
	} {
		if got := answerOf(tc.in, tc.line); got != tc.want {
			t.Errorf("answerOf(%s, %q) = %+v, want %+v", tc.in.Kind, tc.line, got, tc.want)
		}
	}
}

// agents[].interaction is read from the config, and a mode that is not one
// fails the agent's check.
func TestAgentInteractionConfig(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "agentkit.json")
	if err := os.WriteFile(path, []byte(`{"agents": [{"name": "a", "interaction": "interactive"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadServeConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if a := cfg.Agents[0]; a.Interaction != harness.Interactive || a.check(false) != nil {
		t.Fatalf("agent %+v: %v", a, a.check(false))
	}
	if err := (agentDef{Name: "a", Interaction: "chatty"}).check(false); err == nil || !strings.Contains(err.Error(), "interaction") {
		t.Fatalf("a bad mode: %v", err)
	}
}
