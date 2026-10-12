package agentclient_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// TestCatalog: against the real server, the client lists the agents (one,
// the default, with its tools), pages through the conversations newest
// first, deletes one, lists the toolsets with their MCP URLs and a toolset's
// tools, and an unknown agent or toolset matches ErrNotFound.
func TestCatalog(t *testing.T) {
	t.Parallel()
	replies := make([]llmtest.Reply, 6)
	for i := range replies {
		replies[i] = llmtest.Text("ok")
	}
	reg, err := tool.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	r := newRig(t, &model{replies: replies}, agentapi.WithToolsets(
		agentapi.Toolset{Name: "empty", Type: "demo", Tools: reg, MCPPath: "/mcp/empty", Credential: agentapi.Credential{Status: wire.CredentialNotNeeded}},
		agentapi.Toolset{Name: "web", Type: "exa", Skipped: "no key", Credential: agentapi.Credential{Status: wire.CredentialMissing, Env: "EXA_API_KEY"}},
	))
	ctx, c := t.Context(), r.client

	agents, err := c.Agents(ctx)
	if err != nil || len(agents) != 1 || agents[0].Name != agentapi.DefaultAgent || !agents[0].Default {
		t.Fatalf("Agents = %+v, %v; want the default agent", agents, err)
	}
	if a, err := c.Agent(ctx, agentapi.DefaultAgent); err != nil || a.Name != agentapi.DefaultAgent {
		t.Errorf("Agent(default) = %+v, %v", a, err)
	}
	if _, err := c.Agent(ctx, "nobody"); !errors.Is(err, agentclient.ErrNotFound) {
		t.Errorf("Agent(nobody): %v, want ErrNotFound", err)
	}

	var started []string
	for range 5 {
		run, err := c.Run(ctx, agentclient.RunRequest{Message: "hi"})
		if err != nil {
			t.Fatal(err)
		}
		started = append(started, run.ConversationID)
	}
	slices.Reverse(started)
	var listed []string
	q := agentclient.ConversationsQuery{Agent: agentapi.DefaultAgent, Limit: 2}
	for {
		page, err := c.Conversations(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range page.Conversations {
			listed = append(listed, s.ID)
		}
		if q.Cursor = page.NextCursor; q.Cursor == "" {
			break
		}
	}
	if !slices.Equal(listed, started) {
		t.Errorf("Conversations paged %v, want %v", listed, started)
	}
	if _, err := c.Conversations(ctx, agentclient.ConversationsQuery{Cursor: "nope"}); !errors.Is(err, agentclient.ErrInvalidRequest) {
		t.Errorf("a bad cursor: %v, want ErrInvalidRequest", err)
	}
	if err := c.DeleteConversation(ctx, started[0]); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteConversation(ctx, started[0]); !errors.Is(err, agentclient.ErrNotFound) {
		t.Errorf("deleting it again: %v, want ErrNotFound", err)
	}
	if page, err := c.Conversations(ctx, agentclient.ConversationsQuery{}); err != nil || len(page.Conversations) != 4 || page.Conversations[0].ID != started[1] {
		t.Errorf("after the delete: %+v, %v", page, err)
	}

	conv, err := c.CreateConversation(ctx, agentclient.ConversationRequest{})
	if err != nil || conv.Agent != agentapi.DefaultAgent || conv.Status != wire.StatusIdle || conv.Messages == nil || len(conv.Messages) != 0 {
		t.Fatalf("CreateConversation = %+v, %v; want an empty, idle conversation of the default agent", conv, err)
	}
	if run, err := c.Run(ctx, agentclient.RunRequest{Message: "hi", ConversationID: conv.ID}); err != nil || run.ConversationID != conv.ID {
		t.Errorf("a run in the created conversation = %+v, %v", run, err)
	}
	if got, err := c.GetConversation(ctx, conv.ID); err != nil || len(got.Messages) != 2 {
		t.Errorf("the created conversation after a run: %+v, %v; want 2 messages", got, err)
	}
	if _, err := c.CreateConversation(ctx, agentclient.ConversationRequest{Agent: "nobody"}); !errors.Is(err, agentclient.ErrNotFound) {
		t.Errorf("CreateConversation(nobody): %v, want ErrNotFound", err)
	}

	sets, err := c.Toolsets(ctx)
	if err != nil || len(sets) != 2 {
		t.Fatalf("Toolsets = %+v, %v", sets, err)
	}
	if got := c.MCPURL(sets[0]); got != r.ts.URL+"/mcp/empty" {
		t.Errorf("MCPURL(empty) = %q", got)
	}
	if got := c.MCPURL(sets[1]); got != "" || sets[1].Status != wire.ToolsetSkipped || sets[1].Credential.Env != "EXA_API_KEY" {
		t.Errorf("web: %+v, MCP URL %q", sets[1], got)
	}
	if tools, err := c.Tools(ctx, "web"); err != nil || tools.Status != wire.ToolsetSkipped || len(tools.Tools) != 0 {
		t.Errorf("Tools(web) = %+v, %v", tools, err)
	}
	if _, err := c.Tools(ctx, "nope"); !errors.Is(err, agentclient.ErrNotFound) {
		t.Errorf("Tools(nope): %v, want ErrNotFound", err)
	}
}
