package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/bus/busopen"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/storeopen"
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

// openStore opens the store $AGENTKIT_STORE names (package storeopen); an
// unknown spec, or a backend this build leaves out (backends.go), is an
// error, which never echoes the value (a connection string can carry a
// password).
func (c cli) openStore(ctx context.Context, clk clock.Clock) (openedStore, error) {
	migrate := true
	switch v := c.env(envStoreMigrate); v {
	case "", "on":
	case "off":
		migrate = false
	default:
		return openedStore{}, fmt.Errorf("%s: %q is not on or off", envStoreMigrate, v)
	}
	o, err := storeopen.Open(ctx, c.env(envStore), storeopen.WithClock(clk), storeopen.WithMigrate(migrate))
	if err != nil {
		return openedStore{}, fmt.Errorf("%s: %w", envStore, err)
	}
	return openedStore{Store: o.Store, admin: o.Admin, close: func() { _ = o.Close() }}, nil
}

// envBus names the bus that joins replicas of agentkit serve (E18): empty or
// "memory" for one replica, or a redis:// or rediss:// URL. Every replica
// sharing a store must share it.
const envBus = "AGENTKIT_BUS"

// openBus opens the bus $AGENTKIT_BUS names (package busopen), and the
// function that closes it. An unknown value is an error, which never echoes
// it (a URL can carry a password).
func (c cli) openBus(ctx context.Context, clk clock.Clock, log *slog.Logger) (bus.Bus, func(), error) {
	b, closeBus, err := busopen.Open(ctx, c.env(envBus), busopen.WithClock(clk), busopen.WithLogger(log.With("component", "bus")))
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", envBus, err)
	}
	return b, func() { _ = closeBus() }, nil
}
