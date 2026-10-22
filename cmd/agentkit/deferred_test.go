package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/skill"
)

// deferConfig is an agent with the demo toolset, echo and calculate
// deferred, and a skill listing calculate and a tool it does not have.
const deferConfig = `{
  "mcp": {"toolsets": [{"name": "demo", "type": "demo"}]},
  "agents": [{"name": "a", "model": {"id": "m"%s}, "toolsets": ["demo"],
    "defer_loading": ["calc*", "echo"], "skills": ["skills"]}]
}`

func writeToolSkill(t *testing.T, dir string) {
	t.Helper()
	p := filepath.Join(dir, "skills", "math", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: math\ndescription: Do arithmetic.\ntools: [calculate, nope_tool]\n---\nUse calculate.\n"
	if err := os.WriteFile(p, []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runDeferred(t *testing.T, model llm.Model, modelExtra string, runs ...string) []wire.Run {
	t.Helper()
	dir := t.TempDir()
	writeToolSkill(t, dir)
	m, err := newSkillAgents(t, dir, strings.Replace(deferConfig, "%s", modelExtra, 1), model)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(m.Component.Handler())
	t.Cleanup(ts.Close)
	client, err := agentclient.New(ts.URL, agentclient.WithHTTPClient(ts.Client()))
	if err != nil {
		t.Fatal(err)
	}
	var out []wire.Run
	for _, msg := range runs {
		run, err := client.Run(t.Context(), agentclient.RunRequest{Message: msg, Agent: "a"})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, run)
	}
	return out
}

// TestServeDeferLoading: through serve's wiring (Claude, native by
// default), the agent's first request defers echo and calculate and adds
// tool_search; a search's result references echo; skill_load of a skill
// listing calculate loads it, and the tool the agent does not have is
// ignored.
func TestServeDeferLoading(t *testing.T) {
	t.Parallel()
	model := llmtest.NewFake(t,
		llmtest.ToolCalls(llm.ToolUse{ID: "1", Name: harness.SearchTool, Input: json.RawMessage(`{"query":"repeat text"}`)}),
		llmtest.ToolCalls(llm.ToolUse{ID: "2", Name: "echo", Input: json.RawMessage(`{"text":"hi"}`)}),
		llmtest.Text("echoed"),
		llmtest.ToolCalls(llm.ToolUse{ID: "3", Name: skill.LoadTool, Input: json.RawMessage(`{"name":"math"}`)}),
		llmtest.Text("loaded"),
	)
	runDeferred(t, model, "", "Repeat hi.", "Add numbers.")
	reqs := model.Requests()
	first := reqs[0]
	if got := toolNames(first); !slices.Contains(got, harness.SearchTool) || !slices.Contains(got, "echo") {
		t.Errorf("native: first request's tools %v, want every tool and tool_search", got)
	}
	if got := slices.Sorted(slices.Values(first.Deferred)); !slices.Equal(got, []string{"calculate", "echo"}) {
		t.Errorf("deferred %v, want calculate and echo", got)
	}
	if got := lastResult(t, reqs[1]); !slices.Equal(got.Loads, []string{"echo"}) {
		t.Errorf("search result %+v, want it to load echo", got)
	}
	if got := lastResult(t, reqs[2]); got.IsError {
		t.Errorf("echo failed: %+v", got)
	}
	if got := lastResult(t, reqs[4]); !slices.Equal(got.Loads, []string{"calculate"}) {
		t.Errorf("skill_load result loads %v, want calculate alone", got.Loads)
	}
}

// TestServeDeferLoadingEmulated: with tool_search "emulated", the deferred
// tools are left out of the requests until found.
func TestServeDeferLoadingEmulated(t *testing.T) {
	t.Parallel()
	model := llmtest.NewFake(t,
		llmtest.ToolCalls(llm.ToolUse{ID: "1", Name: harness.SearchTool, Input: json.RawMessage(`{"query":"echo"}`)}),
		llmtest.Text("found"),
	)
	runs := runDeferred(t, model, `, "tool_search": "emulated"`, "Find echo.")
	if rec := runs[0].Record; rec == nil || len(rec.Turns) == 0 || !slices.Equal(rec.Turns[0].Loaded, []string{"echo"}) {
		t.Errorf("the run's record %+v, want its first turn to have loaded echo", rec)
	}
	reqs := model.Requests()
	if got := toolNames(reqs[0]); slices.Contains(got, "echo") || slices.Contains(got, "calculate") || !slices.Contains(got, "current_time") {
		t.Errorf("first request's tools %v, want current_time but neither echo nor calculate", got)
	}
	if got := toolNames(reqs[1]); !slices.Contains(got, "echo") || slices.Contains(got, "calculate") || len(reqs[1].Deferred) != 0 {
		t.Errorf("tools after the search %v (deferred %v), want echo added", got, reqs[1].Deferred)
	}
}

// TestServeDeferLoadingAll: defer_loading true defers every toolset tool
// and never the skill tools, which the model needs to load the rest.
func TestServeDeferLoadingAll(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeToolSkill(t, dir)
	model := llmtest.NewFake(t, llmtest.Text("hi"))
	config := strings.Replace(deferConfig, `["calc*", "echo"]`, "true", 1)
	m, err := newSkillAgents(t, dir, strings.Replace(config, "%s", "", 1), model)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(m.Component.Handler())
	t.Cleanup(ts.Close)
	client, err := agentclient.New(ts.URL, agentclient.WithHTTPClient(ts.Client()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Run(t.Context(), agentclient.RunRequest{Message: "Hi.", Agent: "a"}); err != nil {
		t.Fatal(err)
	}
	deferred := model.Requests()[0].Deferred
	for _, name := range []string{"echo", "calculate", "current_time"} {
		if !slices.Contains(deferred, name) {
			t.Errorf("deferred %v, want %s among them", deferred, name)
		}
	}
	for _, name := range []string{skill.LoadTool, skill.ReadTool, harness.SearchTool} {
		if slices.Contains(deferred, name) {
			t.Errorf("deferred %v, want %s sent in full", deferred, name)
		}
	}
}

// TestDeferLoadingConfigErrors: an entry that names nothing of the agent,
// and native search on an OpenAI-compatible model, fail startup.
func TestDeferLoadingConfigErrors(t *testing.T) {
	t.Parallel()
	for name, config := range map[string]string{
		"typo": `{"mcp": {"toolsets": [{"name": "demo", "type": "demo"}]},
		  "agents": [{"name": "a", "model": {"id": "m"}, "toolsets": ["demo"], "defer_loading": ["ehco"]}]}`,
		"native openai": `{"mcp": {"toolsets": [{"name": "demo", "type": "demo"}]},
		  "agents": [{"name": "a", "model": {"provider": "openai", "id": "m", "tool_search": "native"}, "toolsets": ["demo"], "defer_loading": true}]}`,
		"bad mode": `{"agents": [{"name": "a", "model": {"id": "m", "tool_search": "magic"}}]}`,
	} {
		if _, err := newSkillAgentsErr(t, t.TempDir(), config); err == nil || !strings.Contains(err.Error(), "tool_search") && !strings.Contains(err.Error(), "defer_loading") {
			t.Errorf("%s: startup err = %v, want it to fail on defer_loading or tool_search", name, err)
		}
	}
}

// TestSkillToolWarnings: a skill's tools: entry that matches none of the
// agent's tools is reported (serve logs it), unless runtime toolsets could
// have it.
func TestSkillToolWarnings(t *testing.T) {
	t.Parallel()
	lists := map[string][]string{"math": {"calculate", "nope_tool"}, "git": {"github_*"}}
	got := skillToolWarnings(lists, []string{"calculate", "echo"}, false)
	want := [][2]string{{"git", "github_*"}, {"math", "nope_tool"}}
	if !slices.Equal(got, want) {
		t.Errorf("warnings %v, want %v", got, want)
	}
	if got := skillToolWarnings(lists, nil, true); len(got) != 0 {
		t.Errorf("with runtime toolsets: %v, want none", got)
	}
}

// lastResult is the tool result in a request's last message.
func lastResult(t *testing.T, req llm.Request) llm.ToolResult {
	t.Helper()
	for _, b := range req.Messages[len(req.Messages)-1].Content {
		if r, ok := b.(llm.ToolResult); ok {
			return r
		}
	}
	t.Fatalf("no tool result in the last message of %v", req.Messages)
	return llm.ToolResult{}
}
