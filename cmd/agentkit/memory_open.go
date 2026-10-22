//go:build !noagent

package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/inmem"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// Opening an agent's memory: only the agent component gives agents memory
// (the runner refuses it), so the noagent tag leaves this file and the
// stores it opens out. The config it reads is in memory.go.

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

// memoryIndexBackend opens a search store, as memoryBackend a store.
type memoryIndexBackend func(ctx context.Context, c memoryIndexConfig, cfg memory.IndexConfig) (memory.Index, func() error, error)

// memoryIndexBackends are the search store backends this binary can open, by
// the name of memory.index.backend (SQLite in memory_sqlite.go).
func memoryIndexBackends() map[string]memoryIndexBackend {
	return map[string]memoryIndexBackend{
		"memory": func(_ context.Context, c memoryIndexConfig, cfg memory.IndexConfig) (memory.Index, func() error, error) {
			if c.Path != "" {
				return nil, nil, fmt.Errorf("backend %q keeps nothing in a file: leave out path", "memory")
			}
			x, err := inmem.NewIndex(cfg)
			return x, nil, err
		},
		"sqlite": openSQLiteIndex,
	}
}

// openMemory builds an agent's memory from its config: nil, with no
// "memory" section. Saved items are dated by clk.
func openMemory(ctx context.Context, a agentDef, legacy bool, clk clock.Clock) (_ *agentMemory, err error) {
	if a.Memory == nil {
		return nil, nil
	}
	label := a.label(legacy) + ".memory"
	if a.Memory.Store == nil && a.Memory.Index == nil {
		return nil, fmt.Errorf("%s: give a store, an index or both", label)
	}
	if a.Memory.NativeTool && a.Memory.Store == nil {
		return nil, fmt.Errorf("%s.native_tool: the native memory tool needs a store", label)
	}
	m := &agentMemory{native: a.Memory.NativeTool}
	defer func() {
		if err != nil {
			_ = m.close()
		}
	}()
	var tools []tool.Tool
	if c := a.Memory.Store; c != nil {
		t, closeFn, err := openMemoryStore(ctx, label+".store", *c)
		if err != nil {
			return nil, err
		}
		if closeFn != nil {
			m.closers = append(m.closers, closeFn)
		}
		tools = append(tools, t)
	}
	if c := a.Memory.Index; c != nil {
		ts, prepare, closeFn, err := openMemoryIndex(ctx, label+".index", *c, clk)
		if err != nil {
			return nil, err
		}
		if closeFn != nil {
			m.closers = append(m.closers, closeFn)
		}
		tools = append(tools, ts...)
		m.prepare = prepare
	}
	reg, err := tool.NewRegistry(tools)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	m.tools = reg
	for _, t := range tools {
		m.names = append(m.names, t.Definition().Name)
	}
	return m, nil
}

// openMemoryStore opens the context store and its tool.
func openMemoryStore(ctx context.Context, label string, c memoryStoreConfig) (tool.Tool, func() error, error) {
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
	if err != nil {
		return nil, nil, errors.Join(fmt.Errorf("%s: %w", label, err), closeOrNil(closeFn))
	}
	return mt, closeFn, nil
}

// openMemoryIndex opens the search store, its tools and, with auto_recall,
// the prepare hook.
func openMemoryIndex(ctx context.Context, label string, c memoryIndexConfig, clk clock.Clock) ([]tool.Tool, harness.PrepareFunc, func() error, error) {
	open, ok := memoryIndexBackends()[c.Backend]
	if !ok {
		names := slices.Sorted(maps.Keys(memoryIndexBackends()))
		return nil, nil, nil, fmt.Errorf("%s.backend %q: want %q", label, c.Backend, strings.Join(names, `" or "`))
	}
	if c.K < 0 || c.K > memory.MaxK {
		return nil, nil, nil, fmt.Errorf("%s.k %d: want 1 to %d (0 for %d)", label, c.K, memory.MaxK, memory.DefaultK)
	}
	cfg, err := memory.IndexConfig{Scope: memory.Scope(c.Scope), MaxItems: c.MaxItems, MaxTextBytes: c.MaxTextBytes}.Resolve()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", label, err)
	}
	x, closeFn, err := open(ctx, c, cfg)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", label, err)
	}
	fail := func(err error) ([]tool.Tool, harness.PrepareFunc, func() error, error) {
		return nil, nil, nil, errors.Join(fmt.Errorf("%s: %w", label, err), closeOrNil(closeFn))
	}
	opts := []memory.RecallOption{memory.WithRecallClock(clk)}
	if c.K > 0 {
		opts = append(opts, memory.WithRecallK(c.K))
	}
	tools, err := memory.NewRecallTools(x, opts...)
	if err != nil {
		return fail(err)
	}
	var prepare harness.PrepareFunc
	if c.AutoRecall {
		if prepare, err = memory.AutoRecall(x, opts...); err != nil {
			return fail(err)
		}
	}
	return tools, prepare, closeFn, nil
}

func closeOrNil(f func() error) error {
	if f == nil {
		return nil
	}
	return f()
}
