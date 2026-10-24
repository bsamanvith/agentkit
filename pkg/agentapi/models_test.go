package agentapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/modelset"
)

// modelRig is a server with one agent, "multi", on haiku by default, that
// also allows sonnet by name, opus by pattern and an openai model, each
// built by ForModel on a model of its own with its variant's system
// prompt.
type modelRig struct {
	*rig
	mu     sync.Mutex
	models map[string]*model // by model name; the default's too
}

const (
	haiku  = "anthropic/claude-haiku-5-5"
	sonnet = "anthropic/claude-sonnet-5-5"
	opus   = "anthropic/claude-opus-5-5"
	fable  = "anthropic/claude-fable-5-1"
	gpt    = "openai/gpt-5-mini"
)

func newModelRig(t *testing.T) *modelRig {
	t.Helper()
	clk := clocktest.NewFake(time.Time{})
	set, err := modelset.New(haiku, modelset.Spec{
		Allow:         []string{sonnet, gpt},
		AllowPatterns: []string{"anthropic/claude-opus-.*"},
		Variants: []modelset.Variant{
			{Match: sonnet, SystemAppend: "Be brief."},
			{Match: "re:openai/.*", System: "You are GPT."},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := &modelRig{models: map[string]*model{haiku: echo(t)}}
	const system = "You are multi."
	def := agentapi.Agent{
		Name: "multi", Model: wire.Model{Provider: "anthropic", ID: "claude-haiku-5-5"}, System: system,
		Harness: agentHarness(t, m.models[haiku], registry(t), harness.WithSystem(system), harness.WithClock(clk)),
		Models:  set,
	}
	def.ForModel = func(_ context.Context, name string) (agentapi.Agent, error) {
		m.mu.Lock()
		defer m.mu.Unlock()
		mm := m.models[name]
		if mm == nil {
			mm = echo(t)
			m.models[name] = mm
		}
		sys := system
		if v, ok := set.Variant(name); ok {
			switch {
			case v.System != "":
				sys = v.System
			case v.SystemAppend != "":
				sys += "\n\n" + v.SystemAppend
			}
		}
		a := def
		a.System, a.Harness = sys, agentHarness(t, mm, registry(t), harness.WithSystem(sys), harness.WithClock(clk))
		return a, nil
	}
	m.rig = newRig(t, m.models[haiku], rigConfig{agents: []agentapi.Agent{def}, clock: clk, api: []agentapi.Option{
		agentapi.WithModels(
			wire.ModelProvider{Name: "anthropic", Models: []string{haiku, sonnet, opus, fable}},
			wire.ModelProvider{Name: "openai", Models: []string{}},
		),
	}})
	return m
}

func (m *modelRig) modelOf(name string) *model {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.models[name]
}

// runModel posts a run of multi asking for model (JSON) and returns the
// response and its body.
func (m *modelRig) runModel(message, conversationID, model string) (*http.Response, []byte) {
	m.t.Helper()
	b, _ := json.Marshal(agentapi.RunRequest{Message: message, ConversationID: conversationID, Agent: "multi", Model: model})
	return m.do(http.MethodPost, "/v1/runs", string(b), "Accept", "application/json")
}

func (m *modelRig) mustRun(message, conversationID, model string) agentapi.Run {
	m.t.Helper()
	resp, body := m.runModel(message, conversationID, model)
	if resp.StatusCode != http.StatusOK {
		m.t.Fatalf("run on %q = %d %s", model, resp.StatusCode, body)
	}
	return decodeAs[agentapi.Run](m.t, body)
}

// The criterion's agent runs a name and a pattern it allows, each on its
// own model, and refuses another of the same family.
func TestRunsPickAnAllowedModel(t *testing.T) {
	t.Parallel()
	m := newModelRig(t)
	for _, name := range []string{"claude-sonnet-5-5", opus} {
		run := m.mustRun("hi", "", name)
		want, _ := modelset.Canonical(name)
		if run.Model != want {
			t.Errorf("run on %q reports model %q, want %q", name, run.Model, want)
		}
		if got := m.getRun(run.ID).Model; got != want {
			t.Errorf("GET /v1/runs/{id} of a run on %q = model %q", name, got)
		}
		if mm := m.modelOf(want); mm == nil || len(mm.requests()) != 1 {
			t.Errorf("the model of %q was not the one asked", want)
		}
	}
	if n := len(m.modelOf(haiku).requests()); n != 0 {
		t.Errorf("the default model got %d requests, want none", n)
	}
	resp, body := m.runModel("hi", "", fable)
	wantError(t, "a run on a model the agent does not allow", resp, body, http.StatusBadRequest, agentapi.CodeModelNotAllowed)
	resp, body = m.runModel("hi", "", "a model")
	wantError(t, "a run on a name that is no model", resp, body, http.StatusBadRequest, agentapi.CodeModelNotAllowed)
}

// No model runs the default; continuing a conversation without one keeps
// its last model; naming one switches within the provider.
func TestRunsKeepTheConversationsModel(t *testing.T) {
	t.Parallel()
	m := newModelRig(t)
	first := m.mustRun("one", "", "")
	if first.Model != haiku {
		t.Errorf("a run naming no model ran %q, want the default %q", first.Model, haiku)
	}
	conv := first.ConversationID
	if run := m.mustRun("two", conv, sonnet); run.Model != sonnet {
		t.Errorf("switching to sonnet ran %q", run.Model)
	}
	if run := m.mustRun("three", conv, ""); run.Model != sonnet {
		t.Errorf("continuing without a model ran %q, want the conversation's last, %q", run.Model, sonnet)
	}
	if n := len(m.modelOf(sonnet).requests()); n != 2 {
		t.Errorf("sonnet got %d requests, want 2", n)
	}
	// Another provider cannot read this conversation.
	resp, body := m.runModel("four", conv, gpt)
	wantError(t, "switching provider mid-conversation", resp, body, http.StatusConflict, agentapi.CodeModelProviderMismatch)
	if run := m.mustRun("four", "", gpt); run.Model != gpt {
		t.Errorf("a new conversation on gpt ran %q", run.Model)
	}
}

// A variant's system prompt is what its model receives, and only its model.
func TestVariantsChangeTheSystemPrompt(t *testing.T) {
	t.Parallel()
	m := newModelRig(t)
	m.mustRun("hi", "", sonnet)
	m.mustRun("hi", "", gpt)
	m.mustRun("hi", "", opus)
	m.mustRun("hi", "", "")
	for name, want := range map[string]string{
		sonnet: "You are multi.\n\nBe brief.",
		gpt:    "You are GPT.",
		opus:   "You are multi.",
		haiku:  "You are multi.",
	} {
		reqs := m.modelOf(name).requests()
		if len(reqs) != 1 || reqs[0].System != want {
			t.Errorf("%s got %d requests, system %q; want one, %q", name, len(reqs), systemOf(reqs), want)
		}
	}
}

func systemOf(reqs []llm.Request) string {
	if len(reqs) == 0 {
		return ""
	}
	return reqs[0].System
}

// The agent lists its models, and GET /v1/models the providers; a task
// takes a model too, and refuses one the agent does not allow.
func TestModelsAreListed(t *testing.T) {
	t.Parallel()
	m := newModelRig(t)
	var a wire.Agent
	if code := m.get("/v1/agents/multi", &a); code != http.StatusOK {
		t.Fatalf("GET /v1/agents/multi = %d", code)
	}
	if a.Models.Default != haiku || !slices.Equal(a.Models.Allow, []string{haiku, sonnet, gpt}) ||
		!slices.Equal(a.Models.AllowPatterns, []string{"anthropic/claude-opus-.*"}) ||
		!slices.Equal(a.Models.Choices, []string{haiku, sonnet, gpt, opus}) {
		t.Errorf("the agent's models = %+v", a.Models)
	}
	var list wire.ModelList
	if code := m.get("/v1/models", &list); code != http.StatusOK || list.Default != "anthropic" || len(list.Providers) != 2 || list.Providers[0].Name != "anthropic" {
		t.Errorf("GET /v1/models = %d %+v", code, list)
	}
	run := m.task(`{"agent":"multi","input":"go","model":"` + opus + `"}`)
	if run.Model != opus {
		t.Errorf("a task on opus reports model %q", run.Model)
	}
	m.waitTask(run.ID)
	resp, body := m.do(http.MethodPost, "/v1/tasks", `{"agent":"multi","input":"go","model":"`+fable+`"}`)
	wantError(t, "a task on a model the agent does not allow", resp, body, http.StatusBadRequest, agentapi.CodeModelNotAllowed)
}

// A resume keeps the run's model: it cannot name one.
func TestResumeCannotPickAModel(t *testing.T) {
	t.Parallel()
	m := newModelRig(t)
	run := m.mustRun("hi", "", sonnet)
	resp, body := m.do(http.MethodPost, "/v1/runs", `{"conversation_id":"`+run.ConversationID+`","model":"`+opus+`"}`)
	wantError(t, "a resume naming a model", resp, body, http.StatusBadRequest, agentapi.CodeInvalidRequest)
	if e := apiError(t, body); !strings.Contains(e.Message, "model needs a message") {
		t.Errorf("a resume naming a model: %q, want it to say a resume keeps its model", e.Message)
	}
}

// An agent whose models allow more than its default needs ForModel, and
// its default must be its model.
func TestModelsNeedForModel(t *testing.T) {
	t.Parallel()
	set, err := modelset.New(haiku, modelset.Spec{Allow: []string{sonnet}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := agentHarness(t, echo(t), registry(t))
	st, opts := testStore(t, clocktest.NewFake(time.Time{}))
	for name, a := range map[string]agentapi.Agent{
		"no ForModel":   {Name: "x", Model: wire.Model{ID: "claude-haiku-5-5"}, Harness: h, Models: set},
		"other default": {Name: "x", Model: wire.Model{ID: "claude-opus-5-5"}, Harness: h, Models: set, ForModel: func(context.Context, string) (agentapi.Agent, error) { return agentapi.Agent{}, nil }},
	} {
		if _, err := agentapi.NewAgents([]agentapi.Agent{a}, append(opts, agentapi.WithStore(st))...); err == nil {
			t.Errorf("%s: NewAgents succeeded", name)
		}
	}
}
