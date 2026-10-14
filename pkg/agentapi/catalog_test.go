package agentapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// twoAgents is a rig with two agents, each with its own model (echo), system
// prompt and tools: "plain" has toolset a; "search" has a and b, prefixed.
type twoAgents struct {
	*rig
	plain, search *model
	sets          map[string]tool.Registry
}

func newTwoAgents(t *testing.T, api ...agentapi.Option) twoAgents {
	t.Helper()
	clk := clocktest.NewFake(time.Time{})
	sets := demoTools(t)
	merged, err := agentapi.MergeTools(t.Context(), sets, true)
	if err != nil {
		t.Fatal(err)
	}
	plain, search := echo(t), echo(t)
	agents := []agentapi.Agent{
		{
			Name: "plain", Description: "Answers plainly.", Model: wire.Model{ID: "model-plain"}, System: "You are plain.", Toolsets: []string{"a"},
			Harness: agentHarness(t, plain, sets["a"], harness.WithSystem("You are plain."), harness.WithNow(clk.Now)),
		},
		{
			Name: "search", Description: "Searches.", Model: wire.Model{ID: "model-search", Effort: "low"}, System: "You search.", Toolsets: []string{"a", "b"}, PrefixToolNames: true,
			Harness: agentHarness(t, search, merged, harness.WithSystem("You search."), harness.WithNow(clk.Now)),
		},
	}
	toolsets := agentapi.WithToolsets(
		agentapi.Toolset{Name: "a", Type: "demo", Tools: sets["a"], Credential: agentapi.Credential{Status: wire.CredentialNotNeeded}},
		agentapi.Toolset{Name: "b", Type: "demo", Tools: sets["b"], Credential: agentapi.Credential{Status: wire.CredentialNotNeeded}},
	)
	r := newRig(t, plain, rigConfig{agents: agents, clock: clk, api: append([]agentapi.Option{toolsets}, api...)})
	r.models = []*model{plain, search}
	return twoAgents{rig: r, plain: plain, search: search, sets: sets}
}

func agentHarness(t *testing.T, m llm.Model, tools tool.Registry, opts ...harness.Option) *harness.Harness {
	t.Helper()
	h, err := harness.New(m, tools, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func toolNames(defs []tool.Definition) []string {
	names := make([]string, len(defs))
	for i, d := range defs {
		names[i] = d.Name
	}
	slices.Sort(names)
	return names
}

// apiError decodes an error body; it fails the test if body is not one.
func apiError(t *testing.T, body []byte) *agentapi.Error {
	t.Helper()
	var e agentapi.ErrorResponse
	if err := json.Unmarshal(body, &e); err != nil || e.Error == nil {
		t.Fatalf("not an error body: %s", body)
	}
	return e.Error
}

// wantError checks a response is the given status and error code.
func wantError(t *testing.T, what string, resp *http.Response, body []byte, status int, code string) {
	t.Helper()
	if resp.StatusCode != status {
		t.Errorf("%s = %d %s, want %d %s", what, resp.StatusCode, body, status, code)
		return
	}
	if e := apiError(t, body); e.Code != code {
		t.Errorf("%s: code %q, want %q", what, e.Code, code)
	}
}

func jsonRun(message, conversationID, agent string) string {
	b, _ := json.Marshal(agentapi.RunRequest{Message: message, ConversationID: conversationID, Agent: agent})
	return string(b)
}

// TestAgentsRunWithTheirOwnHarness: each agent's runs reach its own model
// with its own system prompt and tools, a follow-up stays with the
// conversation's agent, and GET /v1/agents describes each agent as its model
// sees it: the tool names it lists are the tools the model was offered.
func TestAgentsRunWithTheirOwnHarness(t *testing.T) {
	t.Parallel()
	r := newTwoAgents(t)
	ctx := t.Context()
	first := r.startAgent(ctx, "hello plain", "", "plain")
	first.all()
	r.startAgent(ctx, "hello search", "", "search").all()
	r.run("plain again", first.conversationID()) // no agent: the conversation's

	plainReqs, searchReqs := r.plain.requests(), r.search.requests()
	if len(plainReqs) != 2 || len(searchReqs) != 1 {
		t.Fatalf("plain's model got %d requests and search's %d, want 2 and 1", len(plainReqs), len(searchReqs))
	}
	aDefs, err := r.sets["a"].List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range plainReqs {
		if req.System != "You are plain." || !slices.Equal(toolNames(req.Tools), toolNames(aDefs)) {
			t.Errorf("plain's model got system %q and tools %v", req.System, toolNames(req.Tools))
		}
	}
	if req := searchReqs[0]; req.System != "You search." || len(req.Tools) != 6 || !strings.HasPrefix(req.Tools[0].Name, "a__") {
		t.Errorf("search's model got system %q and tools %v", req.System, toolNames(req.Tools))
	}

	var list wire.AgentList
	if code := r.get("/v1/agents", &list); code != http.StatusOK || len(list.Agents) != 2 {
		t.Fatalf("GET /v1/agents = %d %+v", code, list)
	}
	offered := map[string][]tool.Definition{"plain": plainReqs[0].Tools, "search": searchReqs[0].Tools}
	system := map[string]string{"plain": plainReqs[0].System, "search": searchReqs[0].System}
	for i, a := range list.Agents {
		var listed []string
		for _, ts := range a.Toolsets {
			if ts.Status != wire.ToolsetReady {
				t.Errorf("agent %s: toolset %+v, want ready", a.Name, ts)
			}
			listed = append(listed, ts.Tools...)
		}
		slices.Sort(listed)
		if a.Name != []string{"plain", "search"}[i] || a.Default || a.System != "" || a.Model.ID != "model-"+a.Name || a.Skills == nil {
			t.Errorf("agent %d = %+v, want plain then search, neither default, no system prompt in the list", i, a)
		}
		if !slices.Equal(listed, toolNames(offered[a.Name])) {
			t.Errorf("agent %s lists tools %v, but its model was offered %v", a.Name, listed, toolNames(offered[a.Name]))
		}
		var one wire.Agent
		if code := r.get("/v1/agents/"+a.Name, &one); code != http.StatusOK || one.System != system[a.Name] {
			t.Errorf("GET /v1/agents/%s = %d, system %q; want the prompt its model got, %q", a.Name, code, one.System, system[a.Name])
		}
		one.System = ""
		if !jsonEqual(one, a) {
			t.Errorf("GET /v1/agents/%s = %+v, differs from the list's %+v", a.Name, one, a)
		}
	}

	if run := r.getRun(first.runID()); run.Agent != "plain" {
		t.Errorf("run %s has agent %q, want plain", run.ID, run.Agent)
	}
	if c := r.getConversation(first.conversationID()); c.Agent != "plain" {
		t.Errorf("conversation %s has agent %q, want plain", c.ID, c.Agent)
	}
	// Two agents, none named default: a new conversation must name one.
	resp, body := r.do("POST", "/v1/runs", jsonRun("hi", "", ""), "Accept", "application/json")
	wantError(t, "a run naming no agent", resp, body, http.StatusBadRequest, agentapi.CodeInvalidRequest)
	resp, body = r.do("POST", "/v1/runs", jsonRun("hi", "", "nobody"), "Accept", "application/json")
	wantError(t, "a run naming an unknown agent", resp, body, http.StatusNotFound, agentapi.CodeNotFound)
	resp, body = r.do("GET", "/v1/agents/nobody", "")
	wantError(t, "GET an unknown agent", resp, body, http.StatusNotFound, agentapi.CodeNotFound)
	if n := len(r.plain.requests()) + len(r.search.requests()); n != 3 {
		t.Errorf("refused runs reached a model: %d requests, want 3", n)
	}
}

// TestDefaultAgent: a run naming no agent gets the only agent, else the one
// named "default"; GET /v1/agents marks it.
func TestDefaultAgent(t *testing.T) {
	t.Parallel()
	for name, agents := range map[string][]string{
		"only":    {"solo"},
		"default": {"other", "default"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			models := map[string]*model{}
			var list []agentapi.Agent
			for _, n := range agents {
				models[n] = echo(t)
				list = append(list, agentapi.Agent{Name: n, Harness: agentHarness(t, models[n], registry(t))})
			}
			want := agents[len(agents)-1]
			r := newRig(t, models[want], rigConfig{agents: list})
			run := r.runJSON("hi", "")
			if run.Agent != want || len(models[want].requests()) != 1 {
				t.Errorf("run without an agent went to %q, want %q", run.Agent, want)
			}
			var got wire.AgentList
			r.get("/v1/agents", &got)
			for _, a := range got.Agents {
				if a.Default != (a.Name == want) {
					t.Errorf("agent %s: default %v", a.Name, a.Default)
				}
			}
		})
	}
}

// TestAgentMismatch: continuing a conversation with another agent is 409
// agent_mismatch, and nothing runs; with its own agent named, it goes on.
func TestAgentMismatch(t *testing.T) {
	t.Parallel()
	r := newTwoAgents(t)
	s := r.startAgent(t.Context(), "hello plain", "", "plain")
	s.all()
	conv := s.conversationID()
	before := r.getConversation(conv)

	resp, body := r.do("POST", "/v1/runs", jsonRun("now you, search", conv, "search"), "Accept", "application/json")
	wantError(t, "continuing plain's conversation with search", resp, body, http.StatusConflict, agentapi.CodeAgentMismatch)
	if n := len(r.search.requests()); n != 0 {
		t.Errorf("search's model got %d requests", n)
	}
	if after := r.getConversation(conv); !jsonEqual(after, before) {
		t.Errorf("the refused run changed the conversation:\n%s\nwas\n%s", jsonOf(after), jsonOf(before))
	}
	r.startAgent(t.Context(), "go on", conv, "plain").all()
	if n := len(r.plain.requests()); n != 2 {
		t.Errorf("plain's model got %d requests, want 2", n)
	}
}

// listPage fetches GET /v1/conversations with the query and checks the page
// is newest first and within its limit.
func (r *rig) listPage(query string, limit int) wire.ConversationList {
	r.t.Helper()
	var page wire.ConversationList
	if code := r.get("/v1/conversations"+query, &page); code != http.StatusOK {
		r.t.Fatalf("GET /v1/conversations%s = %d", query, code)
	}
	if len(page.Conversations) > limit || (page.NextCursor != "" && len(page.Conversations) != limit) {
		r.t.Errorf("page %s has %d conversations and cursor %q, limit %d", query, len(page.Conversations), page.NextCursor, limit)
	}
	return page
}

func ids(cs []wire.ConversationSummary) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}

func reversed(s []string) []string {
	out := slices.Clone(s)
	slices.Reverse(out)
	return out
}

// TestConversationListPages: paging through the conversations with a cursor
// sees each one that existed before it started exactly once, newest first,
// while new conversations start between pages; a listing from the start
// then shows those first. Each summary agrees with the conversation itself.
// Bad parameters are 400 (an unknown agent 404).
func TestConversationListPages(t *testing.T) {
	t.Parallel()
	r := newRig(t, echo(t), rigConfig{})
	var created []string
	for i := range 7 {
		created = append(created, r.runJSON(fmt.Sprintf("message %d", i), "").ConversationID)
	}
	var seen []string
	cursor := ""
	for page := 0; ; page++ {
		p := r.listPage("?limit=3&cursor="+cursor, 3)
		seen = append(seen, ids(p.Conversations)...)
		created = append(created, r.runJSON(fmt.Sprintf("arrived during page %d", page), "").ConversationID)
		if p.NextCursor == "" {
			break
		}
		if page > 10 {
			t.Fatal("paging does not end")
		}
		cursor = p.NextCursor
	}
	if want := reversed(created[:7]); !slices.Equal(seen, want) {
		t.Errorf("paging saw %v, want each earlier conversation once, newest first: %v", seen, want)
	}

	all := r.listPage("?limit=100", 100)
	if got, want := ids(all.Conversations), reversed(created); !slices.Equal(got, want) || all.NextCursor != "" {
		t.Errorf("listing from the start = %v (cursor %q), want %v", got, all.NextCursor, want)
	}
	if def := r.listPage("", agentapi.DefaultListLimit); !slices.Equal(ids(def.Conversations), ids(all.Conversations)) {
		t.Errorf("default limit listing = %v", ids(def.Conversations))
	}
	for _, sum := range all.Conversations {
		c := r.getConversation(sum.ID)
		first := c.Messages[0].Message.Text()
		if sum.Agent != c.Agent || sum.Agent != agentapi.DefaultAgent || sum.Status != c.Status || sum.MessageCount != len(c.Messages) ||
			!sum.CreatedAt.Equal(c.CreatedAt) || !sum.UpdatedAt.Equal(c.UpdatedAt) || sum.Title != first {
			t.Errorf("summary %+v disagrees with the conversation (agent %s, status %s, %d messages, first %q)", sum, c.Agent, c.Status, len(c.Messages), first)
		}
	}

	long := strings.Repeat("word ", 30) + "\n\nand more"
	id := r.runJSON(long, "").ConversationID
	got := r.listPage("?limit=1", 1).Conversations[0]
	if got.ID != id || utf8.RuneCountInString(got.Title) > 80 || strings.ContainsAny(got.Title, "\n") ||
		!strings.HasPrefix(strings.Join(strings.Fields(long), " "), strings.TrimSuffix(got.Title, "…")) {
		t.Errorf("title of a long message = %q", got.Title)
	}

	valid := r.listPage("?limit=1", 1).NextCursor
	for _, q := range []string{"?limit=0", "?limit=101", "?limit=x", "?limit=-1", "?cursor=nope", "?cursor=" + valid + "A", "?cursor=" + strings.ToUpper(valid)} {
		resp, body := r.do("GET", "/v1/conversations"+q, "")
		wantError(t, "GET /v1/conversations"+q, resp, body, http.StatusBadRequest, agentapi.CodeInvalidRequest)
	}
	resp, body := r.do("GET", "/v1/conversations?agent=nobody", "")
	wantError(t, "listing an unknown agent's conversations", resp, body, http.StatusNotFound, agentapi.CodeNotFound)
}

// TestConversationListFiltersByAgent: ?agent= lists that agent's
// conversations only, paging included.
func TestConversationListFiltersByAgent(t *testing.T) {
	t.Parallel()
	r := newTwoAgents(t)
	byAgent := map[string][]string{}
	var everyone []string
	for i := range 5 {
		for _, a := range []string{"plain", "search"} {
			resp, body := r.do("POST", "/v1/runs", jsonRun(fmt.Sprintf("%s %d", a, i), "", a), "Accept", "application/json")
			var run agentapi.Run
			if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &run) != nil {
				t.Fatalf("run = %d %s", resp.StatusCode, body)
			}
			byAgent[a] = append(byAgent[a], run.ConversationID)
			everyone = append(everyone, run.ConversationID)
		}
	}
	for a, want := range byAgent {
		var got []string
		cursor := ""
		for {
			p := r.listPage("?agent="+a+"&limit=2&cursor="+cursor, 2)
			for _, c := range p.Conversations {
				if c.Agent != a {
					t.Errorf("?agent=%s listed %+v", a, c)
				}
			}
			got = append(got, ids(p.Conversations)...)
			if cursor = p.NextCursor; cursor == "" {
				break
			}
		}
		if !slices.Equal(got, reversed(want)) {
			t.Errorf("?agent=%s = %v, want %v", a, got, reversed(want))
		}
	}
	if got := ids(r.listPage("", agentapi.DefaultListLimit).Conversations); !slices.Equal(got, reversed(everyone)) {
		t.Errorf("unfiltered = %v, want %v", got, reversed(everyone))
	}
}

// TestConversationsAreTheCallersTenant: with WithTenant, another tenant's
// conversation and runs do not exist for a caller: not listed, not readable,
// not continued, not deleted.
func TestConversationsAreTheCallersTenant(t *testing.T) {
	t.Parallel()
	m := echo(t)
	r := newRig(t, m, rigConfig{})
	resp, body := r.do("POST", "/v1/runs", jsonRun("mine", "", ""), "Accept", "application/json", "Test-Tenant", "a")
	var run agentapi.Run
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &run) != nil {
		t.Fatalf("run = %d %s", resp.StatusCode, body)
	}
	list := func(tenant string) []string {
		resp, body := r.do("GET", "/v1/conversations", "", "Test-Tenant", tenant)
		var l wire.ConversationList
		if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &l) != nil {
			t.Fatalf("list = %d %s", resp.StatusCode, body)
		}
		return ids(l.Conversations)
	}
	if got := list("b"); len(got) != 0 {
		t.Errorf("tenant b lists %v", got)
	}
	for _, req := range []struct{ method, path, body string }{
		{"GET", "/v1/conversations/" + run.ConversationID, ""},
		{"GET", "/v1/runs/" + run.ID, ""},
		{"POST", "/v1/runs", jsonRun("theirs", run.ConversationID, "")},
		{"DELETE", "/v1/conversations/" + run.ConversationID, ""},
	} {
		resp, body := r.do(req.method, req.path, req.body, "Test-Tenant", "b", "Accept", "application/json")
		wantError(t, "tenant b: "+req.method+" "+req.path, resp, body, http.StatusNotFound, agentapi.CodeNotFound)
	}
	if got := list("a"); !slices.Equal(got, []string{run.ConversationID}) {
		t.Errorf("tenant a lists %v", got)
	}
	if resp, _ := r.do("GET", "/v1/conversations/"+run.ConversationID, "", "Test-Tenant", "a"); resp.StatusCode != http.StatusOK {
		t.Errorf("tenant a reads its conversation: %d", resp.StatusCode)
	}
	if n := len(m.requests()); n != 1 {
		t.Errorf("the model got %d requests, want 1", n)
	}
}

// TestDeleteConversation: a deleted conversation and its runs are gone
// (204, then 404 and out of the list); one with a run going on is 409
// conversation_busy and stays.
func TestDeleteConversation(t *testing.T) {
	t.Parallel()
	m := echo(t)
	m.gate = make(chan struct{}, 1)
	m.gate <- struct{}{} // the first run goes through
	r := newRig(t, m, rigConfig{})
	run := r.runJSON("delete me", "")
	resp, body := r.do("DELETE", "/v1/conversations/"+run.ConversationID, "")
	if resp.StatusCode != http.StatusNoContent || len(body) != 0 {
		t.Errorf("DELETE = %d %q, want 204 and no body", resp.StatusCode, body)
	}
	for _, path := range []string{"/v1/conversations/" + run.ConversationID, "/v1/runs/" + run.ID} {
		resp, body := r.do("GET", path, "")
		wantError(t, "GET "+path+" after the delete", resp, body, http.StatusNotFound, agentapi.CodeNotFound)
	}
	resp, body = r.do("DELETE", "/v1/conversations/"+run.ConversationID, "")
	wantError(t, "deleting it again", resp, body, http.StatusNotFound, agentapi.CodeNotFound)

	s := r.start(t.Context(), "keep me", "") // waits at the gate
	conv := s.until(agentapi.TypeRunStart, 0).ConversationID
	resp, body = r.do("DELETE", "/v1/conversations/"+conv, "")
	wantError(t, "deleting a running conversation", resp, body, http.StatusConflict, agentapi.CodeBusy)
	m.gate <- struct{}{}
	s.all()
	if got := ids(r.listPage("", agentapi.DefaultListLimit).Conversations); !slices.Equal(got, []string{conv}) {
		t.Errorf("conversations after the deletes: %v, want only %s", got, conv)
	}
}

// TestToolsetsCatalog: GET /v1/toolsets lists each toolset as given (status,
// skip reason, MCP path, credential status and variable name) with its tool
// count, GET /v1/toolsets/{name}/tools its definitions as its registry has
// them, and an agent's skipped toolset is listed with no tools.
func TestToolsetsCatalog(t *testing.T) {
	t.Parallel()
	sets := demoTools(t)
	const reason = "toolset web (exa): environment variable EXA_API_KEY is unset or empty"
	given := []agentapi.Toolset{
		{Name: "a", Type: "demo", Tools: sets["a"], MCPPath: "/mcp/a", Credential: agentapi.Credential{Status: wire.CredentialNotNeeded}},
		{Name: "web", Type: "exa", Skipped: reason, Credential: agentapi.Credential{Status: wire.CredentialMissing, Env: "EXA_API_KEY"}},
	}
	m := echo(t)
	agents := []agentapi.Agent{{Name: "solo", Harness: agentHarness(t, m, sets["a"]), Toolsets: []string{"a", "web"}}}
	r := newRig(t, m, rigConfig{agents: agents, api: []agentapi.Option{agentapi.WithToolsets(given...)}})
	defs, err := sets["a"].List(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	var list wire.ToolsetList
	if code := r.get("/v1/toolsets", &list); code != http.StatusOK {
		t.Fatalf("GET /v1/toolsets = %d", code)
	}
	want := []wire.Toolset{
		{Name: "a", Type: "demo", Status: wire.ToolsetReady, MCPPath: "/mcp/a", ToolCount: len(defs), Credential: wire.Credential{Status: wire.CredentialNotNeeded}},
		{Name: "web", Type: "exa", Status: wire.ToolsetSkipped, Reason: reason, Credential: wire.Credential{Status: wire.CredentialMissing, Env: "EXA_API_KEY"}},
	}
	if !jsonEqual(list.Toolsets, want) {
		t.Errorf("GET /v1/toolsets =\n%s\nwant\n%s", jsonOf(list.Toolsets), jsonOf(want))
	}

	var tools wire.ToolList
	if code := r.get("/v1/toolsets/a/tools", &tools); code != http.StatusOK || tools.Toolset != "a" || tools.Status != wire.ToolsetReady || len(tools.Tools) != len(defs) {
		t.Fatalf("GET /v1/toolsets/a/tools = %d %+v", code, tools)
	}
	for i, d := range defs {
		got := tools.Tools[i]
		if got.Name != d.Name || got.Description != d.Description || !jsonEqual(got.InputSchema, d.InputSchema) || !jsonEqual(got.OutputSchema, d.OutputSchema) {
			t.Errorf("tool %d = %+v, want the registry's %+v", i, got, d)
		}
	}
	if code := r.get("/v1/toolsets/web/tools", &tools); code != http.StatusOK || tools.Status != wire.ToolsetSkipped || tools.Tools == nil || len(tools.Tools) != 0 {
		t.Errorf("GET /v1/toolsets/web/tools = %d %+v, want skipped with no tools", code, tools)
	}
	resp, body := r.do("GET", "/v1/toolsets/nope/tools", "")
	wantError(t, "an unknown toolset's tools", resp, body, http.StatusNotFound, agentapi.CodeNotFound)

	var agent wire.Agent
	r.get("/v1/agents/solo", &agent)
	if len(agent.Toolsets) != 2 || agent.Toolsets[0].Status != wire.ToolsetReady || len(agent.Toolsets[0].Tools) != len(defs) ||
		agent.Toolsets[1].Name != "web" || agent.Toolsets[1].Status != wire.ToolsetSkipped || len(agent.Toolsets[1].Tools) != 0 {
		t.Errorf("agent toolsets %+v, want a ready and web skipped with no tools", agent.Toolsets)
	}
}

// TestNewAgentsRejectsBadConfig: what NewAgents cannot serve fails with
// ErrInvalidConfig.
func TestNewAgentsRejectsBadConfig(t *testing.T) {
	t.Parallel()
	h := agentHarness(t, echo(t), registry(t))
	reg := registry(t)
	for name, tc := range map[string]struct {
		agents []agentapi.Agent
		sets   []agentapi.Toolset
	}{
		"no agents":             {},
		"nil harness":           {agents: []agentapi.Agent{{Name: "a"}}},
		"empty name":            {agents: []agentapi.Agent{{Harness: h}}},
		"name with a slash":     {agents: []agentapi.Agent{{Name: "a/b", Harness: h}}},
		"name too long":         {agents: []agentapi.Agent{{Name: strings.Repeat("a", 65), Harness: h}}},
		"duplicate name":        {agents: []agentapi.Agent{{Name: "a", Harness: h}, {Name: "a", Harness: h}}},
		"unknown toolset":       {agents: []agentapi.Agent{{Name: "a", Harness: h, Toolsets: []string{"x"}}}},
		"toolset twice":         {agents: []agentapi.Agent{{Name: "a", Harness: h}}, sets: []agentapi.Toolset{{Name: "x", Tools: reg}, {Name: "x", Tools: reg}}},
		"toolset without tools": {agents: []agentapi.Agent{{Name: "a", Harness: h}}, sets: []agentapi.Toolset{{Name: "x"}}},
		"toolset tools skipped": {agents: []agentapi.Agent{{Name: "a", Harness: h}}, sets: []agentapi.Toolset{{Name: "x", Tools: reg, Skipped: "why"}}},
		"bad toolset name":      {agents: []agentapi.Agent{{Name: "a", Harness: h}}, sets: []agentapi.Toolset{{Name: "x y", Tools: reg}}},
	} {
		if _, err := agentapi.NewAgents(tc.agents, agentapi.WithStore(scoped.Memory(nil)), agentapi.WithToolsets(tc.sets...)); !errors.Is(err, agentapi.ErrInvalidConfig) {
			t.Errorf("%s: %v, want ErrInvalidConfig", name, err)
		}
	}
}

// TestToolListMetadata: GET /v1/toolsets/{name}/tools gives each tool's
// class as policies see it (declared, or write/medium with classified false),
// and its flags, tags and display as declared.
func TestToolListMetadata(t *testing.T) {
	t.Parallel()
	ask, err := tool.Func("ask", "Ask the user.",
		func(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil },
		tool.WithMetadata(tool.Metadata{
			Kind: tool.KindSend, Risk: tool.RiskHigh, Interrupts: true, RequiresApproval: true,
			Tags:    []string{"pii", tool.TagExternal},
			Display: tool.Display{Title: "Ask", Icon: "chat", Present: "Asking", Past: "Asked"},
		}))
	if err != nil {
		t.Fatal(err)
	}
	half, err := tool.Func("half", "Classified by kind only.",
		func(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil },
		tool.WithMetadata(tool.Metadata{Kind: tool.KindRead, Display: tool.Display{Icon: "tool"}}))
	if err != nil {
		t.Fatal(err)
	}
	risky, err := tool.Func("risky", "Classified by risk only.",
		func(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil },
		tool.WithMetadata(tool.Metadata{Risk: tool.RiskHigh}))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := tool.Func("plain", "Unclassified.", func(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	reg := registry(t, ask, half, risky, plain)
	r := newRig(t, echo(t), rigConfig{api: []agentapi.Option{agentapi.WithToolsets(
		agentapi.Toolset{Name: "m", Type: "test", Tools: reg, Credential: agentapi.Credential{Status: wire.CredentialNotNeeded}},
	)}})

	var list wire.ToolList
	if code := r.get("/v1/toolsets/m/tools", &list); code != http.StatusOK {
		t.Fatalf("GET /v1/toolsets/m/tools = %d", code)
	}
	want := map[string]wire.Tool{
		"ask": {
			Kind: "send", Risk: "high", Classified: true, Interrupts: true, RequiresApproval: true,
			Tags:    []string{"pii", "external"},
			Display: &wire.ToolDisplay{Title: "Ask", Icon: "chat", Present: "Asking", Past: "Asked"},
		},
		"half":  {Kind: "read", Risk: "medium", Display: &wire.ToolDisplay{Icon: "tool"}},
		"risky": {Kind: "write", Risk: "high"},
		"plain": {Kind: "write", Risk: "medium"},
	}
	if len(list.Tools) != len(want) {
		t.Fatalf("listed %d tools, want %d", len(list.Tools), len(want))
	}
	for _, got := range list.Tools {
		w, ok := want[got.Name]
		if !ok {
			t.Errorf("unexpected tool %q", got.Name)
			continue
		}
		w.Name, w.Description, w.InputSchema, w.OutputSchema = got.Name, got.Description, got.InputSchema, got.OutputSchema
		if !reflect.DeepEqual(got, w) {
			t.Errorf("tool %s = %+v, want %+v", got.Name, got, w)
		}
	}

	// What a tool leaves unset is omitted, not sent empty.
	var raw struct {
		Tools []map[string]json.RawMessage `json:"tools"`
	}
	r.get("/v1/toolsets/m/tools", &raw)
	for _, tl := range raw.Tools {
		if string(tl["name"]) != `"plain"` {
			continue
		}
		for _, k := range []string{"interrupts", "requires_approval", "tags", "display"} {
			if v, ok := tl[k]; ok {
				t.Errorf("unclassified tool: %s = %s, want it omitted", k, v)
			}
		}
	}
}
