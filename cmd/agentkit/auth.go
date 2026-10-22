package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"slices"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/jwt"
	"github.com/bsamanvith/agentkit/pkg/auth/login"
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
	// JWT accepts an identity provider's tokens beside the keys (T041).
	JWT *jwtConfig `json:"jwt"`
	// Users sign in to the console with a password (T061): each a
	// username, the argon2id hash of the password (agentkit users
	// hash-password), a tenant and roles. Only the agent API accepts their
	// sessions; MCP clients use keys or tokens.
	Users []login.User `json:"users"`
	// TrustedProxies are the reverse proxies (addresses or CIDR prefixes)
	// whose X-Forwarded-Proto and X-Forwarded-For sign-in believes: for
	// Secure cookies behind a TLS proxy, and the client's address.
	TrustedProxies []string `json:"trusted_proxies"`
}

// jwtConfig is the "auth.jwt" section: jwt.Config, with durations written
// as strings, and what the MCP endpoints' protected resource metadata says.
type jwtConfig struct {
	jwt.Config
	Leeway             duration `json:"leeway"`
	MinRefreshInterval duration `json:"min_refresh_interval"`
	MaxCacheAge        duration `json:"max_cache_age"`
	// AuthorizationServers are the issuers the metadata names (default: the
	// issuer).
	AuthorizationServers []string `json:"authorization_servers"`
	// ScopesSupported are the scopes the metadata names, if any.
	ScopesSupported []string `json:"scopes_supported"`
}

func (j jwtConfig) config() jwt.Config {
	c := j.Config
	c.Leeway, c.MinRefreshInterval, c.MaxCacheAge = time.Duration(j.Leeway), time.Duration(j.MinRefreshInterval), time.Duration(j.MaxCacheAge)
	return c
}

// authorizationServers are those the metadata names.
func (j jwtConfig) authorizationServers() []string {
	if len(j.AuthorizationServers) > 0 {
		return j.AuthorizationServers
	}
	return []string{j.Issuer}
}

// authenticator is what the agent API and the MCP endpoints authenticate
// requests with: the identity provider's tokens of auth.jwt, then the API
// keys (auth.Chain: a bearer token that is not a JWT goes on to the keys).
// It is nil when nothing is configured: the server is open, which only a
// loopback address allows. The provider's keys are fetched once at startup,
// and a failure only logged: the next token tries again.
func (c cli) authenticator(ctx context.Context, cfg serveConfig, log *slog.Logger) (auth.Authenticator, error) {
	keys, err := c.apiKeys(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.Auth.JWT == nil {
		if keys == nil {
			return nil, nil
		}
		return keys, nil
	}
	tokens, err := jwt.New(cfg.Auth.JWT.config(), jwt.WithLogger(log.With("component", "auth")))
	if err != nil {
		return nil, fmt.Errorf("auth.jwt: %w", err)
	}
	if err := tokens.Refresh(ctx); err != nil {
		log.Warn("auth.jwt: the identity provider's keys could not be fetched; its tokens fail until they are", "issuer", cfg.Auth.JWT.Issuer, "error", err)
	}
	if keys == nil {
		return tokens, nil
	}
	return auth.Chain(tokens, keys), nil
}

// apiKeys are the config's keys, those of $AGENTKIT_API_KEYS_FILE, and
// $AGENTKIT_API_TOKEN as one key of the default tenant with every scope and
// toolset; nil when there are none. A key's toolset pattern that matches none
// of the config's toolsets is an error: a typo would otherwise hide a toolset.
func (c cli) apiKeys(cfg serveConfig) (auth.Authenticator, error) {
	f, err := c.authFile(envAPIKeysFile)
	if err != nil {
		return nil, err
	}
	keys := slices.Concat(cfg.Auth.APIKeys, f.APIKeys)
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
		if err := checkToolsets(cfg, fmt.Sprintf("API key %q", k.ID), k.Toolsets); err != nil {
			return nil, err
		}
	}
	a, err := auth.NewAPIKeys(keys)
	if err != nil {
		return nil, err
	}
	return a, nil
}

// authFile reads the file $name names, in the form of the config's "auth"
// section; nothing when the variable is unset.
func (c cli) authFile(name string) (authConfig, error) {
	var f authConfig
	p := c.env(name)
	if p == "" {
		return f, nil
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return f, fmt.Errorf("$%s: %w", name, err)
	}
	if err := decodeStrict(raw, &f); err != nil {
		return f, fmt.Errorf("$%s: %w", name, err)
	}
	return f, nil
}

// checkToolsets refuses a credential's toolset pattern that matches none of
// the config's toolsets: a typo would otherwise hide a toolset. who names
// the credential in the error.
func checkToolsets(cfg serveConfig, who string, patterns []string) error {
	for _, pat := range patterns {
		if pat != "*" && !slices.ContainsFunc(cfg.MCP.Toolsets, func(s toolset.Spec) bool { ok, _ := path.Match(pat, s.Name); return ok }) {
			return fmt.Errorf("auth: %s: toolset %q matches none of mcp.toolsets", who, pat)
		}
	}
	return nil
}

// needsCredentials is the error of a component that would serve addr, which
// is not loopback, with no authenticator.
func needsCredentials(addr string) error {
	return fmt.Errorf("serving on %s, which is not a loopback address, needs credentials: a token in $%s, API keys (auth.api_keys in the config, or $%s), an identity provider (auth.jwt), or for the agent API alone local users (auth.users, or $%s)", addr, envAPIToken, envAPIKeysFile, envUsersFile)
}
