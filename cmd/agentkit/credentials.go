package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/credentials/file"
	"github.com/bsamanvith/agentkit/pkg/credentials/memory"
	"github.com/bsamanvith/agentkit/pkg/netguard"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// credentialsConfig is the config file's "credentials" section: where the
// credential vault (pkg/credentials) keeps its sealed blobs. Every backend
// but memory needs the key ring in $AGENTKIT_CREDENTIALS_KEY.
//
//	"credentials": {"backend": "file", "path": "/data/credentials.json"}
type credentialsConfig struct {
	// Backend is one of credentialBackends: "memory" (the default: in this
	// process, lost on restart), "file" (one file, one process: path),
	// "sqlite" (the SQLite store's file: path, by default the one
	// $AGENTKIT_STORE names; local images) or "postgres" (the Postgres
	// store's database, which $AGENTKIT_STORE names: replicas; dev and
	// cloud images).
	Backend string `json:"backend"`
	// Path is the file of "file" and "sqlite".
	Path string `json:"path"`
	// OAuth2 are the OAuth2 clients of the slots whose tokens agentkit
	// refreshes, and (with auth_url) connects accounts with.
	OAuth2 []oauth2AppConfig `json:"oauth2"`
	// ReturnURLs are where a connect flow may send the browser back to
	// when it ends (POST /v1/credentials/{slot}/connect's return_url): the
	// console's /connections/done, say. A return URL must have the scheme,
	// host and path of one of them.
	ReturnURLs []string `json:"return_urls"`
	// AllowHosts are internal hosts a stored client credentials token_url
	// may name (PUT /v1/credentials/{slot}); every other one must be https
	// on a public address (the SSRF guard, pkg/netguard).
	AllowHosts []string `json:"allow_hosts"`
}

// credentialsEnv is what a backend reads besides its config: the store
// serve runs on, which the SQL backends share.
type credentialsEnv struct {
	store   string // $AGENTKIT_STORE
	migrate bool   // $AGENTKIT_STORE_MIGRATE is not off
}

// credentialBackend opens a blob store; closing it is the returned func.
type credentialBackend func(ctx context.Context, c credentialsConfig, env credentialsEnv) (credentials.BlobStore, func() error, error)

// credentialBackends are the vault backends this binary can open, by the
// name of credentials.backend. SQLite and Postgres are in files of their own,
// which the nosqlite and nopostgres tags leave out (credentials_sqlite.go,
// credentials_postgres.go), as they leave out the stores.
func credentialBackends() map[string]credentialBackend {
	return map[string]credentialBackend{
		"memory": func(_ context.Context, c credentialsConfig, _ credentialsEnv) (credentials.BlobStore, func() error, error) {
			if c.Path != "" {
				return nil, nil, errors.New(`backend "memory" keeps nothing in a file: leave out path`)
			}
			return memory.New(), func() error { return nil }, nil
		},
		"file": func(_ context.Context, c credentialsConfig, _ credentialsEnv) (credentials.BlobStore, func() error, error) {
			if c.Path == "" {
				return nil, nil, errors.New(`backend "file" needs a path (a file only this process uses)`)
			}
			s, err := file.Open(c.Path)
			if err != nil {
				return nil, nil, err
			}
			return s, s.Close, nil
		},
		"sqlite":   openSQLiteCredentials,
		"postgres": openPostgresCredentials,
	}
}

// openVault opens the credential vault: nil when neither a credentials
// section nor a key ring is given (nothing uses credentials then), and the
// function that closes its backend. A backend that keeps credentials past
// the process needs the key ring; the memory backend without one gives no
// vault.
func (c cli) openVault(ctx context.Context, cfg credentialsConfig, ring *credentials.KeyRing) (credentials.Vault, func() error, error) {
	parts, closeFn, err := c.openVaultParts(ctx, cfg, ring)
	if parts == nil {
		return nil, closeFn, err
	}
	return parts.vault, closeFn, err
}

// vaultParts is an open vault with its backend and sealer, which the
// connect flows keep their state in (pkg/credentials/connect).
type vaultParts struct {
	vault  credentials.Vault
	store  credentials.BlobStore
	sealer *credentials.Sealer
}

// openVaultParts is openVault with the vault's parts; nil when there is no
// vault.
func (c cli) openVaultParts(ctx context.Context, cfg credentialsConfig, ring *credentials.KeyRing) (*vaultParts, func() error, error) {
	noop := func() error { return nil }
	if cfg.Backend == "" {
		cfg.Backend = "memory"
	}
	open, ok := credentialBackends()[cfg.Backend]
	if !ok {
		names := slices.Sorted(maps.Keys(credentialBackends()))
		return nil, nil, fmt.Errorf("credentials.backend %q: want %q", cfg.Backend, strings.Join(names, `", "`))
	}
	if ring == nil {
		if cfg.Backend != "memory" {
			return nil, nil, fmt.Errorf("credentials.backend %q needs the key ring in $%s (<id>:<base64 of %d random bytes>)", cfg.Backend, envCredentialsKey, credentials.KeySize)
		}
		if cfg.Path != "" {
			return nil, nil, errors.New(`credentials: backend "memory" keeps nothing in a file: leave out path`)
		}
		return nil, noop, nil
	}
	env := credentialsEnv{store: c.env(envStore), migrate: c.env(envStoreMigrate) != "off"}
	store, closeFn, err := open(ctx, cfg, env)
	if err != nil {
		return nil, nil, fmt.Errorf("credentials: %w", err)
	}
	sealer := credentials.NewSealer(ring)
	return &vaultParts{vault: credentials.NewVault(store, sealer), store: store, sealer: sealer}, closeFn, nil
}

// oauth2AppConfig is one entry of credentials.oauth2: the OAuth2 client a
// slot's tokens were issued to, which refreshing them needs, and with
// auth_url (or a profile that has one) the authorization code flow that
// connects accounts (POST /v1/credentials/{slot}/connect). The secret is
// read from the environment variable client_secret_env names, never from
// the file.
//
//	"oauth2": [{"slot": "gmail", "profile": "google",
//	            "client_id": "123.apps.googleusercontent.com", "client_secret_env": "GMAIL_CLIENT_SECRET",
//	            "scopes": ["https://www.googleapis.com/auth/gmail.readonly"],
//	            "redirect_url": "https://agents.example.com/oauth/callback"}]
type oauth2AppConfig struct {
	Slot            string `json:"slot"`
	TokenURL        string `json:"token_url"`
	ClientID        string `json:"client_id"`
	ClientSecretEnv string `json:"client_secret_env"`
	// AuthStyle is how the client authenticates to the token endpoint:
	// "header" (HTTP basic, the default) or "params" (in the form).
	AuthStyle string `json:"auth_style"`

	// Profile is a provider preset of the shared OAuth core (package
	// mcp/oauth): "google" (its endpoints, access_type=offline and
	// prompt=consent), "salesforce" (the refresh_token scope) or
	// "microsoft" (offline_access). The fields below override it.
	Profile string `json:"profile"`
	// AuthURL is the provider's authorization endpoint; set, the slot's
	// accounts can be connected.
	AuthURL string `json:"auth_url"`
	// Scopes are asked for when connecting.
	Scopes []string `json:"scopes"`
	// RedirectURL is the server's callback as registered with the
	// provider: the server's public origin and /oauth/callback.
	RedirectURL string `json:"redirect_url"`
	// AuthParams are added to the authorization URL.
	AuthParams map[string]string `json:"auth_params"`
}

// oauth2App is an entry of credentials.oauth2 resolved: its profile
// applied, its secret read.
type oauth2App struct {
	slot  string
	app   credentials.OAuth2App
	scope []string
	// authURL, redirectURL and params are the connect flow's; authURL ""
	// when the slot's accounts are not connected here.
	authURL, redirectURL string
	params               map[string]string
}

// oauth2Apps resolves credentials.oauth2, checking each entry: the slot
// once, a known auth style and profile, the secret set in the
// environment, https endpoints (http only on a loopback host), and for a
// connect flow a redirect URL that ends in /oauth/callback.
func (c cli) oauth2Apps(cfg credentialsConfig) ([]oauth2App, error) {
	var out []oauth2App
	seen := map[string]bool{}
	for i, a := range cfg.OAuth2 {
		where := fmt.Sprintf("credentials.oauth2[%d]", i)
		if seen[a.Slot] {
			return nil, fmt.Errorf("%s: slot %q is listed twice", where, a.Slot)
		}
		seen[a.Slot] = true
		var prof providerProfile
		if a.Profile != "" {
			p, err := oauthProfile(a.Profile)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", where, err)
			}
			prof = p
			a.AuthURL, a.TokenURL = cmp.Or(a.AuthURL, p.authURL), cmp.Or(a.TokenURL, p.tokenURL)
			a.AuthStyle = cmp.Or(a.AuthStyle, p.authStyle)
			if p.refreshScope != "" && !slices.Contains(a.Scopes, p.refreshScope) {
				a.Scopes = append(slices.Clone(a.Scopes), p.refreshScope)
			}
			params := maps.Clone(p.params)
			if params == nil {
				params = map[string]string{}
			}
			maps.Copy(params, a.AuthParams)
			a.AuthParams = params
		}
		var style credentials.AuthStyle
		switch a.AuthStyle {
		case "", "header":
			style = credentials.AuthInHeader
		case "params":
			style = credentials.AuthInParams
		default:
			return nil, fmt.Errorf("%s: auth_style %q: want \"header\" or \"params\"", where, a.AuthStyle)
		}
		if a.ClientSecretEnv == "" {
			return nil, fmt.Errorf("%s: client_secret_env names the variable with the client secret (the file never holds it)", where)
		}
		secret := c.env(a.ClientSecretEnv)
		if secret == "" {
			return nil, fmt.Errorf("%s: $%s (client_secret_env) is not set", where, a.ClientSecretEnv)
		}
		// The client secret and refresh tokens go to it: https, or http only
		// on a loopback host.
		if !secureURL(a.TokenURL) {
			return nil, fmt.Errorf("%s: token_url %q: want an https URL (http only on a loopback host)", where, a.TokenURL)
		}
		app := oauth2App{
			slot: a.Slot, scope: a.Scopes, params: a.AuthParams,
			app: credentials.OAuth2App{
				ClientID: a.ClientID, ClientSecret: credentials.NewSecret(secret), TokenURL: a.TokenURL, AuthStyle: style,
				KeepExtras: prof.keepExtras, DefaultTTL: prof.defaultTTL,
			},
		}
		if a.AuthURL == "" {
			if a.RedirectURL != "" || len(a.AuthParams) > 0 {
				return nil, fmt.Errorf("%s: redirect_url and auth_params are for connecting accounts, which needs auth_url", where)
			}
			out = append(out, app)
			continue
		}
		if !secureURL(a.AuthURL) {
			return nil, fmt.Errorf("%s: auth_url %q: want an https URL (http only on a loopback host)", where, a.AuthURL)
		}
		if u, err := url.Parse(a.RedirectURL); err != nil || !secureURL(a.RedirectURL) || !strings.HasSuffix(u.Path, oauthCallbackPath) {
			return nil, fmt.Errorf("%s: redirect_url %q: want the server's public URL and %s, https (http only on a loopback host)", where, a.RedirectURL, oauthCallbackPath)
		}
		app.authURL, app.redirectURL = a.AuthURL, a.RedirectURL
		out = append(out, app)
	}
	return out, nil
}

// secureURL reports whether raw is an https URL, or http on a loopback
// host, with no user info.
func secureURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && u.User == nil &&
		(u.Scheme == "https" || u.Scheme == "http" && isLoopback(net.JoinHostPort(u.Hostname(), "0")))
}

// oauthCallbackPath is where serve takes the providers' redirects
// (connect.CallbackPath, which this file does not import: the runner and
// toolsets images leave the connect flow out).
const oauthCallbackPath = "/oauth/callback"

// oauthClientMetadataPath is where the MCP OAuth client's metadata is
// served (oauth.ClientMetadataPath, which this file does not import); here,
// not beside its handler, as Cloudflare Access's gate names it in every
// build (accessPublic).
const oauthClientMetadataPath = "/oauth/client-metadata.json"

// providerProfile is what a provider preset sets.
type providerProfile struct {
	authURL, tokenURL, refreshScope string
	authStyle                       string // "" or "params"
	keepExtras                      []string
	defaultTTL                      time.Duration
	params                          map[string]string
}

// credentialSources is what toolset.Build gives the specs that name a
// credential slot: the sources over the vault, with the OAuth2 clients of
// credentials.oauth2. With no vault, every slot is refused, naming the key
// ring that would open one (a required toolset then fails startup; an
// optional one is skipped).
func (c cli) credentialSources(vault credentials.Vault, cfg credentialsConfig, log *slog.Logger) (toolset.SourceProvider, error) {
	if vault == nil {
		if len(cfg.OAuth2) > 0 {
			return nil, fmt.Errorf("credentials.oauth2 needs the credential vault: set $%s", envCredentialsKey)
		}
		return toolset.SourceFunc(func(_, slot string, _ credentials.Scope) (credentials.Source, error) {
			return nil, fmt.Errorf("%w: slot %q needs the credential vault: set $%s (<id>:<base64 of %d random bytes>)",
				toolset.ErrNoCredentials, slot, envCredentialsKey, credentials.KeySize)
		}), nil
	}
	apps, err := c.oauth2Apps(cfg)
	if err != nil {
		return nil, err
	}
	// A client credentials token URL comes from the vault, which users
	// write (PUT /v1/credentials/{slot}): it is called through the SSRF
	// guard.
	guard, err := netguard.New(cfg.AllowHosts)
	if err != nil {
		return nil, fmt.Errorf("credentials.allow_hosts: %w", err)
	}
	opts := []credentials.SourcesOption{
		credentials.WithLogger(log.With("component", "credentials")),
		credentials.WithStoredTokenClient(guard.Client(nil)),
	}
	for _, a := range apps {
		opts = append(opts, credentials.WithOAuth2App(a.slot, a.app))
	}
	s, err := credentials.NewSources(vault, opts...)
	if err != nil {
		return nil, err
	}
	return s, nil
}
