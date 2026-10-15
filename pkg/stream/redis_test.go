//go:build integration

package stream_test

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/bus"
	redisbus "github.com/bsamanvith/agentkit/pkg/bus/redis"
)

func init() { redisBackend = newRedisBackend }

func newRedisBackend(t *testing.T, trim func(bus.Topic) bus.Trim) backend {
	url := testkit.IntegrationEnv(t, "AGENTKIT_REDIS_URL")
	opt, err := goredis.ParseURL(url)
	if err != nil {
		t.Fatalf("AGENTKIT_REDIS_URL: %v", err)
	}
	opt.MaintNotificationsConfig = &maintnotifications.Config{Mode: maintnotifications.ModeDisabled}
	c := goredis.NewClient(opt)
	prefix := "agentkit-test:" + rand.Text() + ":"
	b, err := redisbus.New(c, redisbus.WithPrefix(prefix), redisbus.WithTrim(trim),
		redisbus.WithBackoff(10*time.Millisecond, 200*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = b.Close()
		ctx := context.Background()
		if keys, err := c.Keys(ctx, prefix+"*").Result(); err == nil && len(keys) > 0 {
			_ = c.Del(ctx, keys...).Err()
		}
		_ = c.Close()
	})
	return backend{
		name: "redis",
		bus:  b,
		// As when Redis restarts without its data. The bus still remembers
		// what it published, which only helps.
		lose: func(t *testing.T, topic bus.Topic) {
			if err := c.Del(t.Context(), b.Key(topic)).Err(); err != nil {
				t.Fatal(err)
			}
		},
	}
}
