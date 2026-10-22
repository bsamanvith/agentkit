//go:build !noagent

package main

import (
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/embed"
)

// The embedding APIs a config's memory.index.embedder.type names.
const (
	embedderOpenAI = "openai"
	embedderVoyage = "voyage"
)

// indexConfig is the memory.IndexConfig of an index section: its scope,
// limits, modes and embedder, whose key comes from getenv.
func indexConfig(label string, c memoryIndexConfig, getenv func(string) string) (memory.IndexConfig, error) {
	cfg := memory.IndexConfig{Scope: memory.Scope(c.Scope), MaxItems: c.MaxItems, MaxTextBytes: c.MaxTextBytes}
	for _, m := range c.Modes {
		cfg.Modes = append(cfg.Modes, memory.Mode(m))
	}
	if c.Embedder != nil {
		e, err := newEmbedder(c.Embedder, getenv)
		if err != nil {
			return cfg, fmt.Errorf("%s.embedder: %w", label, err)
		}
		cfg.Embedder = e
	}
	cfg, err := cfg.Resolve()
	if err != nil {
		return cfg, fmt.Errorf("%s: %w", label, err)
	}
	return cfg, nil
}

// newEmbedder builds the client of an embedder section. It makes no request.
func newEmbedder(c *memoryEmbedderConfig, getenv func(string) string) (memory.Embedder, error) {
	keyEnv := map[string]string{embedderOpenAI: "OPENAI_API_KEY", embedderVoyage: "VOYAGE_API_KEY"}[c.Type]
	if c.APIKeyEnv != nil {
		keyEnv = *c.APIKeyEnv
	}
	cfg := embed.Config{Model: c.Model, Dimensions: c.Dimensions, BaseURL: c.BaseURL, BatchSize: c.BatchSize}
	if keyEnv != "" {
		cfg.Key = getenv(keyEnv)
	}
	switch c.Type {
	case embedderOpenAI:
		return embed.NewOpenAI(cfg)
	case embedderVoyage:
		if cfg.Key == "" {
			return nil, fmt.Errorf("type %q needs a key: set $%s", embedderVoyage, keyEnv)
		}
		return embed.NewVoyage(cfg)
	default:
		return nil, fmt.Errorf("type %q: want %q or %q", c.Type, embedderOpenAI, embedderVoyage)
	}
}
