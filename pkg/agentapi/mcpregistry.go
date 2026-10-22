package agentapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
)

// Limits of GET /v1/mcp-registry/search.
const (
	// DefaultRegistryLimit is the servers a search returns when it does
	// not say.
	DefaultRegistryLimit = 20
	// MaxRegistryLimit is the most it may ask for.
	MaxRegistryLimit = 100
	// MaxRegistryQuery caps a query, in bytes.
	MaxRegistryQuery = 200
)

// CodeRegistryUnavailable answers a search the registry could not serve
// (HTTP 502).
const CodeRegistryUnavailable = wire.CodeRegistryUnavailable

// ErrRegistryUnavailable means the MCP Registry could not be searched (502
// registry_unavailable). Adding a server by URL does not depend on it.
var ErrRegistryUnavailable = errors.New("the MCP registry could not be searched")

// MCPRegistry searches a directory of MCP servers for ones that can be
// added by URL (package mcp/servers implements it over package
// mcp/registry). Search acts in the tenant of ctx, whose allowlist marks
// each remote's Allowed.
type MCPRegistry interface {
	Search(ctx context.Context, query string, limit int) (wire.RegistrySearch, error)
}

// WithMCPRegistry lets admins (ScopeAdmin) search an MCP Registry, through
// GET /v1/mcp-registry/search, to fill in POST /v1/toolsets.
func WithMCPRegistry(r MCPRegistry) Option { return func(c *config) { c.mcpRegistry = r } }

func (s *Server) searchRegistry(w http.ResponseWriter, r *http.Request) {
	if s.cfg.mcpRegistry == nil {
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "this server searches no MCP registry"})
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := DefaultRegistryLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > MaxRegistryLimit {
			writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "limit must be 1 to " + strconv.Itoa(MaxRegistryLimit)})
			return
		}
		limit = n
	}
	if q == "" || len(q) > MaxRegistryQuery {
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "q must have 1 to " + strconv.Itoa(MaxRegistryQuery) + " bytes"})
		return
	}
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	res, err := s.cfg.mcpRegistry.Search(ctx, q, limit)
	switch {
	case errors.Is(err, ErrRegistryUnavailable):
		s.log.Warn("searching the MCP registry", "err", err)
		writeError(w, http.StatusBadGateway, &Error{Code: CodeRegistryUnavailable, Message: err.Error() + "; a server can still be added by its URL"})
		return
	case errors.Is(err, ErrToolsetInvalid):
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: err.Error()})
		return
	case err != nil:
		s.internal(w, "searching the MCP registry", err)
		return
	}
	res.V, res.Query = WireVersion, q
	if res.Servers == nil {
		res.Servers = []wire.RegistryServer{}
	}
	writeJSON(w, http.StatusOK, res)
}
