//go:build nosqlite

package main

import (
	"context"

	"github.com/bsamanvith/agentkit/pkg/clock"
)

func (c cli) openSQLite(context.Context, string, clock.Clock) (openedStore, error) {
	return openedStore{}, notBuilt(envStore, "SQLite", "nosqlite")
}
