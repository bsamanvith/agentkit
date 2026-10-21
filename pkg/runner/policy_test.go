package runner_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/policy"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store"
)

// toolResults are the tool results the model was sent for prompt.
func toolResults(w *world, prompt string) []llm.ToolResult {
	var out []llm.ToolResult
	reqs := w.model.requestsFor(prompt)
	if len(reqs) == 0 {
		return nil
	}
	for _, m := range reqs[len(reqs)-1].Messages {
		for _, b := range m.Content {
			if r, ok := b.(llm.ToolResult); ok {
				out = append(out, r)
			}
		}
	}
	return out
}

// A run's tool policy narrows its agent's on every execution: it is stored
// with the prompt, and a resume after a crash keeps it.
func TestToolPolicyKeptOnResume(t *testing.T) {
	t.Parallel()
	w := newWorld(t) // echo is unclassified: write/medium
	prompt := w.model.on("p", 2)
	dead := w.process()
	dead.store.crashAt(crashAtNth("Append", 3, false)) // after its first turn
	x, err := dead.r.Run(w.ctx, "c", harness.UserText(prompt), runner.ToolPolicy(policy.MustPreset(policy.ReadOnly)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dead.drain(x, nil); !errors.Is(err, errCrashed) {
		t.Fatalf("crashed run ended with %v", err)
	}
	w.expire()
	p := w.process()
	evs, err := p.resume(x.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	lastDone(t, evs)
	res := toolResults(w, prompt)
	if len(res) != 2 {
		t.Fatalf("results %+v, want one per turn", res)
	}
	for _, r := range res {
		if !r.IsError || !strings.HasPrefix(r.Content, "not allowed in this run: ") {
			t.Errorf("a write ran under read_only: %+v", r)
		}
	}
	denied := 0
	for _, ev := range evs {
		if ev.Kind == runner.EventHarness && ev.Harness.Kind == harness.EventToolPolicy && ev.Harness.Decision.Action == policy.Deny {
			denied++
		}
	}
	if denied != 1 {
		t.Errorf("the resumed execution had %d deny events, want 1", denied)
	}
	rec, err := runner.DecodeRecord(w.log("c")[0])
	if err != nil || rec.Prompt == nil || rec.Prompt.ToolPolicy == nil || rec.Prompt.ToolPolicy.Name != policy.ReadOnly {
		t.Fatalf("prompt record %+v, %v: want the run's policy", rec.Prompt, err)
	}

	// Without one, the same tool runs.
	free := w.model.on("free", 1)
	if _, err := p.run("c2", free); err != nil {
		t.Fatal(err)
	}
	if res := toolResults(w, free); len(res) != 1 || res[0].IsError {
		t.Fatalf("without a policy: %+v", res)
	}
}

// A run whose stored policy cannot be read is not resumed without it.
func TestToolPolicyUnreadableFailsTheResume(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	prompt := w.model.on("p", 1)
	if _, err := w.mem.Create(w.ctx, "run9", "c"); err != nil {
		t.Fatal(err)
	}
	rec, _ := json.Marshal(map[string]any{"prompt": map[string]any{
		"at": w.clk.Now(), "tool_policy": map[string]any{"default": "sometimes"},
	}})
	if err := w.mem.Append(w.ctx, "c", store.Entry{
		Seq: 1, Kind: store.KindPrompt, Messages: []llm.Message{harness.UserText(prompt)}, RunID: "run9", Record: rec,
	}); err != nil {
		t.Fatal(err)
	}
	p := w.process()
	_, err := p.resume("run9")
	if err == nil {
		t.Fatal("the run was resumed without its policy")
	}
	if n := len(w.model.requestsFor(prompt)); n != 0 {
		t.Fatalf("the model was called %d times", n)
	}
	if run := w.get("run9"); run.Status != store.StatusFailed || run.Retryable {
		t.Fatalf("run %+v, want failed for good", run)
	}
}

// A run compacted after its prompt keeps its policy when a cold resume reads
// the context from the compaction on, without the prompt entry.
func TestToolPolicyKeptAcrossCompaction(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	if _, err := w.process().run("c", w.model.on("a", 1)); err != nil {
		t.Fatal(err)
	}
	prompt := w.model.on("b", 2)
	w.model.failing("b", 0, 1, fmt.Errorf("%w: scripted", llm.ErrContextTooLong))
	dead := w.process()
	dead.store.crashAt(crashAtNth("Append", 3, false)) // the prompt, the compaction, then the first turn
	x, err := dead.r.Run(w.ctx, "c", harness.UserText(prompt), runner.ToolPolicy(policy.MustPreset(policy.ReadOnly)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dead.drain(x, nil); !errors.Is(err, errCrashed) {
		t.Fatalf("crashed run ended with %v", err)
	}
	log := w.log("c")
	if last := log[len(log)-1]; last.Kind != store.KindCompaction || last.RunID != x.Run.ID {
		t.Fatalf("log ends with %s, want the run's compaction", js(last))
	}
	w.expire()
	evs, err := w.process().resume(x.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	lastDone(t, evs)
	res := toolResults(w, prompt)
	if len(res) != 2 {
		t.Fatalf("results %+v, want one per turn", res)
	}
	for _, r := range res {
		if !r.IsError || !strings.HasPrefix(r.Content, "not allowed in this run: ") {
			t.Errorf("a write ran under read_only after a compaction: %+v", r)
		}
	}
}

func TestToolPolicyInvalid(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	p := w.process()
	if _, err := p.r.Run(w.ctx, "c", harness.UserText("hi"), runner.ToolPolicy(policy.Policy{})); !errors.Is(err, runner.ErrInvalidConfig) {
		t.Fatalf("an invalid policy: %v, want ErrInvalidConfig", err)
	}
}
