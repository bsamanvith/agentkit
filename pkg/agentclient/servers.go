package agentclient

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
)

// The toolsets admins add at run time: MCP servers, by URL, whose tools
// agents use only once an admin approved them. Each call needs a credential
// with the scope agent:admin; others get 403, and 404 for a server without
// toolsets added at run time.

// AddToolset adds an MCP server as a toolset (POST /v1/toolsets) and returns
// it for review: its tools are off until [Client.ReviewToolset] approves
// them. A name in use matches [ErrToolsetExists]; a server that cannot be
// reached, [ErrServerUnreachable]; one that wants a credential,
// [ErrAuthRequired].
func (c *Client) AddToolset(ctx context.Context, req wire.AddToolsetRequest) (wire.ServerToolset, error) {
	var ts wire.ServerToolset
	return ts, c.send(ctx, http.MethodPost, "/v1/toolsets", req, http.StatusCreated, &ts)
}

// ToolsetReview reads a toolset added at run time with its tools: each
// approved, new, changed (with what changed), removed or rejected
// (GET /v1/toolsets/{name}/review).
func (c *Client) ToolsetReview(ctx context.Context, name string) (wire.ServerToolset, error) {
	var ts wire.ServerToolset
	return ts, c.get(ctx, toolsetPath(name)+"/review", &ts)
}

// ReviewToolset approves or rejects tools (POST /v1/toolsets/{name}/review),
// each by the fingerprint the review showed. A tool whose definition changed
// since matches [ErrConflict]: read the review again.
func (c *Client) ReviewToolset(ctx context.Context, name string, req wire.ReviewRequest) (wire.ServerToolset, error) {
	var ts wire.ServerToolset
	return ts, c.send(ctx, http.MethodPost, toolsetPath(name)+"/review", req, http.StatusOK, &ts)
}

// UpdateToolset enables or disables a toolset added at run time, or
// replaces its headers (PATCH /v1/toolsets/{name}).
func (c *Client) UpdateToolset(ctx context.Context, name string, req wire.UpdateToolsetRequest) (wire.ServerToolset, error) {
	var ts wire.ServerToolset
	return ts, c.send(ctx, http.MethodPatch, toolsetPath(name), req, http.StatusOK, &ts)
}

// DeleteToolset removes a toolset added at run time
// (DELETE /v1/toolsets/{name}); agents lose its tools at their next turn.
func (c *Client) DeleteToolset(ctx context.Context, name string) error {
	return c.send(ctx, http.MethodDelete, toolsetPath(name), nil, http.StatusNoContent, nil)
}

// SearchMCPRegistry searches the MCP Registry the server is configured with
// (GET /v1/mcp-registry/search) for servers that can be added by URL, up
// to limit of them (0: the server's default). A registry that is down
// matches [ErrRegistryUnavailable].
func (c *Client) SearchMCPRegistry(ctx context.Context, query string, limit int) (wire.RegistrySearch, error) {
	q := url.Values{"q": {query}}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var res wire.RegistrySearch
	return res, c.get(ctx, "/v1/mcp-registry/search?"+q.Encode(), &res)
}

func toolsetPath(name string) string { return "/v1/toolsets/" + url.PathEscape(name) }

// send makes a JSON request (no body when in is nil) and decodes a response
// of status into out (nothing when out is nil).
func (c *Client) send(ctx context.Context, method, path string, in any, status int, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	r, err := c.request(ctx, method, path, body, "application/json")
	if err != nil {
		return err
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(r)
	if err != nil {
		return err
	}
	if out == nil {
		if resp.StatusCode != status {
			return c.apiError(resp)
		}
		return resp.Body.Close()
	}
	return c.decodeStatus(resp, status, method+" "+path, out)
}
