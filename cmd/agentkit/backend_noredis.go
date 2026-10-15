//go:build noredis

package main

import (
	"context"
	"log/slog"

	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/clock"
)

func openRedis(context.Context, string, clock.Clock, *slog.Logger) (bus.Bus, func(), error) {
	return nil, nil, notBuilt(envBus, "Redis", "noredis")
}
