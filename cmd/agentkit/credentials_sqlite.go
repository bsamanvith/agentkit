//go:build !nosqlite

package main

import (
	"context"
	"errors"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/credentials/sqlite"
)

// openSQLiteCredentials opens the vault backend "sqlite": the
// credential_blobs table of the SQLite store's file at c.Path, or, with no
// path, of the store $AGENTKIT_STORE names.
func openSQLiteCredentials(ctx context.Context, c credentialsConfig, env credentialsEnv) (credentials.BlobStore, func() error, error) {
	path := c.Path
	if path == "" {
		var ok bool
		if path, ok = strings.CutPrefix(env.store, "sqlite:"); !ok || path == "" {
			return nil, nil, errors.New(`backend "sqlite" needs a path, or $` + envStore + ` naming a SQLite store (sqlite:/data/agentkit.db)`)
		}
	}
	s, err := sqlite.Open(ctx, path)
	if err != nil {
		return nil, nil, err
	}
	return s, s.Close, nil
}
