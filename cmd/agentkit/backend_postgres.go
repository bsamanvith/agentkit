//go:build !nopostgres

package main

import (
	"context"
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/store/postgres"
)

// openPostgres opens the Postgres store at url, migrating it unless migrate
// is false.
func openPostgres(ctx context.Context, url string, clk clock.Clock, migrate bool) (openedStore, error) {
	s, err := postgres.Open(ctx, url, postgres.WithClock(clk), postgres.WithMigrate(migrate))
	if err != nil {
		return openedStore{}, fmt.Errorf("%s: %w", envStore, err)
	}
	return openedStore{Store: s, admin: postgres.Admin(s), close: s.Close}, nil
}

// migratePostgres applies the store's migrations to the database at url.
func migratePostgres(ctx context.Context, url string) error { return postgres.Migrate(ctx, url) }
