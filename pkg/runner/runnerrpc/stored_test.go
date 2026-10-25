package runnerrpc_test

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"slices"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/rpc"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/runner/runnerrpc"
)

// storedDef is a stored agent's definition in these tests: the text its
// harness answers, or a field to refuse.
type storedDef struct {
	Answer string `json:"answer"`
	Bad    string `json:"bad,omitempty"`
}

// storedBuilds builds storedDef definitions, and records what it was asked.
type storedBuilds struct {
	t     *testing.T
	mu    sync.Mutex
	asked []string // "<name> <definition>"
}

func (b *storedBuilds) build(_ context.Context, name string, def json.RawMessage) (runner.Agent, error) {
	b.mu.Lock()
	b.asked = append(b.asked, name+" "+string(def))
	b.mu.Unlock()
	var d storedDef
	if err := json.Unmarshal(def, &d); err != nil {
		return nil, err
	}
	if d.Bad != "" {
		return nil, &runnerrpc.DefinitionError{Fields: []runnerrpc.FieldError{{Field: d.Bad, Message: "is bad"}, {Message: "and the whole"}}}
	}
	return newHarness(b.t, llmtest.Text(d.Answer), llmtest.Text("summary")), nil
}

// A stored agent's run carries its definition, which the server builds:
// the run answers with that agent, not with a served agent of the same
// name. CheckStored builds without running.
func TestStoredAgentRuns(t *testing.T) {
	t.Parallel()
	b := &storedBuilds{t: t}
	s := rpc.NewServer()
	if err := runnerrpc.Register(s, map[string]runner.Agent{"same": newHarness(t, llmtest.Text("config agent"))}, runnerrpc.WithStored(b.build)); err != nil {
		t.Fatal(err)
	}
	c := runnerrpc.NewClient(rpc.Pipe(s))
	def := json.RawMessage(`{"answer": "stored agent"}`)
	evs, err := collect(c.StoredAgent("same", def, harness.ContextWindow{}), harness.Input{Message: llm.User(llm.Text{Text: "x"})})
	if err != nil {
		t.Fatal(err)
	}
	if r := evs[len(evs)-1].Result; r == nil || r.Final.Text() != "stored agent" {
		t.Fatalf("result %+v, want the stored agent's answer", r)
	}
	if err := c.CheckStored(t.Context(), "other", json.RawMessage(`{"answer": "a"}`)); err != nil {
		t.Errorf("CheckStored = %v", err)
	}
	// The definition crosses compacted.
	want := []string{`same {"answer":"stored agent"}`, `other {"answer":"a"}`}
	if !slices.Equal(b.asked, want) {
		t.Errorf("built %q, want %q", b.asked, want)
	}
	// The served agents are still the given ones.
	if names, err := c.Agents(t.Context()); err != nil || !slices.Equal(names, []string{"same"}) {
		t.Errorf("Agents = %v, %v", names, err)
	}
}

// A definition the builder refuses fails a check, a run and a compaction
// with a *DefinitionError naming the same fields, not retryable.
func TestStoredAgentRefused(t *testing.T) {
	t.Parallel()
	s := rpc.NewServer()
	if err := runnerrpc.Register(s, nil, runnerrpc.WithStored((&storedBuilds{t: t}).build)); err != nil {
		t.Fatal(err)
	}
	c := runnerrpc.NewClient(rpc.Pipe(s))
	def := json.RawMessage(`{"bad": "model.id"}`)
	want := []runnerrpc.FieldError{{Field: "model.id", Message: "is bad"}, {Message: "and the whole"}}
	check := func(what string, err error) {
		t.Helper()
		de, ok := errors.AsType[*runnerrpc.DefinitionError](err)
		if !ok || !errors.Is(err, runnerrpc.ErrInvalidDefinition) || !slices.Equal(de.Fields, want) || llm.IsRetryable(err) {
			t.Errorf("%s: err = %v, want a DefinitionError with %+v, not retryable", what, err, want)
		}
	}
	check("check", c.CheckStored(t.Context(), "a", def))
	a := c.StoredAgent("a", def, harness.ContextWindow{})
	_, err := collect(a, harness.Input{Message: llm.User(llm.Text{Text: "x"})})
	check("run", err)
	_, err = a.Compact(t.Context(), nil)
	check("compact", err)
}

// A server without WithStored refuses stored agents with ErrNoStored,
// though it serves an agent of the same name.
func TestNoStoredAgents(t *testing.T) {
	t.Parallel()
	s := rpc.NewServer()
	if err := runnerrpc.Register(s, map[string]runner.Agent{"same": newHarness(t, llmtest.Text("config agent"))}); err != nil {
		t.Fatal(err)
	}
	c := runnerrpc.NewClient(rpc.Pipe(s))
	def := json.RawMessage(`{"answer": "x"}`)
	if err := c.CheckStored(t.Context(), "same", def); !errors.Is(err, runnerrpc.ErrNoStored) {
		t.Errorf("CheckStored = %v, want ErrNoStored", err)
	}
	_, err := collect(c.StoredAgent("same", def, harness.ContextWindow{}), harness.Input{Message: llm.User(llm.Text{Text: "x"})})
	if !errors.Is(err, runnerrpc.ErrNoStored) {
		t.Errorf("run = %v, want ErrNoStored", err)
	}
}

// A stored agent on another model runs what the built agent's ForModel
// returns, and compacts with the built agent.
func TestStoredAgentOnAModel(t *testing.T) {
	t.Parallel()
	s := rpc.NewServer()
	build := func(context.Context, string, json.RawMessage) (runner.Agent, error) {
		return models{
			Harness: newHarness(t, llmtest.Text("summary")),
			on:      map[string]*harness.Harness{"anthropic/m2": newHarness(t, llmtest.Text("on m2"))},
		}, nil
	}
	if err := runnerrpc.Register(s, nil, runnerrpc.WithStored(build)); err != nil {
		t.Fatal(err)
	}
	c := runnerrpc.NewClient(rpc.Pipe(s))
	def := json.RawMessage(`{}`)
	evs, err := collect(c.StoredModelAgent("a", "anthropic/m2", def, harness.ContextWindow{}), harness.Input{Message: llm.User(llm.Text{Text: "x"})})
	if err != nil {
		t.Fatal(err)
	}
	if r := evs[len(evs)-1].Result; r == nil || r.Final.Text() != "on m2" {
		t.Errorf("result %+v, want m2's answer", r)
	}
	if _, err := c.StoredAgent("a", def, harness.ContextWindow{}).Compact(t.Context(), harness.Transcript{llm.User(llm.Text{Text: "x"})}); err != nil {
		t.Errorf("compact = %v", err)
	}
}

// A runner older than stored agents serves Run, which would run a config
// agent of the same name: a stored agent's run fails instead.
func TestOldRunnerRefusesAStoredAgent(t *testing.T) {
	t.Parallel()
	s := rpc.NewServer()
	type oldReq struct {
		Agent string `json:"agent"`
	}
	ran := false
	for _, name := range []string{"Run", "RunWithPolicy", "RunWithInterrupts", "RunWithTools", "RunWithModel"} {
		rpc.Stream[oldReq, struct{}]{Name: runnerrpc.Service + "/" + name}.Handle(s, func(context.Context, oldReq) iter.Seq2[struct{}, error] {
			return func(func(struct{}, error) bool) { ran = true }
		})
	}
	c := runnerrpc.NewClient(rpc.Pipe(s))
	_, err := collect(c.StoredAgent("a", json.RawMessage(`{}`), harness.ContextWindow{}), harness.Input{Message: llm.User(llm.Text{Text: "x"})})
	if !errors.Is(err, rpc.ErrUnknownMethod) || ran {
		t.Errorf("err = %v (an old method ran: %v), want ErrUnknownMethod", err, ran)
	}
}
