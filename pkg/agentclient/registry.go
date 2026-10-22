package agentclient

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
)

// The agent registry (T075): agents defined through the API, as versions.
// Each call needs a credential with the scope agent:admin; others get 403,
// and 404 for a server that keeps no agent definitions. The agents of the
// server's config file are read-only ([ErrReadOnly]).

// SaveAgent saves a new version of a stored agent, creating it if need be
// (POST /v1/agents/{name}/versions), and returns it. A definition the
// server refuses matches [ErrInvalidDefinition], with [APIError.Fields];
// an agent of the config file, [ErrReadOnly]; an IfLatest that is not the
// latest version, [ErrConflict].
func (c *Client) SaveAgent(ctx context.Context, name string, req wire.SaveAgentRequest) (wire.AgentVersion, error) {
	var v wire.AgentVersion
	return v, c.send(ctx, http.MethodPost, agentPath(name)+"/versions", req, http.StatusCreated, &v)
}

// AgentVersions lists a stored agent's versions, oldest first
// (GET /v1/agents/{name}/versions).
func (c *Client) AgentVersions(ctx context.Context, name string) (wire.AgentVersionList, error) {
	var l wire.AgentVersionList
	return l, c.get(ctx, agentPath(name)+"/versions", &l)
}

// AgentVersion returns one version of a stored agent
// (GET /v1/agents/{name}/versions/{version}).
func (c *Client) AgentVersion(ctx context.Context, name string, version int64) (wire.AgentVersion, error) {
	var v wire.AgentVersion
	return v, c.get(ctx, agentVersionPath(name, version), &v)
}

// PublishAgent makes version the one new runs of the agent use
// (POST /v1/agents/{name}/publish); publishing an older one rolls back.
func (c *Client) PublishAgent(ctx context.Context, name string, version int64) (wire.AgentVersionList, error) {
	var l wire.AgentVersionList
	return l, c.send(ctx, http.MethodPost, agentPath(name)+"/publish", wire.PublishRequest{Version: version}, http.StatusOK, &l)
}

// DeleteAgentVersion deletes a draft (DELETE
// /v1/agents/{name}/versions/{version}); a version that was published
// matches [ErrVersionPublished].
func (c *Client) DeleteAgentVersion(ctx context.Context, name string, version int64) error {
	return c.send(ctx, http.MethodDelete, agentVersionPath(name, version), nil, http.StatusNoContent, nil)
}

func agentPath(name string) string { return "/v1/agents/" + url.PathEscape(name) }

func agentVersionPath(name string, version int64) string {
	return agentPath(name) + "/versions/" + strconv.FormatInt(version, 10)
}
