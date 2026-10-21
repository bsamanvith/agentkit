package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/credentials/file"
	"github.com/bsamanvith/agentkit/pkg/credentials/memory"
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
