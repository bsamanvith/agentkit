---
id: E15
title: "Agent definitions as versioned data: agents, skills, toolset settings"
status: todo
---

# E15: agent definitions

Owner decision (2026-10-12, [docs/PRODUCT.md](../docs/PRODUCT.md)): agents, skills and toolset settings are **stored and versioned**, edited through the API and the **admin console** (the first priority), and exportable as files for images. Today they come only from the config file (T062 adds several agents from config).

## Decisions (made in planning; change them in the task that finds them wrong)

| Question | Decision |
|---|---|
| What is versioned | An **agent definition** (model, system prompt, toolsets and settings, per-tool `defer_loading`, skills, memory, limits, `ui`, `can_message`, **interaction mode** and **tool policy** from [E17](E17-tool-safety-control.md)), a **skill** (its folder: `SKILL.md` and files), and a **toolset instance** (type, settings, credential slot). Each save creates an immutable **version** (number, author, time, message); one version per item is **published** (what runs); others are drafts. Rollback is publishing an older version. |
| Where | The store (SQLite now, T029), tenant-scoped like everything else; skills' files as blobs. |
| Config files | Still supported: on startup, items from config are loaded as **read-only** definitions (marked "from config") so deployments stay reproducible. `agentkit export` writes stored items as files (the same JSON/YAML shape plus skill folders); `agentkit import` loads them. |
| Runs and versions | A run records the agent version it used; changing an agent never changes a running or finished run. Conversations keep their agent; new runs use its published version. |
| Validation | Saving validates fully (toolsets exist, skills load, tool-name clashes, model known) and refuses an invalid definition with field-level errors the console can show. |
| API | `/v1/agents`, `/v1/skills`, `/v1/toolsets` gain create, update (new version), list versions, get version, publish, delete (only unused drafts); admin scope. T062's read endpoints stay the same shape. |

## Tasks

- [x] [T075](T075-agent-registry.md) Agent registry: stored, versioned agent definitions and toolset instances; publish and rollback; config as read-only; export/import
- [ ] T076 Skills store: upload skill folders, versions, validation, used by the harness (after T057) *(to plan)*
- [x] [T077](T077-console-agent-builder.md) Console agent builder: tools with description overrides, models (E24), instructions, limits, save, publish, versions; skills picker, policy editor and test chat later (T087 design)

Order: T075 after T029 and T062. T076 after T057 and T075. T077 after the console basics.
