---
id: T064
title: "Console admin pages: agents, toolsets and tools, credentials"
epic: E11
status: todo
depends_on: [T055, T062]
---

# T064: console admin pages

## Context

The admin half of the console (owner, 2026-10-12): see the agents, the MCP toolsets and their tools, and credentials. Built only on `/v1` (T062); admin-only content is enforced by the API (T061's roles). Design: [docs/UI.md](../docs/UI.md). Decisions: [E11](E11-clients.md).

## Scope

- **Agents:**
  - list (name, description, model, toolsets, skills, memory);
  - detail with the system prompt (admin);
  - a "Chat with this agent" button.
- **Toolsets and tools:**
  - each toolset with its status (ready, or skipped with the reason) and tool count;
  - its MCP URL with a copy button and a ready-to-paste `claude mcp add` command (token placeholder, never the real token);
  - each tool with its description and input and output schemas, as a readable field list plus raw JSON.
- **Credentials:**
  - v1: per toolset, status (configured, missing, keyless), the env var or slot name, never values; how to set it;
  - v2 (after T045): set, rotate and delete keys, and connect OAuth accounts.
- **Navigation:** admin pages are hidden for `user` roles; the API returns 403 anyway (tested).
- Browser tests (chromedp) for each page against `agentkit serve` with fakes.

## Acceptance criteria

- [ ] **Agents page:** lists the configured agents; the detail shows the system prompt only to `admin`; "Chat with this agent" opens a new conversation with that agent.
- [ ] **Toolsets page:** shows a skipped optional toolset with its reason; the copy command contains the right URL and no token.
- [ ] **Tools:** each tool's schema renders as a readable field list.
- [ ] **Credentials page:** shows status and variable names; a canary key value never reaches the page (DOM check).
- [ ] **Roles:** a `user` session sees no admin navigation, and direct API calls to admin data return 403.
- [ ] **Mutation list** recorded.
