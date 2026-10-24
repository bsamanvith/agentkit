package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
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
	// restart), "sqlite" (a file; path; local images) or "postgres" (the
	// Postgres store's database, which $AGENTKIT_STORE names: replicas;
	// dev and cloud images).
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
	// LoadBytes is how much memory each new message starts with as text
	// (T129): every file's, while the memory is at most this; past it, the
	// file list, and the agent greps and views what it needs. 0 for
	// memory.DefaultLoadBytes (4 KB); -1 loads nothing.
	LoadBytes int64 `json:"load_bytes"`
}

// loads reports whether new messages start with the memory.
func (c *memoryStoreConfig) loads() bool { return c.LoadBytes != -1 }

// checkLoadBytes refuses a load_bytes that means nothing.
func checkLoadBytes(label string, c *memoryStoreConfig) error {
	if c != nil && c.LoadBytes < -1 {
		return fmt.Errorf("%s.store.load_bytes %d: want the bytes to load (0 for 4 KB), or -1 for none", label, c.LoadBytes)
	}
	return nil
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
	// Embedder makes the vectors of semantic and hybrid search; none for
	// keyword search only. Changing its model or dimensions needs
	// `agentkit memory reembed` before semantic search works again.
	Embedder *memoryEmbedderConfig `json:"embedder"`
	// Modes are the search modes ("keyword", "semantic", "hybrid"), the
	// default first: by default ["keyword"] without an embedder and
	// ["hybrid", "keyword", "semantic"] with one.
	Modes []string `json:"modes"`
}

// memoryEmbedderConfig chooses the embedding API (pkg/memory/embed).
//
//	"embedder": {"type": "voyage", "model": "voyage-3.5-lite"}
//	"embedder": {"type": "openai", "model": "nomic-embed-text", "dimensions": 768,
//	             "base_url": "http://127.0.0.1:11434/v1", "api_key_env": ""}
type memoryEmbedderConfig struct {
	// Type is "openai" (OpenAI's API and every server that copies it) or
	// "voyage" (Voyage AI).
	Type  string `json:"type"`
	Model string `json:"model"`
	// Dimensions of the vectors: 0 for a known model's default (embed.Config).
	Dimensions int `json:"dimensions"`
	// BaseURL is the API's, ending before /embeddings; empty for the
	// provider's.
	BaseURL string `json:"base_url"`
	// APIKeyEnv names the environment variable with the key: by default
	// OPENAI_API_KEY or VOYAGE_API_KEY. With "openai", an unset variable
	// sends no key (a local server).
	APIKeyEnv *string `json:"api_key_env"`
	// BatchSize caps the texts of one request (0: embed.DefaultBatchSize).
	BatchSize int `json:"batch_size"`
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
	// prepare are its prepare hooks, in order: loading the memory into a new
	// message (loadMemory), and auto recall; each only when on.
	prepare []harness.PrepareFunc
	// api is the context store as the agent API shows it to its users
	// (memory_api.go); nil without a store, and on a runner.
	api     agentapi.Memory
	closers []func() error
}

func (m *agentMemory) close() error { return closeAll(m.closers) }

// The names of the memory tools (pkg/memory's ToolName and its recall
// tools), here so that the runner, which links no pkg/memory, knows which
// tool calls go to the agent API (TestMemoryToolNames keeps them equal).
const memoryToolName = "memory"

var recallToolNames = []string{"recall_get", "recall_save", "recall_search"}

// memoryToolNames are the tools an agent's memory section gives it.
func memoryToolNames(m *memoryConfig) []string {
	var names []string
	if m.Store != nil {
		names = append(names, memoryToolName)
	}
	if m.Index != nil {
		names = append(names, recallToolNames...)
	}
	return names
}

// checkRemoteMemory refuses what memory cannot do for an agent on a runner:
// auto recall adds to each message before the model sees it, on the runner,
// which has no index.
func checkRemoteMemory(label string, m *memoryConfig) error {
	if err := checkLoadBytes(label+".memory", m.Store); err != nil {
		return err
	}
	if m.Index != nil && m.Index.AutoRecall {
		return fmt.Errorf("%s.memory.index.auto_recall: an agent that runs on a runner cannot recall automatically yet; leave it off (the recall tools work)", label)
	}
	return nil
}

// remoteMemory is a runner's agent's memory: its memory tools at the agent
// API (memoryService). Only the names of memory tools are sent there; any
// other is tool.ErrNotFound here, so the agent's own tools never wait on
// the agent API.
func remoteMemory(m *memoryConfig, reg tool.Registry) *agentMemory {
	names := memoryToolNames(m)
	tools := namedTools{names: names, reg: reg}
	mem := &agentMemory{tools: tools, names: names, native: m.NativeTool}
	if m.Store != nil && m.Store.loads() {
		mem.prepare = append(mem.prepare, loadMemory(tools))
	}
	return mem
}

// loadMemory is the prepare hook that starts each new message with the
// agent's memory: what the memory tool's load command gives (pkg/memory
// builds it, T129), through reg, so that an agent on a runner loads its
// memory from the agent API as it uses it.
func loadMemory(reg tool.Registry) harness.PrepareFunc {
	input := json.RawMessage(`{"command":"load"}`)
	return func(ctx context.Context, _ llm.Message) ([]llm.Block, error) {
		out, err := reg.Invoke(ctx, memoryToolName, input)
		if err != nil {
			return nil, fmt.Errorf("memory: loading it into the message: %w", err)
		}
		var text string
		if err := json.Unmarshal(out, &text); err != nil {
			return nil, fmt.Errorf("memory: loading it into the message: the tool answered %.100s", out)
		}
		if text == "" {
			return nil, nil
		}
		return []llm.Block{llm.Text{Text: text}}, nil
	}
}

// namedTools is reg limited to the tools of names.
type namedTools struct {
	names []string
	reg   tool.Registry
}

func (n namedTools) List(ctx context.Context) ([]tool.Definition, error) {
	defs, err := n.reg.List(ctx)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(defs, func(d tool.Definition) bool { return !slices.Contains(n.names, d.Name) }), nil
}

func (n namedTools) Invoke(ctx context.Context, name string, input json.RawMessage) (json.RawMessage, error) {
	if !slices.Contains(n.names, name) {
		return nil, fmt.Errorf("%w: %q", tool.ErrNotFound, name)
	}
	return n.reg.Invoke(ctx, name, input)
}
