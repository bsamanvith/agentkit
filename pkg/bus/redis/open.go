package redis

import (
	"context"
	"errors"
	"fmt"

	goredis "github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"

	"github.com/bsamanvith/agentkit/pkg/bus"
)

// SubscribePoolSize is how many connections [Open] gives subscriptions,
// unless the URL sets pool_size: one per subscription running at once (a
// client following a run, a run's command inbox).
const SubscribePoolSize = 1000

// Open connects to the Redis server rawURL names (redis:// or rediss://, read
// by go-redis's ParseURL, so its query options apply) and returns a bus on
// it, with a client for publishes and one for subscriptions, and the
// function that closes the bus and both clients. It pings the server first.
// Errors never echo the URL, which can carry a password.
func Open(ctx context.Context, rawURL string, opts ...Option) (*Bus, func() error, error) {
	opt, err := goredis.ParseURL(rawURL)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: not a redis:// or rediss:// URL", bus.ErrInvalid)
	}
	opt.MaintNotificationsConfig = &maintnotifications.Config{Mode: maintnotifications.ModeDisabled}
	pub := goredis.NewClient(opt)
	subOpt := *opt
	if subOpt.PoolSize == 0 {
		subOpt.PoolSize = SubscribePoolSize
	}
	sub := goredis.NewClient(&subOpt)
	closeClients := func() error { return errors.Join(sub.Close(), pub.Close()) }
	if err := pub.Ping(ctx).Err(); err != nil {
		_ = closeClients()
		return nil, nil, fmt.Errorf("redis bus: ping %s: %w", opt.Addr, err)
	}
	b, err := New(pub, append([]Option{WithSubscribeClient(sub)}, opts...)...)
	if err != nil {
		_ = closeClients()
		return nil, nil, err
	}
	return b, func() error { return errors.Join(b.Close(), closeClients()) }, nil
}
