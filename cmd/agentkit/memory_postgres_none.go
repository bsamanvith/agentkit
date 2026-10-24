//go:build nopostgres && !noagent

package main

import (
	"context"

	"github.com/bsamanvith/agentkit/pkg/memory"
)

func openPostgresMemory(context.Context, memoryStoreConfig, memory.Config, func(string) string) (memory.Store, func() error, error) {
	return nil, nil, notBuilt(`backend "postgres"`, "Postgres", "nopostgres")
}
