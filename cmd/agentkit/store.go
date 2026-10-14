package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/memory"
	"github.com/bsamanvith/agentkit/pkg/store/postgres"
	"github.com/bsamanvith/agentkit/pkg/store/sqlite"
)

const (
	// envStore names the store of agentkit serve's agent API: "memory" (the
	// default, lost on restart), "sqlite:" and a file path, or a
	// postgres:// URL. agentkit migrate reads it too.
	envStore = "AGENTKIT_STORE"
	// envStoreMigrate=off starts serve without migrating the Postgres store:
	// agentkit migrate does it, as a job.
	envStoreMigrate = "AGENTKIT_STORE_MIGRATE"
)

// openedStore is the store serve runs on: the tenant-scoped view the agent
// API gets, the cross-tenant Admin only the recovery loop gets, and Close.
type openedStore struct {
	agentapi.Store
	admin store.Admin
	close func()
}

// openStore opens the store $AGENTKIT_STORE names; an unknown scheme is an
// error, which never echoes the value (a connection string can carry a
// password).
func (c cli) openStore(ctx context.Context, clk clock.Clock) (openedStore, error) {
	spec := c.env(envStore)
	switch {
	case spec == "" || spec == "memory":
		s := memory.New(memory.WithClock(clk))
		return openedStore{Store: s, admin: memory.Admin(s), close: func() {}}, nil
	case strings.HasPrefix(spec, "sqlite:"):
		path := strings.TrimPrefix(spec, "sqlite:")
		if path == "" {
			return openedStore{}, fmt.Errorf("%s: sqlite: needs a file path (sqlite:/data/agentkit.db)", envStore)
		}
		s, err := sqlite.Open(ctx, path, sqlite.WithClock(clk))
		if err != nil {
			return openedStore{}, fmt.Errorf("%s: %w", envStore, err)
		}
		return openedStore{Store: s, admin: sqlite.Admin(s), close: func() { _ = s.Close() }}, nil
	case strings.HasPrefix(spec, "postgres://"), strings.HasPrefix(spec, "postgresql://"):
		migrate := true
		switch v := c.env(envStoreMigrate); v {
		case "", "on":
		case "off":
			migrate = false
		default:
			return openedStore{}, fmt.Errorf("%s: %q is not on or off", envStoreMigrate, v)
		}
		s, err := postgres.Open(ctx, spec, postgres.WithClock(clk), postgres.WithMigrate(migrate))
		if err != nil {
			return openedStore{}, fmt.Errorf("%s: %w", envStore, err)
		}
		return openedStore{Store: s, admin: postgres.Admin(s), close: s.Close}, nil
	}
	return openedStore{}, fmt.Errorf("%s: unknown store; want memory, sqlite:/path or postgres://…", envStore)
}
