//go:build !nopostgres && !noagent

package main

import (
	"context"
	"errors"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/postgres"
	storepg "github.com/bsamanvith/agentkit/pkg/store/postgres"
)

// openPostgresMemory opens the context store backend "postgres": the
// memory_files table of the Postgres store $AGENTKIT_STORE names, with its
// row-level security. It migrates the store's schema as the store does,
// unless $AGENTKIT_STORE_MIGRATE is off.
func openPostgresMemory(ctx context.Context, c memoryStoreConfig, cfg memory.Config, getenv func(string) string) (memory.Store, func() error, error) {
	if c.Path != "" {
		return nil, nil, errors.New(`backend "postgres" uses the store's database ($` + envStore + `): leave out path`)
	}
	url := getenv(envStore)
	if !strings.HasPrefix(url, "postgres://") && !strings.HasPrefix(url, "postgresql://") {
		return nil, nil, errors.New(`backend "postgres" needs $` + envStore + ` to name a Postgres store (postgres://...)`)
	}
	s, err := postgres.Open(ctx, url, cfg, storepg.WithMigrate(getenv(envStoreMigrate) != "off"))
	if err != nil {
		return nil, nil, err
	}
	return s, s.Close, nil
}
