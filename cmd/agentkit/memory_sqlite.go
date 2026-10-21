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
