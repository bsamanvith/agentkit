---
id: T087
title: "Console UX design, part 2: skills, agent builder, tool policies, interrupts and approvals, tasks with steering"
epic: E11
status: todo
depends_on: [T070]
---

# T087: console UX design, part 2

## Context

T070 designed the console's base: the IA (conversation list column, 56 px icon rail with "Activity (later)"), layouts, states, components and tokens (docs/UI.md, mockup v3). The owner added (2026-10-12):
- **skills**, where each skill can pick which tools it auto-loads;
- an **agent builder**: pick skills, pick tools, a defer-loading flag per tool, the interaction mode, the tool policy;
- **tasks**: agent runs that aren't chats (background and ephemeral), with a transcript you can watch and **steer**;
- **interrupts and approvals**: `ask_user` questions, approval holds, and tool policy decisions shown in transcripts.

Same deliverable as T070: a spec in docs/UI.md plus mockup v4. A design task, no app code.

## Inputs (decided)

- [E17](E17-tool-safety-control.md):
  - tool kinds (read, write, delete, send, execute) and risk (low, medium, high);
  - policy actions (allow, flag, approve, deny), presets, and narrowing by environment;
  - interaction modes (interactive, supervised, autonomous; tasks default to autonomous);
  - interrupts, deferred loading, skills that load tools, steering.
- [E15](E15-agent-definitions.md): agents and skills are versioned (draft, publish, rollback, versions list).
- [E14](E14-background-agents-messaging.md) and [E16](E16-runtimes-jobs.md): **Tasks** is the owner's name for every run that isn't a chat (background or ephemeral). They live under the rail's Activity slot, renamed **Tasks**. Their notifications, triggers and agent-to-agent threads are covered too.
- The tool-step style, tokens and components from T070.

## Scope

1. **Skills:**
   - a list (name, description, tools it loads, used by N agents, version);
   - a detail and editor (`SKILL.md` with preview, files, the tools picker with globs, version history, publish);
   - upload a skill folder.
2. **Agent builder** (desktop-first, a builder beside a test chat):
   - identity (name, description), model and effort, system prompt editor;
   - **skills picker**;
   - **tools picker** by toolset with per-tool **defer loading** toggles, kind and risk badges, and search;
   - **interaction mode**;
   - **tool policy** (preset plus rules, with a preview: "in this run these 12 tools are allowed, 3 need approval, 2 are denied");
   - memory, limits;
   - draft, publish and version history with diff and rollback;
   - validation errors per field.
3. **Tasks:**
   - a list (running, waiting for you, done, failed; agent, started by or trigger, elapsed, cost, environment);
   - a **task view**: the transcript, live, with the same steps as chat; a **steer** composer ("Message the task", delivered at the next step); pause, resume and cancel; outputs and artifacts; the version of the agent used.
3a. **Starting a task** from the console: agent, input, environment and policy narrowing.
4. **Interrupts and approvals:**
   - in chat (inline question or approval card in the transcript);
   - in tasks ("waiting for you" state, notification, answer from the task view or a notification);
   - approval cards show the tool, its kind and risk, the exact input, approve or deny with a reason.
5. **Policy in transcripts:** how flagged, held, denied and approved calls look in the step list (quiet, but unmistakable), and how deferred-tool loading shows ("Loaded 3 tools").
5a. **Adding an MCP server** (E19, T096: the "Add connector" screen T070 asked for):
   - an admin pastes a URL; the console probes it, then shows auth (none, header from a credential, or "users sign in" with OAuth);
   - **review tools**: each tool's raw description, its kind and risk, enable or disable;
   - save, then assign to agents;
   - the "review needed" state when a server changes a tool (a diff of the description and schema, approve or keep disabled);
   - each user's **Connect** card on Connections for servers that need their own sign-in;
   - later, a registry search box (T097).
6. **Updates to T070's IA:** the rail gains Skills (admin) and Tasks (everyone, with a "waiting for you" badge); say what moves.

## References

- T070's docs/UI.md, mockup v3 and Notes (base; keep its decisions unless a new need breaks one, and say which)
- Agent builders: OpenAI GPT builder and Agent Builder, Claude Projects settings, Dify and Flowise agent configuration, LangSmith/LangGraph Studio (versions, test beside edit)
- Task and run views: Claude Code background tasks, Devin/Jules session views (watch and steer), GitHub Actions run logs, Temporal's workflow view
- Approvals and permissions: Claude Code permission prompts (allow once / always / deny), GitHub environment protection approvals, AWS IAM policy simulator (the "what would be allowed" preview)
- Adding MCP servers: [Claude custom connectors](https://claude.com/docs/connectors/custom/remote-mcp) (URL → Add → Connect; org owner adds, members connect), ChatGPT developer-mode connectors, Cursor's MCP settings
- Version history: Vercel deployments (promote, rollback), Notion and Google Docs version history (diff and restore)

## Acceptance criteria

- [ ] **Coverage:** every scope item has layouts at T070's widths (the builder may be desktop-only, with a stated phone fallback), states, and components (mapped to shadcn/Radix or marked custom, with `ui` / `agent-ui` / `console` placement).
- [ ] **Mockup v4:** clickable; it extends v3 and doesn't fork it; both themes; sample data covering every policy action, every interaction mode and a waiting task.
- [ ] **docs/UI.md** updated, with decisions and reasons in this file's Notes.
- [ ] **Owner review:** the owner approves mockup v4.
