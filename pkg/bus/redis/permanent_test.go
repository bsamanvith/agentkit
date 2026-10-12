package redis_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	goredis "github.com/redis/go-redis/v9"

	redisbus "github.com/bsamanvith/agentkit/pkg/bus/redis"
)

// reply is an error reply from Redis, as go-redis reports one.
type reply string

func (r reply) Error() string { return string(r) }
func (reply) RedisError()     {}

// Which errors end a subscription and which it retries.
func TestPermanent(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{io.EOF, false},
		{context.DeadlineExceeded, false},
		{fmt.Errorf("dial tcp: %w", errors.New("connection refused")), false},
		{reply("LOADING Redis is loading the dataset in memory"), false},
		{reply("READONLY You can't write against a read only replica."), false},
		{reply("MASTERDOWN Link with MASTER is down"), false},
		{reply("ERR max number of clients reached"), false},
		{reply("NOPERM User x has no permissions to run the 'xread' command"), true},
		{reply("WRONGTYPE Operation against a key holding the wrong kind of value"), true},
		{goredis.ErrClosed, true},
	} {
		if got := redisbus.Permanent(c.err); got != c.want {
			t.Errorf("permanent(%q) = %v, want %v", c.err, got, c.want)
		}
	}
}
