//go:build !nomcp

package main

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/mcp/oauth"
)

// oauthProfile is a provider preset of the shared OAuth core
// (oauth.Profiles, T095), as credentials.oauth2's profile names it: its
// fixed endpoints, if any, the scope that asks for a refresh token and the
// authorization parameters. Its other fields (resource, extra token fields)
// are about MCP servers and do not apply to a slot.
func oauthProfile(name string) (providerProfile, error) {
	all := oauth.Profiles()
	delete(all, "mcp") // discovery from an MCP server: not a slot's
	p, ok := all[name]
	if !ok {
		return providerProfile{}, fmt.Errorf("profile %q: want %q", name, strings.Join(slices.Sorted(maps.Keys(all)), `", "`))
	}
	out := providerProfile{authURL: p.AuthURL, tokenURL: p.TokenURL, params: maps.Clone(p.AuthParams)}
	if p.AuthInParams {
		out.authStyle = "params"
	}
	switch p.RefreshScope {
	case "-":
	case "":
		out.refreshScope = "offline_access"
	default:
		out.refreshScope = p.RefreshScope
	}
	return out, nil
}
