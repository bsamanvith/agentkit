package main

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/credentials/providers"
)

// oauthProfile is a provider preset (package providers, which the MCP
// OAuth core's profiles share), as credentials.oauth2's profile names it:
// its fixed endpoints, if any, the scope that asks for a refresh token, the
// authorization parameters, how the client authenticates, and the token
// fields to keep. OmitResource is about MCP servers and does not apply to
// a slot.
func oauthProfile(name string) (providerProfile, error) {
	all := providers.Presets()
	p, ok := all[name]
	if !ok {
		return providerProfile{}, fmt.Errorf("profile %q: want %q", name, strings.Join(slices.Sorted(maps.Keys(all)), `", "`))
	}
	out := providerProfile{
		authURL: p.AuthURL, tokenURL: p.TokenURL, params: maps.Clone(p.AuthParams),
		keepExtras: slices.Clone(p.KeepExtras), defaultTTL: p.DefaultTTL,
	}
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
