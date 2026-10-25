---
id: T129
title: "Load memory into each new message; past a threshold, files to grep"
epic: E23
status: done
depends_on: [T123]
---

# T129: memory the agent starts with

## Context

Owner (2026-10-24): memory is loaded into the agent's context while it is
small; once it passes a threshold it stays as files, and the agent reads
what it needs with grep and the like. Until now the agent saw nothing of its
memory unless it called `view`.

## Scope

- Memory tool: a `grep` command. `pattern` is an RE2 regular expression,
  `path` a file or directory (default `/memories`). It returns
  `path:line: text` for each matching line, at most 100, lines cut at 300
  bytes, then how many more there were.
- Loading: at the start of each run with a new message, the agent's memory
  is added to the message (a harness prepare hook, as auto recall is):
  - at most `load_bytes` in all (memory.store's config; default 4 KB): every
    file's text, marked as the agent's own notes, data and not instructions;
  - more than that: no text, but the file list (at most 100, with sizes)
    and a pointer to `grep` and `view`;
  - empty memory, or a user-scoped memory on a run with no user: nothing.
  - `load_bytes: -1` turns loading off (the agent calls `view` itself, as
    before).
- The tool builds the loaded text (a `load` command, left out of the
  description the model reads), so the hook only calls the tool: an agent on
  the split runner loads through `/rpc/memory/` like its other memory calls,
  and the runner still links no `pkg/memory`.

## Decisions

| Question | Decision |
|---|---|
| Each message, or only a conversation's first? | Each new message: the agent's notes change between messages, and a prepare hook sees one message. 4 KB keeps it near 1k tokens a message. |
| What counts toward the threshold | The bytes of every file in the scope, as the limits count them. |
| Native memory tool (`native_tool`) | Loading works the same. Claude's native tool type has no `grep` in its schema, so the pointer names `view` first. |

## Acceptance criteria

- [x] grep finds matching lines across files and in one file, caps its output, and refuses a bad pattern with a text the model can act on. — test: `memory.TestToolGrep`
- [x] Small memory loads as text; past the threshold, a file list with a pointer; empty or off loads nothing; no user loads nothing. — test: `memory.TestToolLoad`
- [x] An agent's new message carries its memory, local and on the split runner. — test: `cmd/agentkit.TestMemoryLoads`, `TestSplitMemory`
- [x] `load_bytes` below -1 is refused. — test: `TestMemoryConfigErrors`
- [x] The split deployment saves and loads memory: deploy/k8s/base's `notes` agent (compose and k8s run the same config). — test: `test/e2e.TestMemory` (`make smoke`, fake model; `make k8s-smoke` and `make compose-smoke`, real model); `TestComponents` checks the api serves `/rpc/memory/`
- [x] Deployed locally (split deployment as processes, Postgres store and memory, real model), an agent with memory past the threshold greps or views it to answer. — run by hand, noted in the PR.
