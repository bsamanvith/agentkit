---
id: T058
title: "pkg/memory: context store, memory toolset, opt-in config"
epic: E12
status: review
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

## References

- The Claude **memory tool** docs (commands, `/memories` paths, security notes; find the current page with the `claude-api` skill)
- Anthropic's context management docs (memory tool plus context editing) for how Claude is trained to use memory

## Acceptance criteria

- [x] **Commands:** each behaves as documented, including the error cases: `str_replace` with zero or two matches, `insert` past the end, `rename` onto an existing file, `view` of a missing path (table test against the reference store). (`TestToolCommands` through the tool, with the memory tool's documented texts; `memorytest` `Commands` and `Directories` on every backend)
- [x] **Isolation:** tenant A and B, and users U1 and U2 of one tenant (with `scope: user`), never see each other's memories (suite); no tenant in `ctx` gives `ErrNoTenant`. (`memorytest` `Isolation/tenant` and `Isolation/user`; through the API with two users' keys, `TestMemoryUserScope`)
- [x] **Path rules:** `..`, absolute paths outside `/memories` and over-long paths are refused. `FuzzMemoryPath` never escapes. (`TestCleanPath`, `FuzzMemoryPath`, `memorytest` `PathRules` for every command)
- [x] **Limits:** writes past the per-file and per-scope limits fail cleanly and change nothing. (`memorytest` `Limits`; every failed command in every test is checked to change nothing)
- [x] **Opt-in:** an agent without `memory` config offers no memory tool (fake model records the tools). (`TestAgentWithoutMemoryHasNoMemoryTool`)
- [x] **End to end** (fake model): run 1 saves a preference; run 2 in a **new conversation** views it and uses it. (`TestMemoryAcrossConversations`, through serve's agent component on the SQLite backend, and again after a restart on the same file)
- [ ] **Live (`-tags live`):** real Claude saves and later recalls a stated preference across two conversations. (`TestLiveRecallAcrossConversations` in `pkg/memory`, as a plain definition and as the native type. Written, not run: no Anthropic credentials in this session. Run `make test-live`.)
- [x] **Mutation list** recorded (below).

## Notes

### What was built

- `pkg/memory`: `Store` (View, Create, StrReplace, Insert, Delete, Rename), `Config` (`Scope`, `Limits`), `OwnerOf` (tenant, and user with `ScopeUser`, from `ctx`), `CleanPath`, the errors (`ErrNoTenant` is `tenancy.ErrNoTenant`; `*MatchError`, `*LineError`, `*LimitError` carry details), and `NewTool`: one tool, `memory`, with the Claude memory tool's input and result texts.
- `pkg/memory/internal/fileops`: the commands' rules, once, over a backend's `Files` in one transaction. Every function checks everything before it writes, so a failed command changes nothing even without a transaction.
- `pkg/memory/inmem`: the reference store (one lock). `pkg/memory/sqlite`: one table keyed by tenant, user and path; each command one IMMEDIATE transaction on a single writer connection, views on read-only connections; schema version in `PRAGMA user_version`, a newer one refused.
- `pkg/memory/memorytest`: `RunStore` (commands, directories, isolation, path rules, limits, cancel, a seeded simulation, a concurrent run), `Step` (apply, check the invariants, check the outcome against a model of the files before), `CheckState`, `Snapshot`, `Simulate` (also a fuzz target in `inmem`).
- `pkg/llm/anthropic`: `Options.NativeMemory` sends a tool named `memory` as the `memory_20250818` type.
- `cmd/agentkit`: an agent's `memory` section (`store.backend` `memory` or `sqlite`, `store.path`, `store.scope`, the three limits, `native_tool`), the backend table `memoryBackends` (`memory_sqlite.go` behind `!nosqlite`), the tool joined ahead of the agent's toolsets, stores closed on shutdown.

### Decisions

- **Claude API check (claude-api skill, the memory tool page):** the tool is `{"type": "memory_20250818", "name": "memory"}`, no beta header, on all Claude 4+ models; commands `view` (`view_range`), `create` (`file_text`), `str_replace` (`old_str`, `new_str` optional), `insert` (`insert_line`, `insert_text`), `delete`, `rename` (`old_path`, `new_path`); the page gives the result and error texts, which the tool returns. With the native type the API adds the memory tool's own system-prompt instructions ("ALWAYS VIEW YOUR MEMORY DIRECTORY...").
- **Native type: done, behind an option, off by default.** The adapter maps only the request side: the calls come back as ordinary `tool_use` blocks named `memory` with the same input, which the harness runs on our tool, so nothing else changes. Off by default because the API then adds its instructions to every request (the agent's system prompt may not want "view your memory before anything else"), and because a config `memory` tool from elsewhere would be sent as the native type. Config: `memory.native_tool`. Other providers get the same tool as a plain definition.
- **`create` overwrites** an existing file. The page's reference returns an error, but says Claude's own tool description says "creates or overwrites", so Claude expects it to; the page calls overwriting a valid choice.
- **Directories are implicit**: a directory exists while a file is under it, so deleting the last file removes it. `/memories` always exists (the first `view` of an empty store is an empty listing, as the page asks) and cannot be deleted or renamed. A file where a directory is needed is `ErrNotDir`; a file command on a directory `ErrIsDir`.
- **Paths are refused, never resolved:** anything with a `..` segment, a name starting with `.` (hidden files; this also rules out every traversal), a backslash, a control character, an escaped traversal (`%2e%2e`, `%2f`), a name over 128 bytes or a path over 256 bytes (as given and once cleaned). Repeated slashes, trailing slashes and `.` segments are normalized.
- **Limits** default to 16 KiB per file (the page says Claude's tool truncates a view past 16,000 characters), 1 MiB per scope and 200 files. A write that shrinks a file goes through even over the scope limit (so a lowered limit never locks a scope). A view longer than 16,000 characters stops at a whole line and says which `view_range` reads on.
- **Insert is by whole lines:** `insert_line` is in `[0, lines]`, where lines count a last line with no newline; the inserted text gets a newline if it has none, and so does a last line it goes after. An empty file has 0 lines.
- **Errors reach the model** as tool failures whose text is the memory tool's (`tool "memory": tool failed: <text>`); bad input (unknown command, a missing field, a bad `view_range`) is `ErrInvalidInput`. The tool's output is a JSON string (tool outputs are JSON everywhere), so the model sees the text quoted.
- **Tool metadata:** kind write, risk low, tag `memory`: it writes only the agent's own notes.
- **SQLite is a file of its own** (`store.path`), not the server store's database: the store's migrations stay untouched, and the memory file can live elsewhere. Agents naming the same file share memories; agents on `memory` each have their own.
- **User scope needs a user in the run's context.** The agent API dropped it twice: `tenantContext` re-asserted the tenant with `tenancy.With`, which starts a new scope (T027 noted this), and `execContext` copied only the tenant. Both now keep the user (and `tenantContext` the request id) when the tenant is the same; `TestMemoryUserScope` fails if either is undone. A run resumed by recovery or a command from another replica still has no user, so a user-scoped memory call there gets `ErrNoUser` as a tool error.
- **Not on a runner component yet:** the tenant does not cross `pkg/rpc` (ENGINEERING 1.5), so every memory call there would be refused; serve refuses an agent with `memory` on the runner component and in an agent API that sends runs to one, like `runtime_toolsets`.
- `memory` is opt-in per agent: with no section the agent gets no tool and nothing is opened. A toolset of the agent with its own tool named `memory` fails startup.

### Mutation check

Each bug was planted alone and `go test` run on the affected packages (`./pkg/memory/...`, `./pkg/llm/anthropic/`, or `-run Memory ./cmd/agentkit/`), then restored.

| Planted bug | Failing tests |
|---|---|
| `CleanPath` allows `..` and hidden names | `TestCleanPath`, `FuzzMemoryPath` seeds, `PathRules` |
| `CleanPath` skips the percent-escape check | `TestCleanPath`, `PathRules` |
| `CleanPath` drops the length check on the path as given | `TestCleanPath` |
| `OwnerOf` ignores the user under `ScopeUser` | `Isolation/user` |
| No tenant reads as the empty tenant | `Isolation/tenant`, `Isolation/user`, `TestToolLimitAndScopeErrors` |
| `str_replace` replaces the first of several matches | `Commands`, `Limits`, `Simulation`, `FuzzSimulation` seeds, `TestToolCommands` |
| `insert` accepts the line past the end | `Commands`, `Simulation`, `FuzzSimulation` seeds |
| No scope-bytes limit | `Limits` |
| No file-count limit | `Limits` |
| `create` writes before checking limits | `Limits` (the failed write changed the files) |
| `rename` overwrites the destination | `Commands`, `Directories`, `Simulation`, `TestToolCommands` |
| `delete` of `/memories` allowed | `Commands`, `Simulation`, `FuzzSimulation` seeds |
| Listings go three levels deep | `Directories`, `Simulation`, `Concurrent` (via `CheckState`) |
| `rename` skips the moved paths' length check | `PathRules` |
| SQLite `Under` range too wide (`dir+"\xff"`) | `Directories`, `Simulation` |
| SQLite `Get` ignores the user | `Isolation/user` |
| In-memory store without its lock | `Concurrent` and others (concurrent map writes; the race detector too) |
| In-memory store keys scopes by tenant only | `Isolation/user` |
| Tool: wrong lines in the two-match text | `TestToolCommands` |
| Tool: `view_range` numbering off by one | `TestToolCommands`, `Example` |
| Tool: wrong line count in the insert error | `TestToolCommands` |
| Adapter ignores `NativeMemory` | `TestNativeMemory` |
| Adapter does not merge the model's `NativeMemory` | `TestNativeMemory/model_default` |
| Agent API `tenantContext` drops the user | `TestMemoryUserScope` |
| Agent API `execContext` drops the user | `TestMemoryUserScope` |
| Every agent gets memory | `TestAgentWithoutMemoryHasNoMemoryTool` |
| `native_tool` not passed to the model | `TestAgentWithoutMemoryHasNoMemoryTool` |
| No check for a toolset's own `memory` tool | `TestMemoryConfigErrors` |

One mutant survives and is equivalent: committing the SQLite transaction of a failed command changes nothing, because `fileops` checks everything before its first write.

### Extra tests beyond the criteria

`Directories`, `Canceled`, `Simulation` and `Concurrent` in the suite (six workers per tenant, two tenants: each owns a line of a shared file it rewrites with `str_replace`, and files it creates, appends to and renames; no edit may be lost); `TestToolViewOfALongFile`; `TestFilesSurviveReopening` and `TestOpenRefusesANewerSchema` for SQLite; `TestMemoryIsRefusedOnARunner`; `TestMemoryConfigErrors`.

### Sizes

`agentkit` binary +159,744 bytes, the other variants +127 to +135 KB; no new dependency (docs/SIZES.md has the rows).

### For T059 (search store) and later

- Scope with `memory.OwnerOf(ctx, scope)`: the same tenant and user rules, `ErrNoTenant` and `ErrNoUser`.
- `memorytest.Ctx(t, tenant, user)` builds a scoped context for tests.
- Add an agent's `memory.index` next to `memory.store` in `cmd/agentkit/memory.go` (`memoryConfig`); the backend table pattern is `memoryBackends`.
- `auto_recall` (the harness hook) is T059's.

### Follow-ups (not in this task)

- Carry the tenant and user across `pkg/rpc` so agents on a runner component can have memory (and runtime toolsets).
- Keep the user on runs resumed by recovery and by commands from another replica (the store's run records have no user today).
- `GET /v1/agents` does not list the memory tool (it lists toolsets); the console's "memories" panel (E12) needs an API to view and delete a scope's files.
- With `native_tool`, the result could go to Claude as plain text instead of a JSON string.
- T088 (Postgres): a `pkg/memory/postgres` backend through `fileops`, if memories should live in the server's database.
