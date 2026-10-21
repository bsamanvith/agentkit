//go:build nosqlite

package main

import (
	"context"

	"github.com/bsamanvith/agentkit/pkg/credentials"
)

func openSQLiteCredentials(context.Context, credentialsConfig, credentialsEnv) (credentials.BlobStore, func() error, error) {
	return nil, nil, notBuilt(`backend "sqlite"`, "SQLite", "nosqlite")
}
