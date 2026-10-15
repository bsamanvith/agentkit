//go:build nopostgres

package main

import (
	"context"

	"github.com/bsamanvith/agentkit/pkg/clock"
)

func openPostgres(context.Context, string, clock.Clock, bool) (openedStore, error) {
	return openedStore{}, notBuilt(envStore, "Postgres", "nopostgres")
}

func migratePostgres(context.Context, string) error {
	return notBuilt(envStore, "Postgres", "nopostgres")
}
