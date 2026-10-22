package agui_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agui"
)

// envRequireClient makes TestReferenceClient fail, not skip, when node or
// the client's packages are missing: CI sets it after `make agui-client`
// installs them.
const envRequireClient = "AGENTKIT_REQUIRE_AGUI_CLIENT"

// clientRun is one run of testdata/client/run.mjs, as it prints it.
type clientRun struct {
	Name       string            `json:"name"`
	Events     []json.RawMessage `json:"events"`
	Outcome    string            `json:"outcome"`
	Interrupts []agui.Interrupt  `json:"interrupts"`
	Result     json.RawMessage   `json:"result"`
	Error      *string           `json:"error"`
	Messages   []message         `json:"messages"`
}

// TestReferenceClient: AG-UI's reference client (@ag-ui/client 1.0.2's
// HttpAgent, which checks every event against the protocol's schemas and
// verifier), with AG-UI's A2UI middleware in front as CopilotKit's runtime
// has it, completes a run with a tool call and an A2UI surface against our
// server on the fake model; sends an action on that surface on the same
// thread; and resumes a run that ended on an approval. It prints no
// warning: the client strips, with a warning, anything the protocol does
// not describe.
//
// It needs node and the client's packages: `make agui-client` installs
// them (npm ci in testdata/client) and runs it.
func TestReferenceClient(t *testing.T) {
	t.Parallel()
	dir := filepath.Join("testdata", "client")
	node, err := exec.LookPath("node")
	if err == nil {
		_, err = os.Stat(filepath.Join(dir, "node_modules", "@ag-ui", "client"))
	}
	if err != nil {
		if os.Getenv(envRequireClient) != "" {
			t.Fatalf("the reference client cannot run: %v ($%s is set)", err, envRequireClient)
		}
		t.Skipf("the reference client cannot run (%v): `make agui-client` installs it and runs this test", err)
	}
	r := newRig(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "run.mjs", r.ts.URL, keyA)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("node run.mjs: %v\n%s", err, stderr.String())
	}
	var out struct {
		Runs     []clientRun `json:"runs"`
		Warnings []string    `json:"warnings"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("the client's output: %v\n%s", err, stdout.String())
	}
	for _, w := range out.Warnings {
		t.Errorf("the client warned: %s", w)
	}
	runs := map[string]clientRun{}
	for _, cr := range out.Runs {
		if cr.Error != nil {
			t.Errorf("run %s failed in the client: %s", cr.Name, *cr.Error)
		}
		runs[cr.Name] = cr
	}
	if len(runs) != 4 {
		t.Fatalf("runs %v, want four", out.Runs)
	}
	events := func(name string) []agui.Event { return decode(t, runs[name].Events) }

	// A tool call and a surface.
	surface := runs["surface"]
	evs := events("surface")
	conv, _ := bound(t, evs)
	var calls, results, activities int
	for _, e := range evs {
		switch e.Type {
		case agui.EventToolCallStart:
			calls++
		case agui.EventToolCallResult:
			results++
		case agui.EventActivitySnapshot:
			activities++
		}
	}
	if surface.Outcome != agui.OutcomeSuccess || string(surface.Result) != `"Pick a topic above."` || calls != 2 || results != 2 || activities != 1 {
		t.Errorf("surface: outcome %q result %s, %d calls, %d results, %d activities", surface.Outcome, surface.Result, calls, results, activities)
	}
	i := slices.IndexFunc(surface.Messages, func(m message) bool { return m.Role == "activity" })
	if i < 0 {
		t.Fatalf("surface: the client has no activity message: %+v", surface.Messages)
	}
	var content struct {
		Ops []map[string]json.RawMessage `json:"a2ui_operations"`
	}
	if err := json.Unmarshal(surface.Messages[i].Content, &content); err != nil || surface.Messages[i].ActivityType != agui.ActivityTypeA2UI ||
		len(content.Ops) != 3 || content.Ops[0]["createSurface"] == nil {
		t.Errorf("surface: the client's activity %+v", surface.Messages[i])
	}
	if !slices.ContainsFunc(surface.Messages, func(m message) bool {
		return m.Role == "assistant" && slices.ContainsFunc(m.ToolCalls, func(c agui.ToolCall) bool { return c.Function.Name == "echo" })
	}) {
		t.Errorf("surface: the client has no echo call: %+v", surface.Messages)
	}

	// The action, on the same thread: the same conversation.
	action := runs["action"]
	if c, _ := bound(t, events("action")); c != conv || action.Outcome != agui.OutcomeSuccess || string(action.Result) != `"You chose {\"topic\":[\"go\"]}."` {
		t.Errorf("action: in %s (want %s), outcome %q, result %s", c, conv, action.Outcome, action.Result)
	}

	// The approval, and the resume.
	held := runs["interrupt"]
	if held.Outcome != agui.OutcomeInterrupt || len(held.Interrupts) != 1 || held.Interrupts[0].Reason != agui.ReasonToolCall {
		t.Fatalf("interrupt: outcome %q, interrupts %+v", held.Outcome, held.Interrupts)
	}
	resumed := runs["resume"]
	if resumed.Outcome != agui.OutcomeSuccess || string(resumed.Result) != `"They said \"noted hi\"."` || r.notes.Load() != 1 {
		t.Errorf("resume: outcome %q, result %s, note ran %d times", resumed.Outcome, resumed.Result, r.notes.Load())
	}
	for _, e := range events("resume") {
		if e.Type == agui.EventToolCallStart {
			t.Errorf("resume: the answered call started again: %+v", e)
		}
	}
	if !slices.ContainsFunc(resumed.Messages, func(m message) bool {
		return m.Role == "tool" && m.ToolCallID == held.Interrupts[0].ToolCallID
	}) {
		t.Errorf("resume: the client has no result of the approved call: %+v", resumed.Messages)
	}
}
