//go:build nosqlite && !noagent

package main

import (
	"context"

	"github.com/bsamanvith/agentkit/pkg/memory"
)

func openSQLiteMemory(context.Context, memoryStoreConfig, memory.Config, func(string) string) (memory.Store, func() error, error) {
	return nil, nil, notBuilt("backend \"sqlite\"", "SQLite", "nosqlite")
}

func openSQLiteIndex(context.Context, memoryIndexConfig, memory.IndexConfig) (memory.Index, func() error, error) {
	return nil, nil, notBuilt("backend \"sqlite\"", "SQLite", "nosqlite")
}
