package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentstep"
	"github.com/bsamanvith/agentkit/pkg/agentstep/agentsteptest"
	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/flow/flowtest"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
)

// process is one run of the binary, with a scripted model.
type process struct {
	model          *llmtest.Fake
	stdout, stderr bytes.Buffer
}

func newProcess(t *testing.T, replies ...llmtest.Reply) *process {
	return &process{model: llmtest.NewFake(t, replies...)}
}

func (p *process) main(t *testing.T, args ...string) int {
	t.Helper()
	c := cli{stdout: &p.stdout, stderr: &p.stderr, newModel: func(modelFlags) (llm.Model, error) { return p.model, nil }}
	code := c.main(t.Context(), args)
	t.Logf("agentkit %s → %d\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), code, &p.stdout, &p.stderr)
	return code
}

func mustContain(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("output does not contain %q", w)
		}
	}
}

func TestRunStreamsEvents(t *testing.T) {
	t.Parallel()
	p := newProcess(t,
		llmtest.ToolCalls(llm.ToolUse{ID: "c1", Name: "calculate", Input: json.RawMessage(`{"op":"mul","a":6,"b":7}`)}).
			WithUsage(llm.Usage{InputTokens: 100, OutputTokens: 20}),
		llmtest.Text("6 × 7 = 42.").WithUsage(llm.Usage{InputTokens: 130, OutputTokens: 9, CacheReadTokens: 90}),
	)
	if code := p.main(t, "run", "What is 6 times 7?"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	mustContain(t, p.stdout.String(),
		`→ calculate {"op":"mul","a":6,"b":7}`,
		`← calculate (`, `): {"result":42}`,
		"· turn 1: tool_use · model ", "· in 100 · out 20 · cache read 0 · cache write 0",
		"6 × 7 = 42.\n· turn 2: end_turn",
		"· done: 2 turns · in 230 · out 29 · cache read 90 · cache write 0",
	)
	if n := p.model.Remaining(); n != 0 {
		t.Errorf("%d replies unused", n)
	}
}

func TestRunNeedsAPrompt(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{}, {"run"}, {"run", "a", "b"}, {"flow"}, {"nope"}} {
		if code := newProcess(t).main(t, args...); code != 2 {
			t.Errorf("agentkit %q exit %d, want 2", args, code)
		}
	}
}

// research is the demo's research agent: it calls a tool, then answers.
func research() []llmtest.Reply {
	return []llmtest.Reply{
		llmtest.ToolCalls(llm.ToolUse{ID: "c1", Name: "current_time", Input: json.RawMessage(`{}`)}),
		llmtest.Text("- Go 1.18 added generics."),
	}
}

func TestFlowDemoStopsAndResumesInAnotherProcess(t *testing.T) {
	t.Parallel()
	state := filepath.Join(t.TempDir(), "demo.json")

	first := newProcess(t, research()...)
	if code := first.main(t, "flow", "demo", "-state", state, "Go generics"); code != 0 {
		t.Fatalf("first process exit %d", code)
	}
	mustContain(t, first.stdout.String(), "── demo/research ──", "→ current_time", "- Go 1.18 added generics.", "Waiting for approval at demo/approve")

	// Without a decision, it waits again and calls no model.
	waiting := newProcess(t)
	if code := waiting.main(t, "flow", "demo", "-state", state); code != 0 {
		t.Fatalf("second process exit %d", code)
	}
	mustContain(t, waiting.stdout.String(), "Waiting for approval at demo/approve")

	second := newProcess(t, llmtest.Text("Since Go 1.18, Go has generics."))
	if code := second.main(t, "flow", "demo", "-state", state, "-approve"); code != 0 {
		t.Fatalf("third process exit %d", code)
	}
	mustContain(t, second.stdout.String(), "── demo/write ──", "=== draft ===\nSince Go 1.18, Go has generics.")
	reqs := second.model.Requests()
	if len(reqs) != 1 {
		t.Fatalf("resumed process made %d model calls, want 1 (the writer)", len(reqs))
	}
	mustContain(t, reqs[0].Messages[0].Text(), "Topic: Go generics", "- Go 1.18 added generics.")

	// The file holds both agents' transcripts and the completed run.
	store, err := openFileStore(state)
	if err != nil {
		t.Fatal(err)
	}
	ck, err := store.Load(t.Context(), "demo")
	if err != nil || ck.Status != flow.StatusCompleted {
		t.Errorf("checkpoint = %+v, %v; want completed", ck, err)
	}
	for path, n := range map[string]int{"demo/research": 4, "demo/write": 2} {
		if tr, err := store.transcripts().Load(t.Context(), "demo", path); err != nil || len(tr) != n {
			t.Errorf("%s: %d messages saved (%v), want %d", path, len(tr), err, n)
		}
	}
}

func TestFlowDemoRejection(t *testing.T) {
	t.Parallel()
	state := filepath.Join(t.TempDir(), "demo.json")
	if code := newProcess(t, research()...).main(t, "flow", "demo", "-state", state, "Go generics"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	p := newProcess(t)
	if code := p.main(t, "flow", "demo", "-state", state, "-reject", "too thin"); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	mustContain(t, p.stderr.String(), "too thin")
}

func TestFileStoreCheckpoints(t *testing.T) {
	flowtest.RunCheckpointer(t, func(t *testing.T) flow.Checkpointer {
		s, err := openFileStore(filepath.Join(t.TempDir(), "s.json"))
		if err != nil {
			t.Fatal(err)
		}
		return s
	})
}

func TestFileStoreTranscripts(t *testing.T) {
	agentsteptest.RunSaver(t, func(t *testing.T) agentstep.TranscriptSaver {
		s, err := openFileStore(filepath.Join(t.TempDir(), "s.json"))
		if err != nil {
			t.Fatal(err)
		}
		return s.transcripts()
	})
}

func TestFileStoreSurvivesTheProcess(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "s.json")
	want := harness.Transcript{
		llm.User(llm.Text{Text: "hi"}),
		llm.Assistant(llm.Opaque{Provider: "anthropic", Data: json.RawMessage(`{"type":"thinking",  "signature":"<&>"}`)}, llm.Text{Text: "hello"}),
	}
	s, err := openFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.transcripts().Append(t.Context(), "run", "flow/agent", want); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(t.Context(), "run", flow.Checkpoint{Version: flow.CheckpointVersion, RunID: "run", Status: flow.StatusSuspended}); err != nil {
		t.Fatal(err)
	}

	again, err := openFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := again.transcripts().Load(t.Context(), "run", "flow/agent")
	if err != nil {
		t.Fatal(err)
	}
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	if string(g) != string(w) {
		t.Errorf("reloaded transcript = %s, want %s", g, w)
	}
	if ck, err := again.Load(t.Context(), "run"); err != nil || ck.Status != flow.StatusSuspended {
		t.Errorf("reloaded checkpoint = %+v, %v", ck, err)
	}
}
