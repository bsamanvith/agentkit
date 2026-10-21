package main

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/inmem"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// memoryConfig is an agent's "memory" section: its context store (pkg/memory),
// which the model reads and writes through one tool, "memory". With no
// section the agent has no memory tool and nothing is stored.
//
//	"memory": {"store": {"backend": "sqlite", "path": "memories.db", "scope": "user"}}
type memoryConfig struct {
	Store memoryStoreConfig `json:"store"`
	// NativeTool sends the tool to Claude as its native memory tool type
	// (anthropic.Options.NativeMemory) instead of as a plain definition.
	NativeTool bool `json:"native_tool"`
}

// memoryStoreConfig chooses the backend and its scope and limits.
type memoryStoreConfig struct {
	// Backend is one of memoryBackends: "memory" (in this process, lost on
	// restart) or "sqlite" (a file; path).
	Backend string `json:"backend"`
	// Path is the SQLite file, absolute or relative to the working
	// directory. Agents naming the same file share their memories.
	Path string `json:"path"`
	// Scope is "tenant" (the default: everyone of a tenant shares the
	// files) or "user" (each user of a tenant has their own; a run with no
	// user gets an error from the tool).
	Scope string `json:"scope"`
	// The limits of each scope; 0 for pkg/memory's defaults.
	MaxFileBytes  int64 `json:"max_file_bytes"`
	MaxScopeBytes int64 `json:"max_scope_bytes"`
	MaxFiles      int   `json:"max_files"`
}

// memoryBackend opens a store; closing it is the returned func (nil when
// there is nothing to close).
type memoryBackend func(ctx context.Context, c memoryStoreConfig, cfg memory.Config) (memory.Store, func() error, error)

// memoryBackends are the context store backends this binary can open, by the
// name a config's memory.store.backend gives. SQLite is in its own file,
// which the nosqlite tag leaves out (memory_sqlite.go).
func memoryBackends() map[string]memoryBackend {
	return map[string]memoryBackend{
		"memory": func(_ context.Context, c memoryStoreConfig, cfg memory.Config) (memory.Store, func() error, error) {
			if c.Path != "" {
				return nil, nil, fmt.Errorf("backend %q keeps nothing in a file: leave out path", "memory")
			}
			s, err := inmem.New(cfg)
			return s, nil, err
		},
		"sqlite": openSQLiteMemory,
	}
}

// openMemory builds an agent's memory tool from its config: nil, with no
// "memory" section. closeFn closes its store.
func openMemory(ctx context.Context, a agentDef, legacy bool) (tools tool.Registry, closeFn func() error, err error) {
	if a.Memory == nil {
		return nil, nil, nil
	}
	label := a.label(legacy) + ".memory.store"
	c := a.Memory.Store
	open, ok := memoryBackends()[c.Backend]
	if !ok {
		names := slices.Sorted(maps.Keys(memoryBackends()))
		return nil, nil, fmt.Errorf("%s.backend %q: want %q", label, c.Backend, strings.Join(names, `" or "`))
	}
	cfg, err := memory.Config{
		Scope:  memory.Scope(c.Scope),
		Limits: memory.Limits{MaxFileBytes: c.MaxFileBytes, MaxScopeBytes: c.MaxScopeBytes, MaxFiles: c.MaxFiles},
	}.Resolve()
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", label, err)
	}
	s, closeFn, err := open(ctx, c, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", label, err)
	}
	mt, err := memory.NewTool(s)
	if err == nil {
		tools, err = tool.NewRegistry([]tool.Tool{mt})
	}
	if err != nil {
		if closeFn != nil {
			_ = closeFn()
		}
		return nil, nil, fmt.Errorf("%s: %w", label, err)
	}
	return tools, closeFn, nil
}
