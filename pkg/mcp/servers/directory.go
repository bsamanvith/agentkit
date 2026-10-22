package servers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/mcp/client"
	mcpregistry "github.com/bsamanvith/agentkit/pkg/mcp/registry"
)

// WithAllowlist sets the tenant setting mcp_servers.allow: [Manager.Add]
// refuses a URL a tenant's list does not include (agentapi.ErrURLNotAllowed),
// before connecting to it. Default: none, so any URL the guard allows.
func WithAllowlist(a *Allowlist) Option { return func(m *Manager) { m.allow = a } }

// WithRegistry sets the MCP Registry admins search ([Manager.Directory]).
func WithRegistry(c *mcpregistry.Client) Option { return func(m *Manager) { m.registry = c } }

// Directory returns the search of the registry of [WithRegistry], for
// agentapi.WithMCPRegistry; nil without one.
func (m *Manager) Directory() agentapi.MCPRegistry {
	if m.registry == nil {
		return nil
	}
	return directory{m}
}

// checkAllowed refuses a URL ctx's tenant may not add.
func (m *Manager) checkAllowed(ctx context.Context, url string) error {
	if !m.allow.Allows(ctx, url) {
		return fmt.Errorf("%w: %s is not in the tenant's mcp_servers.allow", agentapi.ErrURLNotAllowed, url)
	}
	return nil
}

type directory struct{ m *Manager }

// maxTitleRunes caps a server's name and title in a search.
const maxTitleRunes = 200

// Search implements agentapi.MCPRegistry: the registry's servers, their
// text sanitized, each remote marked with whether the tenant may add it.
func (d directory) Search(ctx context.Context, query string, limit int) (wire.RegistrySearch, error) {
	found, err := d.m.registry.Search(ctx, query, limit)
	switch {
	case errors.Is(err, mcpregistry.ErrUnavailable):
		return wire.RegistrySearch{}, fmt.Errorf("%w: %w", agentapi.ErrRegistryUnavailable, err)
	case errors.Is(err, mcpregistry.ErrInvalid):
		return wire.RegistrySearch{}, fmt.Errorf("%w: %w", agentapi.ErrToolsetInvalid, err)
	case err != nil:
		return wire.RegistrySearch{}, err
	}
	out := wire.RegistrySearch{
		Registry: d.m.registry.URL(), Restricted: d.m.allow.Restricted(ctx), Servers: make([]wire.RegistryServer, 0, len(found)),
	}
	for _, s := range found {
		ws := wire.RegistryServer{
			Name: short(s.Name), SuggestedName: suggestName(s.Name), Title: short(s.Title),
			Description: client.SanitizeDescription(s.Description), Version: short(s.Version),
			WebsiteURL: s.WebsiteURL, RepositoryURL: s.RepositoryURL, Status: short(s.Status),
		}
		for _, r := range s.Remotes {
			wr := wire.RegistryRemote{Transport: string(client.TransportHTTP), URL: r.URL, Variables: r.Variables, Allowed: d.m.allow.Allows(ctx, r.URL)}
			if r.Transport == mcpregistry.TransportSSE {
				wr.Transport = string(client.TransportSSE)
			}
			for _, h := range r.Headers {
				wr.Headers = append(wr.Headers, wire.RegistryHeader{
					Name: short(h.Name), Description: client.SanitizeDescription(h.Description), Required: h.Required, Secret: h.Secret,
				})
			}
			ws.Remotes = append(ws.Remotes, wr)
		}
		out.Servers = append(out.Servers, ws)
	}
	return out, nil
}

// short sanitizes a line of a registry's text and caps it.
func short(s string) string {
	s = strings.Join(strings.Fields(client.SanitizeDescription(s)), " ")
	if utf8.RuneCountInString(s) > maxTitleRunes {
		s = string([]rune(s)[:maxTitleRunes]) + "…"
	}
	return s
}

// suggestName is a toolset name for a registry's server: the last part of
// its name ("app.linear/linear" is "linear"), in the characters a toolset
// name takes.
func suggestName(name string) string {
	if i := strings.LastIndexAny(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-_")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	if len(s) > 64 {
		s = strings.TrimRight(s[:64], "-_")
	}
	if s == "" {
		return "mcp"
	}
	return s
}
