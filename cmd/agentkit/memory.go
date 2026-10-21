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

// memoryConfig is an agent's "memory" section: its context store (pkg/memory),
// which the model reads and writes through one tool, "memory", and its
// search store, which it reaches through recall_search, recall_get and
// recall_save (and, with auto_recall, in each new message). Either or both;
// with no section the agent has no memory tool and nothing is stored.
//
//	"memory": {"store": {"backend": "sqlite", "path": "memories.db", "scope": "user"},
//	           "index": {"backend": "sqlite", "path": "memories.db", "scope": "user", "auto_recall": true, "k": 5}}
type memoryConfig struct {
	Store *memoryStoreConfig `json:"store"`
	Index *memoryIndexConfig `json:"index"`
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

// memoryIndexConfig chooses the search store's backend, its scope and
// limits, and auto recall.
type memoryIndexConfig struct {
	// Backend is one of memoryIndexBackends: "memory" (in this process,
	// lost on restart) or "sqlite" (a file, which may be the store's).
	Backend string `json:"backend"`
	Path    string `json:"path"`
	// Scope is "tenant" (the default) or "user", as the store's.
	Scope string `json:"scope"`
	// The limits of each scope; 0 for pkg/memory's defaults.
	MaxItems     int `json:"max_items"`
	MaxTextBytes int `json:"max_text_bytes"`
	// AutoRecall adds the top K results for each new user message to that
	// message, marked as recalled data (memory.AutoRecall). Off by default:
	// then nothing is searched but what the model asks for.
	AutoRecall bool `json:"auto_recall"`
	// K is how many results auto recall adds, and recall_search returns
	// when the model gives no k (0: memory.DefaultK).
	K int `json:"k"`
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

// agentMemory is what an agent's memory section gives it.
type agentMemory struct {
	// tools are the memory tool and the recall tools, those configured.
	tools tool.Registry
	// names are their names, which none of the agent's toolsets may have.
	names []string
	// native sends the memory tool as Claude's native type.
	native bool
	// prepare is auto recall; nil when off.
	prepare harness.PrepareFunc
	closers []func() error
}

func (m *agentMemory) close() error { return closeAll(m.closers) }

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
