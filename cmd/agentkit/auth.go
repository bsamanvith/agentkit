package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/cfaccess"
	"github.com/bsamanvith/agentkit/pkg/auth/jwt"
	"github.com/bsamanvith/agentkit/pkg/auth/login"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// envAPIKeysFile names a JSON file of API keys, in the form of the config's
// "auth" section ({"api_keys": [...]}): for hashes kept in a mounted secret.
const envAPIKeysFile = "AGENTKIT_API_KEYS_FILE"

// tokenKeyID names the key $AGENTKIT_API_TOKEN makes, in logs.
const tokenKeyID = "api-token"

// Authentication read from the environment, beside the config's "auth".
const (
	// envRequireAuth, "1" or "true", is auth.required: serve refuses to
	// start with no authentication, even on a loopback address.
	envRequireAuth = "AGENTKIT_REQUIRE_AUTH"
	// envCFAccessTeamDomain and envCFAccessAUD are auth.cloudflare_access's
	// team_domain and aud; set, they win over the file's.
	envCFAccessTeamDomain = "AGENTKIT_CF_ACCESS_TEAM_DOMAIN"
	envCFAccessAUD        = "AGENTKIT_CF_ACCESS_AUD"
)

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
	// Required makes serve refuse to start with no authentication (no API
	// key, token, identity provider, users or Cloudflare Access), even on a
	// loopback address: behind a tunnel such as cloudflared, which connects
	// over loopback, an open server is open to whoever reaches the tunnel.
	// $AGENTKIT_REQUIRE_AUTH also sets it.
	Required bool `json:"required"`
	// CloudflareAccess verifies the assertion Cloudflare Access adds to
	// each request it lets through: every request but the public ones
	// needs a valid one, and its email is the principal's user.
	CloudflareAccess *cfaccess.Config `json:"cloudflare_access"`
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
	a, _, err := c.authenticators(ctx, cfg, log)
	return a, err
}

// authenticators is authenticator, and the Cloudflare Access verifier on its
// own (nil unless configured), which serve puts in front of every request.
// Access comes last in the chain: a request with a key or a token is that
// credential's principal, one with only Access's assertion its email's.
func (c cli) authenticators(ctx context.Context, cfg serveConfig, log *slog.Logger) (auth.Authenticator, *cfaccess.Authenticator, error) {
	keys, err := c.apiKeys(cfg)
	if err != nil {
		return nil, nil, err
	}
	var chain []auth.Authenticator
	if cfg.Auth.JWT != nil {
		tokens, err := jwt.New(cfg.Auth.JWT.config(), jwt.WithLogger(log.With("component", "auth")))
		if err != nil {
			return nil, nil, fmt.Errorf("auth.jwt: %w", err)
		}
		if err := tokens.Refresh(ctx); err != nil {
			log.Warn("auth.jwt: the identity provider's keys could not be fetched; its tokens fail until they are", "issuer", cfg.Auth.JWT.Issuer, "error", err)
		}
		chain = append(chain, tokens)
	}
	if keys != nil {
		chain = append(chain, keys)
	}
	access, err := c.cloudflareAccess(ctx, cfg, log)
	if err != nil {
		return nil, nil, err
	}
	if access != nil {
		chain = append(chain, access)
	}
	switch len(chain) {
	case 0:
		return nil, nil, nil
	case 1:
		return chain[0], access, nil
	}
	return auth.Chain(chain...), access, nil
}

// cloudflareAccess is the verifier of auth.cloudflare_access, with
// $AGENTKIT_CF_ACCESS_TEAM_DOMAIN and $AGENTKIT_CF_ACCESS_AUD over the
// file's (either one alone turns it on); nil when neither configures it. Its
// tenant defaults to the default one. The team's keys are fetched once at
// startup, a failure only logged.
//
// With no emails, every email the Access policy admits gets the scopes,
// "*" unless set: with authentication required that is refused unless the
// scopes are set, otherwise it is a warning.
func (c cli) cloudflareAccess(ctx context.Context, cfg serveConfig, log *slog.Logger) (*cfaccess.Authenticator, error) {
	if !c.cloudflareAccessSet(cfg) {
		return nil, nil
	}
	var ac cfaccess.Config
	if cfg.Auth.CloudflareAccess != nil {
		ac = *cfg.Auth.CloudflareAccess
	}
	team, aud := c.env(envCFAccessTeamDomain), c.env(envCFAccessAUD)
	if team != "" {
		ac.TeamDomain = team
	}
	if aud != "" {
		ac.AUD = aud
	}
	if ac.Tenant == "" {
		ac.Tenant = agentapi.DefaultTenant
	}
	if err := checkToolsets(cfg, "auth.cloudflare_access", ac.Toolsets); err != nil {
		return nil, err
	}
	l := log.With("component", "auth")
	a, err := cfaccess.New(ac, cfaccess.WithLogger(l))
	if err != nil {
		return nil, fmt.Errorf("auth.cloudflare_access (or $%s, $%s): %w", envCFAccessTeamDomain, envCFAccessAUD, err)
	}
	if len(ac.Emails) == 0 {
		required, err := c.requireAuth(cfg)
		if err != nil {
			return nil, err
		}
		if required && len(ac.Scopes) == 0 {
			return nil, errors.New("auth.cloudflare_access: with authentication required, list the emails let in (emails), or set the scopes every admitted email gets (scopes): otherwise anyone the Access policy admits gets every scope")
		}
		scopes := ac.Scopes
		if len(scopes) == 0 {
			scopes = []string{"*"}
		}
		l.Warn("auth.cloudflare_access: no emails listed, so every email the Access policy admits gets these scopes; list them in auth.cloudflare_access.emails", "scopes", scopes)
	}
	if err := a.Refresh(ctx); err != nil {
		l.Warn("auth.cloudflare_access: the team's keys could not be fetched; every request fails until they are", "issuer", a.Issuer(), "error", err)
	}
	return a, nil
}

// cloudflareAccessSet reports whether Cloudflare Access is configured: the
// file's auth.cloudflare_access section, or either of its variables.
func (c cli) cloudflareAccessSet(cfg serveConfig) bool {
	return cfg.Auth.CloudflareAccess != nil || c.env(envCFAccessTeamDomain) != "" || c.env(envCFAccessAUD) != ""
}

// requireAuth reports whether serve must refuse to start open: auth.required,
// or $AGENTKIT_REQUIRE_AUTH set to 1, true or yes. 0, false or no do not turn
// off the file's "required": true.
func (c cli) requireAuth(cfg serveConfig) (bool, error) {
	v := c.env(envRequireAuth)
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		return cfg.Auth.Required, nil
	case "1", "true", "yes":
		return true, nil
	case "0", "false", "no":
		return cfg.Auth.Required, nil // the file's true is not turned off from outside
	}
	return false, fmt.Errorf("$%s: %q is not 1, true, yes, 0, false or no", envRequireAuth, v)
}

// accessPublic are the requests that need no Cloudflare Access assertion:
// the health probes, which the orchestrator sends to the origin, and the
// public endpoints that callers outside Access reach, which its application
// must bypass (docs: deploy/cloudflare). Exactly these, nothing more: a
// webhook's id is letters, digits, '-' and '_', never "." or "..".
func accessPublic(r *http.Request) bool {
	p := r.URL.Path
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		return p == "/healthz" || p == "/readyz" || p == oauthCallbackPath || p == oauthClientMetadataPath
	case http.MethodPost:
		id, ok := strings.CutPrefix(p, "/v1/hooks/")
		return ok && hookID(id)
	}
	return false
}

// hookID reports whether s may be a webhook's id: 1 to 128 letters, digits,
// '-' or '_'.
func hookID(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// accessGate is the middleware Cloudflare Access puts in front of the whole
// server: access.Require with the public requests, then /readyz answered
// with its status alone to a caller without a valid assertion. The RPC of a
// runner or toolsets component served beside the API passes when it has its
// own token ($AGENTKIT_RPC_TOKEN): its callers send that, never an assertion.
func accessGate(access *cfaccess.Authenticator, env serveEnv) []func(http.Handler) http.Handler {
	rpc := env.rpcToken != "" && (slices.Contains(env.components, runnerName) || slices.Contains(env.components, toolsetsName))
	public := func(r *http.Request) bool {
		return accessPublic(r) || rpc && strings.HasPrefix(r.URL.Path, "/rpc/")
	}
	return []func(http.Handler) http.Handler{access.Require(public), redactReadyz(access)}
}

// redactReadyz answers GET /readyz, to a caller without a valid assertion,
// with the status alone: the components' names, reasons (which may name
// hosts) and details are for those Access let in.
func redactReadyz(access *cfaccess.Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/readyz" {
				next.ServeHTTP(w, r)
				return
			}
			if _, err := access.Authenticate(r); err == nil {
				next.ServeHTTP(w, r)
				return
			}
			rec := &statusOnly{header: http.Header{}}
			next.ServeHTTP(rec, r)
			if rec.code == 0 {
				rec.code = http.StatusOK
			}
			status := "ready"
			if rec.code != http.StatusOK {
				status = "not ready"
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(rec.code)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
		})
	}
}

// statusOnly is a ResponseWriter that keeps the status and drops the rest.
type statusOnly struct {
	header http.Header
	code   int
}

func (s *statusOnly) Header() http.Header { return s.header }

func (s *statusOnly) WriteHeader(code int) {
	if s.code == 0 {
		s.code = code
	}
}

func (s *statusOnly) Write(b []byte) (int, error) {
	s.WriteHeader(http.StatusOK)
	return len(b), nil
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
	return fmt.Errorf("serving on %s, which is not a loopback address, needs credentials: %s", addr, credentialChoices)
}

// credentialChoices are the ways to configure authentication, for errors and
// warnings.
var credentialChoices = fmt.Sprintf("a token in $%s, API keys (auth.api_keys in the config, or $%s), an identity provider (auth.jwt), Cloudflare Access (auth.cloudflare_access, or $%s and $%s), or for the agent API alone local users (auth.users, or $%s)", envAPIToken, envAPIKeysFile, envCFAccessTeamDomain, envCFAccessAUD, envUsersFile)
