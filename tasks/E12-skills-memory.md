---
id: E12
title: "Skills and memory for the harness"
status: todo
---

# E12: skills and memory

Owner request (2026-10-12): the agent should have **skills** (packaged know-how it loads when relevant) and **memory**:
- **context stores**: notes the agent keeps and reads back;
- **memory search stores**: opt-in, with save, search and retrieve, where search is by phrase (keyword) or by embeddings, depending on the backend.

Everything here plugs in from outside the harness (PLAN: "the harness holds no store"). Skills and memory reach the model as **tools** and as **context blocks**. `pkg/harness` gains at most one small hook.

## Decisions (made in planning; change them in the task that finds them wrong)

| Question | Decision |
|---|---|
| Skill format | **The Agent Skills format** used by Claude Code and the Claude API: a folder with `SKILL.md` (YAML front matter `name` and `description`, then Markdown instructions) and optional reference files. Existing skills (including this repo's `.agents/skills/`) work unchanged. |
| How a skill reaches the model | **Progressive disclosure.** The system prompt lists only each skill's name and description (a few tokens each). The model calls `skill_load(name)` to read `SKILL.md`, and `skill_read(name, path)` for a file the skill references. Paths are confined to the skill's folder (no `..`, no symlinks out). Large skill sets don't fill the context. |
| Skill scripts | **Not executed** in E12. Running a skill's scripts needs a sandbox (a later epic, with code execution). A skill that needs scripts is still listed, and its instructions say what it can't do. |
| Where skills come from | Folders named in config (`agent.skills: ["deploy/skills", ...]`) and an `fs.FS` for embedded skills; per-tenant skills come with E07. Loaded once at startup, validated (name rule, description required, size limits), with bad skills failing startup unless marked optional (like toolsets). |
| Memory: two kinds | **Context store** (`memory.Store`): named notes the agent creates, reads, edits and deletes, like files. Good for preferences, project facts and progress notes. **Search store** (`memory.Index`): many items, searched by query, for recall over past conversations, documents and facts. Different access patterns, so they are two small interfaces, not one big one. |
| Context store's tool shape | Mirrors the **Claude memory tool's commands** (view, create, str_replace, insert, delete, rename over `/memories/...` paths), because Claude is trained on that shape. T058 checks the current Claude API docs (the `claude-api` skill); if the Anthropic adapter can map it onto the native memory tool type, it does. Other providers get the same commands as a plain toolset. |
| Search store API | `Put(ctx, Item{ID, Text, Metadata, Time})`, `Search(ctx, Query{Text, K, Filter, Mode})`, `Get`, `Delete`. `Mode` is `keyword`, `semantic` or `hybrid`, whichever the backend supports; asking for one it doesn't have is `ErrModeUnsupported`, never a silent fallback. Results carry a score and which mode produced them. |
| Backends | **Keyword:** in-memory BM25 (reference and tests) and SQLite FTS5 (with T029). **Semantic:** an `Embedder` interface plus a vector index: in-memory brute-force cosine first (fine to about 100k items), SQLite-stored vectors next, pgvector with Postgres later. **Hybrid:** reciprocal rank fusion of both. |
| Embedder | `memory.Embedder` (`Embed(ctx, texts, purpose) ([][]float32, error)`; purpose is `query` or `document`, since providers differ). Implementations: an OpenAI-compatible endpoint (covers OpenAI, local servers, gateways), Voyage AI (Anthropic's recommended embeddings partner), and a **deterministic fake** for tests. Which real one ships first is an owner question (below). Every stored vector records its model and dimension, and a mismatch is an error, so switching models never mixes vectors. |
| Opt-in and scope | Memory is **off unless the agent config turns it on**: `agent.memory: {store: ..., index: ..., scope: "user"|"tenant", auto_recall: false, k: 5}`. Everything is tenant-scoped through `ctx` (like `pkg/store`), and `user` scope adds the user from `ctx`. Nothing is written without a tool call the model made (no silent extraction in E12; automatic "save what you learned" is a later opt-in). |
| How memory reaches the model | **As tools** (`memory_*` for the context store, `recall_search`/`recall_get`/`recall_save` for the search store) always. Optional **`auto_recall`**: before each run, the top-k results for the user's message go into the **user turn** as a marked context block, not the system prompt, so the cached system prompt stays the same across runs. Retrieved text is third-party data (it may hold injected instructions) and is labelled as such. |
| Harness change | At most one hook: a per-run "prepare" option that can add content blocks to the new user message (for `auto_recall`, and later RAG). Everything else lives in `pkg/skill`, `pkg/memory` and toolsets. |
| Testing | Conformance suites for every interface (`skilltest`, `memorytest`), as `storetest` does. Search quality gets a small fixed relevance set (queries with expected hits) run per backend: keyword must find exact phrases, semantic must find paraphrases (with the fake embedder built so paraphrase pairs are close). Live embedding tests only in `make test-live`. |

## Tasks

- [ ] [T057](T057-skills.md) `pkg/skill`: Agent Skills loading, catalog in the system prompt, `skill_load`/`skill_read` tools
- [ ] [T058](T058-memory-context-store.md) `pkg/memory`: context store (Claude memory-tool shape), memory toolset, opt-in config
- [ ] [T059](T059-memory-search-keyword.md) Search store interface, conformance suite, keyword backends (BM25 in memory; SQLite FTS5 with T029), `auto_recall`
- [ ] [T060](T060-memory-search-semantic.md) Embedders and semantic and hybrid search (in-memory vectors, then SQLite)

T057 and T058 can start now (new packages). T059 after T058's scoping helpers (or in parallel, sharing only `ctx` scoping rules). T060 after T059. The clients (E11) show memory later: a "memories" panel to view and delete, since users must be able to see what is remembered.

## Owner questions

1. **First real embedding provider**: Voyage AI, OpenAI, or a local model (through the OpenAI-compatible endpoint)? The fake is enough to build and test T060; a real one is needed for its live test.
