//go:build !noagent && !nomodels

package main

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// describedConfig has two agents on the same toolset: "told" overrides
// echo's description, and on m2 its variant overrides echo and calculate;
// "plain" overrides nothing.
const describedConfig = `{
  "mcp": {"toolsets": [{"name": "demo-a", "type": "demo"}]},
  "agents": [
    {"name": "told", "model": {"id": "m1"}, "toolsets": ["demo-a"],
     "tool_descriptions": {"echo": "Echo only greetings."},
     "models": {"allow": ["m2"], "variants": [{"match": "m2",
       "tool_descriptions": {"echo": "Echo on m2.", "calculate": "Add on m2."}}]}},
    {"name": "plain", "model": {"id": "m1"}, "toolsets": ["demo-a"]}
  ]
}`

// description is the description of tool name in req ("" when absent).
func description(req llm.Request, name string) string {
	for _, d := range req.Tools {
		if d.Name == name {
			return d.Description
		}
	}
	return ""
}

// The model gets an agent's overrides, a variant's over the agent's on its
// model only, and another agent on the same toolset gets the originals.
func TestToolDescriptionOverrides(t *testing.T) {
	t.Parallel()
	m := newCountingModels(t, "m1", "m2")
	c := serveModels(t, describedConfig, m, "")
	ctx := t.Context()
	for _, r := range []agentclient.RunRequest{
		{Agent: "plain", Message: "hi"},
		{Agent: "told", Message: "hi"},
		{Agent: "told", Message: "hi", Model: "m2"},
	} {
		if _, err := c.Run(ctx, r); err != nil {
			t.Fatalf("run %+v: %v", r, err)
		}
	}
	plain, told := m.fakes["m1"].reqs[0], m.fakes["m1"].reqs[1]
	onM2 := m.fakes["m2"].reqs[0]
	orig := description(plain, "echo")
	if orig == "" || orig == "Echo only greetings." {
		t.Fatalf("plain's echo = %q, want the toolset's own", orig)
	}
	for _, tc := range []struct {
		what string
		req  llm.Request
		tool string
		want string
	}{
		{"told's echo", told, "echo", "Echo only greetings."},
		{"told's calculate", told, "calculate", description(plain, "calculate")},
		{"told's echo on m2", onM2, "echo", "Echo on m2."},
		{"told's calculate on m2", onM2, "calculate", "Add on m2."},
	} {
		if got := description(tc.req, tc.tool); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.what, got, tc.want)
		}
	}
	// The schema is the toolset's.
	i := slices.IndexFunc(told.Tools, func(d tool.Definition) bool { return d.Name == "echo" })
	j := slices.IndexFunc(plain.Tools, func(d tool.Definition) bool { return d.Name == "echo" })
	if i < 0 || j < 0 || string(told.Tools[i].InputSchema) != string(plain.Tools[j].InputSchema) {
		t.Errorf("told's echo schema differs from plain's")
	}
	a, err := c.Agent(ctx, "told")
	if err != nil {
		t.Fatal(err)
	}
	if a.ToolDescriptions["echo"] != "Echo only greetings." || len(a.ToolDescriptions) != 1 {
		t.Errorf("GET told's tool_descriptions = %v", a.ToolDescriptions)
	}
	if a, err := c.Agent(ctx, "plain"); err != nil || a.ToolDescriptions != nil {
		t.Errorf("GET plain = %v, %v; want no tool_descriptions", a.ToolDescriptions, err)
	}
}

// deferredDescribed is an agent deferring echo, with an override for it.
const deferredDescribed = `{
  "mcp": {"toolsets": [{"name": "demo", "type": "demo"}]},
  "agents": [{"name": "a", "model": {"id": "m"%s}, "toolsets": ["demo"],
    "defer_loading": ["echo"], "tool_descriptions": {"echo": "Told echo."}}]
}`

// Overrides reach deferred tools: listed deferred (native), and added when
// a search finds them (emulated).
func TestToolDescriptionsDeferred(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, extra string }{{"native", ""}, {"emulated", `, "tool_search": "emulated"`}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := llmtest.NewFake(t,
				llmtest.ToolCalls(llm.ToolUse{ID: "1", Name: harness.SearchTool, Input: json.RawMessage(`{"query":"echo"}`)}),
				llmtest.Text("found"),
			)
			m, err := newSkillAgents(t, t.TempDir(), strings.Replace(deferredDescribed, "%s", tc.extra, 1), model)
			if err != nil {
				t.Fatal(err)
			}
			ts := httptest.NewServer(m.Component.Handler())
			t.Cleanup(ts.Close)
			client, err := agentclient.New(ts.URL, agentclient.WithHTTPClient(ts.Client()))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Run(t.Context(), agentclient.RunRequest{Message: "Find echo.", Agent: "a"}); err != nil {
				t.Fatal(err)
			}
			reqs := model.Requests()
			at := 0 // native lists echo, deferred, from the first request
			if tc.name == "emulated" {
				at = 1
				if description(reqs[0], "echo") != "" {
					t.Errorf("emulated: echo was sent before the search")
				}
			}
			if got := description(reqs[at], "echo"); got != "Told echo." {
				t.Errorf("echo's description in request %d = %q, want the override", at, got)
			}
		})
	}
}

// Bad overrides are refused by field, for an agent of the file and for a
// stored one; nothing is saved.
func TestToolDescriptionsRefused(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", maxToolDescription+1)
	cases := []struct{ name, def, field, says string }{
		{"unknown tool", `"tool_descriptions": {"nope": "x"}`, "tool_descriptions.nope", "not a tool of the agent"},
		{"unprefixed name", `"prefix_tool_names": true, "tool_descriptions": {"echo": "x"}`, "tool_descriptions.echo", "not a tool of the agent"},
		{"empty", `"tool_descriptions": {"echo": " "}`, "tool_descriptions.echo", "empty description"},
		{"too long", `"tool_descriptions": {"echo": "` + long + `"}`, "tool_descriptions.echo", "longer than"},
		{"variant's unknown tool", `"models": {"allow": ["m2"], "variants": [{"match": "m2", "tool_descriptions": {"nope": "x"}}]}`, "models.variants[0].tool_descriptions.nope", "not a tool of the agent"},
	}
	m := newCountingModels(t, "model-plain", "m1", "m2")
	c := serveModels(t, registryTestConfig, m, "")
	for _, tc := range cases {
		def := `{"model": {"id": "m1"}, "toolsets": ["demo-a"], ` + tc.def + `}`
		_, err := c.SaveAgent(t.Context(), "stored", wire.SaveAgentRequest{Definition: json.RawMessage(def), Publish: true})
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
	if _, err := c.AgentVersions(t.Context(), "stored"); !errors.Is(err, agentclient.ErrNotFound) {
		t.Errorf("versions after refused saves = %v, want not found", err)
	}
	// A prefixed name is a tool of the agent that prefixes; a runtime
	// toolset's prefix is taken on trust.
	ok := `{"model": {"id": "m1"}, "toolsets": ["demo-a"], "prefix_tool_names": true,
	  "tool_descriptions": {"demo-a__echo": "x"}}`
	if _, err := c.SaveAgent(t.Context(), "prefixed", wire.SaveAgentRequest{Definition: json.RawMessage(ok), Publish: true}); err != nil {
		t.Errorf("saving a prefixed override: %v", err)
	}
}

// A name under a runtime toolset's prefix is taken on trust: its tools are
// known only in a run's tenant.
func TestRuntimeToolDescriptions(t *testing.T) {
	t.Parallel()
	descs := map[string]string{"rt__search": "x"}
	if err := checkDescriptions("f", descs, []string{"echo"}, []string{"rt"}); err != nil {
		t.Errorf("rt__search with runtime toolset rt: %v", err)
	}
	if err := checkDescriptions("f", descs, []string{"echo"}, []string{"r"}); err == nil {
		t.Error("rt__search with runtime toolset r alone was taken")
	}
}
