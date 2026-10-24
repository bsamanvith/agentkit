---
id: E23
title: "Agent memory: plain text, size-capped, everywhere agents run, visible to users"
status: in-progress
---

# E23: agent memory people can read

Owner request (2026-10-24): agents get memory, with caps on its size. For
now memory is **plain readable text with size limits**; RAG-based memory
comes later, not now.

E12 built the parts: the context store (`memory.Store`, Claude's memory-tool
shape, T058) with per-file, per-scope and file-count limits, and the search
store (`memory.Index`, T059/T060). What was missing for the owner's use:

1. Nothing stopped a file holding bytes that are not text.
2. The backends are in-process and SQLite only, so memory did not work in
   the deployed setup (Compose and Kubernetes run on Postgres, split into
   components), and the runner component refused an agent with memory.
3. Users could not see what an agent remembers about them (E12: "users must
   be able to see what is remembered").

## Decisions

| Question | Decision |
|---|---|
| What a memory file holds | Readable text only (`memory.CheckText`): valid UTF-8, no control characters but tab, LF and CR. Anything else is `ErrNotText` and changes nothing. Checked on what a command writes, so files written before the rule can still be edited. |
| Caps | Stay as T058 made them, per agent in config: `max_file_bytes` (default 16 KB), `max_scope_bytes` (1 MB per user or tenant), `max_files` (200). A write past one fails and changes nothing; the tool's text tells the model to shorten or delete. The tool description tells the model files are plain text and limited. |
| RAG | Not now. The search store (`memory.index`, keyword and, with an embedder, semantic) stays opt-in as it is; nothing in this epic changes it. A later epic can move it to Postgres (pgvector) the way T122 moves the context store. |
| Where it is stored in a deployment | The Postgres store's database, a `memory_files` table with the store's row-level security (T122), as the credential vault does. |
| Memory in the split deployment | The runner has no database (its image leaves Postgres out). It reaches the agent API's memory over `pkg/rpc` (T123), with the caller's tenant and user, which already cross `/rpc/`. |
| Who sees memory | A user sees and deletes their own memory of an agent (user scope) through the agent API and the console's Memory page (T124). Tenant-scoped memory is shared, so viewing it needs `agent:read` and deleting it `agent:admin`. |
| Agent builder | When agents are built in the console (E15's T077), the memory section (on or off, scope, the three caps) is one of the builder's fields. Not in this epic. |

## Tasks

- [x] [T121](T121-memory-readable-text.md) Memory files hold readable text only
- [x] [T122](T122-memory-postgres.md) Context store backend `postgres`, in the store's database
- [ ] [T123](T123-memory-split-runner.md) Memory for agents on the split runner, over `pkg/rpc`
- [ ] [T124](T124-memory-api-console.md) Memory API and the console's Memory page
