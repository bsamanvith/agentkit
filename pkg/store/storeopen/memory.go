package storeopen

import (
	"context"

	"github.com/bsamanvith/agentkit/pkg/store/memory"
)

// memoryBackend keeps the store in memory, lost when the process ends.
func memoryBackend() Backend {
	return Backend{
		Name: "memory", Prefixes: []string{"memory"},
		Open: func(_ context.Context, _ string, cfg Config) (Opened, error) {
			s := memory.New(memory.WithClock(cfg.Clock))
			return Opened{Store: s, Admin: memory.Admin(s), Close: func() error { return nil }}, nil
		},
	}
}
