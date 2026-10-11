package agentstep_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentstep"
	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// rig runs agent steps in flows and, when the test ends, checks the
// invariants that must hold after every run (ENGINEERING §2.6):
//
//  1. Every transcript the saver held is valid (ends on a whole turn) and a
//     prefix of the next one it held for the same step.
//  2. A harness run that finished has a Result whose transcript equals the
//     saver's final transcript for that step.
//  3. Every Output in a final state holds the saver's final transcript of some
//     step, and its Answer is that transcript's last text.
//  4. A flow that finished holds exactly the declared state keys.
//
// Every test of the package builds its steps with a rig.
type rig struct {
	t      *testing.T
	saver  *recSaver
	cp     *failingCheckpointer
	tools  tool.Registry
	keys   []string // the state keys a finished flow must hold
	mu     sync.Mutex
	result map[string]harness.Result // path → last finished harness run
	finals []*flow.State
}

type lookupInput struct {
	Q string `json:"q"`
}

func newRig(t *testing.T, keys ...string) *rig {
	t.Helper()
	lookup, err := tool.Func("lookup", "Look a topic up.",
		func(_ context.Context, in lookupInput) (string, error) { return "notes on " + in.Q, nil })
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tool.NewRegistry([]tool.Tool{lookup})
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{
		t:      t,
		saver:  newRecSaver(agentstep.NewMemorySaver()),
		cp:     &failingCheckpointer{Memory: flow.NewMemory()},
		tools:  reg,
		keys:   keys,
		result: map[string]harness.Result{},
	}
	t.Cleanup(r.check)
	return r
}

// agent returns an agent step that asks model.
func (r *rig) agent(name string, model llm.Model, keys agentstep.Keys) *agentstep.Step {
	r.t.Helper()
	h, err := harness.New(model, r.tools, harness.WithSystem("You are a test agent."))
	if err != nil {
		r.t.Fatal(err)
	}
	s, err := agentstep.New(name, h, r.saver, keys, agentstep.WithObserver(r.observe))
	if err != nil {
		r.t.Fatal(err)
	}
	return s
}

func (r *rig) observe(path string, ev harness.Event) {
	if ev.Kind != harness.EventRunDone {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.result[path] = *ev.Result
}

// run starts flow run id with the state {"topic": "go"}.
func (r *rig) run(ctx context.Context, id string, step flow.Step) (*flow.State, error) {
	st := flow.NewState()
	if err := flow.Set(st, "topic", "go"); err != nil {
		r.t.Fatal(err)
	}
	return r.done(flow.Run(ctx, id, step, flow.WithCheckpointer(r.cp), flow.WithState(st)))
}

func (r *rig) resume(ctx context.Context, id string, step flow.Step, opts ...flow.Option) (*flow.State, error) {
	return r.done(flow.Resume(ctx, id, step, append(opts, flow.WithCheckpointer(r.cp))...))
}

func (r *rig) done(st *flow.State, err error) (*flow.State, error) {
	if err == nil {
		r.mu.Lock()
		r.finals = append(r.finals, st.Clone())
		r.mu.Unlock()
	}
	return st, err
}

func (r *rig) check() {
	t := r.t
	final := map[string]string{} // step path → its saved final transcript, as JSON
	saved := map[string]bool{}   // every saved final transcript, as JSON
	for k, snaps := range r.saver.history() {
		for i, s := range snaps {
			if err := s.Validate(); err != nil {
				t.Errorf("invariant 1: %s: saved transcript %d is not valid: %v", k, i, err)
			}
			if i > 0 && !isPrefix(snaps[i-1], s) {
				t.Errorf("invariant 1: %s: saved transcript %d does not extend the one before", k, i)
			}
		}
		final[k[1]] = jsonOf(t, snaps[len(snaps)-1])
		saved[final[k[1]]] = true
	}
	for path, res := range r.result {
		if jsonOf(t, res.Transcript) != final[path] {
			t.Errorf("invariant 2: %s: the harness Result's transcript is not what was saved", path)
		}
	}
	for _, st := range r.finals {
		if got := st.Keys(); !slices.Equal(got, r.keys) {
			t.Errorf("invariant 4: final state keys = %q, want %q", got, r.keys)
		}
		for _, k := range st.Keys() {
			out, err := flow.Get[agentstep.Output](st, k)
			if err != nil || out.Transcript == nil {
				continue // not an agent's output
			}
			if !saved[jsonOf(t, out.Transcript)] {
				t.Errorf("invariant 3: output %q holds a transcript that was never saved", k)
			}
			if want := out.Transcript[len(out.Transcript)-1].Text(); out.Answer != want {
				t.Errorf("invariant 3: output %q answer = %q, want %q", k, out.Answer, want)
			}
		}
	}
}

func isPrefix(a, b harness.Transcript) bool {
	if len(a) > len(b) {
		return false
	}
	for i := range a {
		x, _ := json.Marshal(a[i])
		y, _ := json.Marshal(b[i])
		if string(x) != string(y) {
			return false
		}
	}
	return true
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// recSaver records what its saver holds after every Append, and can fail an
// Append or run a function after one (to stop the run there, like a crash).
type recSaver struct {
	agentstep.TranscriptSaver
	mu      sync.Mutex
	snaps   map[[2]string][]harness.Transcript
	appends int
	failAt  int    // the Append (counting from 1) that fails; 0 for none
	after   func() // called after each successful Append, with its count
	afterN  int
}

var errSave = errors.New("save failed")

func newRecSaver(s agentstep.TranscriptSaver) *recSaver {
	return &recSaver{TranscriptSaver: s, snaps: map[[2]string][]harness.Transcript{}}
}

func (s *recSaver) Append(ctx context.Context, runID, path string, msgs []llm.Message) error {
	s.mu.Lock()
	s.appends++
	n := s.appends
	s.mu.Unlock()
	if n == s.failAt {
		return errSave
	}
	if err := s.TranscriptSaver.Append(ctx, runID, path, msgs); err != nil {
		return err
	}
	t, err := s.Load(context.WithoutCancel(ctx), runID, path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	k := [2]string{runID, path}
	s.snaps[k] = append(s.snaps[k], t)
	after := s.after
	s.mu.Unlock()
	if after != nil && n == s.afterN {
		after()
	}
	return nil
}

func (s *recSaver) history() map[[2]string][]harness.Transcript {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[[2]string][]harness.Transcript{}
	for k, v := range s.snaps {
		out[k] = slices.Clone(v)
	}
	return out
}

// failingCheckpointer fails the first save of a checkpoint that records the
// step at failPath: the step finished, but the process stopped before flow
// could record it.
type failingCheckpointer struct {
	*flow.Memory
	mu       sync.Mutex
	failPath string
}

var errCheckpoint = errors.New("checkpoint lost")

func (c *failingCheckpointer) Save(ctx context.Context, id string, ck flow.Checkpoint) error {
	c.mu.Lock()
	if _, ok := ck.Steps[c.failPath]; ok && c.failPath != "" {
		c.failPath = ""
		c.mu.Unlock()
		return errCheckpoint
	}
	c.mu.Unlock()
	return c.Memory.Save(ctx, id, ck)
}

func call(id, q string) llm.ToolUse {
	return llm.ToolUse{ID: id, Name: "lookup", Input: json.RawMessage(`{"q":"` + q + `"}`)}
}

// threeTurns is a model that looks two things up, one per turn, then answers.
func threeTurns() []llmtest.Reply {
	return []llmtest.Reply{
		llmtest.ToolCalls(call("c1", "syntax")),
		llmtest.ToolCalls(call("c2", "runtime")),
		llmtest.Text("Go is simple."),
	}
}

// texts returns the text of every message, for short comparisons.
func texts(t harness.Transcript) string {
	var b strings.Builder
	for _, m := range t {
		b.WriteString(string(m.Role) + ":")
		for _, blk := range m.Content {
			switch v := blk.(type) {
			case llm.Text:
				b.WriteString(v.Text)
			case llm.ToolUse:
				b.WriteString("call " + v.ID)
			case llm.ToolResult:
				b.WriteString("result " + v.Content)
			}
		}
		b.WriteString("|")
	}
	return b.String()
}
