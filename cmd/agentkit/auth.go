package main

import (
	"errors"
	"fmt"
	"os"
	"path"
	"slices"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// envAPIKeysFile names a JSON file of API keys, in the form of the config's
// "auth" section ({"api_keys": [...]}): for hashes kept in a mounted secret.
const envAPIKeysFile = "AGENTKIT_API_KEYS_FILE"

// tokenKeyID names the key $AGENTKIT_API_TOKEN makes, in logs.
const tokenKeyID = "api-token"

// authConfig is the "auth" section of the config file.
type authConfig struct {
	// APIKeys are the keys the agent API and the MCP endpoints accept: each
	// the hash of a key (auth.HashKey; never the key) and the tenant,
	// scopes and toolsets it grants.
	APIKeys []auth.APIKey `json:"api_keys"`
}

// authenticator is what the agent API and the MCP endpoints authenticate
// requests with: the config's keys, those of $AGENTKIT_API_KEYS_FILE, and
// $AGENTKIT_API_TOKEN as one key of the default tenant with every scope and
// toolset. It is nil when none gives a key: the server is open, which only a
// loopback address allows. A key's toolset pattern that matches none of the
// config's toolsets is an error: a typo would otherwise hide a toolset.
func (c cli) authenticator(cfg serveConfig) (auth.Authenticator, error) {
	keys := slices.Clone(cfg.Auth.APIKeys)
	if p := c.env(envAPIKeysFile); p != "" {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("$%s: %w", envAPIKeysFile, err)
		}
		var f authConfig
		if err := decodeStrict(raw, &f); err != nil {
			return nil, fmt.Errorf("$%s: %w", envAPIKeysFile, err)
		}
		keys = append(keys, f.APIKeys...)
	}
	if token := c.env(envAPIToken); token != "" {
		if len(token) < auth.MinKeyLength {
			return nil, fmt.Errorf("$%s: the token must be at least %d bytes", envAPIToken, auth.MinKeyLength)
		}
		e, err := auth.KeyEntry(auth.APIKey{ID: tokenKeyID, Tenant: agentapi.DefaultTenant, Scopes: []string{"*"}, Toolsets: []string{"*"}}, token)
		if err != nil {
			return nil, fmt.Errorf("$%s: %w", envAPIToken, errors.Unwrap(err))
		}
		keys = append(keys, e)
	}
	if len(keys) == 0 {
		return nil, nil
	}
	for _, k := range keys {
		for _, pat := range k.Toolsets {
			if pat != "*" && !slices.ContainsFunc(cfg.MCP.Toolsets, func(s toolset.Spec) bool { ok, _ := path.Match(pat, s.Name); return ok }) {
				return nil, fmt.Errorf("auth: API key %q: toolset %q matches none of mcp.toolsets", k.ID, pat)
			}
		}
	}
	a, err := auth.NewAPIKeys(keys)
	if err != nil {
		return nil, err
	}
	return a, nil
}

// needsCredentials is the error of a component that would serve addr, which
// is not loopback, with no authenticator.
func needsCredentials(addr string) error {
	return fmt.Errorf("serving on %s, which is not a loopback address, needs credentials: a token in $%s, or API keys (auth.api_keys in the config, or $%s)", addr, envAPIToken, envAPIKeysFile)
}
