package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/credentials/file"
	"github.com/bsamanvith/agentkit/pkg/credentials/memory"
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
	// refreshes.
	OAuth2 []oauth2AppConfig `json:"oauth2"`
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
	return credentials.NewVault(store, credentials.NewSealer(ring)), closeFn, nil
}

// oauth2AppConfig is one entry of credentials.oauth2: the OAuth2 client a
// slot's tokens were issued to, which refreshing them needs. The secret is
// read from the environment variable client_secret_env names, never from
// the file.
//
//	"oauth2": [{"slot": "gmail", "token_url": "https://oauth2.googleapis.com/token",
//	            "client_id": "123.apps.googleusercontent.com", "client_secret_env": "GMAIL_CLIENT_SECRET"}]
type oauth2AppConfig struct {
	Slot            string `json:"slot"`
	TokenURL        string `json:"token_url"`
	ClientID        string `json:"client_id"`
	ClientSecretEnv string `json:"client_secret_env"`
	// AuthStyle is how the client authenticates to the token endpoint:
	// "header" (HTTP basic, the default) or "params" (in the form).
	AuthStyle string `json:"auth_style"`
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
	opts := []credentials.SourcesOption{credentials.WithLogger(log.With("component", "credentials"))}
	seen := map[string]bool{}
	for i, a := range cfg.OAuth2 {
		where := fmt.Sprintf("credentials.oauth2[%d]", i)
		if seen[a.Slot] {
			return nil, fmt.Errorf("%s: slot %q is listed twice", where, a.Slot)
		}
		seen[a.Slot] = true
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
		if u, err := url.Parse(a.TokenURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, fmt.Errorf("%s: token_url %q is not an http(s) URL", where, a.TokenURL)
		}
		opts = append(opts, credentials.WithOAuth2App(a.Slot, credentials.OAuth2App{
			ClientID: a.ClientID, ClientSecret: credentials.NewSecret(secret), TokenURL: a.TokenURL, AuthStyle: style,
		}))
	}
	s, err := credentials.NewSources(vault, opts...)
	if err != nil {
		return nil, err
	}
	return s, nil
}
