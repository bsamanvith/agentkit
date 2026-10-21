//go:build nopostgres

package main

import (
	"context"

	"github.com/bsamanvith/agentkit/pkg/credentials"
)

func openPostgresCredentials(context.Context, credentialsConfig, credentialsEnv) (credentials.BlobStore, func() error, error) {
	return nil, nil, notBuilt(`backend "postgres"`, "Postgres", "nopostgres")
}
