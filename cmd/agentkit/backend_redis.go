//go:build !noredis

package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/bsamanvith/agentkit/pkg/bus"
	redisbus "github.com/bsamanvith/agentkit/pkg/bus/redis"
	"github.com/bsamanvith/agentkit/pkg/clock"
)

// openRedis opens the Redis bus at url.
func openRedis(ctx context.Context, url string, clk clock.Clock, log *slog.Logger) (bus.Bus, func(), error) {
	b, closeBus, err := redisbus.Open(ctx, url, redisbus.WithClock(clk), redisbus.WithLogger(log.With("component", "bus")))
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", envBus, err)
	}
	return b, func() { _ = closeBus() }, nil
}
