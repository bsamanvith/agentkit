package main

import (
	"github.com/bsamanvith/agentkit/pkg/harness"
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

// agentMemory is what an agent's memory section gives it (openMemory, in
// memory_open.go, which a build without the agent component leaves out).
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
