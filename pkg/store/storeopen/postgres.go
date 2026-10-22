//go:build !nopostgres

package storeopen

import (
	"context"

	"github.com/bsamanvith/agentkit/pkg/store/postgres"
)

// postgresBackend opens a postgres:// or postgresql:// URL.
func postgresBackend() Backend {
	return Backend{
		Name: "Postgres", Prefixes: []string{"postgres://", "postgresql://"},
		Open: func(ctx context.Context, url string, cfg Config) (Opened, error) {
			opts := []postgres.Option{postgres.WithClock(cfg.Clock), postgres.WithMigrate(cfg.Migrate)}
			if cfg.AdminURL != "" {
				opts = append(opts, postgres.WithAdminURL(cfg.AdminURL))
			}
			s, err := postgres.Open(ctx, url, opts...)
			if err != nil {
				return Opened{}, err
			}
			return Opened{Store: s, Admin: postgres.Admin(s), Triggers: postgres.TriggerAdmin(s), Close: func() error { s.Close(); return nil }}, nil
		},
		Migrate: func(ctx context.Context, url string) error { return postgres.Migrate(ctx, url) },
	}
}
