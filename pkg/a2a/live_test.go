//go:build live

package a2a_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/a2a"
	"github.com/bsamanvith/agentkit/pkg/llm/anthropic"
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

// TestLiveStreamingMessage: real Claude, behind the agent API, answers an
// A2A SendStreamingMessage that needs the demo echo tool. The stream is the
// task, then the answer's chunks, then one terminal COMPLETED status, and
// the rebuilt answer is the run's (the rig's invariants check the shape).
func TestLiveStreamingMessage(t *testing.T) {
	t.Parallel()
	requireCredentials(t)
	m, err := anthropic.New(anthropic.WithMaxTokens(4000), anthropic.WithOptions(anthropic.Options{Effort: anthropic.EffortLow}))
	if err != nil {
		t.Fatal(err)
	}
	r := newRigOn(t, m)
	items := r.stream("default", keyA, "SendStreamingMessage", map[string]any{
		"message": message(`Call the echo tool once with the text "mangosteen", then reply with exactly what it returned and nothing else.`),
	})
	if len(items) < 3 || items[0].Task == nil {
		t.Fatalf("stream %+v", items)
	}
	last := items[len(items)-1].StatusUpdate
	if last == nil || last.Status.State != a2a.StateCompleted {
		t.Fatalf("last item %+v", items[len(items)-1])
	}
	var answer strings.Builder
	for _, it := range items {
		if it.ArtifactUpdate != nil && it.ArtifactUpdate.LastChunk {
			answer.WriteString(text(it.ArtifactUpdate.Artifact.Parts))
		}
	}
	t.Logf("answer: %q", answer.String())
	if !strings.Contains(strings.ToLower(answer.String()), "mangosteen") {
		t.Errorf("the answer does not carry the tool's result: %q", answer.String())
	}
	if run := r.run(keyA, items[0].Task.ID); run.Status != "done" || run.Turns < 2 {
		t.Errorf("run %s: %s after %d turns", run.ID, run.Status, run.Turns)
	}
}
