//go:build !nopostgres

package main

import (
	"context"
	"errors"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/credentials/postgres"
	storepg "github.com/bsamanvith/agentkit/pkg/store/postgres"
)

// openPostgresCredentials opens the vault backend "postgres": the
// credential_blobs table of the Postgres store $AGENTKIT_STORE names, with
// its row-level security. It migrates the store's schema as the store does,
// unless $AGENTKIT_STORE_MIGRATE is off.
func openPostgresCredentials(ctx context.Context, c credentialsConfig, env credentialsEnv) (credentials.BlobStore, func() error, error) {
	if c.Path != "" {
		return nil, nil, errors.New(`backend "postgres" uses the store's database ($` + envStore + `): leave out path`)
	}
	if !strings.HasPrefix(env.store, "postgres://") && !strings.HasPrefix(env.store, "postgresql://") {
		return nil, nil, errors.New(`backend "postgres" needs $` + envStore + ` to name a Postgres store (postgres://...)`)
	}
	s, err := postgres.Open(ctx, env.store, storepg.WithMigrate(env.migrate))
	if err != nil {
		return nil, nil, err
	}
	return s, s.Close, nil
}
