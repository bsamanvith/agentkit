//go:build nosqlite

package main

import (
	"context"

	"github.com/bsamanvith/agentkit/pkg/memory"
)

func openSQLiteMemory(context.Context, memoryStoreConfig, memory.Config) (memory.Store, func() error, error) {
	return nil, nil, notBuilt("backend \"sqlite\"", "SQLite", "nosqlite")
}
