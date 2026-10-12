package agentclient

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
)

// Agents lists the server's agents (GET /v1/agents), without their system
// prompts.
func (c *Client) Agents(ctx context.Context) ([]wire.Agent, error) {
	var list wire.AgentList
	return list.Agents, c.get(ctx, "/v1/agents", &list)
}

// Agent returns one agent with its system prompt (GET /v1/agents/{name}). An
// unknown agent matches [ErrNotFound].
func (c *Client) Agent(ctx context.Context, name string) (wire.Agent, error) {
	var a wire.Agent
	return a, c.get(ctx, "/v1/agents/"+url.PathEscape(name), &a)
}

// ConversationsQuery selects a page of [Client.Conversations]. The zero value
// is the first page of every agent's conversations, at the server's default
// limit.
type ConversationsQuery struct {
	// Agent keeps only that agent's conversations; an unknown agent matches
	// [ErrNotFound].
	Agent string
	// Limit is the page size, 1 to 100; 0 for the server's default (20).
	Limit int
	// Cursor is the previous page's NextCursor; empty for the first page.
	Cursor string
}

// Conversations returns a page of the caller's conversations, newest first
// (GET /v1/conversations). Pass its NextCursor in the next query for the next
// page; paging sees each conversation that existed when it began exactly
// once. A bad limit or cursor matches [ErrInvalidRequest].
func (c *Client) Conversations(ctx context.Context, q ConversationsQuery) (wire.ConversationList, error) {
	v := url.Values{}
	if q.Agent != "" {
		v.Set("agent", q.Agent)
	}
	if q.Limit != 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.Cursor != "" {
		v.Set("cursor", q.Cursor)
	}
	path := "/v1/conversations"
	if len(v) > 0 {
		path += "?" + v.Encode()
	}
	var list wire.ConversationList
	return list, c.get(ctx, path, &list)
}

// ConversationRequest is the body of [Client.CreateConversation].
type ConversationRequest = wire.CreateConversationRequest

// CreateConversation creates an empty conversation (POST /v1/conversations)
// of req.Agent, or of the default agent, and returns it: a client has its ID
// before the first message, which [Client.Stream] or [Client.Run] then sends
// with that ConversationID. An unknown agent matches [ErrNotFound].
func (c *Client) CreateConversation(ctx context.Context, req ConversationRequest) (wire.Conversation, error) {
	var conv wire.Conversation
	resp, err := c.postJSON(ctx, "/v1/conversations", req, "application/json")
	if err != nil {
		return conv, err
	}
	return conv, c.decodeStatus(resp, http.StatusCreated, "POST /v1/conversations", &conv)
}

// DeleteConversation deletes a conversation and its runs
// (DELETE /v1/conversations/{id}). An unknown conversation matches
// [ErrNotFound]; one with a run going on, [ErrConversationBusy].
func (c *Client) DeleteConversation(ctx context.Context, id string) error {
	r, err := c.request(ctx, http.MethodDelete, "/v1/conversations/"+url.PathEscape(id), nil, "application/json")
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(r)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusNoContent {
		return c.apiError(resp)
	}
	return resp.Body.Close()
}

// Toolsets lists the server's toolsets (GET /v1/toolsets): each ready or
// skipped (with the reason), where it is served over MCP ([Client.MCPURL]),
// and its credential's status and variable name, never the value.
func (c *Client) Toolsets(ctx context.Context) ([]wire.Toolset, error) {
	var list wire.ToolsetList
	return list.Toolsets, c.get(ctx, "/v1/toolsets", &list)
}

// Tools lists a toolset's tools under their own names
// (GET /v1/toolsets/{name}/tools); a skipped toolset has none. An unknown
// toolset matches [ErrNotFound].
func (c *Client) Tools(ctx context.Context, toolset string) (wire.ToolList, error) {
	var list wire.ToolList
	return list, c.get(ctx, "/v1/toolsets/"+url.PathEscape(toolset)+"/tools", &list)
}

// MCPURL is the URL of a toolset's MCP endpoint: its MCPPath under the
// client's base URL. It is empty when the server does not serve the toolset
// over MCP.
func (c *Client) MCPURL(ts wire.Toolset) string {
	if ts.MCPPath == "" {
		return ""
	}
	return c.base + ts.MCPPath
}
