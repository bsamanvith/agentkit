//go:build noredis

package busopen

import (
	"context"
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/bus"
)

func redisBackend() Backend {
	err := fmt.Errorf("%w: this agentkit is built without Redis (-tags noredis); use an image that has it", ErrNotBuilt)
	return Backend{
		Name: "Redis", Prefixes: []string{"redis://", "rediss://"},
		Open: func(context.Context, string, Config) (bus.Bus, func() error, error) { return nil, nil, err },
	}
}
