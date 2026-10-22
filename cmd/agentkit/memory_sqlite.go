//go:build !nosqlite

package main

import (
	"context"
	"errors"

	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/sqlite"
)

// openSQLiteMemory opens the context store backend "sqlite" at c.Path.
func openSQLiteMemory(ctx context.Context, c memoryStoreConfig, cfg memory.Config) (memory.Store, func() error, error) {
	if c.Path == "" {
		return nil, nil, errors.New(`backend "sqlite" needs a path (a file)`)
	}
	s, err := sqlite.Open(ctx, c.Path, cfg)
	if err != nil {
		return nil, nil, err
	}
	return s, s.Close, nil
}

// openSQLiteIndex opens the search store backend "sqlite" at c.Path, which
// may be the context store's file.
func openSQLiteIndex(ctx context.Context, c memoryIndexConfig, cfg memory.IndexConfig) (memory.Index, func() error, error) {
	if c.Path == "" {
		return nil, nil, errors.New(`backend "sqlite" needs a path (a file)`)
	}
	x, err := sqlite.OpenIndex(ctx, c.Path, cfg)
	if err != nil {
		return nil, nil, err
	}
	return x, x.Close, nil
}
