//go:build !noagent && !nomodels

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/rpc"
	"github.com/bsamanvith/agentkit/pkg/runner/runnerrpc"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
)

// modelsConfig is a server whose agent "multi" runs m1 by default, allows
// m2 by name and m3-* by pattern, and appends to its prompt on m2.
var modelsConfig = `{
  "mcp": {"toolsets": [{"name": "demo-a", "type": "demo"}]},
  "agents": [{"name": "multi", ` + multiDef[1:] + `]
}`

// multiDef is multi's definition, as a stored agent too.
const multiDef = `{"model": {"id": "m1"}, "system": "You are multi.", "toolsets": ["demo-a"],
  "models": {
    "allow": ["m2"],
    "allow_patterns": ["anthropic/m3-.*"],
    "variants": [{"match": "m2", "system_append": "Be brief."}, {"match": "re:anthropic/m3-.*", "system": "You are three."}]
  }}`

// countingModels are fake models by id, each answering every request, that
// count how often each is built.
type countingModels struct {
	t      *testing.T
	mu     sync.Mutex
	fakes  map[string]*modelRecorder
	builds map[string]int
}

// modelRecorder answers every request with its id and keeps the requests.
type modelRecorder struct {
	id   string
	mu   sync.Mutex
	reqs []llm.Request
	t    *testing.T
}

func (r *modelRecorder) Generate(ctx context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	r.mu.Lock()
	r.reqs = append(r.reqs, req.Clone())
	r.mu.Unlock()
	return llmtest.NewFake(r.t, llmtest.Text("from "+r.id)).Generate(ctx, req)
}

func (r *modelRecorder) systems() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, q := range r.reqs {
		out = append(out, q.System)
	}
	return out
}

func newCountingModels(t *testing.T, ids ...string) *countingModels {
	m := &countingModels{t: t, fakes: map[string]*modelRecorder{}, builds: map[string]int{}}
	for _, id := range ids {
		m.fakes[id] = &modelRecorder{id: id, t: t}
	}
	return m
}

func (m *countingModels) build(f modelFlags) (llm.Model, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.fakes[f.model]
	if !ok {
		return nil, fmt.Errorf("no fake model %q", f.model)
	}
	m.builds[f.model]++
	return r, nil
}

func (m *countingModels) count(id string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.builds[id]
}

// serveModels serves config as serveAgents does, on m's models; with
// runnerURL, the agents run on the runner there.
func serveModels(t *testing.T, config string, m *countingModels, runnerURL string) *agentclient.Client {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agentkit.json")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadServeConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	c := cli{getenv: func(string) string { return "" }, newModel: m.build}
	env := serveEnv{components: []string{agentapi.Name}, addr: "127.0.0.1:0", shutdownTimeout: time.Second, runnerURL: runnerURL}
	log := slog.New(slog.DiscardHandler)
	built, problems, err := c.buildToolsets(t.Context(), env, cfg, path, log)
	if err != nil {
		t.Fatal(err)
	}
	mount, _, err := c.newAgents(t.Context(), cfg, built, problems, env, nil, scoped.Memory(nil), nil, clock.Real(), log)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(mount.Component.Handler())
	t.Cleanup(ts.Close)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = mount.Component.Shutdown(ctx)
	})
	client, err := agentclient.New(ts.URL, agentclient.WithHTTPClient(ts.Client()))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// checkModelRuns runs multi on each of its models through client, twice,
// and checks which fake answered, what system prompt it got, and that no
// run built a model again.
func checkModelRuns(t *testing.T, client *agentclient.Client, m *countingModels) {
	t.Helper()
	ctx := t.Context()
	for _, tc := range []struct{ model, id, want, system string }{
		{"", "m1", "anthropic/m1", "You are multi."},
		{"m2", "m2", "anthropic/m2", "You are multi.\n\nBe brief."},
		{"anthropic/m3-x", "m3-x", "anthropic/m3-x", "You are three."},
	} {
		before := m.count(tc.id)
		for range 2 {
			run, err := client.Run(ctx, agentclient.RunRequest{Agent: "multi", Message: "hi", Model: tc.model})
			if err != nil {
				t.Fatalf("run on %q: %v", tc.model, err)
			}
			if run.Model != tc.want || run.Answer != "from "+tc.id {
				t.Errorf("run on %q = model %q, answer %q; want %q from %s", tc.model, run.Model, run.Answer, tc.want, tc.id)
			}
		}
		if got := m.fakes[tc.id].systems(); len(got) != 2 || got[0] != tc.system {
			t.Errorf("%s got systems %q, want %q twice", tc.id, got, tc.system)
		}
		// The models allowed by name were built with the agent; one a
		// pattern allows, by its first run.
		want := 0
		if tc.id == "m3-x" {
			want = 1
		}
		if n := m.count(tc.id) - before; n != want {
			t.Errorf("%s was built %d times by its runs, want %d", tc.id, n, want)
		}
	}
	_, err := client.Run(ctx, agentclient.RunRequest{Agent: "multi", Message: "hi", Model: "m4"})
	if !errors.Is(err, agentclient.ErrModelNotAllowed) {
		t.Errorf("run on m4 = %v, want ErrModelNotAllowed", err)
	}
	a, err := client.Agent(ctx, "multi")
	if err != nil {
		t.Fatal(err)
	}
	if a.Models.Default != "anthropic/m1" || !slices.Equal(a.Models.Allow, []string{"anthropic/m1", "anthropic/m2"}) || !slices.Equal(a.Models.AllowPatterns, []string{"anthropic/m3-.*"}) {
		t.Errorf("multi's models = %+v", a.Models)
	}
}

// An agent of the config file runs each model it allows, on its own model
// and with its variant's prompt, building each once.
func TestConfigAgentModels(t *testing.T) {
	t.Parallel()
	m := newCountingModels(t, "m1", "m2", "m3-x")
	checkModelRuns(t, serveModels(t, modelsConfig, m, ""), m)
}

// The same through the split deployment: the agent API in this process,
// the runner component serving the models.
func TestSplitRunnerModels(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "agentkit.json")
	if err := os.WriteFile(path, []byte(modelsConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadServeConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	m := newCountingModels(t, "m1", "m2", "m3-x", "m5")
	c := cli{getenv: func(string) string { return "" }, newModel: m.build}
	env := serveEnv{components: []string{runnerName}, addr: "127.0.0.1:0", shutdownTimeout: time.Second}
	log := slog.New(slog.DiscardHandler)
	built, _, err := c.buildToolsets(t.Context(), env, cfg, path, log)
	if err != nil {
		t.Fatal(err)
	}
	mount, err := c.newRunner(t.Context(), cfg, built, env, clock.Real(), log)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(mount.Component.Handler())
	t.Cleanup(ts.Close)
	api := newCountingModels(t) // the API builds no model of its own
	checkModelRuns(t, serveModels(t, modelsConfig, api, ts.URL), m)
	if len(api.builds) != 0 {
		t.Errorf("the agent API built models %v", api.builds)
	}
	// The runner refuses a model the agent does not allow, whoever asks,
	// though it could build it.
	conn, err := rpc.Dial(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	a := runnerrpc.NewClient(conn).ModelAgent("multi", "anthropic/m5", harness.ContextWindow{})
	for _, err := range a.Run(t.Context(), harness.Input{Message: harness.UserText("hi")}) {
		if !errors.Is(err, runnerrpc.ErrNoModel) {
			t.Errorf("a run on m5 at the runner = %v, want ErrNoModel", err)
		}
		break
	}
}

// A stored agent takes models too; a bad models section is refused by
// field, and nothing is saved.
func TestStoredAgentModels(t *testing.T) {
	t.Parallel()
	m := newCountingModels(t, "model-plain", "m1", "m2", "m3-x")
	c := serveModels(t, registryTestConfig, m, "")
	ctx := t.Context()
	for _, tc := range []struct{ name, models, field, says string }{
		{"bad pattern", `{"allow_patterns": ["("]}`, "models.allow_patterns[0]", "not a pattern"},
		{"unknown provider", `{"allow": ["gemini/x"]}`, "models.allow[0]", `provider "gemini"`},
		{"bad name", `{"allow": ["a b"]}`, "models.allow[0]", "invalid model name"},
		{"variant not allowed", `{"variants": [{"match": "m2"}]}`, "models.variants[0].match", "not a model the agent allows"},
		{"system and append", `{"allow": ["m2"], "variants": [{"match": "m2", "system": "a", "system_append": "b"}]}`, "models.variants[0].system_append", "not both"},
		{"does not build", `{"allow": ["no-such"]}`, "models", "no fake model"},
		{"unknown field", `{"allowed": ["m2"]}`, "allowed", "unknown field"},
	} {
		def := `{"model": {"id": "m1"}, "models": ` + tc.models + `}`
		_, err := c.SaveAgent(ctx, "stored", wire.SaveAgentRequest{Definition: json.RawMessage(def), Publish: true})
		var api *agentclient.APIError
		if !errors.Is(err, agentclient.ErrInvalidDefinition) || !errors.As(err, &api) {
			t.Errorf("%s: save = %v, want invalid_definition", tc.name, err)
			continue
		}
		i := slices.IndexFunc(api.Fields, func(f wire.FieldError) bool { return f.Field == tc.field })
		if i < 0 || !strings.Contains(api.Fields[i].Message, tc.says) {
			t.Errorf("%s: fields = %+v, want %s saying %q", tc.name, api.Fields, tc.field, tc.says)
		}
	}
	if _, err := c.AgentVersions(ctx, "stored"); !errors.Is(err, agentclient.ErrNotFound) {
		t.Errorf("versions after refused saves = %v, want not found", err)
	}
	if _, err := c.SaveAgent(ctx, "multi", wire.SaveAgentRequest{Definition: json.RawMessage(multiDef), Publish: true}); err != nil {
		t.Fatalf("saving multi: %v", err)
	}
	// The registry builds a published version on its first run, and keeps
	// it: every model allowed by name with it.
	if _, err := c.Run(ctx, agentclient.RunRequest{Agent: "multi", Message: "warm up"}); err != nil {
		t.Fatal(err)
	}
	m.fakes["m1"].reqs = nil
	checkModelRuns(t, c, m)
}
