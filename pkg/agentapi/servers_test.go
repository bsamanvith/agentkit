package agentapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/authtest"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/mcp/servers"
	"github.com/bsamanvith/agentkit/pkg/netguard"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// The keys of adminRig, besides keyA, keyB and keyOnlyA (accessRig's): a
// key of tenant A that may read and run but not administer.
const keyRun = "key-a-run-not-admin-01"

// mcpFake is an MCP server with the tools "search" and "delete_all".
func mcpFake(t *testing.T) *httptest.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "1"}, nil)
	obj := map[string]any{"type": "object"}
	srv.AddTool(&mcp.Tool{Name: "search", Description: "Search.", InputSchema: obj}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "found " + string(req.Params.Arguments)}}}, nil
	})
	srv.AddTool(&mcp.Tool{Name: "delete_all", Description: "Delete.", InputSchema: obj}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "deleted"}}}, nil
	})
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	t.Cleanup(func() {
		for ss := range srv.Sessions() {
			_ = ss.Close()
		}
		ts.CloseClientConnections()
		ts.Close()
	})
	return ts
}

// adminRig serves an agent "ops" that uses the runtime toolset "linear",
// and the config toolset "a", with toolsets added at run time on, behind
// the keys. The model of "ops" calls linear__search once, then answers.
// Each of opts, given the fake MCP server's URL, adds an option of the
// manager; the server searches the manager's registry if it has one.
func adminRig(t *testing.T, opts ...func(mcpURL string) servers.Option) (*rig, *model, string) {
	t.Helper()
	mcpURL := mcpFake(t).URL
	ten := func(name string) tenancy.Tenant {
		tn, err := tenancy.NewTenant(fmt.Sprintf("%s%d", name, time.Now().UnixNano()))
		if err != nil {
			t.Fatal(err)
		}
		return tn
	}
	a, b := ten("a"), ten("b")
	all := []string{"*"}
	authn := authtest.NewFake(map[string]auth.Principal{
		keyA:     {Tenant: a, Scopes: all, Toolsets: all, Via: auth.Credential{Kind: auth.KindAPIKey, ID: "admin-a"}},
		keyB:     {Tenant: b, Scopes: all, Toolsets: all},
		keyRun:   {Tenant: a, Scopes: []string{agentapi.ScopeRead, agentapi.ScopeRun}, Toolsets: all},
		keyOnlyA: {Tenant: a, Scopes: all, Toolsets: []string{"a"}},
	})
	st := scoped.Memory(nil)
	g, err := netguard.New([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	mopts := []servers.Option{servers.WithGuard(g)}
	for _, o := range opts {
		mopts = append(mopts, o(mcpURL))
	}
	mgr, err := servers.New(st, mopts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	m := script(t,
		llmtest.ToolCalls(llm.ToolUse{ID: "toolu_1", Name: "linear__search", Input: json.RawMessage(`{"q":"bug"}`)}),
		llmtest.Text("done"),
	)
	sets := demoTools(t)
	agents := []agentapi.Agent{{Name: "ops", RuntimeToolsets: []string{"linear"}, Harness: agentHarness(t, m, mgr.Registry("linear"))}}
	api := []agentapi.Option{
		agentapi.WithTenant(nil),
		agentapi.WithAuthenticator(authn),
		agentapi.WithToolsets(agentapi.Toolset{Name: "a", Type: "demo", Tools: sets["a"], Credential: agentapi.Credential{Status: wire.CredentialNotNeeded}}),
		agentapi.WithToolsetAdmin(mgr),
	}
	if d := mgr.Directory(); d != nil {
		api = append(api, agentapi.WithMCPRegistry(d))
	}
	r := newRig(t, m, rigConfig{agents: agents, store: st, api: api})
	r.models, r.token = nil, keyA
	return r, m, mcpURL
}

func decodeAs[T any](t *testing.T, body []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	return v
}

// The whole path through the API: add a server by URL, review a tool, and
// an agent calls it.
func TestRuntimeToolsetFlow(t *testing.T) {
	t.Parallel()
	r, m, url := adminRig(t)
	resp, body := r.do("POST", "/v1/toolsets", `{"type":"mcp","name":"linear","url":"`+url+`"}`, as(keyA)...)
	if resp.StatusCode != http.StatusCreated || resp.Header.Get("Location") != "/v1/toolsets/linear/review" {
		t.Fatalf("POST /v1/toolsets = %d %s", resp.StatusCode, body)
	}
	added := decodeAs[wire.ServerToolset](t, body)
	if added.Status != wire.ToolsetReviewNeeded || len(added.Tools) != 2 {
		t.Fatalf("added: %+v", added)
	}

	_, body = r.do("GET", "/v1/toolsets", "", as(keyA)...)
	list := decodeAs[wire.ToolsetList](t, body)
	if i := slices.IndexFunc(list.Toolsets, func(ts wire.Toolset) bool { return ts.Name == "linear" }); i < 0 ||
		!list.Toolsets[i].Runtime || list.Toolsets[i].Status != wire.ToolsetReviewNeeded || list.Toolsets[i].ToolCount != 0 || list.Toolsets[0].Name != "a" {
		t.Errorf("GET /v1/toolsets = %+v", list.Toolsets)
	}
	_, body = r.do("GET", "/v1/agents/ops", "", as(keyA)...)
	if ag := decodeAs[wire.Agent](t, body); len(ag.Toolsets) != 1 || ag.Toolsets[0].Name != "linear" || ag.Toolsets[0].Status != wire.ToolsetReady || len(ag.Toolsets[0].Tools) != 0 {
		t.Errorf("the agent before the review: %+v", ag.Toolsets)
	}

	var search wire.ReviewTool
	for _, rt := range added.Tools {
		if rt.Name == "search" {
			search = rt
		}
	}
	review := fmt.Sprintf(`{"tools":[{"name":"search","fingerprint":%q,"decision":"approve","kind":"read","risk":"low"}]}`, search.Fingerprint)
	resp, body = r.do("POST", "/v1/toolsets/linear/review", review, as(keyA)...)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST review = %d %s", resp.StatusCode, body)
	}
	reviewed := decodeAs[wire.ServerToolset](t, body)
	if i := slices.IndexFunc(reviewed.Tools, func(rt wire.ReviewTool) bool { return rt.Name == "search" }); reviewed.Tools[i].Status != wire.ToolApproved || reviewed.Tools[i].ReviewedBy != "admin-a" {
		t.Errorf("reviewed: %+v", reviewed.Tools[i])
	}
	// The same decision again is fine; one on another definition is 409.
	stale := `{"tools":[{"name":"search","fingerprint":"sha256:` + strings.Repeat("0", 64) + `","decision":"approve"}]}`
	if resp, body := r.do("POST", "/v1/toolsets/linear/review", stale, as(keyA)...); resp.StatusCode != http.StatusConflict || apiCode(body) != agentapi.CodeConflict {
		t.Errorf("a stale fingerprint = %d %s, want 409 conflict", resp.StatusCode, body)
	}

	_, body = r.do("GET", "/v1/toolsets/linear/tools", "", as(keyRun)...)
	if tl := decodeAs[wire.ToolList](t, body); tl.Status != wire.ToolsetReady || len(tl.Tools) != 1 || tl.Tools[0].Name != "search" || tl.Tools[0].Kind != "read" {
		t.Errorf("GET tools = %+v", tl)
	}

	resp, body = r.do("POST", "/v1/runs", `{"message":"find bugs","agent":"ops"}`, asJSON(keyRun)...)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("run = %d %s", resp.StatusCode, body)
	}
	reqs := m.requests()
	if len(reqs) != 2 {
		t.Fatalf("%d model requests, want 2", len(reqs))
	}
	if len(reqs[0].Tools) != 1 || reqs[0].Tools[0].Name != "linear__search" {
		t.Errorf("tools offered to the model: %+v", reqs[0].Tools)
	}
	last := reqs[1].Messages[len(reqs[1].Messages)-1]
	res, ok := last.Content[0].(llm.ToolResult)
	if !ok || res.IsError || !strings.Contains(res.Content, `found {\"q\":\"bug\"}`) {
		t.Errorf("the tool's result: %+v", last.Content)
	}

	// Disabled, then removed.
	if resp, body := r.do("PATCH", "/v1/toolsets/linear", `{"enabled":false}`, as(keyA)...); resp.StatusCode != http.StatusOK || decodeAs[wire.ServerToolset](t, body).Status != wire.ToolsetDisabled {
		t.Errorf("PATCH = %d %s", resp.StatusCode, body)
	}
	if resp, body := r.do("DELETE", "/v1/toolsets/linear", "", as(keyA)...); resp.StatusCode != http.StatusNoContent {
		t.Errorf("DELETE = %d %s", resp.StatusCode, body)
	}
	if resp, _ := r.do("GET", "/v1/toolsets/linear/review", "", as(keyA)...); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET review of a removed toolset = %d", resp.StatusCode)
	}
	_, body = r.do("GET", "/v1/agents/ops", "", as(keyA)...)
	if ag := decodeAs[wire.Agent](t, body); ag.Toolsets[0].Status != wire.ToolsetNotAdded {
		t.Errorf("the agent after the removal: %+v", ag.Toolsets)
	}
}

// Only admins add, review, change and remove; a toolset a key may not use
// does not exist for it; another tenant's does not either.
func TestRuntimeToolsetAccess(t *testing.T) {
	t.Parallel()
	r, _, url := adminRig(t)
	add := `{"type":"mcp","name":"linear","url":"` + url + `"}`
	if resp, body := r.do("POST", "/v1/toolsets", add, as(keyA)...); resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST = %d %s", resp.StatusCode, body)
	}
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/v1/toolsets", strings.Replace(add, "linear", "other", 1)},
		{"GET", "/v1/toolsets/linear/review", ""},
		{"POST", "/v1/toolsets/linear/review", `{"tools":[]}`},
		{"PATCH", "/v1/toolsets/linear", `{"enabled":false}`},
		{"DELETE", "/v1/toolsets/linear", ""},
	} {
		if resp, body := r.do(c.method, c.path, c.body, as(keyRun)...); resp.StatusCode != http.StatusForbidden || apiCode(body) != agentapi.CodeForbidden {
			t.Errorf("%s %s without admin = %d %s, want 403", c.method, c.path, resp.StatusCode, body)
		}
		for _, key := range []string{keyOnlyA, keyB} {
			if c.method == "POST" && c.path == "/v1/toolsets" {
				continue
			}
			if resp, body := r.do(c.method, c.path, c.body, as(key)...); resp.StatusCode != http.StatusNotFound {
				t.Errorf("%s %s with a key that may not see it = %d %s, want 404", c.method, c.path, resp.StatusCode, body)
			}
		}
	}
	for _, key := range []string{keyOnlyA, keyB} {
		_, body := r.do("GET", "/v1/toolsets", "", as(key)...)
		if slices.ContainsFunc(decodeAs[wire.ToolsetList](t, body).Toolsets, func(ts wire.Toolset) bool { return ts.Name == "linear" }) {
			t.Errorf("a key that may not see it lists it: %s", body)
		}
		if resp, _ := r.do("GET", "/v1/toolsets/linear/tools", "", as(key)...); resp.StatusCode != http.StatusNotFound {
			t.Errorf("tools of a hidden toolset = %d", resp.StatusCode)
		}
	}
	if resp, body := r.do("POST", "/v1/toolsets", strings.Replace(add, "linear", "other", 1), as(keyOnlyA)...); resp.StatusCode != http.StatusForbidden {
		t.Errorf("adding a toolset the key may not use = %d %s, want 403", resp.StatusCode, body)
	}
	// The agent that uses it is hidden from a key that may not use it.
	if resp, _ := r.do("GET", "/v1/agents/ops", "", as(keyOnlyA)...); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an agent with a hidden runtime toolset = %d, want 404", resp.StatusCode)
	}
}

func TestRuntimeToolsetErrors(t *testing.T) {
	t.Parallel()
	r, _, url := adminRig(t)
	for _, c := range []struct {
		body   string
		status int
		code   string
	}{
		{`{"type":"mcp","name":"a","url":"` + url + `"}`, http.StatusConflict, agentapi.CodeToolsetExists}, // the config's
		{`{"type":"mcp","name":"x","url":"https://10.0.0.1/mcp"}`, http.StatusBadRequest, agentapi.CodeInvalidRequest},
		{`{"type":"mcp","name":"x","url":"` + url + `","nope":1}`, http.StatusBadRequest, agentapi.CodeInvalidRequest},
		{`{"type":"mcp","name":"x","url":"http://127.0.0.1:1/mcp"}`, http.StatusUnprocessableEntity, agentapi.CodeServerUnreachable},
	} {
		if resp, body := r.do("POST", "/v1/toolsets", c.body, as(keyA)...); resp.StatusCode != c.status || apiCode(body) != c.code {
			t.Errorf("POST %s = %d %s, want %d %s", c.body, resp.StatusCode, body, c.status, c.code)
		}
	}
	ok := `{"type":"mcp","name":"x","url":"` + url + `"}`
	r.do("POST", "/v1/toolsets", ok, as(keyA)...)
	if resp, body := r.do("POST", "/v1/toolsets", ok, as(keyA)...); resp.StatusCode != http.StatusConflict || apiCode(body) != agentapi.CodeToolsetExists {
		t.Errorf("adding twice = %d %s", resp.StatusCode, body)
	}
	// The config's toolsets are not managed here.
	if resp, _ := r.do("DELETE", "/v1/toolsets/a", "", as(keyA)...); resp.StatusCode != http.StatusNotFound {
		t.Errorf("DELETE of a config toolset = %d, want 404", resp.StatusCode)
	}
}

// A server without WithToolsetAdmin adds no toolsets.
func TestNoToolsetAdmin(t *testing.T) {
	t.Parallel()
	r := newRig(t, echo(t), rigConfig{})
	if resp, body := r.do("POST", "/v1/toolsets", `{"type":"mcp","name":"x","url":"https://example.com"}`); resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST /v1/toolsets = %d %s, want 404", resp.StatusCode, body)
	}
	if _, err := agentapi.NewAgents([]agentapi.Agent{{Name: "x", RuntimeToolsets: []string{"y"}, Harness: agentHarness(t, echo(t), registry(t))}}); err == nil {
		t.Error("an agent with runtime toolsets and no admin")
	}
}
