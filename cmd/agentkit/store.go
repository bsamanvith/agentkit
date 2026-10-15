package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/memory"
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

// openStore opens the store $AGENTKIT_STORE names; an unknown scheme, or a
// backend this build leaves out (backends.go), is an error, which never
// echoes the value (a connection string can carry a password).
func (c cli) openStore(ctx context.Context, clk clock.Clock) (openedStore, error) {
	spec := c.env(envStore)
	switch {
	case spec == "" || spec == "memory":
		s := memory.New(memory.WithClock(clk))
		return openedStore{Store: s, admin: memory.Admin(s), close: func() {}}, nil
	case strings.HasPrefix(spec, "sqlite:"):
		return c.openSQLite(ctx, strings.TrimPrefix(spec, "sqlite:"), clk)
	case strings.HasPrefix(spec, "postgres://"), strings.HasPrefix(spec, "postgresql://"):
		migrate := true
		switch v := c.env(envStoreMigrate); v {
		case "", "on":
		case "off":
			migrate = false
		default:
			return openedStore{}, fmt.Errorf("%s: %q is not on or off", envStoreMigrate, v)
		}
		return openPostgres(ctx, spec, clk, migrate)
	}
	return openedStore{}, fmt.Errorf("%s: unknown store; want memory, sqlite:/path or postgres://…", envStore)
}

// envBus names the bus that joins replicas of agentkit serve (E18): empty or
// "memory" for one replica (the agent API keeps its own, in process), or a
// redis:// or rediss:// URL. Every replica sharing a store must share it.
const envBus = "AGENTKIT_BUS"

// openBus opens the bus $AGENTKIT_BUS names: nil for one replica, and the
// function that closes it. An unknown value is an error, which never echoes
// it (a URL can carry a password).
func (c cli) openBus(ctx context.Context, clk clock.Clock, log *slog.Logger) (bus.Bus, func(), error) {
	spec := c.env(envBus)
	switch {
	case spec == "" || spec == "memory":
		return nil, func() {}, nil
	case strings.HasPrefix(spec, "redis://"), strings.HasPrefix(spec, "rediss://"):
		return openRedis(ctx, spec, clk, log)
	}
	return nil, nil, fmt.Errorf("%s: unknown bus; want memory or redis://…", envBus)
}
