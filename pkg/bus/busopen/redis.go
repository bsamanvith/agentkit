//go:build !noredis

package busopen

import (
	"context"

	"github.com/bsamanvith/agentkit/pkg/bus"
	redisbus "github.com/bsamanvith/agentkit/pkg/bus/redis"
)

// redisBackend opens a redis:// or rediss:// URL.
func redisBackend() Backend {
	return Backend{
		Name: "Redis", Prefixes: []string{"redis://", "rediss://"},
		Open: func(ctx context.Context, url string, cfg Config) (bus.Bus, func() error, error) {
			b, closeBus, err := redisbus.Open(ctx, url, redisbus.WithClock(cfg.Clock), redisbus.WithLogger(cfg.Logger))
			if err != nil {
				return nil, nil, err
			}
			return b, closeBus, nil
		},
	}
}
