//go:build !nosqlite

package storeopen

import (
	"context"
	"fmt"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/store/sqlite"
)

// sqliteBackend opens "sqlite:" and a file path.
func sqliteBackend() Backend {
	return Backend{
		Name: "SQLite", Prefixes: []string{"sqlite:"},
		Open: func(ctx context.Context, spec string, cfg Config) (Opened, error) {
			path := strings.TrimPrefix(spec, "sqlite:")
			if path == "" {
				return Opened{}, fmt.Errorf("%w: sqlite: needs a file path (sqlite:/data/agentkit.db)", ErrUnknownSpec)
			}
			s, err := sqlite.Open(ctx, path, sqlite.WithClock(cfg.Clock))
			if err != nil {
				return Opened{}, err
			}
			return Opened{Store: s, Admin: sqlite.Admin(s), Triggers: sqlite.TriggerAdmin(s), Close: s.Close}, nil
		},
	}
}
