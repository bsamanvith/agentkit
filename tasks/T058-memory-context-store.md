---
id: T058
title: "pkg/memory: context store, memory toolset, opt-in config"
epic: E12
status: todo
depends_on: [T027, T036]
---

# T058: memory context store

## Context

The agent's own notes: preferences, project facts, progress across conversations. It reads and writes them like files through tools, shaped like the Claude memory tool's commands. Decisions: [E12](E12-skills-memory.md).

## Scope

- **`memory.Store`** (tenant from `ctx`, optional user scope): `View(path)` (a file's content, or a directory listing), `Create(path, text)`, `StrReplace(path, old, new)` (exactly one match, else an error), `Insert(path, line, text)`, `Delete(path)`, `Rename(from, to)`.
  - Paths are under `/memories`, normalized, with no `..`.
  - Size limits: per file, per scope, and file count.
- **Backends:** an in-memory reference store; a SQLite store once T029 lands (or a directory backend for single-process use, with the same confinement rules as skills).
- **`memorytest.RunStore`**: a conformance suite with invariants (tenant and user isolation, limits, path rules) and a seeded simulation of concurrent edits.
- **Memory toolset:**
  - one tool, `memory`, with a `command` field, matching the Claude memory tool's input shape;
  - check the current Claude API docs with the `claude-api` skill;
  - if the Anthropic adapter can send the native memory tool type and map its calls onto this store, do it behind an option, and record the decision.
- **Opt-in config:** `agent.memory.store: {backend, scope}`. With no config, there is no memory tool and nothing is stored.

## Acceptance criteria

- [ ] **Commands:** each behaves as documented, including the error cases: `str_replace` with zero or two matches, `insert` past the end, `rename` onto an existing file, `view` of a missing path (table test against the reference store).
- [ ] **Isolation:** tenant A and B, and users U1 and U2 of one tenant (with `scope: user`), never see each other's memories (suite); no tenant in `ctx` gives `ErrNoTenant`.
- [ ] **Path rules:** `..`, absolute paths outside `/memories` and over-long paths are refused. `FuzzMemoryPath` never escapes.
- [ ] **Limits:** writes past the per-file and per-scope limits fail cleanly and change nothing.
- [ ] **Opt-in:** an agent without `memory` config offers no memory tool (fake model records the tools).
- [ ] **End to end** (fake model): run 1 saves a preference; run 2 in a **new conversation** views it and uses it.
- [ ] **Live (`-tags live`):** real Claude saves and later recalls a stated preference across two conversations.
- [ ] **Mutation list** recorded.
