package runner_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentstep"
	"github.com/bsamanvith/agentkit/pkg/agentstep/agentsteptest"
	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/memory"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

func TestSaverConformance(t *testing.T) {
	t.Parallel()
	agentsteptest.RunSaver(t, func(t *testing.T) agentstep.TranscriptSaver {
		acme, err := tenancy.NewTenant("acme")
		if err != nil {
			t.Fatal(err)
		}
		return runner.NewSaver(tenantStore{memory.New(), acme})
	})
}

// tenantStore puts its tenant into contexts that have none: the suite calls
// the saver as a flow would, and in the runner every flow runs with its
// run's tenant.
type tenantStore struct {
	store.ConversationStore
	tenant tenancy.Tenant
}

func (s tenantStore) with(ctx context.Context) context.Context {
	if _, err := tenancy.Require(ctx); err != nil {
		return tenancy.With(ctx, s.tenant)
	}
	return ctx
}

func (s tenantStore) Append(ctx context.Context, id string, e store.Entry) error {
	return s.ConversationStore.Append(s.with(ctx), id, e)
}

func (s tenantStore) History(ctx context.Context, id string, p store.Page) (store.EntryPage, error) {
	return s.ConversationStore.History(s.with(ctx), id, p)
}

func (s tenantStore) Context(ctx context.Context, id string) (store.ContextView, error) {
	return s.ConversationStore.Context(s.with(ctx), id)
}

// agentFlow is a flow of one agent step "f/agent" that answers the prompt in
// the state key "prompt", saved through the process's store.
func (w *world) agentFlow() {
	w.flows = func(p *proc, _ string) (flow.Step, error) {
		h, err := harness.New(w.model, w.tools, harness.WithClock(w.clk))
		if err != nil {
			return nil, err
		}
		s, err := runner.NewAgentStep("agent", h, runner.NewSaver(p.store), agentstep.Keys{Input: "prompt", Output: "out"})
		if err != nil {
			return nil, err
		}
		return flow.Sequence("f", s), nil
	}
}

func (p *proc) runAgentFlow(prompt string) (*runner.Execution, error) {
	st := flow.NewState()
	if err := flow.Set(st, "prompt", prompt); err != nil {
		return nil, err
	}
	return p.r.RunFlow(p.w.ctx, "c", st)
}

// agentReference is what a plain agent step, with agentstep's own saver,
// answers to the prompt, and the requests it makes.
func agentReference(t *testing.T, name string, tools int) (agentstep.Output, []llm.Request) {
	t.Helper()
	w := newWorld(t)
	prompt := w.model.on(name, tools)
	h, err := harness.New(w.model, w.tools, harness.WithClock(w.clk))
	if err != nil {
		t.Fatal(err)
	}
	s, err := agentstep.New("agent", h, agentstep.NewMemorySaver(), agentstep.Keys{Input: "prompt", Output: "out"})
	if err != nil {
		t.Fatal(err)
	}
	st := flow.NewState()
	if err := flow.Set(st, "prompt", prompt); err != nil {
		t.Fatal(err)
	}
	st, err = flow.Run(w.ctx, "ref", flow.Sequence("f", s), flow.WithState(st), flow.WithCheckpointer(flow.NewMemory()))
	if err != nil {
		t.Fatal(err)
	}
	out, err := flow.Get[agentstep.Output](st, "out")
	if err != nil {
		t.Fatal(err)
	}
	return out, w.model.requestsFor(prompt)
}

// An agent step killed after k turns is resumed by recovery from the
// transcript the saver stored, and makes the requests an uninterrupted step
// makes after them. Its output references a transcript that reads back
// byte-identical to the uninterrupted one.
func TestAgentStepResumesFromTheStoreAfterACrash(t *testing.T) {
	t.Parallel()
	const tools = 3
	ref, refReqs := agentReference(t, "a", tools)
	for k := 1; k <= tools+1; k++ { // the prompt and k-1 turns are stored
		t.Run(js(k), func(t *testing.T) {
			t.Parallel()
			w := newWorld(t)
			w.agentFlow()
			prompt := w.model.on("a", tools)
			dead := w.process()
			dead.store.crashAt(crashAtNth("Append", k+1, false))
			x, err := dead.runAgentFlow(prompt)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := x.Wait(); !errors.Is(err, errCrashed) {
				t.Fatalf("crashed run ended with %v", err)
			}
			before := len(w.model.Requests())
			w.expire()
			p := w.process()
			if rep, err := w.recovery(p).Recover(w.ctx); err != nil || rep.Resumed != 1 {
				t.Fatalf("Recover = %+v, %v", rep, err)
			}
			if got, want := w.model.Requests()[before:], refReqs[k-1:]; !sameJSON(got, want) {
				t.Errorf("after %d stored turns the resumed step made %d requests, want %d", k-1, len(got), len(want))
			}
			evs, err := p.resume(x.Run.ID) // completed: its final state
			if err != nil {
				t.Fatal(err)
			}
			out, err := flow.Get[runner.StepOutput](lastDone(t, evs).State, "out")
			if err != nil {
				t.Fatal(err)
			}
			if out.Answer != ref.Answer || out.ConversationID != runner.StepConversation(x.Run.ID, "f/agent") {
				t.Errorf("output = %+v, want answer %q", out, ref.Answer)
			}
			// The step's conversation is a log like a chat's: the prompt,
			// then one turn per saved turn, each labelled with the run.
			for i, e := range w.log(out.ConversationID) {
				if want := map[bool]store.Kind{true: store.KindPrompt, false: store.KindTurn}[i == 0]; e.Kind != want || e.RunID != x.Run.ID {
					t.Errorf("entry %d of the step's conversation is a %s of %q, want a %s of %q", e.Seq, e.Kind, e.RunID, want, x.Run.ID)
				}
			}
			tr, err := runner.NewSaver(w.mem).Transcript(w.ctx, out)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := json.Marshal(tr)
			want, _ := json.Marshal(ref.Transcript)
			if string(got) != string(want) {
				t.Errorf("stored transcript\n%s\nuninterrupted\n%s", got, want)
			}
		})
	}
}

// The checkpoint holds a reference to the agent's transcript, so its size
// does not grow with the number of turns.
func TestCheckpointSizeDoesNotGrowWithTurns(t *testing.T) {
	t.Parallel()
	size := func(tools int) (int, runner.StepOutput) {
		w := newWorld(t)
		w.agentFlow()
		x, err := w.process().runAgentFlow(w.model.on("a", tools))
		if err != nil {
			t.Fatal(err)
		}
		done, err := x.Wait()
		if err != nil {
			t.Fatal(err)
		}
		out, err := flow.Get[runner.StepOutput](done.State, "out")
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(w.checkpoint(x.Run.ID))
		if err != nil {
			t.Fatal(err)
		}
		return len(b), out
	}
	// Sizes compare exactly as long as the references have as many digits.
	small, out1 := size(1)
	large, out7 := size(7)
	if small != large {
		t.Errorf("checkpoint is %d bytes after 2 turns and %d after 8", small, large)
	}
	if out1.Seq != 3 || out7.Seq != 9 {
		t.Errorf("outputs reference entries %d and %d, want 3 and 9", out1.Seq, out7.Seq)
	}
}

func TestStepConversationsAreDistinct(t *testing.T) {
	t.Parallel()
	seen := map[string][2]string{}
	for _, c := range [][2]string{{"a", "x/y"}, {"a/x", "y"}, {"a%2Fx", "y"}, {"a", "x"}, {"b", "x"}} {
		id := runner.StepConversation(c[0], c[1])
		if prev, ok := seen[id]; ok {
			t.Errorf("%v and %v share %q", prev, c, id)
		}
		seen[id] = c
	}
}
