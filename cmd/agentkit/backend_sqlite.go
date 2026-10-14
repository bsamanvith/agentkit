//go:build !nosqlite

package main

import (
	"context"
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/store/sqlite"
)

// openSQLite opens the SQLite store in the file at path.
func (c cli) openSQLite(ctx context.Context, path string, clk clock.Clock) (openedStore, error) {
	if path == "" {
		return openedStore{}, fmt.Errorf("%s: sqlite: needs a file path (sqlite:/data/agentkit.db)", envStore)
	}
	s, err := sqlite.Open(ctx, path, sqlite.WithClock(clk))
	if err != nil {
		return openedStore{}, fmt.Errorf("%s: %w", envStore, err)
	}
	return openedStore{Store: s, admin: sqlite.Admin(s), close: func() { _ = s.Close() }}, nil
}
