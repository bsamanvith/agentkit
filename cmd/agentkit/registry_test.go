//go:build !noagent && !nomodels

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
)

// registryTestConfig is a server with two demo toolsets whose tools clash
// unprefixed, one config agent ("plain") and the registry on by default.
const registryTestConfig = `{
  "mcp": {"toolsets": [
    {"name": "demo-a", "type": "demo"},
    {"name": "demo-b", "type": "demo"}
  ]},
  "agents": [{"name": "plain", "model": {"id": "model-plain"}, "toolsets": ["demo-a"]}]
}`

// TestStoredAgentRefused: serve's builder refuses a stored agent as it
// refuses an agent of its file, naming the field: a toolset the server does
// not have, tools that clash, a model that cannot be built or a provider it
// does not know, and what stored agents cannot have yet. Nothing is saved.
func TestStoredAgentRefused(t *testing.T) {
	t.Parallel()
	c := serveAgents(t, registryTestConfig, map[string]*llmtest.Fake{"model-plain": llmtest.NewFake(t), "m": llmtest.NewFake(t)})
	ctx := t.Context()
	for _, tc := range []struct {
		name, def, field, says string
	}{
		{"unknown toolset", `{"model": {"id": "m"}, "toolsets": ["nope"]}`, "toolsets", `"nope" is not a toolset`},
		{"tools clash", `{"model": {"id": "m"}, "toolsets": ["demo-a", "demo-b"]}`, "toolsets", "tool name clash"},
		{"bad provider", `{"model": {"provider": "nope", "id": "m"}}`, "model.provider", `"nope": want`},
		{"bad model", `{"model": {"id": "no-such-model"}}`, "model", "no fake model"},
		{"bad interaction", `{"model": {"id": "m"}, "interaction": "sometimes"}`, "interaction", "sometimes"},
		{"isolation", `{"model": {"id": "m"}, "isolation": "vm"}`, "isolation", "only process pools"},
		{"skills", `{"model": {"id": "m"}, "skills": [{"path": "skills"}]}`, "skills", "cannot have skills"},
		{"memory", `{"model": {"id": "m"}, "memory": {}}`, "memory", "cannot have memory"},
		{"ui", `{"model": {"id": "m"}, "ui": "a2ui"}`, "ui", "cannot show UI"},
		{"notify", `{"model": {"id": "m"}, "notify": {"url": "https://example.com/hook"}}`, "notify", "cannot have a webhook"},
		{"unknown field", `{"model": {"id": "m"}, "tolsets": ["demo-a"]}`, "tolsets", "unknown field"},
		{"wrong type", `{"model": {"id": "m"}, "max_turns": "many"}`, "max_turns", "want int"},
		{"config file's toolset at run time", `{"model": {"id": "m"}, "runtime_toolsets": ["demo-a"]}`, "runtime_toolsets", "toolset of the config file"},
	} {
		_, err := c.SaveAgent(ctx, "stored", wire.SaveAgentRequest{Definition: json.RawMessage(tc.def), Publish: true})
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
		t.Errorf("after refusals, versions = %v, want not found: nothing saved", err)
	}
	// The same definition, valid, is taken, and runs.
	if _, err := c.SaveAgent(ctx, "stored", wire.SaveAgentRequest{Definition: json.RawMessage(`{"model": {"id": "m"}, "toolsets": ["demo-a", "demo-b"], "prefix_tool_names": true}`), Publish: true}); err != nil {
		t.Fatalf("valid save: %v", err)
	}
	// The config file's agent is read-only.
	if _, err := c.SaveAgent(ctx, "plain", wire.SaveAgentRequest{Definition: json.RawMessage(`{"model": {"id": "m"}}`)}); !errors.Is(err, agentclient.ErrReadOnly) {
		t.Errorf("save over plain = %v, want read_only", err)
	}
}

// TestStoredAgentModelServer: a stored agent's openai model talks to the
// operator's server with the operator's key variable, or to a server and
// key variable an agent of the config file uses; any other base_url or
// api_key_env is refused, or a tenant admin could have the server send any
// of its variables (its keys, the store's URL) to a server of theirs.
func TestStoredAgentModelServer(t *testing.T) {
	t.Parallel()
	const cfg = `{
  "mcp": {"toolsets": [{"name": "demo-a", "type": "demo"}]},
  "agents": [
    {"name": "plain", "model": {"id": "model-plain"}},
    {"name": "local", "model": {"provider": "openai", "id": "m", "base_url": "http://localhost:11434/v1", "api_key_env": "OLLAMA_KEY"}}
  ]
}`
	c := serveAgents(t, cfg, map[string]*llmtest.Fake{"model-plain": llmtest.NewFake(t), "m": llmtest.NewFake(t)})
	ctx := t.Context()
	for _, tc := range []struct{ name, model, field string }{
		{"another server", `{"provider": "openai", "id": "m", "base_url": "https://attacker.example/v1"}`, "model.base_url"},
		{"another server, the file's variable", `{"provider": "openai", "id": "m", "base_url": "https://attacker.example/v1", "api_key_env": "OLLAMA_KEY"}`, "model.base_url"},
		{"the file's server, another variable", `{"provider": "openai", "id": "m", "base_url": "http://localhost:11434/v1", "api_key_env": "ANTHROPIC_API_KEY"}`, "model.base_url"},
		{"the operator's server, another variable", `{"provider": "openai", "id": "m", "api_key_env": "AGENTKIT_STORE"}`, "model.api_key_env"},
	} {
		_, err := c.SaveAgent(ctx, "stored", wire.SaveAgentRequest{Definition: json.RawMessage(`{"model": ` + tc.model + `}`), Publish: true})
		var api *agentclient.APIError
		if !errors.Is(err, agentclient.ErrInvalidDefinition) || !errors.As(err, &api) {
			t.Errorf("%s: save = %v, want invalid_definition", tc.name, err)
			continue
		}
		if !slices.ContainsFunc(api.Fields, func(f wire.FieldError) bool { return f.Field == tc.field }) {
			t.Errorf("%s: fields = %+v, want %s", tc.name, api.Fields, tc.field)
		}
	}
	for i, model := range []string{
		`{"provider": "openai", "id": "m"}`,
		`{"provider": "openai", "id": "m", "api_key_env": "OPENAI_API_KEY"}`,
		`{"provider": "openai", "id": "m", "base_url": "http://localhost:11434/v1", "api_key_env": "OLLAMA_KEY"}`,
	} {
		if _, err := c.SaveAgent(ctx, "stored-"+strconv.Itoa(i), wire.SaveAgentRequest{Definition: json.RawMessage(`{"model": ` + model + `}`), Publish: true}); err != nil {
			t.Errorf("save with model %s: %v, want it taken", model, err)
		}
	}
}

// TestExportImport: agentkit export writes each published stored agent
// (not the config file's) in the config file's shape, and agentkit import
// into a server with an empty store reproduces the same published
// definitions, which run.
func TestExportImport(t *testing.T) {
	t.Parallel()
	fromURL, from := serveAgentsAt(t, registryTestConfig, map[string]*llmtest.Fake{"model-plain": llmtest.NewFake(t), "m": llmtest.NewFake(t)})
	ctx := t.Context()
	save := func(name, def string, publish bool) wire.AgentVersion {
		t.Helper()
		v, err := from.SaveAgent(ctx, name, wire.SaveAgentRequest{Definition: json.RawMessage(def), Publish: publish})
		if err != nil {
			t.Fatalf("save %s: %v", name, err)
		}
		return v
	}
	// writer: v1, v2 published, then a draft v3 (not exported).
	save("writer", `{"model": {"id": "m"}, "system": "Write v1.", "toolsets": ["demo-a"]}`, true)
	save("writer", `{"model": {"id": "m"}, "system": "Write v2.", "toolsets": ["demo-a"], "description": "Writes."}`, true)
	save("writer", `{"model": {"id": "m"}, "system": "Write v3 draft."}`, false)
	// rolled: v2 published, then rolled back to v1.
	save("rolled", `{"model": {"id": "m"}, "system": "Rolled v1 <&>.", "tool_policy": "read_only"}`, true)
	save("rolled", `{"model": {"id": "m"}, "system": "Rolled v2."}`, true)
	if _, err := from.PublishAgent(ctx, "rolled", 1); err != nil {
		t.Fatal(err)
	}
	// draft: never published, not exported.
	save("draft", `{"model": {"id": "m"}}`, false)

	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	env := map[string]string{envServer: fromURL}
	c := cli{stdout: &stdout, stderr: &stderr, getenv: func(k string) string { return env[k] }}
	if code := c.main(ctx, []string{"export", dir}); code != exitDone {
		t.Fatalf("export = %d: %s", code, stderr.String())
	}
	files, _ := filepath.Glob(filepath.Join(dir, agentsDir, "*.json"))
	slices.Sort(files)
	if want := []string{filepath.Join(dir, agentsDir, "rolled.json"), filepath.Join(dir, agentsDir, "writer.json")}; !slices.Equal(files, want) {
		t.Fatalf("exported %v, want %v", files, want)
	}
	// In the config file's shape: it loads as an entry of "agents".
	raw, err := os.ReadFile(filepath.Join(dir, agentsDir, "writer.json"))
	if err != nil {
		t.Fatal(err)
	}
	a := defaultAgentDef()
	if err := decodeStrict(raw, &a); err != nil || a.Name != "writer" || a.System != "Write v2." || a.Description != "Writes." {
		t.Fatalf("writer.json = %+v, %v", a, err)
	}

	toURL, to := serveAgentsAt(t, registryTestConfig, map[string]*llmtest.Fake{"model-plain": llmtest.NewFake(t), "m": llmtest.NewFake(t, llmtest.Text("ok"))})
	env[envServer] = toURL
	stdout.Reset()
	if code := c.main(ctx, []string{"import", dir}); code != exitDone {
		t.Fatalf("import = %d: %s", code, stderr.String())
	}
	for _, name := range []string{"rolled", "writer"} {
		want := published(t, from, name)
		got := published(t, to, name)
		if !bytes.Equal(got.Definition, want.Definition) {
			t.Errorf("%s: imported %s, exported %s", name, got.Definition, want.Definition)
		}
	}
	if _, err := to.AgentVersions(ctx, "draft"); !errors.Is(err, agentclient.ErrNotFound) {
		t.Errorf("draft imported: %v", err)
	}
	run, err := to.Run(ctx, agentclient.RunRequest{Message: "hi", Agent: "writer"})
	if err != nil || run.Answer != "ok" || run.AgentVersion != 1 {
		t.Fatalf("run of imported writer = %+v, %v", run, err)
	}

	// A file the server refuses is reported by field; the rest still go.
	bad := t.TempDir()
	if err := os.MkdirAll(filepath.Join(bad, agentsDir), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(bad, agentsDir, "broken.json"), []byte(`{"model": {"id": "m"}, "toolsets": ["nope"]}`), 0o644)
	_ = os.WriteFile(filepath.Join(bad, agentsDir, "fine.json"), []byte(`{"model": {"id": "m"}}`), 0o644)
	stderr.Reset()
	if code := c.main(ctx, []string{"import", bad}); code != exitClient || !strings.Contains(stderr.String(), `toolsets: "nope" is not a toolset`) {
		t.Fatalf("import of a bad file = %d: %s", code, stderr.String())
	}
	if published(t, to, "fine").Version != 1 {
		t.Error("fine.json was not imported beside broken.json")
	}
}

// published is the published version of a stored agent.
func published(t *testing.T, c *agentclient.Client, name string) wire.AgentVersion {
	t.Helper()
	l, err := c.AgentVersions(t.Context(), name)
	if err != nil || l.Published == 0 {
		t.Fatalf("versions of %s = %+v, %v", name, l, err)
	}
	v, err := c.AgentVersion(t.Context(), name, l.Published)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
