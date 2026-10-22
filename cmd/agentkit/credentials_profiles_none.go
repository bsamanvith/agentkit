//go:build nomcp

package main

// oauthProfile, in a build without the MCP client (nomcp: the agentkit-api
// image), has no provider presets: they are the MCP OAuth core's. Set
// auth_url, token_url, scopes and auth_params instead.
func oauthProfile(name string) (providerProfile, error) {
	return providerProfile{}, notBuilt("credentials.oauth2 profile "+name, "the provider profiles (package mcp/oauth)", "nomcp")
}
