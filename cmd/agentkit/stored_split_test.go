//go:build !noagent && !nomodels

package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/rpc"
	"github.com/bsamanvith/agentkit/pkg/runner/runnerrpc"
)

// splitConfig is registryTestConfig's toolsets with no agent of the file:
// every agent is stored.
const splitConfig = `{
  "mcp": {"toolsets": [
    {"name": "demo-a", "type": "demo"},
    {"name": "demo-b", "type": "demo"}
  ]},
  "agents": [{"name": "plain", "model": {"id": "m1"}, "toolsets": ["demo-a"]}]
}`

// serveRunner serves config's runner component, building models with m,
// and returns its URL.
func serveRunner(t *testing.T, config string, m *countingModels) string {
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
	return ts.URL
}

// TestSplitStoredAgents (T132): an agent API that runs its agents on a
// runner, and builds no model, keeps stored agents: it refuses a definition
// by field, its own checks' or the runner's, and saves nothing then; a
// published version runs on the runner, which builds it once, with every
// toolset of the file, and on each of its models.
func TestSplitStoredAgents(t *testing.T) {
	t.Parallel()
	m := newCountingModels(t, "m1", "m2", "m3-x", "m4")
	runnerURL := serveRunner(t, splitConfig, m)
	api := newCountingModels(t) // the API builds no model of its own
	c := serveModels(t, splitConfig, api, runnerURL)
	ctx := t.Context()
	for _, tc := range []struct{ name, def, field, says string }{
		{"unknown toolset", `{"model": {"id": "m4"}, "toolsets": ["nope"]}`, "toolsets", `"nope" is not a toolset`},
		{"memory", `{"model": {"id": "m4"}, "memory": {}}`, "memory", "cannot have memory"},
		{"unknown field", `{"model": {"id": "m4"}, "tolsets": ["demo-a"]}`, "tolsets", "unknown field"},
		{"runtime toolsets", `{"model": {"id": "m4"}, "runtime_toolsets": ["x"]}`, "runtime_toolsets", "off on this server"},
		// What only a build finds: the runner's refusals, by field.
		{"tools clash", `{"model": {"id": "m4"}, "toolsets": ["demo-a", "demo-b"]}`, "toolsets", "tool name clash"},
		{"bad model", `{"model": {"id": "no-such-model"}}`, "model", "no fake model"},
		{"model does not build", `{"model": {"id": "m4"}, "models": {"allow": ["no-such"]}}`, "models", "no fake model"},
	} {
		_, err := c.SaveAgent(ctx, "stored", wire.SaveAgentRequest{Definition: json.RawMessage(tc.def), Publish: true})
		var apiErr *agentclient.APIError
		if !errors.Is(err, agentclient.ErrInvalidDefinition) || !errors.As(err, &apiErr) {
			t.Errorf("%s: save = %v, want invalid_definition", tc.name, err)
			continue
		}
		i := slices.IndexFunc(apiErr.Fields, func(f wire.FieldError) bool { return f.Field == tc.field })
		if i < 0 || !strings.Contains(apiErr.Fields[i].Message, tc.says) {
			t.Errorf("%s: fields = %+v, want %s saying %q", tc.name, apiErr.Fields, tc.field, tc.says)
		}
	}
	if _, err := c.AgentVersions(ctx, "stored"); !errors.Is(err, agentclient.ErrNotFound) {
		t.Errorf("versions after refused saves = %v, want not found", err)
	}

	// A stored agent with a toolset no agent of the file names.
	if _, err := c.SaveAgent(ctx, "stored", wire.SaveAgentRequest{Definition: json.RawMessage(`{"model": {"id": "m4"}, "toolsets": ["demo-b"]}`), Publish: true}); err != nil {
		t.Fatalf("save: %v", err)
	}
	run, err := c.Run(ctx, agentclient.RunRequest{Agent: "stored", Message: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if run.Answer != "from m4" {
		t.Fatalf("run answered %q, want m4's answer", run.Answer)
	}
	if got := lastTools(m.fakes["m4"]); !slices.Contains(got, "echo") {
		t.Errorf("m4 was offered %v, want demo-b's tools", got)
	}
	builds := m.count("m4")
	if _, err := c.Run(ctx, agentclient.RunRequest{Agent: "stored", Message: "again"}); err != nil {
		t.Fatal(err)
	}
	if n := m.count("m4"); n != builds {
		t.Errorf("m4 built %d times after a second run, want %d: the runner keeps what it built", n, builds)
	}

	// The model picker: every model of a stored agent, on the runner.
	if _, err := c.SaveAgent(ctx, "multi", wire.SaveAgentRequest{Definition: json.RawMessage(multiDef), Publish: true}); err != nil {
		t.Fatalf("saving multi: %v", err)
	}
	if _, err := c.Run(ctx, agentclient.RunRequest{Agent: "multi", Message: "warm up"}); err != nil {
		t.Fatal(err)
	}
	m.fakes["m1"].reqs = nil
	checkModelRuns(t, c, m)
	if len(api.builds) != 0 {
		t.Errorf("the agent API built models %v", api.builds)
	}
}

// The runner builds a stored agent with the agent API's checks, whoever
// asks: a definition the agent API would refuse is refused there too.
func TestRunnerChecksStoredAgents(t *testing.T) {
	t.Parallel()
	m := newCountingModels(t, "m1", "m4")
	conn, err := rpc.Dial(serveRunner(t, splitConfig, m))
	if err != nil {
		t.Fatal(err)
	}
	c := runnerrpc.NewClient(conn)
	for _, tc := range []struct{ def, field string }{
		{`{"model": {"id": "m4"}, "skills": [{"path": "/etc"}]}`, "skills"},
		{`{"model": {"id": "m4"}, "isolation": "vm"}`, "isolation"},
		{`{"model": {"provider": "openai", "id": "x", "base_url": "https://evil.example"}}`, "model.base_url"},
	} {
		err := c.CheckStored(t.Context(), "stored", json.RawMessage(tc.def))
		de, ok := errors.AsType[*runnerrpc.DefinitionError](err)
		if !ok || !slices.ContainsFunc(de.Fields, func(f runnerrpc.FieldError) bool { return f.Field == tc.field }) {
			t.Errorf("%s: CheckStored = %v, want a refusal of %s", tc.def, err, tc.field)
		}
		for _, err := range c.StoredAgent("stored", json.RawMessage(tc.def), harness.ContextWindow{}).Run(t.Context(), harness.Input{Message: harness.UserText("hi")}) {
			if !errors.Is(err, runnerrpc.ErrInvalidDefinition) {
				t.Errorf("%s: run = %v, want ErrInvalidDefinition", tc.def, err)
			}
			break
		}
	}
}

// lastTools names the tools r's last request offered.
func lastTools(r *modelRecorder) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.reqs) == 0 {
		return nil
	}
	var out []string
	for _, d := range r.reqs[len(r.reqs)-1].Tools {
		out = append(out, d.Name)
	}
	return out
}
